"""Exact pgvector cosine baseline implementing the existing Retriever v1.

    No HTTP, migrations, ANN index, query rewriting, FTS or reranking.
"""
import math
from collections.abc import Mapping, Sequence
from uuid import UUID

from psycopg import Connection, DatabaseError, sql
from psycopg.pq import TransactionStatus
from psycopg.rows import tuple_row

from ml.database import ML_SCHEMA
from ml.embeddings import Embedder, EmbeddingError
from ml.embeddings.core import validate_vector
from ml.embeddings.profile import V1
from ml.retrieval.interfaces import DocumentVersion
from ml.shared.dto import Chunk, GetChunksRequest, GetChunksResponse, SearchRequest, SearchResponse
from ml.shared.validation import ContractValidationError, timestamp, validate, validate_pair, validate_uuid

RETRIEVAL_CONFIG_VERSION = 'ml1-pgvector-cosine-qwen1024-v1'
TITLE_FALLBACK = 'Без заголовка'


class PostgresRetriever:
    def __init__(self, connection: Connection, embedder: Embedder, *, versions: Sequence[DocumentVersion],
                 context_chunks: Mapping[str, Sequence[str]], service_chunk_ids: Sequence[str] | None = None,
                 schema: str = ML_SCHEMA):
        if embedder.profile != V1:
            raise ValueError('Retriever requires the pinned embedding profile')
        if not isinstance(schema, str) or not schema.strip():
            raise ValueError('Expected a schema name')
        self.connection, self.embedder, self.schema = connection, embedder, schema
        self._versions = {}
        self._latest = {}
        numbers = set()
        for version in versions:
            if not isinstance(version, DocumentVersion):
                raise TypeError('Expected trusted DocumentVersion metadata')
            validate_uuid(version.document_id)
            validate_uuid(version.document_version_id)
            if type(version.version_no) is not int or version.version_no < 1 or type(version.ready) is not bool:
                raise ValueError('Invalid version ordering/readiness')
            key = (UUID(version.document_id), version.version_no)
            identity = UUID(version.document_version_id)
            if identity in self._versions or key in numbers:
                raise ValueError('Duplicate version metadata')
            self._versions[identity] = version
            numbers.add(key)
            previous = self._latest.get(key[0])
            if previous is None or version.version_no > previous.version_no:
                self._latest[key[0]] = version
        self._access = {}
        for context, ids in context_chunks.items():
            validate_uuid(context)
            self._access[context] = self._allowlist(ids)
        self._service_access = None if service_chunk_ids is None else self._allowlist(service_chunk_ids)

    @staticmethod
    def _allowlist(ids):
        ids = frozenset(ids)
        for identity in ids:
            validate_uuid(identity)
        return ids

    def _allowed(self, context):
        return self._service_access if context is None else self._access.get(context)

    @staticmethod
    def _error(code, *, retryable=False):
        return {'code': code, 'message': 'Retrieval operation could not be completed.', 'retryable': retryable}

    @staticmethod
    def _chunk(row) -> Chunk:
        identity, version, text, document_id, snapshot = row[:5]
        source = snapshot['source']
        result = {
            'chunk_id': str(identity), 'document_id': str(document_id), 'document_version_id': str(version),
            'chunk_text': text, 'title': snapshot['title'] if snapshot['title'].strip() else TITLE_FALLBACK,
            'canonical_url': snapshot['canonical_url'], 'published_at': snapshot['published_at'],
            'retrieved_at': snapshot['retrieved_at'], 'source_id': source['source_id'],
            'source_name': source['name'], 'source_type': source['source_type'],
            'source_role': source['source_role'], 'trust_level': source['trust_level'],
        }
        validate(result, 'Chunk')
        return result

    def search(self, request: SearchRequest) -> SearchResponse:
        validate(request, 'SearchRequest')
        response: SearchResponse = {
            'contract_version': 'v1', 'trace_id': request['trace_id'], 'status': 'empty', 'results': [],
            'retrieval_config_version': RETRIEVAL_CONFIG_VERSION, 'warnings': [], 'error': None,
        }
        allowed = self._allowed(request['business_context_id'])
        if allowed is None:
            response.update(status='error', error=self._error('CONTEXT_MISMATCH'))
        else:
            explicit = {UUID(identity) for identity in request['filters']['document_version_ids']}
            candidates = list(self._versions.values()) if explicit else list(self._latest.values())
            eligible = [version for version in candidates if version.ready
                        and (not explicit or UUID(version.document_version_id) in explicit)]
            if allowed and eligible:
                try:
                    if self.connection.info.transaction_status != TransactionStatus.IDLE:
                        raise ValueError('Retrieval requires an idle connection')
                    vector = self.embedder.encode_queries((request['query'].strip(),))[0]
                    validate_vector(vector)
                    results, fallback = self._search_rows(request, allowed, eligible, vector)
                    response['results'] = results
                    if fallback:
                        response['warnings'] = ['Empty titles displayed as Без заголовка.']
                    if results:
                        response['status'] = 'ok'
                except EmbeddingError:
                    response.update(status='error', error=self._error('MODEL_UNAVAILABLE', retryable=True))
                except (DatabaseError, ContractValidationError, ValueError, KeyError, TypeError, IndexError):
                    response.update(status='error', results=[], warnings=[],
                                    error=self._error('SEARCH_UNAVAILABLE', retryable=True))
        validate_pair(request, response, 'search')
        return response

    def _search_rows(self, request, allowed, eligible, vector):
        chunks = sql.Identifier(self.schema, 'document_chunks')
        versions = sql.Identifier(self.schema, 'document_versions')
        filters = request['filters']
        params = [list(UUID(v.document_version_id) for v in eligible),
                  list(UUID(v.document_id) for v in eligible),
                  '[' + ','.join(repr(value) for value in vector) + ']',
                  list(UUID(identity) for identity in allowed), 'v1', V1.model, V1.version]
        predicates = []
        if not filters['document_version_ids']:
            # An uncatalogued newer snapshot cannot silently expose an older
            # version. Caller must refresh the trusted catalog first.
            predicates.append(sql.SQL(
                'NOT EXISTS (SELECT 1 FROM {} u WHERE u.document_id=d.document_id AND NOT (u.id=ANY(%s::uuid[])))'
            ).format(versions))
            params.append(list(self._versions))
        if filters['source_ids']:
            predicates.append(sql.SQL("(d.snapshot#>>'{source,source_id}')::uuid=ANY(%s::uuid[])"))
            params.append(list(UUID(identity) for identity in filters['source_ids']))
        if filters['source_roles']:
            predicates.append(sql.SQL("d.snapshot#>>'{source,source_role}'=ANY(%s::text[])"))
            params.append(filters['source_roles'])
        bounds = []
        for key, operator in (('published_from', '>='), ('published_to', '<')):
            if filters[key] is not None:
                bounds.append(sql.SQL("(d.snapshot->>'published_at')::timestamptz {} %s").format(sql.SQL(operator)))
                params.append(timestamp(filters[key]))
        if bounds:
            date_clause = sql.SQL(' AND ').join(bounds)
            if filters['include_undated']:
                date_clause = sql.SQL("(d.snapshot->>'published_at' IS NULL OR ({}))").format(date_clause)
            predicates.append(date_clause)
        extra = sql.SQL('') if not predicates else sql.SQL(' AND ') + sql.SQL(' AND ').join(predicates)
        params.append(request['top_k'])
        with self.connection.transaction():
            with self.connection.cursor(row_factory=tuple_row) as cursor:
                cursor.execute("SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='vector'")
                extension = cursor.fetchone()
                if extension is None:
                    raise ValueError('pgvector is unavailable')
                namespace = sql.Identifier(extension[0])
                query = sql.SQL('''
                    WITH selected AS (
                        SELECT * FROM unnest(%s::uuid[], %s::uuid[]) AS t(version_id, document_id)
                    ), q AS (SELECT %s::{} AS embedding)
                    SELECT c.id, c.document_version_id, c.chunk_text, d.document_id, d.snapshot,
                           c.embedding OPERATOR({}.<=>) q.embedding AS distance
                    FROM {} c JOIN {} d ON d.id=c.document_version_id
                    JOIN selected s ON s.version_id=d.id AND s.document_id=d.document_id CROSS JOIN q
                    WHERE c.id=ANY(%s::uuid[]) AND c.chunking_profile_id=%s
                      AND c.embedding_model=%s AND c.embedding_version=%s AND c.embedding IS NOT NULL
                      AND {}(c.embedding)>0
                      AND NOT EXISTS (
                          SELECT 1 FROM {} p WHERE p.document_version_id=c.document_version_id
                            AND p.chunking_profile_id=c.chunking_profile_id
                            AND (p.embedding IS NULL OR p.embedding_model IS DISTINCT FROM c.embedding_model
                                 OR p.embedding_version IS DISTINCT FROM c.embedding_version OR {}(p.embedding)=0)
                      ) {}
                    ORDER BY distance ASC, c.id ASC LIMIT %s
                ''').format(sql.Identifier(extension[0], 'vector'), namespace, chunks, versions,
                            sql.Identifier(extension[0], 'vector_norm'), chunks,
                            sql.Identifier(extension[0], 'vector_norm'), extra)
                cursor.execute(query, params)
                rows = cursor.fetchall()
        results = []
        for rank, row in enumerate(rows, 1):
            distance = float(row[5])
            if not math.isfinite(distance):
                raise ValueError('Invalid cosine distance')
            results.append({**self._chunk(row), 'rank': rank, 'retrieval_score': 1.0 - distance})
        return results, any(not row[4]['title'].strip() for row in rows)

    def get_chunks(self, request: GetChunksRequest) -> GetChunksResponse:
        validate(request, 'GetChunksRequest')
        response: GetChunksResponse = {
            'contract_version': 'v1', 'trace_id': request['trace_id'], 'status': 'empty', 'results': [],
            'unresolved_chunk_ids': [], 'warnings': [], 'error': None,
        }
        allowed = self._allowed(request['business_context_id'])
        if allowed is None:
            response.update(status='error', error=self._error('CONTEXT_MISMATCH'))
        else:
            unique = list(dict.fromkeys(request['chunk_ids']))
            identities = [UUID(identity) for identity in unique if identity in allowed]
            try:
                if self.connection.info.transaction_status != TransactionStatus.IDLE:
                    raise ValueError('Retrieval requires an idle connection')
                rows = []
                if identities:
                    with self.connection.transaction():
                        with self.connection.cursor(row_factory=tuple_row) as cursor:
                            cursor.execute(sql.SQL(
                                'SELECT c.id, c.document_version_id, c.chunk_text, d.document_id, d.snapshot '
                                'FROM {} c JOIN {} d ON d.id=c.document_version_id WHERE c.id=ANY(%s::uuid[])'
                            ).format(sql.Identifier(self.schema, 'document_chunks'),
                                     sql.Identifier(self.schema, 'document_versions')), (identities,))
                            rows = cursor.fetchall()
                by_id = {str(row[0]): self._chunk(row) for row in rows}
                response['results'] = [by_id[identity] for identity in unique if identity in by_id]
                response['unresolved_chunk_ids'] = [identity for identity in unique if identity not in by_id]
                if response['unresolved_chunk_ids']:
                    response['warnings'].append('Some requested chunks could not be resolved.')
                if any(not row[4]['title'].strip() for row in rows):
                    response['warnings'].append('Empty titles displayed as Без заголовка.')
                if response['results']:
                    response['status'] = 'partial' if response['unresolved_chunk_ids'] else 'ok'
            except (DatabaseError, ContractValidationError, ValueError, KeyError, TypeError):
                response.update(status='error', results=[], unresolved_chunk_ids=[], warnings=[],
                                error=self._error('SEARCH_UNAVAILABLE', retryable=True))
        validate_pair(request, response, 'get_chunks')
        return response
