"""A03: bounded search planning and execution around the ML1 Retriever."""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path
from time import monotonic
from typing import Any, Callable, Protocol, Sequence

from .retrieval import RetrievalContractError, Retriever, retrieve_for_search


class SearchPlanner(Protocol):
    def plan(self, analysis_request: dict[str, Any]) -> Sequence[dict[str, Any]]: ...


StopCondition = Callable[[Sequence[dict[str, Any]], Sequence[dict[str, Any]]], bool]


@dataclass(frozen=True)
class SearchLimits:
    max_search_calls: int = 6
    max_seconds_per_search: float = 10.0
    max_seconds_per_analysis: float = 90.0
    max_technical_retries: int = 1

    def __post_init__(self) -> None:
        if self.max_search_calls < 1 or self.max_technical_retries < 0:
            raise ValueError("search call/retry limits are invalid")
        if self.max_seconds_per_search <= 0 or self.max_seconds_per_analysis <= 0:
            raise ValueError("search time limits must be positive")


@dataclass(frozen=True)
class SearchRun:
    search_responses: tuple[dict[str, Any], ...]
    chunks: tuple[dict[str, Any], ...]
    warnings: tuple[str, ...]
    search_calls: int
    technical_retries: int
    elapsed_seconds: float
    stop_reason: str
    had_technical_error: bool


class FixtureSearchPlanner:
    """One registered ML1 fixture query; no dynamic query-generation policy."""

    def __init__(self, fixture_dir: Path | None = None, top_k: int = 10) -> None:
        self.fixture_dir = fixture_dir or (
            Path(__file__).resolve().parents[1] / "docs/ml/fixtures/v1"
        )
        self.top_k = top_k

    def plan(self, analysis_request: dict[str, Any]) -> Sequence[dict[str, Any]]:
        from .retrieval import build_search_request

        fixture = json.loads(
            (self.fixture_dir / "search.request.json").read_text(encoding="utf-8")
        )
        return [build_search_request(
            analysis_request,
            top_k=self.top_k,
            query_override=fixture["query"],
        )]


class EventSearchPlanner:
    """Provider-independent first pass, scoped to the event's document versions."""

    def __init__(self, top_k: int = 10) -> None:
        self.top_k = top_k

    def plan(self, analysis_request: dict[str, Any]) -> Sequence[dict[str, Any]]:
        from .retrieval import build_search_request

        return [build_search_request(analysis_request, top_k=self.top_k)]


@dataclass
class BoundedSearchRunner:
    """Executes planned searches within local call and elapsed-time budgets.

    The runner detects a slow call after it returns. A production Retriever that
    performs I/O must also enforce the per-call timeout in its transport; a
    synchronous Python call cannot be safely cancelled here.
    """

    retriever: Retriever
    planner: SearchPlanner
    limits: SearchLimits = field(default_factory=SearchLimits)

    def run(
        self,
        analysis_request: dict[str, Any],
        stop_condition: StopCondition | None = None,
    ) -> SearchRun:
        started = monotonic()
        tasks = list(self.planner.plan(analysis_request))
        plan_truncated = len(tasks) > self.limits.max_search_calls
        if plan_truncated:
            tasks = tasks[: self.limits.max_search_calls]
        warnings: list[str] = []
        if plan_truncated:
            warnings.append("Search plan was capped at the configured call limit.")
        search_responses: list[dict[str, Any]] = []
        chunks_by_id: dict[str, dict[str, Any]] = {}
        calls = retries = 0
        had_technical_error = False
        stop_reason = "plan_exhausted"

        for task in tasks:
            if monotonic() - started >= self.limits.max_seconds_per_analysis:
                stop_reason = "analysis_deadline"
                warnings.append("Search stopped at the analysis time limit.")
                break
            if calls >= self.limits.max_search_calls:
                stop_reason = "search_budget_exhausted"
                warnings.append("Search stopped at the call limit.")
                break
            self._validate_task_context(task, analysis_request)

            while True:
                calls += 1
                call_started = monotonic()
                try:
                    outcome = retrieve_for_search(self.retriever, task)
                except (TimeoutError, OSError):
                    had_technical_error = True
                    warnings.append("Retriever call failed due to a temporary transport error.")
                    if (
                        retries < self.limits.max_technical_retries
                        and calls < self.limits.max_search_calls
                        and monotonic() - started < self.limits.max_seconds_per_analysis
                    ):
                        retries += 1
                        continue
                    break

                duration = monotonic() - call_started
                response = outcome.search_response
                if duration > self.limits.max_seconds_per_search:
                    had_technical_error = True
                    warnings.append("Retriever call exceeded the per-search time budget.")
                    stop_reason = "search_timeout"
                    break

                search_responses.append(response)
                if response["status"] == "error":
                    had_technical_error = True
                    error = response["error"] or {}
                    if (
                        error.get("retryable")
                        and retries < self.limits.max_technical_retries
                        and calls < self.limits.max_search_calls
                        and monotonic() - started < self.limits.max_seconds_per_analysis
                    ):
                        retries += 1
                        continue
                    warnings.append("A search task ended with a technical error.")
                    break

                warnings.extend(response.get("warnings", []))
                if outcome.chunks_response is not None:
                    chunks_response = outcome.chunks_response
                    if chunks_response["status"] == "error":
                        had_technical_error = True
                        warnings.append("Chunks for a search result could not be loaded.")
                    else:
                        warnings.extend(chunks_response.get("warnings", []))
                        for chunk in chunks_response["results"]:
                            chunks_by_id.setdefault(chunk["chunk_id"], chunk)

                if stop_condition and stop_condition(
                    tuple(chunks_by_id.values()), tuple(search_responses)
                ):
                    stop_reason = "evidence_sufficient"
                    return self._result(
                        started, search_responses, chunks_by_id, warnings,
                        calls, retries, stop_reason, had_technical_error,
                    )
                break

            if stop_reason == "search_timeout":
                break

        if plan_truncated or (calls >= self.limits.max_search_calls and len(search_responses) < len(tasks)):
            stop_reason = "search_budget_exhausted"
            warnings.append("Search stopped at the call limit.")
        elif monotonic() - started >= self.limits.max_seconds_per_analysis:
            stop_reason = "analysis_deadline"
            warnings.append("Search stopped at the analysis time limit.")
        return self._result(
            started, search_responses, chunks_by_id, warnings,
            calls, retries, stop_reason, had_technical_error,
        )

    @staticmethod
    def _validate_task_context(task: dict[str, Any], analysis_request: dict[str, Any]) -> None:
        if task.get("trace_id") != analysis_request["trace_id"]:
            raise RetrievalContractError("planned SearchRequest trace_id differs from analysis")
        if task.get("business_context_id") != analysis_request["context"]["business_context_id"]:
            raise RetrievalContractError("planned SearchRequest context differs from analysis")

    @staticmethod
    def _result(
        started: float,
        responses: list[dict[str, Any]],
        chunks_by_id: dict[str, dict[str, Any]],
        warnings: list[str],
        calls: int,
        retries: int,
        stop_reason: str,
        had_technical_error: bool,
    ) -> SearchRun:
        return SearchRun(
            search_responses=tuple(responses),
            chunks=tuple(chunks_by_id.values()),
            warnings=tuple(dict.fromkeys(warnings)),
            search_calls=calls,
            technical_retries=retries,
            elapsed_seconds=monotonic() - started,
            stop_reason=stop_reason,
            had_technical_error=had_technical_error,
        )
