"""Article-processing lifecycle and ML1/ML2 stage boundaries.

The included store is process-local scaffolding only. Production must provide a
transactional durable store before exposing POST/GET to the Go worker.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timezone
from threading import RLock
from typing import Any, Protocol
from uuid import uuid4

from .article_processing import ArticleProcessingInput, ProcessingContractError
from .quality_gate import DocumentQualityGate


@dataclass
class ArticleProcessingOperation:
    processing_id: str
    article_version_id: str
    pipeline_version: str
    trace_id: str
    created_at: str
    fingerprint: str
    state: str = "QUEUED"
    document_version_id: str | None = None
    event_ids: list[str] | None = None
    error: dict[str, Any] | None = None
    quality_result: dict[str, Any] | None = None

    def to_wire(self) -> dict[str, Any]:
        value: dict[str, Any] = {
            "processing_id": self.processing_id,
            "article_version_id": self.article_version_id,
            "pipeline_version": self.pipeline_version,
            "state": self.state,
            "trace_id": self.trace_id,
            "created_at": self.created_at,
        }
        if self.state == "SUCCEEDED":
            value["document_version_id"] = self.document_version_id
            value["event_ids"] = list(self.event_ids or [])
        if self.state == "FAILED":
            value["error"] = self.error
        return value


class OperationStore(Protocol):
    def create_or_get(self, request: ArticleProcessingInput) -> tuple[ArticleProcessingOperation, bool]: ...

    def get(self, processing_id: str) -> ArticleProcessingOperation | None: ...

    def get_input(self, processing_id: str) -> ArticleProcessingInput | None: ...

    def transition(self, processing_id: str, expected: str, target: str, **updates: Any) -> ArticleProcessingOperation: ...


class InMemoryOperationStore:
    """Single-process development store; operations disappear on restart."""

    def __init__(self) -> None:
        self._by_key: dict[str, ArticleProcessingOperation] = {}
        self._by_id: dict[str, ArticleProcessingOperation] = {}
        self._inputs: dict[str, ArticleProcessingInput] = {}
        self._lock = RLock()

    def create_or_get(self, request: ArticleProcessingInput) -> tuple[ArticleProcessingOperation, bool]:
        with self._lock:
            existing = self._by_key.get(request.idempotency_key)
            if existing is not None:
                if existing.fingerprint != request.fingerprint:
                    raise ProcessingContractError(
                        409, "CONFLICT", "Idempotency-Key was already used with different parameters"
                    )
                return existing, False
            operation = ArticleProcessingOperation(
                processing_id=str(uuid4()),
                article_version_id=request.article["article_version_id"],
                pipeline_version=request.pipeline_version,
                trace_id=request.trace_id,
                created_at=datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
                fingerprint=request.fingerprint,
            )
            self._by_key[request.idempotency_key] = operation
            self._by_id[operation.processing_id] = operation
            self._inputs[operation.processing_id] = request
            return operation, True

    def get(self, processing_id: str) -> ArticleProcessingOperation | None:
        with self._lock:
            return self._by_id.get(processing_id)

    def get_input(self, processing_id: str) -> ArticleProcessingInput | None:
        with self._lock:
            return self._inputs.get(processing_id)

    def transition(self, processing_id: str, expected: str, target: str, **updates: Any) -> ArticleProcessingOperation:
        with self._lock:
            operation = self._by_id.get(processing_id)
            if operation is None:
                raise KeyError(processing_id)
            if operation.state != expected:
                raise RuntimeError(f"invalid operation transition {operation.state} -> {target}")
            allowed = {
                "QUEUED": {"RUNNING", "FAILED"},
                "RUNNING": {"SUCCEEDED", "FAILED"},
            }
            if target not in allowed.get(expected, set()):
                raise RuntimeError(f"unsupported operation transition {expected} -> {target}")
            for key, value in updates.items():
                if key not in {"document_version_id", "event_ids", "error", "quality_result"}:
                    raise ValueError(f"unsupported operation update: {key}")
                setattr(operation, key, value)
            if target == "SUCCEEDED" and (not operation.document_version_id or operation.event_ids is None):
                raise ValueError("SUCCEEDED requires document_version_id and event_ids")
            if target == "FAILED" and operation.error is None:
                raise ValueError("FAILED requires a structured error")
            operation.state = target
            return operation


class Indexer(Protocol):
    """ML1 stage: return only after chunks and embeddings are searchable."""

    def index(self, request: ArticleProcessingInput) -> str: ...


class EventProcessor(Protocol):
    """ML2 stage: persist event updates and return their stable identifiers."""

    def process(self, request: ArticleProcessingInput, document_version_id: str) -> list[str]: ...


@dataclass
class ArticleProcessingCoordinator:
    """Runs quality, indexing, then event processing under one operation."""

    store: OperationStore
    quality_gate: DocumentQualityGate
    indexer: Indexer
    event_processor: EventProcessor

    def submit(self, request: ArticleProcessingInput) -> tuple[dict[str, Any], bool]:
        operation, created = self.store.create_or_get(request)
        return operation.to_wire(), created

    def get(self, processing_id: str) -> dict[str, Any] | None:
        operation = self.store.get(processing_id)
        return operation.to_wire() if operation else None

    def execute(self, processing_id: str) -> dict[str, Any]:
        operation = self.store.get(processing_id)
        if operation is None:
            raise KeyError(processing_id)
        if operation.state != "QUEUED":
            return operation.to_wire()
        request = self.store.get_input(processing_id)
        if request is None:
            raise RuntimeError("operation payload is missing from the operation store")
        self.store.transition(processing_id, "QUEUED", "RUNNING")
        quality_result: dict[str, Any] | None = None
        try:
            quality_result = self.quality_gate.assess(request)
            if not isinstance(quality_result, dict) or quality_result.get("status") not in {"PASS", "FAIL"}:
                raise RuntimeError("ML1 quality gate returned an invalid result")
            if quality_result["status"] == "FAIL":
                return self.store.transition(
                    processing_id,
                    "RUNNING",
                    "FAILED",
                    error={
                        "code": "ML_PROCESSING_ERROR",
                        "message": "Document did not pass ML quality checks",
                        "retryable": False,
                    },
                    quality_result=quality_result,
                ).to_wire()
            document_version_id = self.indexer.index(request)
            if not isinstance(document_version_id, str) or not document_version_id.strip():
                raise RuntimeError("ML1 indexer returned no document_version_id")
            event_ids = self.event_processor.process(request, document_version_id)
            if not isinstance(event_ids, list) or any(not isinstance(item, str) or not item.strip() for item in event_ids):
                raise RuntimeError("ML2 event processor returned invalid event_ids")
        except Exception:
            return self.store.transition(
                processing_id,
                "RUNNING",
                "FAILED",
                error={
                    "code": "ML_PROCESSING_ERROR",
                    "message": "Article processing failed in an ML stage",
                    "retryable": False,
                },
                quality_result=quality_result,
            ).to_wire()
        return self.store.transition(
            processing_id,
            "RUNNING",
            "SUCCEEDED",
            document_version_id=document_version_id,
            event_ids=event_ids,
            quality_result=quality_result,
        ).to_wire()
