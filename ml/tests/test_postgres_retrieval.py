"""Existing retrieval v1 contract exercised against exact PostgreSQL cosine search."""
import importlib.util
import os
import unittest
from dataclasses import replace
from unittest.mock import MagicMock
from uuid import UUID, uuid4

from ml.chunking.dto import DocumentChunk
from ml.embeddings import ChunkEmbedding, Embedder, EmbeddingError, V1
from ml.embeddings.core import text_hash
from ml.retrieval.interfaces import DocumentVersion
from ml.shared.validation import ContractValidationError, validate_pair
from ml.tests.test_chunking import document
from ml.tests.test_embeddings import unit_vector

HAS_PSYCOPG = importlib.util.find_spec('psycopg') is not None
if HAS_PSYCOPG:
    import psycopg
    from psycopg import sql
    from psycopg.pq import TransactionStatus
    from ml.adapters.postgres_retrieval import PostgresRetriever, TITLE_FALLBACK
    from ml.adapters.postgres_chunks import save_document_version_and_chunks, save_document_embeddings
    import ml.tests.test_ml_storage as fixture


class QueryEmbedder:
    """Unit fixture; database ranking tests never claim to test model semantics."""
    profile = V1

    def __init__(self):
        self.calls = []

    def encode_queries(self, queries):
        self.calls.append(queries)
        return tuple(unit_vector() for _ in queries)


def request(context, **changes):
    result = {
        'contract_version': 'v1', 'trace_id': str(uuid4()), 'business_context_id': context,
        'query': 'Повышение ключевой ставки', 'top_k': 3,
        'filters': {'published_from': None, 'published_to': None, 'source_ids': [],
                    'source_roles': [], 'document_version_ids': [], 'include_undated': False},
    }
    result.update(changes)
    return result


@unittest.skipUnless(HAS_PSYCOPG, 'Optional persistence dependencies')
class PostgresRetrievalUnitTests(unittest.TestCase):
    def setUp(self):
        self.context, self.id = str(uuid4()), str(uuid4())
        self.version = DocumentVersion(str(uuid4()), str(uuid4()), 1)
        self.connection = MagicMock()
        self.connection.info.transaction_status = TransactionStatus.IDLE
        self.embedder = QueryEmbedder()

    def configured(self, **kwargs):
        return PostgresRetriever(self.connection, self.embedder, versions=[self.version],
                                 context_chunks={self.context: [self.id]}, **kwargs)

    def test_validation_before_embedding_or_database(self):
        adapter = self.configured()
        for top_k in (0, 51, True, 1.0):
            with self.assertRaises(ContractValidationError):
                adapter.search(request(self.context, top_k=top_k))
        with self.assertRaises(ContractValidationError):
            adapter.search(request(self.context, query=' \n'))
        self.assertEqual(self.embedder.calls, [])
        self.connection.transaction.assert_not_called()

    def test_unknown_context_and_null_do_not_disable_access(self):
        adapter = self.configured()
        for context in (None, str(uuid4())):
            response = adapter.search(request(context))
            self.assertEqual(response['error']['code'], 'CONTEXT_MISMATCH')
            response = adapter.get_chunks({'contract_version': 'v1', 'trace_id': str(uuid4()),
                                           'business_context_id': context, 'chunk_ids': [self.id]})
            self.assertEqual(response['error']['code'], 'CONTEXT_MISMATCH')
        self.assertEqual(self.embedder.calls, [])
        self.connection.transaction.assert_not_called()

    def test_empty_access_and_unready_latest_skip_inference(self):
        adapter = PostgresRetriever(self.connection, self.embedder, versions=[self.version],
                                    context_chunks={self.context: []})
        self.assertEqual(adapter.search(request(self.context))['status'], 'empty')
        newer = DocumentVersion(self.version.document_id, str(uuid4()), 2, ready=False)
        adapter = PostgresRetriever(self.connection, self.embedder, versions=[self.version, newer],
                                    context_chunks={self.context: [self.id]})
        self.assertEqual(adapter.search(request(self.context))['status'], 'empty')
        self.assertEqual(self.embedder.calls, [])
        self.connection.transaction.assert_not_called()

    def test_failure_responses_are_safe_and_distinct_from_empty(self):
        adapter = self.configured()
        self.embedder.encode_queries = MagicMock(side_effect=EmbeddingError('secret query'))
        response = adapter.search(request(self.context))
        self.assertEqual(response['status'], 'error')
        self.assertEqual(response['error']['code'], 'MODEL_UNAVAILABLE')
        self.assertNotIn('secret', str(response))
        self.embedder = QueryEmbedder()
        adapter = self.configured()
        cursor = self.connection.cursor.return_value.__enter__.return_value
        cursor.execute.side_effect = psycopg.OperationalError('private SQL password')
        response = adapter.search(request(self.context))
        self.assertEqual(response['error']['code'], 'SEARCH_UNAVAILABLE')
        self.assertNotIn('password', str(response))

    def test_caller_transaction_is_not_used_or_ended(self):
        self.connection.info.transaction_status = TransactionStatus.INTRANS
        adapter = self.configured()
        self.assertEqual(adapter.search(request(self.context))['status'], 'error')
        self.assertEqual(self.embedder.calls, [])
        self.connection.transaction.assert_not_called()
        self.connection.commit.assert_not_called()
        self.connection.rollback.assert_not_called()

    def test_catalog_and_profile_validation(self):
        for versions in ([self.version, self.version],
                         [replace(self.version, version_no=0)], [replace(self.version, ready=1)]):
            with self.assertRaises(ValueError):
                PostgresRetriever(self.connection, self.embedder, versions=versions, context_chunks={})
        self.embedder.profile = None
        with self.assertRaises(ValueError):
            self.configured()


@unittest.skipUnless(HAS_PSYCOPG and os.environ.get('ML1_TEST_POSTGRES_DSN'), 'Explicit PostgreSQL + pgvector')
class PostgresRetrievalTests(unittest.TestCase):
    def setUp(self):
        fixture.MLStoragePostgresTests.setUp(self)
        self.context = str(uuid4())
        self.catalog = []
        self.stored = []
        self.query_embedder = QueryEmbedder()

    if HAS_PSYCOPG:
        drop_schema = fixture.MLStoragePostgresTests.drop_schema

    def add(self, text, vector=None, *, ready=True, number=1, doc_id=None, **metadata):
        saved = replace(document(text), document_id=doc_id or str(uuid4()), document_version_id=str(uuid4()), **metadata)
        # Synthetic DTO for exact database tests; real-model test uses chunker v1.
        chunk = DocumentChunk(str(uuid4()), saved.document_version_id, 'v1', 0, text, 0, len(text), 1)
        save_document_version_and_chunks(self.connection, saved, (chunk,), schema=self.schema)
        if vector is not None:
            embedding = ChunkEmbedding(chunk.id, text_hash(text), vector)
            save_document_embeddings(self.connection, (chunk,), (embedding,), schema=self.schema)
        self.catalog.append(DocumentVersion(saved.document_id, saved.document_version_id, number, ready))
        self.stored.append((saved, chunk))
        return saved, chunk

    def configured(self, *, allowed=None, versions=None, embedder=None, service=None):
        ids = [chunk.id for _, chunk in self.stored] if allowed is None else allowed
        return PostgresRetriever(self.connection, embedder or self.query_embedder,
                                 versions=self.catalog if versions is None else versions,
                                 context_chunks={self.context: ids}, service_chunk_ids=service, schema=self.schema)

    def seed(self):
        self.add('Банк России повысил ключевую ставку.', unit_vector())
        self.add('Банк изменил условия кредитования.', (0.6, 0.8) + (0.0,) * 1022)
        self.add('Компания открыла новый завод.', unit_vector(1))

    def test_cosine_ranking_top_k_contract_and_provenance(self):
        self.seed()
        adapter = self.configured()
        search = request(self.context, top_k=2)
        response = adapter.search(search)
        self.assertEqual(response['status'], 'ok', response)
        self.assertEqual([item['chunk_id'] for item in response['results']], [chunk.id for _, chunk in self.stored[:2]])
        self.assertEqual([item['rank'] for item in response['results']], [1, 2])
        self.assertAlmostEqual(response['results'][0]['retrieval_score'], 1.0)
        self.assertAlmostEqual(response['results'][1]['retrieval_score'], .6, places=6)
        self.assertEqual(response['results'][0]['chunk_text'], self.stored[0][1].chunk_text)
        self.assertEqual(response['results'][0]['source_id'], self.stored[0][0].source.source_id)
        validate_pair(search, response, 'search')

    def test_tie_break_by_chunk_id(self):
        self.add('First', unit_vector())
        self.add('Second', unit_vector())
        ids = [item['chunk_id'] for item in self.configured().search(request(self.context))['results']]
        self.assertEqual(ids, sorted(chunk.id for _, chunk in self.stored))

    def test_empty_database_and_absent_embeddings(self):
        self.assertEqual(self.configured().search(request(self.context))['status'], 'empty')
        self.add('Not yet embedded')
        response = self.configured().search(request(self.context))
        self.assertEqual(response['status'], 'empty', response)
        self.assertEqual(response['results'], [])

    def test_empty_database_with_trusted_catalog_returns_empty(self):
        version = DocumentVersion(str(uuid4()), str(uuid4()), 1)
        response = self.configured(allowed=[str(uuid4())], versions=[version]).search(request(self.context))
        self.assertEqual(response['status'], 'empty', response)
        self.assertEqual(response['results'], [])
        self.assertEqual(len(self.query_embedder.calls), 1)

    def test_incomplete_embedding_set_excludes_entire_version(self):
        saved = replace(document('FirstSecond'), document_id=str(uuid4()), document_version_id=str(uuid4()))
        chunks = (
            DocumentChunk(str(uuid4()), saved.document_version_id, 'v1', 0, 'First', 0, 5, 1),
            DocumentChunk(str(uuid4()), saved.document_version_id, 'v1', 1, 'Second', 5, 11, 1),
        )
        save_document_version_and_chunks(self.connection, saved, chunks, schema=self.schema)
        items = tuple(ChunkEmbedding(c.id, text_hash(c.chunk_text), unit_vector()) for c in chunks)
        # Simulate a partial/legacy writer; normal persistence saves the complete set atomically.
        self.connection.execute(sql.SQL(
            'UPDATE {} SET embedding=%s, embedding_model=%s, embedding_version=%s WHERE id=%s'
        ).format(sql.Identifier(self.schema, 'document_chunks')),
            ('[' + ','.join(str(v) for v in items[0].vector) + ']', V1.model, V1.version, UUID(chunks[0].id)))
        self.catalog.append(DocumentVersion(saved.document_id, saved.document_version_id, 1))
        adapter = self.configured(allowed=[c.id for c in chunks])
        self.assertEqual(adapter.search(request(self.context))['status'], 'empty')
        save_document_embeddings(self.connection, chunks, items, schema=self.schema)
        response = adapter.search(request(self.context))
        self.assertEqual(response['status'], 'ok', response)
        self.assertEqual({r['chunk_id'] for r in response['results']}, {c.id for c in chunks})

    def test_incompatible_embedding_model_version_and_chunking_are_excluded(self):
        self.seed()
        table = sql.Identifier(self.schema, 'document_chunks')
        self.connection.execute(sql.SQL("UPDATE {} SET embedding_version='older' WHERE id=%s").format(table),
                                (UUID(self.stored[1][1].id),))
        self.connection.execute(sql.SQL("UPDATE {} SET chunking_profile_id='other' WHERE id=%s").format(table),
                                (UUID(self.stored[2][1].id),))
        response = self.configured().search(request(self.context))
        self.assertEqual([item['chunk_id'] for item in response['results']], [self.stored[0][1].id])
        self.connection.execute(sql.SQL("UPDATE {} SET embedding_model='other' WHERE id=%s").format(table),
                                (UUID(self.stored[0][1].id),))
        self.assertEqual(self.configured().search(request(self.context))['status'], 'empty')

    def test_zero_vector_excluded(self):
        _, chunk = self.add('Zero vector', unit_vector())
        literal = '[' + ','.join('0' for _ in range(1024)) + ']'
        self.connection.execute(sql.SQL('UPDATE {} SET embedding=%s WHERE id=%s').format(
            sql.Identifier(self.schema, 'document_chunks')), (literal, UUID(chunk.id)))
        self.assertEqual(self.configured().search(request(self.context))['status'], 'empty')

    def test_access_filter_is_applied_before_top_k_and_null_requires_allowlist(self):
        self.seed()
        allowed = [self.stored[-1][1].id]
        adapter = self.configured(allowed=allowed, service=allowed)
        for context in (self.context, None):
            response = adapter.search(request(context, top_k=1))
            self.assertEqual([item['chunk_id'] for item in response['results']], allowed)

    def test_metadata_filters_before_limit_and_no_match(self):
        self.seed()
        bank_source = replace(self.stored[0][0].source, source_id=str(uuid4()), source_role='bank')
        _, chunk = self.add('Bank evidence', unit_vector(1), source=bank_source)
        search = request(self.context, top_k=1)
        search['filters']['source_roles'] = ['bank']
        search['filters']['source_ids'] = [bank_source.source_id]
        response = self.configured().search(search)
        self.assertEqual([item['chunk_id'] for item in response['results']], [chunk.id], response)
        search['filters']['source_ids'] = [str(uuid4())]
        self.assertEqual(self.configured().search(search)['status'], 'empty')

    def test_publication_dates_half_open_and_undated(self):
        self.add('Before', unit_vector(), published_at='2026-10-19T23:59:59Z')
        _, inside = self.add('Inside', unit_vector(1), published_at='2026-10-20T00:00:00Z')
        self.add('At end', unit_vector(), published_at='2026-10-21T00:00:00Z')
        _, undated = self.add('Unknown', unit_vector(), published_at=None)
        search = request(self.context, top_k=10)
        search['filters'].update(published_from='2026-10-20T00:00:00Z', published_to='2026-10-21T00:00:00Z')
        adapter = self.configured()
        self.assertEqual([r['chunk_id'] for r in adapter.search(search)['results']], [inside.id])
        search['filters']['include_undated'] = True
        self.assertEqual({r['chunk_id'] for r in adapter.search(search)['results']}, {inside.id, undated.id})

    def test_latest_selected_before_readiness_or_date_filters_and_historical_explicit(self):
        old, chunk = self.add('Old version', unit_vector(), published_at='2026-10-20T00:00:00Z')
        self.add('New version', ready=False, number=2, doc_id=old.document_id,
                 published_at='2026-10-22T00:00:00Z')
        adapter = self.configured()
        search = request(self.context)
        search['filters']['published_to'] = '2026-10-21T00:00:00Z'
        self.assertEqual(adapter.search(search)['status'], 'empty')
        search['filters']['document_version_ids'] = [old.document_version_id]
        self.assertEqual([r['chunk_id'] for r in adapter.search(search)['results']], [chunk.id])

    def test_uncatalogued_version_does_not_allow_stale_fallback(self):
        old, _ = self.add('Old', unit_vector())
        self.add('Unknown new snapshot', number=2, doc_id=old.document_id)
        adapter = self.configured(versions=self.catalog[:1])
        self.assertEqual(adapter.search(request(self.context))['status'], 'empty')

    def test_empty_title_fallback_preserves_saved_text(self):
        _, chunk = self.add('Exact body', unit_vector(), title=' \n')
        response = self.configured().search(request(self.context))
        self.assertEqual(response['results'][0]['title'], TITLE_FALLBACK)
        self.assertEqual(response['results'][0]['chunk_text'], chunk.chunk_text)
        self.assertTrue(response['warnings'])

    def test_get_chunks_exact_historical_order_duplicates_and_unresolved(self):
        self.seed()
        ids = [chunk.id for _, chunk in self.stored]
        missing = str(uuid4())
        adapter = self.configured(allowed=ids[:2] + [missing], versions=[])
        get = {'contract_version': 'v1', 'trace_id': str(uuid4()), 'business_context_id': self.context,
               'chunk_ids': [ids[1], ids[0], ids[1], ids[2], missing]}
        response = adapter.get_chunks(get)
        self.assertEqual(response['status'], 'partial')
        self.assertEqual([item['chunk_id'] for item in response['results']], [ids[1], ids[0]])
        self.assertEqual(response['unresolved_chunk_ids'], [ids[2], missing])
        self.assertNotIn('rank', response['results'][0])
        validate_pair(get, response, 'get_chunks')


@unittest.skipUnless(HAS_PSYCOPG and os.environ.get('ML1_TEST_POSTGRES_DSN')
                     and os.environ.get('ML1_TEST_QWEN_MODEL') == '1', 'Real cached Qwen + PostgreSQL opt-in')
class RealQwenVectorRetrievalTests(unittest.TestCase):
    if HAS_PSYCOPG:
        drop_schema = fixture.MLStoragePostgresTests.drop_schema

    def test_text_query_to_pgvector_semantic_top_k(self):
        fixture.MLStoragePostgresTests.setUp(self)
        from ml.chunking import chunk_document
        from ml.chunking.tokenization import QwenTokenCounter
        from ml.embeddings.qwen import QwenBatchEncoder

        counter = QwenTokenCounter(cache_dir=os.environ.get('ML1_MODEL_CACHE'))
        embedder = Embedder(QwenBatchEncoder(cache_dir=os.environ.get('ML1_MODEL_CACHE')), batch_size=4)
        all_chunks, catalog = [], []
        for text in ('Банк России увеличил ключевую ставку.',
                     'Компания открыла новый завод по производству станков.',
                     'Футбольная команда выиграла чемпионат страны.'):
            saved = replace(document(text), document_id=str(uuid4()), document_version_id=str(uuid4()))
            chunks = chunk_document(saved, counter)
            save_document_version_and_chunks(self.connection, saved, chunks, schema=self.schema)
            all_chunks.extend(chunks)
            catalog.append(DocumentVersion(saved.document_id, saved.document_version_id, 1))
        save_by_version = {chunk.document_version_id: (chunk,) for chunk in all_chunks}
        for item in embedder.encode_chunks(all_chunks):
            chunk = next(c for c in all_chunks if c.id == item.chunk_id)
            save_document_embeddings(self.connection, save_by_version[chunk.document_version_id], (item,), schema=self.schema)
        context = str(uuid4())
        adapter = PostgresRetriever(self.connection, embedder, versions=catalog,
                                    context_chunks={context: [c.id for c in all_chunks]}, schema=self.schema)
        search = request(context, query='Повышение ключевой ставки Банком России', top_k=2)
        result = adapter.search(search)
        self.assertEqual(result['status'], 'ok', result)
        self.assertEqual(len(result['results']), 2)
        self.assertEqual(result['results'][0]['chunk_id'], all_chunks[0].id)
        scores = [item['retrieval_score'] for item in result['results']]
        self.assertEqual(scores, sorted(scores, reverse=True))
        self.assertGreater(scores[0], scores[1])
        print('\nVECTOR QUERY:', search['query'], flush=True)
        for item in result['results']:
            print(f"rank={item['rank']} similarity={item['retrieval_score']:.9f} text={item['chunk_text']}", flush=True)
        validate_pair(search, result, 'search')
