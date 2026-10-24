"""Reusable analysis orchestration; provider and retrieval implementations are injected."""

from __future__ import annotations

from time import monotonic
from typing import Any
from uuid import uuid4

from .agent import AgentExecutionError, AnalysisAgent
from .analysis_validation import validate_analysis_request
from .retrieval import RetrievalContractError, Retriever
from .search_plan import BoundedSearchRunner, EventSearchPlanner, SearchLimits, SearchPlanner


class AnalysisService:
    """Run any contract-valid request through bounded retrieval and the agent."""

    def __init__(
        self,
        retriever: Retriever,
        agent: AnalysisAgent,
        planner: SearchPlanner | None = None,
        limits: SearchLimits | None = None,
    ) -> None:
        self.retriever = retriever
        self.agent = agent
        self.planner = planner or EventSearchPlanner()
        self.limits = limits or SearchLimits()

    def analyze(self, request: dict[str, Any]) -> dict[str, Any]:
        started = monotonic()
        validate_analysis_request(request)
        if request["request_type"] == "follow_up":
            chunks = request["follow_up"]["initial_insight"]["chunks"]
            try:
                remaining = self.limits.max_seconds_per_analysis - (monotonic() - started)
                return self.agent.run(request, chunks, remaining)
            except AgentExecutionError as exc:
                return _failed(request, exc.code, str(exc), exc.retryable)

        try:
            search_run = BoundedSearchRunner(
                self.retriever, self.planner, self.limits
            ).run(request)
        except (RetrievalContractError, ValueError, TypeError) as exc:
            return _failed(request, "INVALID_REQUEST", str(exc), False)

        chunks = list(search_run.chunks)
        if not chunks:
            if search_run.had_technical_error:
                return _failed(
                    request,
                    "SEARCH_UNAVAILABLE",
                    "Search did not complete; retry the operation later.",
                    True,
                    list(search_run.warnings),
                )
            return _insufficient(request, list(search_run.warnings))

        try:
            remaining = self.limits.max_seconds_per_analysis - (monotonic() - started)
            response = self.agent.run(request, chunks, remaining)
        except AgentExecutionError as exc:
            return _failed(request, exc.code, str(exc), exc.retryable, list(search_run.warnings))
        response["warnings"] = list(dict.fromkeys(
            list(response["warnings"]) + list(search_run.warnings)
        ))
        return response


def _failed(
    request: dict[str, Any],
    code: str,
    message: str,
    retryable: bool,
    warnings: list[str] | None = None,
) -> dict[str, Any]:
    return {
        "contract_version": "v1",
        "trace_id": request["trace_id"],
        "request_operation_id": request["request_operation_id"],
        "analysis_run_id": str(uuid4()),
        "status": "failed",
        "insight": None,
        "message": None,
        "warnings": warnings or [],
        "gaps": [],
        "error": {"code": code, "message": message, "retryable": retryable},
    }


def _insufficient(request: dict[str, Any], warnings: list[str]) -> dict[str, Any]:
    return {
        "contract_version": "v1",
        "trace_id": request["trace_id"],
        "request_operation_id": request["request_operation_id"],
        "analysis_run_id": str(uuid4()),
        "status": "insufficient_evidence",
        "insight": None,
        "message": None,
        "warnings": warnings,
        "gaps": [{
            "code": "NO_EVENT_EVIDENCE",
            "description": "No usable source chunks were found for this event.",
            "required_feature_key": None,
        }],
        "error": None,
    }
