"""Synchronous logical API used by ML2; no HTTP or SDK dependency."""
from typing import Protocol
from dataclasses import dataclass

from ml.shared.dto import GetChunksRequest, GetChunksResponse, SearchRequest, SearchResponse


@dataclass(frozen=True)
class DocumentVersion:
    """Trusted version ordering/readiness supplied by the calling application.

    Not a wire DTO and never populated from an untrusted search request.
    """

    document_id: str
    document_version_id: str
    version_no: int
    ready: bool = True


class Retriever(Protocol):
    def search(self, request: SearchRequest) -> SearchResponse: ...

    def get_chunks(self, request: GetChunksRequest) -> GetChunksResponse: ...
