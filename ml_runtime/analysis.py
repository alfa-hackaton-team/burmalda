"""Contract-shaped analysis orchestration for fixtures and injected agent clients.

Retrieval remains fixture-backed here. Production persistence, authorization,
provider configuration, and independent semantic verification are not included.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Mapping
from uuid import uuid4

from .analysis_validation import validate_analysis_request
from .agent import AgentExecutionError, AnalysisAgent
from .drafting import build_evidence_draft
from .retrieval import Retriever
from .search_plan import BoundedSearchRunner, FixtureSearchPlanner
from .verification import OutputVerificationError, verify_analysis_response


class FixtureAnalysisService:
    def __init__(
        self,
        retriever: Retriever,
        fixture_dir: Path | None = None,
        agent: AnalysisAgent | None = None,
    ) -> None:
        self.retriever = retriever
        self.agent = agent
        self.fixture_dir = fixture_dir or (
            Path(__file__).resolve().parents[1] / "docs/ml/fixtures/v1"
        )

    def analyze(
        self,
        request: dict[str, Any],
        required_profile_features: Mapping[str, str] | None = None,
    ) -> dict[str, Any]:
        validate_analysis_request(request)
        if request["request_type"] == "follow_up":
            return self._analyze_follow_up(request)
        self._require_fixture_scenario(request)
        canonical_request = json.loads(
            (self.fixture_dir / "analysis.request.json").read_text(encoding="utf-8")
        )
        if request != canonical_request:
            raise ValueError("fixture analyzer only accepts the checked-in analysis.request.json")

        # The fixture planner uses the ML1-registered query; the runner applies
        # the bounded call/time budget before any future dynamic query strategy.
        search_run = BoundedSearchRunner(
            self.retriever,
            FixtureSearchPlanner(self.fixture_dir),
        ).run(request)
        if not search_run.chunks:
            if search_run.had_technical_error:
                result = self._failure(request, "SEARCH_UNAVAILABLE", "Search did not complete")
            else:
                result = self._insufficient_evidence(request, search_run.warnings)
            return self._verify_or_fail(request, result, list(search_run.chunks))

        if self.agent is not None:
            return self._run_agent(request, list(search_run.chunks))

        draft = build_evidence_draft(request, list(search_run.chunks))
        missing_feature_gaps = self._missing_profile_feature_gaps(
            request,
            required_profile_features or {},
        )
        if missing_feature_gaps:
            result = self._insufficient_evidence(
                request,
                tuple(search_run.warnings) + draft.warnings,
                missing_feature_gaps,
            )
            return self._verify_or_fail(request, result, list(search_run.chunks))

        response = json.loads(
            (self.fixture_dir / "analysis.ok.json").read_text(encoding="utf-8")
        )
        # The checked-in response is a deterministic fixture illustrating the v1
        # shape. IDs are rebound to this invocation so request/response pairing is
        # correct. Replace this fixture engine with the ML2 agent in later work.
        run_id = str(uuid4())
        response.update(
            trace_id=request["trace_id"],
            request_operation_id=request["request_operation_id"],
            analysis_run_id=run_id,
        )
        response["insight"]["analysis_run_id"] = run_id
        response["insight"]["business_context_id"] = request["context"]["business_context_id"]
        response["insight"]["event_id"] = request["event"]["id"]
        response["warnings"] = list(search_run.warnings) + list(draft.warnings)
        return self._verify_or_fail(request, response, list(search_run.chunks))

    def _analyze_follow_up(self, request: dict[str, Any]) -> dict[str, Any]:
        """Answer from the supplied conversation and original evidence snapshot."""
        canonical_request = json.loads(
            (self.fixture_dir / "follow_up.request.json").read_text(encoding="utf-8")
        )
        if request != canonical_request:
            raise ValueError("fixture analyzer only accepts the checked-in follow_up.request.json")

        response = json.loads(
            (self.fixture_dir / "follow_up.ok.json").read_text(encoding="utf-8")
        )
        run_id = str(uuid4())
        response.update(
            trace_id=request["trace_id"],
            request_operation_id=request["request_operation_id"],
            analysis_run_id=run_id,
        )
        response["message"]["conversation_id"] = request["follow_up"]["conversation_id"]
        response["message"]["parent_message_id"] = request["follow_up"]["request_message_id"]
        chunks = request["follow_up"]["initial_insight"]["chunks"]
        if self.agent is not None:
            return self._run_agent(request, chunks)
        return self._verify_or_fail(request, response, chunks)

    @staticmethod
    def _require_fixture_scenario(request: dict[str, Any]) -> None:
        if request["request_type"] not in ("initial", "reanalyze") or request["follow_up"] is not None:
            raise ValueError("fixture analysis currently supports initial/reanalyze only")

    def _run_agent(self, request: dict[str, Any], chunks: list[dict[str, Any]]) -> dict[str, Any]:
        try:
            return self.agent.run(request, chunks)
        except AgentExecutionError as exc:
            return self._failure(request, exc.code, str(exc), exc.retryable)

    @staticmethod
    def _failure(
        request: dict[str, Any], code: str, message: str, retryable: bool = True
    ) -> dict[str, Any]:
        return {
            "contract_version": "v1",
            "trace_id": request["trace_id"],
            "request_operation_id": request["request_operation_id"],
            "analysis_run_id": str(uuid4()),
            "status": "failed",
            "insight": None,
            "message": None,
            "warnings": [],
            "gaps": [],
            "error": {"code": code, "message": message, "retryable": retryable},
        }

    @staticmethod
    def _insufficient_evidence(
        request: dict[str, Any],
        warnings: tuple[str, ...],
        gaps: list[dict[str, Any]] | None = None,
    ) -> dict[str, Any]:
        if gaps is None:
            gaps = [{
                "code": "NO_EVENT_EVIDENCE",
                "description": "No usable source chunks were found for this event.",
                "required_feature_key": None,
            }]
        return {
            "contract_version": "v1",
            "trace_id": request["trace_id"],
            "request_operation_id": request["request_operation_id"],
            "analysis_run_id": str(uuid4()),
            "status": "insufficient_evidence",
            "insight": None,
            "message": None,
            "warnings": list(warnings),
            "gaps": gaps,
            "error": None,
        }

    @staticmethod
    def _missing_profile_feature_gaps(
        request: dict[str, Any], required_features: Mapping[str, str]
    ) -> list[dict[str, Any]]:
        available = {
            feature["feature_key"]: feature["feature_value"]
            for feature in request["context"]["profile_features"]
        }
        return [
            {
                "code": "MISSING_PROFILE_FEATURE",
                "description": description,
                "required_feature_key": key,
            }
            for key, description in required_features.items()
            if key not in available or available[key] is None
        ]

    @staticmethod
    def _verify_or_fail(
        request: dict[str, Any], response: dict[str, Any], chunks: list[dict[str, Any]]
    ) -> dict[str, Any]:
        try:
            report = verify_analysis_response(request, response, chunks)
        except OutputVerificationError:
            return FixtureAnalysisService._failure(
                request,
                "VERIFICATION_FAILED",
                "Analysis output failed structural or evidence verification.",
                False,
            )
        response["warnings"].extend(report.warnings)
        return response
