"""Validation of Go -> ML AnalysisRequest v1 before agent execution."""

from __future__ import annotations

import json
from functools import lru_cache
from pathlib import Path
from typing import Any

from jsonschema import Draft202012Validator, FormatChecker


_SCHEMA_PATH = Path(__file__).resolve().parents[1] / "docs/ml/contracts/v1/contracts.schema.json"
_CONTRACTS = json.loads(_SCHEMA_PATH.read_text(encoding="utf-8"))
@lru_cache(maxsize=None)
def _validator(definition: str) -> Draft202012Validator:
    if definition not in _CONTRACTS["$defs"]:
        raise ValueError(f"unknown ML contract definition: {definition}")
    schema = {
        "$schema": _CONTRACTS["$schema"],
        "$defs": _CONTRACTS["$defs"],
        "$ref": f"#/$defs/{definition}",
    }
    return Draft202012Validator(schema, format_checker=FormatChecker())


class AnalysisRequestError(ValueError):
    """A request violates the shared JSON Schema or an application invariant."""


def validate_contract_message(value: Any, definition: str) -> Any:
    errors = sorted(_validator(definition).iter_errors(value), key=lambda item: _path(item.absolute_path))
    if errors:
        details = "; ".join(
            f"{_path(error.absolute_path)}: {error.message}" for error in errors[:8]
        )
        raise AnalysisRequestError(details)
    return value


def validate_analysis_request(request: Any) -> dict[str, Any]:
    validate_contract_message(request, "AnalysisRequest")
    _validate_semantics(request)
    return request


def validate_search_request(request: Any) -> dict[str, Any]:
    return validate_contract_message(request, "SearchRequest")


def _validate_semantics(request: dict[str, Any]) -> None:
    if request["trace_id"] == request["parent_trace_id"] and request["parent_trace_id"] is not None:
        raise AnalysisRequestError("trace_id must differ from parent_trace_id")
    if request["request_type"] == "initial" and request["parent_trace_id"] is not None:
        raise AnalysisRequestError("initial request must not have parent_trace_id")

    follow_up = request["follow_up"]
    if follow_up is None:
        return
    if request["request_type"] != "follow_up":
        raise AnalysisRequestError("follow_up payload is only valid for request_type=follow_up")

    snapshot = follow_up["initial_insight"]
    insight = snapshot["insight"]
    if snapshot["context"] != request["context"] or snapshot["event"] != request["event"]:
        raise AnalysisRequestError("follow-up context and event must match the original snapshot")
    if insight["id"] != follow_up["initial_insight_id"]:
        raise AnalysisRequestError("initial_insight_id does not match the supplied insight")
    if insight["business_context_id"] != request["context"]["business_context_id"]:
        raise AnalysisRequestError("initial insight belongs to a different business context")
    if insight["event_id"] != request["event"]["id"]:
        raise AnalysisRequestError("initial insight belongs to a different event")

    snapshot_chunks = {chunk["chunk_id"]: chunk for chunk in snapshot["chunks"]}
    feature_ids = {feature["id"] for feature in request["context"]["profile_features"]}
    initial_claim_ids = {claim["id"] for claim in insight["claims"]}
    _validate_snapshot_claims(insight["claims"], snapshot_chunks, feature_ids, "initial insight")

    history = follow_up["history"]
    ids = [message["id"] for message in history]
    if len(ids) != len(set(ids)):
        raise AnalysisRequestError("follow-up history contains duplicate message IDs")
    if not history or ids[-1] != follow_up["request_message_id"]:
        raise AnalysisRequestError("last history item must be the current request message")
    if ids[-1] != follow_up["history_through_message_id"]:
        raise AnalysisRequestError("history must end at history_through_message_id")
    last_message = history[-1]
    if last_message["role"] != "user" or last_message["content"] != follow_up["question"]:
        raise AnalysisRequestError("last history item must contain the current user question")
    known_claim_ids = set(initial_claim_ids)
    for message in history:
        if message["role"] == "user":
            if message["claims"] or message["citations"] or message["chunks"]:
                raise AnalysisRequestError("user history messages cannot contain claims, citations, or chunks")
            continue
        message_chunks = dict(snapshot_chunks)
        message_chunks.update({chunk["chunk_id"]: chunk for chunk in message["chunks"]})
        _validate_snapshot_claims(message["claims"], message_chunks, feature_ids, "history message")
        msg_claim_ids = {claim["id"] for claim in message["claims"]}
        if known_claim_ids.intersection(msg_claim_ids):
            raise AnalysisRequestError("history reuses an existing claim ID")
        known_claim_ids.update(msg_claim_ids)
        for citation in message["citations"]:
            if citation["chunk_id"] is not None and citation["chunk_id"] not in message_chunks:
                raise AnalysisRequestError("history citation references an unavailable chunk")
            if citation["claim_id"] is not None and citation["claim_id"] not in known_claim_ids:
                raise AnalysisRequestError("history citation references an unavailable claim")


def _validate_snapshot_claims(
    claims: list[dict[str, Any]],
    chunks: dict[str, dict[str, Any]],
    feature_ids: set[str],
    label: str,
) -> None:
    for claim in claims:
        for evidence in claim["evidence"]:
            if evidence["chunk_id"] is not None:
                chunk = chunks.get(evidence["chunk_id"])
                if chunk is None:
                    raise AnalysisRequestError(f"{label} references a chunk outside its supplied snapshot")
                quote = evidence["quote_text"]
                if not quote or quote not in chunk["chunk_text"]:
                    raise AnalysisRequestError(f"{label} contains a quote not found in its chunk")
            elif evidence["profile_feature_id"] not in feature_ids:
                raise AnalysisRequestError(f"{label} references a profile feature outside the request")


def _path(parts: Any) -> str:
    result = "$"
    for part in parts:
        result += f"[{part}]" if isinstance(part, int) else f".{part}"
    return result
