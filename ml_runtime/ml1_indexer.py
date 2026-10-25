"""Adapter from the production ``ml.pipeline.ML1Pipeline`` to the ML2 boundary."""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from typing import Any, Protocol

from .article_processing import ArticleProcessingInput
from .event_extraction import EventTextChunk
from .operations import IndexedArticleVersion


class ML1Pipeline(Protocol):
    """The relevant synchronous methods exposed by ML1's indexed pipeline."""

    def index(self, payload: Mapping[str, Any], *, service_access: bool = False) -> Any: ...

    def get_chunks(self, request: Mapping[str, Any]) -> Mapping[str, Any]: ...


class ML1IndexerAdapter:
    """Run ML1's BackendInput indexer and resolve its newly indexed chunk IDs.

    ML1 ``index`` persists the canonical document/chunks/embeddings and returns
    chunk IDs. The worker marks those IDs service-accessible for this pipeline
    instance, then uses ML1's existing ``get_chunks`` boundary to obtain the
    exact stored chunk text for ML2 evidence.
    """

    def __init__(self, pipeline: ML1Pipeline) -> None:
        self._pipeline = pipeline

    def index(self, request: ArticleProcessingInput) -> IndexedArticleVersion:
        payload = {
            "article": request.article,
            "source": request.source,
            "pipeline_version": request.pipeline_version,
            "trace_id": request.trace_id,
        }
        result = self._pipeline.index(payload, service_access=True)
        quality = getattr(result, "quality", None)
        if getattr(quality, "status", None) != "PASS" or getattr(result, "indexed", None) is not True:
            raise RuntimeError("ML1 did not persist a quality-approved indexed document")
        if getattr(quality, "document_version_id", None) != request.article["article_version_id"]:
            raise RuntimeError("ML1 index result does not match the submitted article version")

        chunk_ids = getattr(result, "chunk_ids", None)
        if not isinstance(chunk_ids, (list, tuple)) or not chunk_ids:
            raise RuntimeError("ML1 index result contains no chunk IDs")
        if any(not isinstance(chunk_id, str) or not chunk_id.strip() for chunk_id in chunk_ids):
            raise RuntimeError("ML1 index result contains an invalid chunk ID")
        if len(chunk_ids) != len(set(chunk_ids)):
            raise RuntimeError("ML1 index result contains duplicate chunk IDs")

        response = self._pipeline.get_chunks({
            "contract_version": "v1",
            "trace_id": request.trace_id,
            "business_context_id": None,
            "chunk_ids": list(chunk_ids),
        })
        if not isinstance(response, Mapping):
            raise RuntimeError("ML1 get_chunks response must be an object")
        if response.get("status") != "ok" or response.get("error") is not None:
            raise RuntimeError("ML1 could not resolve the newly indexed chunks")
        if response.get("unresolved_chunk_ids") != []:
            raise RuntimeError("ML1 returned unresolved chunk IDs for a newly indexed document")

        rows = response.get("results")
        if not isinstance(rows, Sequence) or isinstance(rows, (str, bytes)) or len(rows) != len(chunk_ids):
            raise RuntimeError("ML1 get_chunks returned an incomplete chunk set")
        chunks: list[EventTextChunk] = []
        for row in rows:
            if not isinstance(row, Mapping):
                raise RuntimeError("ML1 get_chunks returned a malformed chunk")
            if row.get("document_version_id") != request.article["article_version_id"]:
                raise RuntimeError("ML1 get_chunks returned a chunk from another document version")
            chunks.append(EventTextChunk(chunk_id=row.get("chunk_id"), text=row.get("chunk_text")))
        if [chunk.chunk_id for chunk in chunks] != list(chunk_ids):
            raise RuntimeError("ML1 get_chunks changed the requested chunk order or identity")
        return IndexedArticleVersion(request.article["article_version_id"], tuple(chunks))
