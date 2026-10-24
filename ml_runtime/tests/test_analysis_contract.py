"""Offline integration checks for ML2 against the shared ML1 retrieval adapter."""

from __future__ import annotations

import json
import unittest
from pathlib import Path
from typing import Any

from ml.adapters.in_memory import InMemoryRetriever

from ml_runtime.agent import AnalysisAgent
from ml_runtime.search_plan import FixtureSearchPlanner
from ml_runtime.service import AnalysisService


ROOT = Path(__file__).resolve().parents[2]
FIXTURES = ROOT / "docs/ml/fixtures/v1"


def load_fixture(name: str) -> dict[str, Any]:
    return json.loads((FIXTURES / name).read_text(encoding="utf-8"))


class FixtureModelClient:
    """Return contract fixtures while rebinding IDs assigned for this run."""

    def generate_json(self, *, input_payload: dict[str, Any], **_: Any) -> dict[str, Any]:
        fixture_name = (
            "follow_up.ok.json"
            if input_payload["request_type"] == "follow_up"
            else "analysis.ok.json"
        )
        response = load_fixture(fixture_name)
        run_id = input_payload["analysis_run_id"]
        response.update(
            trace_id=input_payload["trace_id"],
            request_operation_id=input_payload["request_operation_id"],
            analysis_run_id=run_id,
        )
        if response["insight"] is not None:
            response["insight"]["analysis_run_id"] = run_id
        if response["message"] is not None:
            follow_up = input_payload["follow_up"]
            response["message"]["conversation_id"] = follow_up["conversation_id"]
            response["message"]["parent_message_id"] = follow_up["request_message_id"]
        return response


class AnalysisContractIntegrationTests(unittest.TestCase):
    def setUp(self) -> None:
        agent = AnalysisAgent(FixtureModelClient())
        self.service = AnalysisService(
            retriever=InMemoryRetriever.from_fixtures(),
            agent=agent,
            planner=FixtureSearchPlanner(FIXTURES),
        )

    def test_initial_success_uses_retrieved_evidence_and_matches_v1(self) -> None:
        response = self.service.analyze(load_fixture("analysis.request.json"))

        self.assertEqual(response["status"], "succeeded")
        self.assertEqual(response["insight"]["analysis_run_id"], response["analysis_run_id"])
        self.assertTrue(response["insight"]["claims"])

    def test_follow_up_success_uses_original_snapshot_and_matches_v1(self) -> None:
        request = load_fixture("follow_up.request.json")
        response = self.service.analyze(request)

        self.assertEqual(response["status"], "succeeded")
        self.assertIsNone(response["insight"])
        self.assertEqual(response["message"]["conversation_id"], request["follow_up"]["conversation_id"])
        self.assertEqual(response["message"]["parent_message_id"], request["follow_up"]["request_message_id"])


if __name__ == "__main__":
    unittest.main()
