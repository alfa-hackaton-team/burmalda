"""Synchronous logical API used by ML2; no HTTP or SDK dependency."""
from typing import Protocol

from ml.shared.dto import GetChunksRequest, GetChunksResponse, SearchRequest, SearchResponse


class Retriever(Protocol):
    def search(self, request: SearchRequest) -> SearchResponse: ...

    def get_chunks(self, request: GetChunksRequest) -> GetChunksResponse: ...
