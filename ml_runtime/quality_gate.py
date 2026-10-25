"""Adapter from the ML2 article-processing flow to ML1's R02 quality gate."""

from __future__ import annotations

from typing import Any, Protocol

from .article_processing import ArticleProcessingInput


class DocumentQualityGate(Protocol):
    def assess(self, request: ArticleProcessingInput) -> dict[str, Any]: ...


class ML1R02QualityGate:
    """Use ML1's canonical adapter and deterministic quality rules.

    Imports are lazy because ML1 R02 is delivered in the sibling PR and may not
    be present in the shared base until that PR is merged.
    """

    @property
    def max_text_bytes(self) -> int:
        """Expose ML1's authoritative R02 limit to the HTTP configuration check."""
        from ml.ingestion.quality import MAX_TEXT_BYTES

        return MAX_TEXT_BYTES

    def assess(self, request: ArticleProcessingInput) -> dict[str, Any]:
        from ml.adapters.backend_documents import document_from_backend
        from ml.ingestion.quality import check_document

        payload = {
            "article": request.article,
            "source": request.source,
            "pipeline_version": request.pipeline_version,
            "trace_id": request.trace_id,
        }
        document = document_from_backend(payload)
        return check_document(document).to_dict()
