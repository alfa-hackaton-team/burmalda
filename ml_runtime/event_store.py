"""PostgreSQL persistence for evidence-checked ML2 event candidates."""

from __future__ import annotations

import hashlib
import json
import os
from typing import Any, Callable, Protocol
from uuid import uuid4

from .article_processing import ArticleProcessingInput
from .event_extraction import (
    CandidateEvent,
    EventExtractionResult,
    LLMEventExtractor,
)
from .operations import IndexedArticleVersion
from .event_result_contract import validate_event_extraction_result


class EventCandidateStore(Protocol):
    def get_saved_event_ids(
        self, document_version_id: str, pipeline_version: str,
        event_pipeline_config_version: str,
    ) -> list[str] | None: ...

    def save_result(
        self, result: EventExtractionResult, indexed: IndexedArticleVersion,
        pipeline_version: str,
    ) -> list[str]: ...


class PostgresEventStore:
    """Persist events and article links atomically in the ML-owned schema.

    Each operation owns its candidate set. Cross-document event merging is
    deliberately deferred until a reviewed merge policy exists.
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
            raise RuntimeError("Install the psycopg runtime dependency for PostgreSQL event storage") from exc
        return psycopg.connect(self._dsn)

    def get_saved_event_ids(
        self, document_version_id: str, pipeline_version: str,
        event_pipeline_config_version: str,
    ) -> list[str] | None:
        with self._connect() as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    """SELECT event_ids, event_pipeline_config_version
                       FROM ml.article_processing_operations
                       WHERE article_version_id = %s AND pipeline_version = %s""",
                    (document_version_id, pipeline_version),
                )
                row = cursor.fetchone()
                if row is None or row[0] is None:
                    return None
                if row[1] != event_pipeline_config_version:
                    raise RuntimeError("Saved event result uses a different event pipeline configuration")
                return [str(item) for item in row[0]]

    def save_result(
        self, result: EventExtractionResult, indexed: IndexedArticleVersion,
        pipeline_version: str,
    ) -> list[str]:
        result = validate_event_extraction_result(
            result, indexed, result.event_pipeline_config_version
        )
        document_version_id = result.document_version_id
        config_version = result.event_pipeline_config_version
        event_ids: list[str] = []
        with self._connect() as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    """SELECT event_ids, event_pipeline_config_version
                       FROM ml.article_processing_operations
                       WHERE article_version_id = %s AND pipeline_version = %s
                       FOR UPDATE""",
                    (document_version_id, pipeline_version),
                )
                operation = cursor.fetchone()
                if operation is None:
                    raise RuntimeError("Event result has no saved article-processing operation")
                if operation[0] is not None:
                    if operation[1] != config_version:
                        raise RuntimeError("Saved event result uses a different event pipeline configuration")
                    return [str(item) for item in operation[0]]

                for index, candidate in enumerate(result.events):
                    event_id = str(uuid4())
                    fingerprint = candidate_fingerprint(candidate)
                    evidence = json.dumps(
                        candidate.to_result_dict()["evidence"],
                        ensure_ascii=True, separators=(",", ":"),
                    )
                    geography = json.dumps(
                        list(candidate.geography), ensure_ascii=True, sort_keys=True, separators=(",", ":")
                    )
                    cursor.execute(
                        """INSERT INTO ml.events (
                               id, event_type, title, summary, occurred_at,
                               geography, status, confidence
                           ) VALUES (%s, %s, %s, %s, %s, %s::jsonb, 'candidate', %s)""",
                        (event_id, candidate.event_type, candidate.title, candidate.summary,
                         candidate.occurred_at, geography, candidate.confidence),
                    )
                    cursor.execute(
                        """INSERT INTO ml.event_documents (
                               event_id, document_version_id, event_pipeline_config_version,
                               candidate_index, candidate_fingerprint, evidence,
                               evidence_role, is_primary
                           ) VALUES (%s, %s, %s, %s, %s, %s::jsonb, 'support', %s)""",
                        (event_id, document_version_id, config_version, index, fingerprint,
                         evidence, False),
                    )
                    event_ids.append(event_id)
                cursor.execute(
                    """UPDATE ml.article_processing_operations
                       SET event_ids = %s::jsonb, event_pipeline_config_version = %s,
                           updated_at = now()
                       WHERE article_version_id = %s AND pipeline_version = %s""",
                    (json.dumps(event_ids), config_version, document_version_id, pipeline_version),
                )
        return event_ids


class ML2EventProcessor:
    """Extract, verify, and persist candidate events for one article version."""

    def __init__(
        self,
        extractor: LLMEventExtractor,
        store: EventCandidateStore,
    ) -> None:
        self.extractor = extractor
        self.store = store

    def process(self, request: ArticleProcessingInput, indexed: IndexedArticleVersion) -> list[str]:
        document_version_id = indexed.document_version_id
        if document_version_id != request.article["article_version_id"]:
            raise ValueError("event processing document_version_id must match article_version_id")
        config_version = self.extractor.event_pipeline_config_version
        prior = self.store.get_saved_event_ids(
            document_version_id, request.pipeline_version, config_version
        )
        if prior is not None:
            return prior
        if not indexed.chunks:
            raise RuntimeError("ML1 returned no indexed chunks for the document version")
        result = self.extractor.extract(request, chunks=list(indexed.chunks))
        result = validate_event_extraction_result(result, indexed, config_version)
        return self.store.save_result(result, indexed, request.pipeline_version)


def candidate_fingerprint(candidate: CandidateEvent) -> str:
    stable = json.dumps(
        candidate.to_dict(), ensure_ascii=False, sort_keys=True, separators=(",", ":")
    )
    return hashlib.sha256(stable.encode("utf-8")).hexdigest()
