"""Structural and evidence validation at the ML2 result/storage boundary."""

from __future__ import annotations

from datetime import datetime, timezone
import unicodedata
from typing import Any
from uuid import UUID

from .event_extraction import (
    CandidateEvent,
    EVENT_EXTRACTION_CONTRACT_VERSION,
    EventExtractionResult,
)
from .operations import IndexedArticleVersion


class EventResultContractError(ValueError):
    """An extraction result does not satisfy the versioned storage contract."""


def parse_event_extraction_result(value: Any) -> EventExtractionResult:
    """Parse the JSON DTO without trusting the extractor implementation."""
    if isinstance(value, EventExtractionResult):
        value = value.to_dict()
    if not isinstance(value, dict) or set(value) != {
        "contract_version", "document_version_id", "event_pipeline_config_version", "events",
    }:
        raise EventResultContractError("event result has an invalid top-level shape")
    if value["contract_version"] != EVENT_EXTRACTION_CONTRACT_VERSION:
        raise EventResultContractError("event result contract_version is unsupported")
    document_version_id = _nonempty(value["document_version_id"], "document_version_id")
    _uuid(document_version_id, "document_version_id")
    config_version = _nonempty(value["event_pipeline_config_version"], "event_pipeline_config_version")
    if not isinstance(value["events"], list):
        raise EventResultContractError("events must be an array")

    events: list[CandidateEvent] = []
    identities: set[tuple[Any, ...]] = set()
    for index, raw in enumerate(value["events"]):
        if not isinstance(raw, dict) or set(raw) != {
            "event_type", "title", "summary", "occurred_at", "geography",
            "status", "confidence", "evidence",
        }:
            raise EventResultContractError(f"event {index} has an invalid shape")
        event_type = _nonempty(raw["event_type"], f"events[{index}].event_type")
        title = _nonempty(raw["title"], f"events[{index}].title")
        summary = _nonempty(raw["summary"], f"events[{index}].summary")
        if raw["status"] != "candidate" or raw["confidence"] is not None:
            raise EventResultContractError(f"event {index} has an unsupported status or confidence")
        occurred_at = _date_time(raw["occurred_at"], index)
        geography = _geography(raw["geography"], index)
        evidence = raw["evidence"]
        if not isinstance(evidence, list) or not evidence:
            raise EventResultContractError(f"event {index} requires evidence")

        quotes: list[str] = []
        chunk_ids_by_quote: list[list[str]] = []
        quote_positions: dict[str, int] = {}
        for evidence_index, item in enumerate(evidence):
            if not isinstance(item, dict) or set(item) != {"chunk_id", "quote_text", "evidence_role"}:
                raise EventResultContractError(f"event {index} evidence {evidence_index} has an invalid shape")
            chunk_id = _nonempty(item["chunk_id"], f"events[{index}].evidence[{evidence_index}].chunk_id")
            _uuid(chunk_id, f"events[{index}].evidence[{evidence_index}].chunk_id")
            quote = _nonempty(item["quote_text"], f"events[{index}].evidence[{evidence_index}].quote_text")
            if item["evidence_role"] != "support":
                raise EventResultContractError("event extraction may emit support evidence only")
            if quote not in quote_positions:
                quote_positions[quote] = len(quotes)
                quotes.append(quote)
                chunk_ids_by_quote.append([])
            position = quote_positions[quote]
            if chunk_id in chunk_ids_by_quote[position]:
                raise EventResultContractError("duplicate evidence for the same chunk and quote")
            chunk_ids_by_quote[position].append(chunk_id)

        identity = _identity(event_type, title, occurred_at, geography)
        if identity in identities:
            raise EventResultContractError("event result contains duplicate candidates")
        identities.add(identity)
        events.append(CandidateEvent(
            event_type=event_type,
            title=title,
            summary=summary,
            occurred_at=occurred_at,
            geography=tuple(geography),
            confidence=None,
            evidence_quotes=tuple(quotes),
            evidence_chunk_ids=tuple(tuple(ids) for ids in chunk_ids_by_quote),
        ))

    return EventExtractionResult(
        document_version_id=document_version_id,
        event_pipeline_config_version=config_version,
        events=tuple(events),
        contract_version=value["contract_version"],
    )


def validate_event_extraction_result(
    result: Any,
    indexed: IndexedArticleVersion,
    expected_config_version: str,
) -> EventExtractionResult:
    """Check identity and every evidence reference against ML1's indexed chunks."""
    parsed = parse_event_extraction_result(result)
    if parsed.document_version_id != indexed.document_version_id:
        raise EventResultContractError("event result document_version_id does not match indexed version")
    if parsed.event_pipeline_config_version != expected_config_version:
        raise EventResultContractError("event result configuration version does not match configured version")
    chunks = {chunk.chunk_id: chunk.text for chunk in indexed.chunks}
    for event_index, event in enumerate(parsed.events):
        for quote_index, quote in enumerate(event.evidence_quotes):
            chunk_ids = event.evidence_chunk_ids[quote_index]
            if not chunk_ids:
                raise EventResultContractError(f"event {event_index} evidence has no chunk reference")
            for chunk_id in chunk_ids:
                if chunk_id not in chunks:
                    raise EventResultContractError(f"event {event_index} references an unknown chunk")
                if quote not in chunks[chunk_id]:
                    raise EventResultContractError(f"event {event_index} quote does not match its chunk")
    return parsed


def _nonempty(value: Any, label: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise EventResultContractError(f"{label} must be a non-empty string")
    return value.strip()


def _uuid(value: str, label: str) -> None:
    try:
        UUID(value)
    except ValueError as exc:
        raise EventResultContractError(f"{label} must be a UUID") from exc


def _date_time(value: Any, index: int) -> str | None:
    if value is None:
        return None
    if not isinstance(value, str):
        raise EventResultContractError(f"event {index} occurred_at must be a date-time or null")
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise EventResultContractError(f"event {index} occurred_at is invalid") from exc
    if parsed.tzinfo is None:
        raise EventResultContractError(f"event {index} occurred_at must include a timezone")
    return value


def _geography(value: Any, index: int) -> list[dict[str, str | None]]:
    if not isinstance(value, list):
        raise EventResultContractError(f"event {index} geography must be an array")
    result = []
    for place in value:
        if not isinstance(place, dict) or set(place) != {"type", "code", "name"}:
            raise EventResultContractError(f"event {index} contains invalid geography")
        place_type = _nonempty(place["type"], f"event {index} geography type")
        name = _nonempty(place["name"], f"event {index} geography name")
        code = place["code"]
        if code is not None and (not isinstance(code, str) or not code.strip()):
            raise EventResultContractError(f"event {index} geography code is invalid")
        result.append({"type": place_type, "code": code, "name": name})
    return result


def _identity(
    event_type: str,
    title: str,
    occurred_at: str | None,
    geography: list[dict[str, str | None]],
) -> tuple[Any, ...]:
    def normalize(text: str) -> str:
        return " ".join(unicodedata.normalize("NFKC", text).casefold().split())

    normalized_geo = tuple(sorted(
        (normalize(place["type"]), normalize(place["code"] or ""), normalize(place["name"]))
        for place in geography
    ))
    normalized_date = None
    if occurred_at is not None:
        parsed = datetime.fromisoformat(occurred_at.replace("Z", "+00:00"))
        normalized_date = parsed.astimezone(timezone.utc).isoformat(timespec="microseconds")
    return normalize(event_type), normalize(title), normalized_date, normalized_geo
