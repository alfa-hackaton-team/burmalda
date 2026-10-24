"""A05: structural and provenance checks before returning an ML v1 response."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any, Callable

from .analysis_validation import AnalysisRequestError, validate_contract_message


class OutputVerificationError(ValueError):
    """Response violates ML v1 or references evidence outside the request/results."""


SemanticVerifier = Callable[
    [dict[str, Any], dict[str, Any], list[dict[str, Any]]], tuple[bool, str | None]
]


@dataclass(frozen=True)
class VerificationReport:
    semantic_verified: bool
    warnings: tuple[str, ...]


def verify_analysis_response(
    request: dict[str, Any],
    response: dict[str, Any],
    chunks: list[dict[str, Any]],
    semantic_verifier: SemanticVerifier | None = None,
) -> VerificationReport:
    """Validate schema, request/response identity, IDs, support and exact quotes.

    Without a semantic verifier this cannot prove that a paraphrase or inference
    is entailed by its citations. Fixture mode reports that limitation explicitly.
    """
    try:
        validate_contract_message(response, "AnalysisResponse")
    except AnalysisRequestError as exc:
        raise OutputVerificationError(f"AnalysisResponse schema failure: {exc}") from exc

    for key in ("contract_version", "trace_id", "request_operation_id"):
        if response[key] != request[key]:
            raise OutputVerificationError(f"AnalysisResponse {key} does not match its request")

    if response["status"] != "succeeded":
        if response["status"] == "insufficient_evidence" and not response["gaps"]:
            raise OutputVerificationError("insufficient_evidence requires at least one gap")
        return VerificationReport(False, ())

    if request["request_type"] == "follow_up":
        if response["insight"] is not None or response["message"] is None:
            raise OutputVerificationError("follow-up success must contain a message and no insight")
        message = response["message"]
        follow_up = request["follow_up"]
        if message["conversation_id"] != follow_up["conversation_id"]:
            raise OutputVerificationError("follow-up message belongs to a different conversation")
        if message["parent_message_id"] != follow_up["request_message_id"]:
            raise OutputVerificationError("follow-up message parent must be the request message")
        if message["role"] != "assistant":
            raise OutputVerificationError("follow-up response message must have assistant role")
        initial = follow_up["initial_insight"]
        initial_claim_ids = {claim["id"] for claim in initial["insight"]["claims"]}
        response_claims = message["claims"]
        if initial_claim_ids.intersection(claim["id"] for claim in response_claims):
            raise OutputVerificationError("follow-up must not reuse initial insight claim IDs")
        chunk_map = {chunk["chunk_id"]: chunk for chunk in chunks}
        _validate_claims(response_claims, chunk_map, request)
        allowed_claim_ids = initial_claim_ids | {claim["id"] for claim in response_claims}
        _validate_citations(message["citations"], chunk_map, allowed_claim_ids)
    else:
        if response["insight"] is None or response["message"] is not None:
            raise OutputVerificationError("analysis success must contain an insight and no message")
        insight = response["insight"]
        if insight["analysis_run_id"] != response["analysis_run_id"]:
            raise OutputVerificationError("insight analysis_run_id does not match response")
        if insight["business_context_id"] != request["context"]["business_context_id"]:
            raise OutputVerificationError("insight belongs to a different business context")
        if insight["event_id"] != request["event"]["id"]:
            raise OutputVerificationError("insight belongs to a different event")
        chunk_map = {chunk["chunk_id"]: chunk for chunk in chunks}
        claims = insight["claims"]
        _validate_claims(claims, chunk_map, request)
        if (insight["bank_view"] is None) != (insight["bank_view_document_version_id"] is None):
            raise OutputVerificationError("bank_view and its document version must both be null or both set")
        if insight["bank_view"] is not None and not any(
            claim["claim_type"] == "bank_view"
            and any(
                evidence["chunk_id"] in chunk_map
                and evidence["evidence_role"] == "support"
                and chunk_map[evidence["chunk_id"]]["source_role"] == "bank"
                and chunk_map[evidence["chunk_id"]]["document_version_id"] == insight["bank_view_document_version_id"]
                for evidence in claim["evidence"]
            )
            for claim in claims
        ):
            raise OutputVerificationError("bank_view has no matching bank-supported claim")

    if semantic_verifier is None:
        return VerificationReport(
            False,
            ("Fixture mode checked evidence links and exact quotes, not semantic entailment.",),
        )
    valid, explanation = semantic_verifier(request, response, chunks)
    if not valid:
        raise OutputVerificationError(explanation or "semantic verifier rejected the response")
    return VerificationReport(True, ())


def _validate_claims(
    claims: list[dict[str, Any]],
    chunk_map: dict[str, dict[str, Any]],
    request: dict[str, Any],
) -> None:
    claim_ids = [claim["id"] for claim in claims]
    positions = [claim["position"] for claim in claims]
    if len(claim_ids) != len(set(claim_ids)) or len(positions) != len(set(positions)):
        raise OutputVerificationError("claim IDs and positions must be unique")
    feature_ids = {feature["id"] for feature in request["context"]["profile_features"]}
    for claim in claims:
        if not any(item["evidence_role"] == "support" for item in claim["evidence"]):
            raise OutputVerificationError("every claim requires at least one supporting evidence item")
        if claim["verification_status"] == "PARTIALLY_SUPPORTED" and not claim["assumptions"]:
            raise OutputVerificationError("partially supported claims require explicit assumptions")
        bank_support = False
        for evidence in claim["evidence"]:
            if evidence["chunk_id"] is not None:
                chunk = chunk_map.get(evidence["chunk_id"])
                if chunk is None:
                    raise OutputVerificationError("claim references a chunk outside the supplied snapshot/results")
                quote = evidence["quote_text"]
                if not quote or quote not in chunk["chunk_text"]:
                    raise OutputVerificationError("evidence quote is not an exact substring of its chunk")
                bank_support |= evidence["evidence_role"] == "support" and chunk["source_role"] == "bank"
            elif evidence["profile_feature_id"] not in feature_ids:
                raise OutputVerificationError("claim references a profile feature outside the request")
        if (claim["claim_type"] == "bank_view" or claim["knowledge_type"] == "ALFA_ANALYTICS") and not bank_support:
            raise OutputVerificationError("bank-related claims require supporting evidence from a bank source")


def _validate_citations(
    citations: list[dict[str, Any]],
    chunk_map: dict[str, dict[str, Any]],
    allowed_claim_ids: set[str],
) -> None:
    orders = [citation["citation_order"] for citation in citations]
    targets = [(item["chunk_id"], item["claim_id"]) for item in citations]
    if len(orders) != len(set(orders)) or len(targets) != len(set(targets)):
        raise OutputVerificationError("citation order and targets must be unique")
    for citation in citations:
        if citation["chunk_id"] is not None and citation["chunk_id"] not in chunk_map:
            raise OutputVerificationError("citation references a chunk outside the supplied snapshot")
        if citation["claim_id"] is not None and citation["claim_id"] not in allowed_claim_ids:
            raise OutputVerificationError("citation references an unknown claim")
