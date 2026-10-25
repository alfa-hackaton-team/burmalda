"""PostgreSQL-backed idempotent article-processing operation store."""

from __future__ import annotations

import json
import os
from datetime import datetime
from typing import Any, Callable
from uuid import UUID, uuid4

from .article_processing import ArticleProcessingInput, ProcessingContractError
from .operations import ArticleProcessingOperation


_COLUMNS = """
processing_id, article_version_id, pipeline_version, trace_id, created_at,
request_fingerprint, state, document_version_id, event_ids, error, quality_result
"""
_ALLOWED_TRANSITIONS = {
    "QUEUED": {"FAILED"},
    "RUNNING": {"SUCCEEDED", "FAILED"},
}
_JSON_FIELDS = {"event_ids", "error", "quality_result"}


class PostgresOperationStore:
    """Persist request snapshots and operation outcomes in the ML-owned schema.

    The connection factory is injectable for integration tests. The normal
    constructor reads ML_DATABASE_URL and imports psycopg only when connecting.
    """

    def __init__(
        self,
        dsn: str | None = None,
        *,
        connection_factory: Callable[[], Any] | None = None,
    ) -> None:
        self._dsn = dsn if dsn is not None else os.environ.get("ML_DATABASE_URL", "")
        self._connection_factory = connection_factory
        if not self._dsn and connection_factory is None:
            raise ValueError("ML_DATABASE_URL must be configured")

    def _connect(self):
        if self._connection_factory is not None:
            return self._connection_factory()
        try:
            import psycopg
        except ImportError as exc:  # pragma: no cover - depends on deployment extras
            raise RuntimeError("Install the psycopg runtime dependency for PostgreSQL storage") from exc
        return psycopg.connect(self._dsn)

    def create_or_get(self, request: ArticleProcessingInput) -> tuple[ArticleProcessingOperation, bool]:
        processing_id = str(uuid4())
        request_payload = {
            "article": request.article,
            "source": request.source,
            "pipeline_version": request.pipeline_version,
            "trace_id": request.trace_id,
        }
        with self._connect() as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    """
                    INSERT INTO ml.article_processing_operations (
                        processing_id, idempotency_key, request_fingerprint,
                        article_version_id, pipeline_version, trace_id, request_payload
                    ) VALUES (%s, %s, %s, %s, %s, %s, %s)
                    ON CONFLICT (idempotency_key) DO NOTHING
                    RETURNING """ + _COLUMNS,
                    (
                        processing_id,
                        request.idempotency_key,
                        request.fingerprint,
                        request.article["article_version_id"],
                        request.pipeline_version,
                        request.trace_id,
                        json.dumps(request_payload, ensure_ascii=True, separators=(",", ":")).encode("ascii"),
                    ),
                )
                row = cursor.fetchone()
                if row is not None:
                    return _operation_from_row(row), True

                cursor.execute(
                    f"SELECT {_COLUMNS} FROM ml.article_processing_operations "
                    "WHERE idempotency_key = %s FOR UPDATE",
                    (request.idempotency_key,),
                )
                row = cursor.fetchone()
                if row is None:
                    # The conflicting row can only disappear through an
                    # administrative delete; do not silently create a new run.
                    raise RuntimeError("Idempotent operation disappeared during lookup")
                operation = _operation_from_row(row)
                if operation.fingerprint != request.fingerprint:
                    raise ProcessingContractError(
                        409, "CONFLICT", "Idempotency-Key was already used with different parameters"
                    )
                return operation, False

    def get(self, processing_id: str) -> ArticleProcessingOperation | None:
        try:
            normalized_id = str(UUID(processing_id))
        except (ValueError, TypeError, AttributeError):
            return None
        with self._connect() as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    f"SELECT {_COLUMNS} FROM ml.article_processing_operations WHERE processing_id = %s",
                    (normalized_id,),
                )
                row = cursor.fetchone()
                return _operation_from_row(row) if row is not None else None

    def get_input(self, processing_id: str) -> ArticleProcessingInput | None:
        try:
            normalized_id = str(UUID(processing_id))
        except (ValueError, TypeError, AttributeError):
            return None
        with self._connect() as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    """SELECT idempotency_key, request_fingerprint, request_payload
                       FROM ml.article_processing_operations WHERE processing_id = %s""",
                    (normalized_id,),
                )
                row = cursor.fetchone()
        if row is None:
            return None
        key, fingerprint, payload = row
        if isinstance(payload, bytes):
            payload = payload.decode("ascii")
        if isinstance(payload, str):
            payload = json.loads(payload)
        return ArticleProcessingInput(
            article=payload["article"],
            source=payload["source"],
            pipeline_version=payload["pipeline_version"],
            trace_id=payload["trace_id"],
            idempotency_key=key,
            fingerprint=fingerprint,
        )

    def claim(self, processing_id: str) -> tuple[ArticleProcessingOperation, bool]:
        """Atomically claim a queued operation; RUNNING is never auto-replayed."""
        normalized_id = str(UUID(processing_id))
        with self._connect() as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    f"""UPDATE ml.article_processing_operations
                        SET state = 'RUNNING', attempt_count = attempt_count + 1,
                            started_at = COALESCE(started_at, now()), updated_at = now()
                        WHERE processing_id = %s AND state = 'QUEUED'
                        RETURNING {_COLUMNS}""",
                    (normalized_id,),
                )
                row = cursor.fetchone()
                if row is not None:
                    return _operation_from_row(row), True
                cursor.execute(
                    f"SELECT {_COLUMNS} FROM ml.article_processing_operations WHERE processing_id = %s",
                    (normalized_id,),
                )
                row = cursor.fetchone()
                if row is None:
                    raise KeyError(processing_id)
                return _operation_from_row(row), False

    def claim_next(self) -> ArticleProcessingOperation | None:
        """Claim one queued operation with SKIP LOCKED for concurrent workers."""
        with self._connect() as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    f"""UPDATE ml.article_processing_operations
                        SET state = 'RUNNING', attempt_count = attempt_count + 1,
                            started_at = COALESCE(started_at, now()), updated_at = now()
                        WHERE processing_id = (
                            SELECT processing_id
                            FROM ml.article_processing_operations
                            WHERE state = 'QUEUED'
                            ORDER BY created_at, processing_id
                            FOR UPDATE SKIP LOCKED
                            LIMIT 1
                        )
                        RETURNING {_COLUMNS}"""
                )
                row = cursor.fetchone()
                return _operation_from_row(row) if row is not None else None

    def transition(
        self, processing_id: str, expected: str, target: str, **updates: Any
    ) -> ArticleProcessingOperation:
        normalized_id = str(UUID(processing_id))
        if target not in _ALLOWED_TRANSITIONS.get(expected, set()):
            raise RuntimeError(f"unsupported operation transition {expected} -> {target}")
        unknown = set(updates) - {"document_version_id", "event_ids", "error", "quality_result"}
        if unknown:
            raise ValueError(f"unsupported operation update: {', '.join(sorted(unknown))}")
        if target == "SUCCEEDED" and (
            not updates.get("document_version_id") or updates.get("event_ids") is None
        ):
            raise ValueError("SUCCEEDED requires document_version_id and event_ids")
        if target == "FAILED" and updates.get("error") is None:
            raise ValueError("FAILED requires a structured error")

        assignments = ["state = %s", "updated_at = now()"]
        values: list[Any] = [target]
        for key, value in updates.items():
            if key in _JSON_FIELDS:
                assignments.append(f"{key} = %s::jsonb")
                values.append(
                    json.dumps(value, ensure_ascii=True, separators=(",", ":"))
                    if value is not None else None
                )
            else:
                assignments.append(f"{key} = %s")
                values.append(value)
        if target in {"SUCCEEDED", "FAILED"}:
            assignments.append("finished_at = now()")
        values.extend((normalized_id, expected))

        with self._connect() as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    f"""UPDATE ml.article_processing_operations
                        SET {', '.join(assignments)}
                        WHERE processing_id = %s AND state = %s
                        RETURNING {_COLUMNS}""",
                    tuple(values),
                )
                row = cursor.fetchone()
                if row is not None:
                    return _operation_from_row(row)
                cursor.execute(
                    f"SELECT {_COLUMNS} FROM ml.article_processing_operations WHERE processing_id = %s",
                    (normalized_id,),
                )
                row = cursor.fetchone()
                if row is None:
                    raise KeyError(processing_id)
                raise RuntimeError(f"invalid operation transition {row[6]} -> {target}")


def _operation_from_row(row: tuple[Any, ...]) -> ArticleProcessingOperation:
    created_at = row[4]
    if isinstance(created_at, datetime):
        created_at = created_at.isoformat().replace("+00:00", "Z")
    return ArticleProcessingOperation(
        processing_id=str(row[0]),
        article_version_id=row[1],
        pipeline_version=row[2],
        trace_id=row[3],
        created_at=created_at,
        fingerprint=row[5],
        state=row[6],
        document_version_id=row[7],
        event_ids=row[8],
        error=row[9],
        quality_result=row[10],
    )
