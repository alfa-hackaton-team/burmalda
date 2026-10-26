"""A04: build evidence-bound draft inputs without inventing conclusions.

These are internal agent inputs, not final ML v1 Claim objects. The LLM/agent
stage must assign claim_type, reason about applicability, and propose actions;
the verifier then decides whether any candidate can be published.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any


@dataclass(frozen=True)
class EventFactDraft:
    text: str
    evidence: tuple[dict[str, Any], ...]
    knowledge_type: str = "FACT"


@dataclass(frozen=True)
class ProfileFactDraft:
    feature_key: str
    feature_value: Any
    evidence: dict[str, Any]
    knowledge_type: str = "FACT"


@dataclass(frozen=True)
class EvidenceBoundDraft:
    event_facts: tuple[EventFactDraft, ...]
    profile_facts: tuple[ProfileFactDraft, ...]
    applicability_candidates: tuple[dict[str, Any], ...]
    action_candidates: tuple[dict[str, Any], ...]
    warnings: tuple[str, ...]


def build_evidence_draft(
    analysis_request: dict[str, Any],
    chunks: list[dict[str, Any]],
) -> EvidenceBoundDraft:
    """Prepare source facts and profile inputs for later interpretation.

    Event facts are emitted only when the event summary appears verbatim in a
    retrieved chunk from one of the event's declared document versions. Profile
    values are carried as typed context with provenance, not turned into impact
    claims. No applicability or action is inferred at this stage.
    """
    request_event = analysis_request["event"]
    allowed_versions = set(request_event["document_version_ids"])
    summary = request_event["summary"].strip()
    matching_evidence: list[dict[str, Any]] = []
    warnings: list[str] = []

    for chunk in chunks:
        if chunk["document_version_id"] not in allowed_versions:
            continue
        if summary and summary in chunk["chunk_text"]:
            matching_evidence.append({
                "evidence_type": "document_chunk",
                "chunk_id": chunk["chunk_id"],
                "fact_id": None,
                "profile_feature_id": None,
                "evidence_role": "support",
                "quote_text": summary,
            })

    event_facts: tuple[EventFactDraft, ...]
    if matching_evidence:
        event_facts = (EventFactDraft(summary, tuple(matching_evidence)),)
    else:
        event_facts = ()
        warnings.append("Event summary was not found verbatim in the retrieved source chunks.")

    profile_facts = tuple(
        ProfileFactDraft(
            feature_key=feature["feature_key"],
            feature_value=feature["feature_value"],
            evidence={
                "evidence_type": "profile_feature",
                "chunk_id": None,
                "fact_id": None,
                "profile_feature_id": feature["id"],
                "evidence_role": "context",
                "quote_text": None,
            },
        )
        for feature in analysis_request["context"]["profile_features"]
    )

    return EvidenceBoundDraft(
        event_facts=event_facts,
        profile_facts=profile_facts,
        applicability_candidates=(),
        action_candidates=(),
        warnings=tuple(warnings),
    )
