"""Adapter for ML1's canonical-document indexer result."""

from __future__ import annotations

from collections.abc import Callable, Mapping
from typing import Any, Protocol

from .article_processing import ArticleProcessingInput
from .event_extraction import EventTextChunk
from .operations import IndexedArticleVersion


class CanonicalDocumentIndexer(Protocol):
    """ML1 component that indexes one canonical saved document."""

    def index(self, document: Any) -> Mapping[str, Any]: ...


class ML1IndexerAdapter:
    """Convert the Go request into ML1's canonical input and validate its chunks.

    The injected ML1 indexer returns a mapping with ``document_version_id`` and
    ``chunks``; each chunk contains ``chunk_id`` and ``text``. Imports of ML1's
    R02 adapter are lazy so ML2 unit tests also run before that code is merged.
    """

    def __init__(
        self,
        indexer: CanonicalDocumentIndexer,
        *,
        document_factory: Callable[[Mapping[str, Any]], Any] | None = None,
    ) -> None:
        self._indexer = indexer
        self._document_factory = document_factory or _canonical_document_from_backend

    def index(self, request: ArticleProcessingInput) -> IndexedArticleVersion:
        payload = {
            "article": request.article,
            "source": request.source,
            "pipeline_version": request.pipeline_version,
            "trace_id": request.trace_id,
        }
        document = self._document_factory(payload)
        result = self._indexer.index(document)
        if not isinstance(result, Mapping):
            raise ValueError("ML1 indexer result must be an object")
        document_version_id = result.get("document_version_id")
        chunks = result.get("chunks")
        if not isinstance(chunks, (list, tuple)):
            raise ValueError("ML1 indexer result chunks must be a list")
        if document_version_id != request.article["article_version_id"]:
            raise ValueError("ML1 indexer document_version_id must match the submitted article version")
        try:
            event_chunks = tuple(
                EventTextChunk(chunk_id=chunk["chunk_id"], text=chunk["text"])
                for chunk in chunks
                if isinstance(chunk, Mapping)
            )
        except (KeyError, TypeError) as exc:
            raise ValueError("ML1 indexer chunks require chunk_id and text") from exc
        if len(event_chunks) != len(chunks):
            raise ValueError("ML1 indexer chunks must be objects")
        return IndexedArticleVersion(document_version_id, event_chunks)


def _canonical_document_from_backend(payload: Mapping[str, Any]) -> Any:
    try:
        from ml.adapters.backend_documents import document_from_backend
    except ImportError as exc:  # pragma: no cover - depends on ML1 merge state
        raise RuntimeError("ML1 R02 backend document adapter is unavailable") from exc
    return document_from_backend(dict(payload))
