"""Synchronous ML1 v1: backend document -> index -> ranked evidence for ML2.

One instance owns no connection lifecycle and is used sequentially. Trusted
version ordering/access are application inputs, never derived from user queries.
"""
from dataclasses import dataclass, field, replace
from collections.abc import Mapping, Sequence
from uuid import uuid4

from ml.adapters.backend_documents import document_from_backend
from ml.adapters.postgres_chunks import save_indexed_document
from ml.adapters.postgres_catalog import load_versions, remember_version
from ml.adapters.postgres_retrieval import PostgresRetriever, RETRIEVAL_CONFIG_VERSION
from ml.chunking import chunk_document
from ml.database import ML_SCHEMA
from ml.embeddings import Embedder
from ml.ingestion.dto import BackendInput, QualityResult
from ml.ingestion.quality import check_document
from ml.reranking import INFERENCE_PROFILES, QwenReranker, keyed_scores
from ml.retrieval.interfaces import DocumentVersion
from ml.shared.dto import SearchFilters, SearchRequest, SearchResponse
from ml.shared.validation import validate, validate_pair, validate_uuid


@dataclass(frozen=True)
class PipelineConfig:
    candidate_k: int = 20
    final_k: int = 5
    reranker_enabled: bool = True
    reranker_profile: str = 'cpu-sdpa-b4'

    def __post_init__(self):
        if (type(self.candidate_k) is not int or type(self.final_k) is not int
                or not 1 <= self.final_k <= self.candidate_k <= 50):
            raise ValueError('Expected 1 <= final_k <= candidate_k <= 50')
        if type(self.reranker_enabled) is not bool or self.reranker_profile != 'cpu-sdpa-b4':
            raise ValueError('ML1 v1 uses the validated CPU SDPA batch-4 reranker')


@dataclass(frozen=True)
class IndexResult:
    quality: QualityResult
    chunk_ids: tuple[str, ...] = ()
    indexed: bool = False


@dataclass(frozen=True)
class SearchEvidence:
    """Internal diagnostics alongside the unchanged v1 wire SearchResponse.

    retrieval_score in response remains cosine. Sidecar scores mean relevance,
    never claim verification; no extra fields are added to the contracted DTO.
    """
    response: SearchResponse
    reranker_scores: Mapping[str, float] = field(default_factory=dict)
    reranker_applied: bool = False


class ML1Pipeline:
    def __init__(self, connection, embedder: Embedder, token_counter, *,
                 versions: Sequence[DocumentVersion] = (),
                 context_chunks: Mapping[str, Sequence[str]] | None = None,
                 service_chunk_ids: Sequence[str] | None = None,
                 config: PipelineConfig = PipelineConfig(), schema: str = ML_SCHEMA,
                 reranker=None, cache_dir=None):
        self.connection, self.embedder, self.token_counter = connection, embedder, token_counter
        self.config, self.schema, self.cache_dir = config, schema, cache_dir
        self._reranker = reranker
        # PostgresRetriever validates this existing trusted catalog/allowlist boundary.
        PostgresRetriever(connection, embedder, versions=versions, context_chunks=context_chunks or {},
                          service_chunk_ids=service_chunk_ids, schema=schema)
        self._versions = {v.document_version_id: v for v in versions}
        self._contexts = {key: set(ids) for key, ids in (context_chunks or {}).items()}
        self._service = None if service_chunk_ids is None else set(service_chunk_ids)

    @classmethod
    def from_local_models(cls, connection, *, cache_dir=None, **kwargs):
        """Load pinned embedding/tokenizer offline; reranker loads lazily with fallback."""
        from ml.chunking.tokenization import QwenTokenCounter
        from ml.embeddings.qwen import QwenBatchEncoder
        if 'versions' not in kwargs:
            kwargs['versions'] = load_versions(connection, schema=kwargs.get('schema', ML_SCHEMA))
        return cls(connection, Embedder(QwenBatchEncoder(cache_dir=cache_dir)),
                   QwenTokenCounter(cache_dir=cache_dir), cache_dir=cache_dir, **kwargs)

    def _retriever(self):
        return PostgresRetriever(self.connection, self.embedder, versions=tuple(self._versions.values()),
                                 context_chunks=self._contexts, service_chunk_ids=self._service,
                                 schema=self.schema)

    def index(self, payload: BackendInput, *, version: DocumentVersion | None = None,
              business_context_ids: Sequence[str] = (), service_access: bool = False) -> IndexResult:
        """Caller supplies trusted version_no and access; publish only after commit.

        Current BackendInput supplies article.version_no. Explicit trusted version
        metadata remains supported for legacy inputs; readiness is owned by ML.
        """
        saved = document_from_backend(payload)
        number = payload['article'].get('version_no')
        if version is None:
            if number is None:
                raise ValueError('Backend article.version_no or explicit trusted version is required')
            version = DocumentVersion(saved.document_id, saved.document_version_id, number, ready=False)
        elif number is not None and number != version.version_no:
            raise ValueError('Backend version_no differs from trusted metadata')
        if not isinstance(version, DocumentVersion) or (version.document_id, version.document_version_id) != (
                saved.document_id, saved.document_version_id):
            raise ValueError('Trusted version metadata must match the document')
        contexts = tuple(business_context_ids)
        for context in contexts:
            validate_uuid(context)
        if type(service_access) is not bool:
            raise ValueError('service_access must be boolean')
        previous = self._versions.get(version.document_version_id)
        if previous and (previous.document_id, previous.version_no) != (version.document_id, version.version_no):
            raise ValueError('Conflicting trusted version ordering')
        pending = {**self._versions, version.document_version_id: replace(version, ready=False)}
        PostgresRetriever(self.connection, self.embedder, versions=tuple(pending.values()),
                          context_chunks=self._contexts, schema=self.schema)
        # A newly announced latest version blocks stale fallback even on quality/inference failure.
        # An already committed version remains available during an identical retry.
        remember_version(self.connection, version, schema=self.schema)
        self._versions[version.document_version_id] = previous or pending[version.document_version_id]
        quality = check_document(saved)
        if quality.status != 'PASS':
            return IndexResult(quality)
        chunks = chunk_document(saved, self.token_counter)
        embeddings = self.embedder.encode_chunks(chunks)
        save_indexed_document(self.connection, saved, chunks, embeddings, schema=self.schema, version=version)
        self._versions[version.document_version_id] = replace(version, ready=True)
        ids = tuple(c.id for c in chunks)
        for context in contexts:
            self._contexts.setdefault(context, set()).update(ids)
        if service_access:
            if self._service is None:
                self._service = set()
            self._service.update(ids)
        return IndexResult(quality, ids, True)

    def search(self, query: str | SearchRequest, *, filters: SearchFilters | None = None,
               business_context_id: str | None = None, top_k: int | None = None,
               trace_id: str | None = None) -> SearchResponse:
        return self.search_with_scores(query, filters=filters, business_context_id=business_context_id,
                                       top_k=top_k, trace_id=trace_id).response

    def search_with_scores(self, query: str | SearchRequest, *, filters: SearchFilters | None = None,
                           business_context_id: str | None = None, top_k: int | None = None,
                           trace_id: str | None = None) -> SearchEvidence:
        if isinstance(query, dict):
            request = query
        else:
            request = {'contract_version': 'v1', 'trace_id': trace_id or str(uuid4()),
                       'business_context_id': business_context_id, 'query': query,
                       'filters': filters if filters is not None else {
                           'published_from': None, 'published_to': None, 'source_ids': [],
                           'source_roles': [], 'document_version_ids': [], 'include_undated': False},
                       'top_k': self.config.final_k if top_k is None else top_k}
        validate(request, 'SearchRequest')
        if request['top_k'] > self.config.candidate_k:
            raise ValueError('top_k cannot exceed candidate_k')
        response = self._retriever().search({**request, 'top_k': self.config.candidate_k})
        response = {**response, 'warnings': list(response['warnings'])}
        rows, scores, applied = response['results'], {}, False
        if rows and self.config.reranker_enabled:
            try:
                if self._reranker is None:
                    self._reranker = QwenReranker(cache_dir=self.cache_dir,
                                                profile=INFERENCE_PROFILES[self.config.reranker_profile])
                candidates = [{'chunk_id': c['chunk_id'], 'chunk_text': c['chunk_text']} for c in rows]
                scores = keyed_scores(candidates, self._reranker.score(request['query'], candidates))
                rows = sorted(rows, key=lambda c: (-scores[c['chunk_id']], c['rank'], c['chunk_id']))
                applied = True
            except Exception:
                # Model loading/inference/invalid keyed output must not lose usable vector evidence.
                scores = {}
                response['warnings'].append('Reranker unavailable; returned vector-only results.')
        response['results'] = [{**row, 'rank': i} for i, row in enumerate(rows[:request['top_k']], 1)]
        suffix = INFERENCE_PROFILES[self.config.reranker_profile].version if applied else 'vector-only'
        response['retrieval_config_version'] = f'{RETRIEVAL_CONFIG_VERSION}/{suffix}'
        validate_pair(request, response, 'search')
        return SearchEvidence(response, {r['chunk_id']: scores[r['chunk_id']]
                                         for r in response['results']} if applied else {}, applied)

    def get_chunks(self, request):
        """Exact authorized historical evidence, unchanged Retriever v1 contract."""
        return self._retriever().get_chunks(request)
