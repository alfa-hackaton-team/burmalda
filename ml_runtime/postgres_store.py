"""PostgreSQL operation store over ML1's shared ``ml.article_processings`` table."""

from __future__ import annotations

import base64
import json
import os
from datetime import datetime
from typing import Any, Callable
from uuid import UUID, uuid4

from .article_processing import ArticleProcessingInput, ProcessingContractError
from .operations import ArticleProcessingOperation


_COLUMNS = """
processing_id, article_version_id, pipeline_version, trace_id, created_at,
request_fingerprint, state, document_version_id, event_ids, quality_result,
error, event_pipeline_config_version
"""
_ALLOWED_TRANSITIONS = {
    "QUEUED": {"FAILED"},
    "RUNNING": {"SUCCEEDED", "FAILED"},
}
_JSON_FIELDS = {"event_ids", "quality_result", "error"}


class PostgresOperationStore:
    """Use ML1's operation identity and add ML2 request/result fields by migration."""

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
        payload = _request_payload(request)
        payload_text = payload.decode("ascii")
        snapshot = _request_snapshot(payload, request)
        with self._connect() as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    """INSERT INTO ml.article_processings (
                           processing_id, article_version_id, pipeline_version,
                           idempotency_key, request_fingerprint, request_snapshot,
                           trace_id, request_payload
                       ) VALUES (%s, %s, %s, %s, %s, %s::jsonb, %s, %s)
                       -- Both the idempotency key and article/version pair
                       -- are unique; concurrent identical posts may hit either.
                       ON CONFLICT DO NOTHING
                       RETURNING """ + _COLUMNS,
                    (
                        processing_id,
                        request.article["article_version_id"],
                        request.pipeline_version,
                        request.idempotency_key,
                        bytes.fromhex(request.fingerprint),
                        snapshot,
                        request.trace_id,
                        payload_text.encode("ascii"),
                    ),
                )
                row = cursor.fetchone()
                if row is not None:
                    return _operation_from_row(row), True

                cursor.execute(
                    f"SELECT {_COLUMNS} FROM ml.article_processings "
                    "WHERE idempotency_key = %s FOR UPDATE",
                    (request.idempotency_key,),
                )
                row = cursor.fetchone()
                if row is None:
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
                    f"SELECT {_COLUMNS} FROM ml.article_processings WHERE processing_id = %s",
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
                    """SELECT idempotency_key, encode(request_fingerprint, 'hex'), request_payload
                       FROM ml.article_processings WHERE processing_id = %s""",
                    (normalized_id,),
                )
                row = cursor.fetchone()
        if row is None:
            return None
        key, fingerprint, payload = row
        if isinstance(payload, memoryview):
            payload = payload.tobytes()
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
        """Atomically claim one queued operation; RUNNING is never auto-replayed."""
        normalized_id = str(UUID(processing_id))
        with self._connect() as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    f"""UPDATE ml.article_processings
                        SET state = 'RUNNING', updated_at = now()
                        WHERE processing_id = %s AND state = 'QUEUED'
                        RETURNING {_COLUMNS}""",
                    (normalized_id,),
                )
                row = cursor.fetchone()
                if row is not None:
                    return _operation_from_row(row), True
                cursor.execute(
                    f"SELECT {_COLUMNS} FROM ml.article_processings WHERE processing_id = %s",
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
                    f"""UPDATE ml.article_processings
                        SET state = 'RUNNING', updated_at = now()
                        WHERE processing_id = (
                            SELECT processing_id
                            FROM ml.article_processings
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
        allowed = {"document_version_id", "event_ids", "error", "quality_result"}
        unknown = set(updates) - allowed
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
        if target == "SUCCEEDED":
            result = {"event_ids": updates["event_ids"]}
            if updates.get("quality_result") is not None:
                result["quality_result"] = updates["quality_result"]
            assignments.append("result = %s::jsonb")
            values.append(json.dumps(result, ensure_ascii=True, separators=(",", ":")))
        values.extend((normalized_id, expected))

        with self._connect() as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    f"""UPDATE ml.article_processings
                        SET {', '.join(assignments)}
                        WHERE processing_id = %s AND state = %s
                        RETURNING {_COLUMNS}""",
                    tuple(values),
                )
                row = cursor.fetchone()
                if row is not None:
                    return _operation_from_row(row)
                cursor.execute(
                    f"SELECT {_COLUMNS} FROM ml.article_processings WHERE processing_id = %s",
                    (normalized_id,),
                )
                row = cursor.fetchone()
                if row is None:
                    raise KeyError(processing_id)
                raise RuntimeError(f"invalid operation transition {row[6]} -> {target}")


def _request_payload(request: ArticleProcessingInput) -> bytes:
    payload = {
        "article": request.article,
        "source": request.source,
        "pipeline_version": request.pipeline_version,
        "trace_id": request.trace_id,
    }
    return json.dumps(payload, ensure_ascii=True, separators=(",", ":")).encode("ascii")


def _request_snapshot(payload: bytes, request: ArticleProcessingInput) -> str:
    values = (request.article, request.source, request.pipeline_version, request.trace_id)
    if any(_contains_unsafe_json_text(value) for value in values):
        snapshot = {
            "encoding": "base64-json-request",
            "payload": base64.b64encode(payload).decode("ascii"),
        }
        return json.dumps(snapshot, separators=(",", ":"))
    return payload.decode("ascii")


def _contains_unsafe_json_text(value: Any) -> bool:
    if isinstance(value, str):
        return any(ord(char) == 0 or 0xD800 <= ord(char) <= 0xDFFF for char in value)
    if isinstance(value, dict):
        return any(_contains_unsafe_json_text(key) or _contains_unsafe_json_text(item)
                   for key, item in value.items())
    if isinstance(value, (list, tuple)):
        return any(_contains_unsafe_json_text(item) for item in value)
    return False


def _operation_from_row(row: tuple[Any, ...]) -> ArticleProcessingOperation:
    created_at = row[4]
    if isinstance(created_at, datetime):
        created_at = created_at.isoformat().replace("+00:00", "Z")
    fingerprint = row[5].hex() if isinstance(row[5], (bytes, bytearray, memoryview)) else str(row[5])
    return ArticleProcessingOperation(
        processing_id=str(row[0]),
        article_version_id=str(row[1]),
        pipeline_version=row[2],
        trace_id=row[3],
        created_at=created_at,
        fingerprint=fingerprint,
        state=row[6],
        document_version_id=str(row[7]) if row[7] is not None else None,
        event_ids=[str(item) for item in row[8]] if row[8] is not None else None,
        quality_result=row[9],
        error=row[10],
    )
