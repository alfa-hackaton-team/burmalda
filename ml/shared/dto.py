"""JSON-compatible retrieval DTOs. Canonical definitions live in docs/ml/contracts/v1.

TypedDict provides static hints, not runtime validation or coercion. Nullable fields
remain required; validate() checks the canonical JSON Schema at the boundary.
"""
from typing import Literal, TypedDict

Status = Literal['ok', 'empty', 'partial', 'error']
SourceRole = Literal['external', 'bank', 'expert']
ErrorCode = Literal[
    'INVALID_REQUEST', 'UNSUPPORTED_VERSION', 'SEARCH_UNAVAILABLE', 'TIMEOUT',
    'CONTEXT_MISMATCH', 'MODEL_UNAVAILABLE', 'BUDGET_EXCEEDED',
    'VERIFICATION_FAILED', 'PERSISTENCE_UNAVAILABLE',
]


class Error(TypedDict):
    code: ErrorCode
    message: str
    retryable: bool


class Chunk(TypedDict):
    chunk_id: str
    document_id: str
    document_version_id: str
    chunk_text: str
    title: str
    canonical_url: str | None
    published_at: str | None
    retrieved_at: str
    source_id: str
    source_name: str
    source_type: Literal['news', 'bank_analytics', 'expert', 'regulator']
    source_role: SourceRole
    trust_level: Literal['primary', 'trusted', 'standard', 'unknown']


class RankedChunk(Chunk):
    rank: int
    retrieval_score: float | None


class SearchFilters(TypedDict):
    published_from: str | None
    published_to: str | None
    source_ids: list[str]
    source_roles: list[SourceRole]
    document_version_ids: list[str]
    include_undated: bool


class SearchRequest(TypedDict):
    contract_version: Literal['v1']
    trace_id: str
    business_context_id: str | None
    query: str
    filters: SearchFilters
    top_k: int


class SearchResponse(TypedDict):
    contract_version: Literal['v1']
    trace_id: str
    status: Status
    results: list[RankedChunk]
    retrieval_config_version: str
    warnings: list[str]
    error: Error | None


class GetChunksRequest(TypedDict):
    contract_version: Literal['v1']
    trace_id: str
    business_context_id: str | None
    chunk_ids: list[str]


class GetChunksResponse(TypedDict):
    contract_version: Literal['v1']
    trace_id: str
    status: Status
    results: list[Chunk]
    unresolved_chunk_ids: list[str]
    warnings: list[str]
    error: Error | None
