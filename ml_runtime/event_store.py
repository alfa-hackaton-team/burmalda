"""PostgreSQL persistence for evidence-checked ML2 event candidates."""

from __future__ import annotations

import hashlib
import json
import os
import re
import unicodedata
from datetime import datetime, timezone
from typing import Any, Callable, Protocol
from uuid import uuid4

from .article_processing import ArticleProcessingInput
from .event_extraction import CandidateEvent, LLMEventExtractor


_SPACE = re.compile(r"\s+")


class EventCandidateStore(Protocol):
    def save_candidates(
        self,
        document_version_id: str,
        pipeline_version: str,
        candidates: list[CandidateEvent],
    ) -> list[str]: ...


class PostgresEventStore:
    """Persist events and article links atomically in the ML-owned schema.

    Cross-document merging is deliberately conservative: candidates share an
    event only when normalized event type, title, occurrence time, and geography
    match exactly. Broader semantic matching remains a separate review step.
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

    def save_candidates(
        self,
        document_version_id: str,
        pipeline_version: str,
        candidates: list[CandidateEvent],
    ) -> list[str]:
        dedupe_keys = [event_dedupe_key(candidate) for candidate in candidates]
        if len(dedupe_keys) != len(set(dedupe_keys)):
            raise ValueError("duplicate normalized event identities in one document")
        event_ids: list[str] = []
        with self._connect() as connection:
            with connection.cursor() as cursor:
                for index, candidate in enumerate(candidates):
                    event_id = self._insert_or_find_event(cursor, candidate)
                    fingerprint = candidate_fingerprint(candidate)
                    quotes = json.dumps(
                        list(candidate.evidence_quotes), ensure_ascii=True, separators=(",", ":")
                    )
                    cursor.execute(
                        """INSERT INTO ml.event_documents (
                               event_id, document_version_id, pipeline_version,
                               candidate_index, candidate_fingerprint, evidence_quotes
                           ) VALUES (%s, %s, %s, %s, %s, %s::jsonb)
                           ON CONFLICT (document_version_id, pipeline_version, candidate_index)
                           DO NOTHING
                           RETURNING event_id, candidate_fingerprint""",
                        (
                            event_id, document_version_id, pipeline_version,
                            index, fingerprint, quotes,
                        ),
                    )
                    row = cursor.fetchone()
                    if row is None:
                        cursor.execute(
                            """SELECT event_id, candidate_fingerprint FROM ml.event_documents
                               WHERE document_version_id = %s AND pipeline_version = %s
                                 AND candidate_index = %s""",
                            (document_version_id, pipeline_version, index),
                        )
                        prior = cursor.fetchone()
                        if prior is None or str(prior[0]) != event_id or prior[1].strip() != fingerprint:
                            raise RuntimeError(
                                "Event extraction for this document version and pipeline changed on replay"
                            )
                    if event_id not in event_ids:
                        event_ids.append(event_id)
        return event_ids

    @staticmethod
    def _insert_or_find_event(cursor: Any, candidate: CandidateEvent) -> str:
        dedupe_key = event_dedupe_key(candidate)
        geography = json.dumps(
            list(candidate.geography), ensure_ascii=True, sort_keys=True, separators=(",", ":")
        )
        cursor.execute(
            """INSERT INTO ml.events (
                   event_id, event_type, title, summary, occurred_at, geography,
                   status, confidence, dedupe_key
               ) VALUES (%s, %s, %s, %s, %s, %s::jsonb, 'candidate', %s, %s)
               ON CONFLICT (dedupe_key) DO NOTHING
               RETURNING event_id""",
            (
                str(uuid4()), candidate.event_type, candidate.title, candidate.summary,
                candidate.occurred_at, geography, candidate.confidence, dedupe_key,
            ),
        )
        row = cursor.fetchone()
        if row is not None:
            return str(row[0])
        cursor.execute("SELECT event_id FROM ml.events WHERE dedupe_key = %s", (dedupe_key,))
        row = cursor.fetchone()
        if row is None:
            raise RuntimeError("Deduplicated event disappeared during persistence")
        return str(row[0])


class ML2EventProcessor:
    """Extract, verify, and persist candidate events for one article version."""

    def __init__(self, extractor: LLMEventExtractor, store: EventCandidateStore) -> None:
        self.extractor = extractor
        self.store = store

    def process(self, request: ArticleProcessingInput, document_version_id: str) -> list[str]:
        if document_version_id != request.article["article_version_id"]:
            raise ValueError("event processing document_version_id must match article_version_id")
        candidates = self.extractor.extract(request)
        return self.store.save_candidates(
            document_version_id, request.pipeline_version, candidates
        )


def event_dedupe_key(candidate: CandidateEvent) -> str:
    """Fingerprint an exact normalized event identity, not a semantic topic."""
    geography = sorted(
        (
            _normalize(place["type"]),
            _normalize(place["code"] or ""),
            _normalize(place["name"]),
        )
        for place in candidate.geography
    )
    identity = {
        "event_type": _normalize(candidate.event_type),
        "title": _normalize(candidate.title),
        "occurred_at": _normalize_datetime(candidate.occurred_at),
        "geography": geography,
    }
    stable = json.dumps(identity, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(stable.encode("utf-8")).hexdigest()


def candidate_fingerprint(candidate: CandidateEvent) -> str:
    stable = json.dumps(
        candidate.to_dict(), ensure_ascii=False, sort_keys=True, separators=(",", ":")
    )
    return hashlib.sha256(stable.encode("utf-8")).hexdigest()


def _normalize(value: str) -> str:
    normalized = unicodedata.normalize("NFKC", value).casefold()
    return _SPACE.sub(" ", normalized).strip()


def _normalize_datetime(value: str | None) -> str | None:
    if value is None:
        return None
    parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if parsed.tzinfo is None:
        raise ValueError("event occurred_at must include a timezone")
    return parsed.astimezone(timezone.utc).isoformat(timespec="microseconds")
