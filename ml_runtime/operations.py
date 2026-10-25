"""Article-processing lifecycle and ML1/ML2 stage boundaries.

The in-memory store supports local development. PostgresOperationStore in
``postgres_store`` provides durable operation state for service deployments.
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

    def claim(self, processing_id: str) -> tuple[ArticleProcessingOperation, bool]: ...

    def claim_next(self) -> ArticleProcessingOperation | None: ...

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

    def claim(self, processing_id: str) -> tuple[ArticleProcessingOperation, bool]:
        """Atomically grant one in-process worker the queued operation."""
        with self._lock:
            operation = self._by_id.get(processing_id)
            if operation is None:
                raise KeyError(processing_id)
            if operation.state != "QUEUED":
                return operation, False
            operation.state = "RUNNING"
            return operation, True

    def claim_next(self) -> ArticleProcessingOperation | None:
        """Claim the oldest queued operation, including after process restart."""
        with self._lock:
            for operation in self._by_id.values():
                if operation.state == "QUEUED":
                    operation.state = "RUNNING"
                    return operation
        return None

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
        operation, claimed = self.store.claim(processing_id)
        if not claimed:
            return operation.to_wire()
        return self._execute_claimed(operation)

    def execute_next(self) -> dict[str, Any] | None:
        """Process one durable queued operation; a worker can call this on startup."""
        operation = self.store.claim_next()
        if operation is None:
            return None
        return self._execute_claimed(operation)

    def _execute_claimed(self, operation: ArticleProcessingOperation) -> dict[str, Any]:
        processing_id = operation.processing_id
        request = self.store.get_input(processing_id)
        if request is None:
            return self.store.transition(
                processing_id,
                "RUNNING",
                "FAILED",
                error={
                    "code": "INTERNAL_ERROR",
                    "message": "Saved article-processing input is unavailable",
                    "retryable": False,
                },
            ).to_wire()
        quality_result: dict[str, Any] | None = None
        try:
            quality_result = self.quality_gate.assess(request)
            if not isinstance(quality_result, dict) or quality_result.get("status") not in {"PASS", "FAIL"}:
                raise RuntimeError("ML1 quality gate returned an invalid result")
            _validate_quality_result_identity(request, quality_result)
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
            if document_version_id != request.article["article_version_id"]:
                raise RuntimeError("ML1 document_version_id does not match the submitted article_version_id")
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


def _validate_quality_result_identity(
    request: ArticleProcessingInput, result: dict[str, Any]
) -> None:
    expected = {
        "document_id": request.article["article_id"],
        "document_version_id": request.article["article_version_id"],
        "pipeline_version": request.pipeline_version,
    }
    for field, value in expected.items():
        if result.get(field) != value:
            raise RuntimeError(f"ML1 quality result {field} does not match the submitted article")
    if not isinstance(result.get("quality_rules_version"), str) or not result["quality_rules_version"].strip():
        raise RuntimeError("ML1 quality result has no quality_rules_version")
    if not isinstance(result.get("failures"), list) or not isinstance(result.get("warnings"), list):
        raise RuntimeError("ML1 quality result diagnostics are invalid")
