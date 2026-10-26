"""Offline integration checks for ML2 against the shared ML1 retrieval adapter."""

from __future__ import annotations

import json
import unittest
from pathlib import Path
from typing import Any

from ml.adapters.in_memory import InMemoryRetriever

from ml_runtime.agent import AnalysisAgent
from ml_runtime.model_policy import DEFAULT_VERIFIER_MODEL
from ml_runtime.search_plan import FixtureSearchPlanner
from ml_runtime.semantic_verifier import LLMSemanticVerifier
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


class HallucinatingTwoPassClient(FixtureModelClient):
    """Inject an unsupported amount, then reject it in the verifier pass."""

    def __init__(self) -> None:
        self.calls = 0

    def generate_json(self, *, input_payload: dict[str, Any], **kwargs: Any) -> dict[str, Any]:
        self.calls += 1
        if "claim_records" in input_payload:
            claim_id = input_payload["claim_records"][0]["claim"]["id"]
            return {
                "claim_checks": [{
                    "claim_id": claim_id,
                    "supported": False,
                    "reason": "The cited evidence does not state the invented amount.",
                }],
                "unattributed_assertions": [],
                "overall_supported": False,
            }

        response = super().generate_json(input_payload=input_payload, **kwargs)
        claim = response["insight"]["claims"][0]
        claim["claim_text"] = "У компании 10 млн рублей свободных средств."
        response["insight"]["summary"] = claim["claim_text"]
        return response


class AnalysisContractIntegrationTests(unittest.TestCase):
    def setUp(self) -> None:
        agent = AnalysisAgent(
            FixtureModelClient(),
            semantic_verifier=lambda _request, _response, _chunks, _timeout: (True, None),
        )
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

    def test_analysis_without_semantic_verifier_fails_closed(self) -> None:
        service = AnalysisService(
            retriever=InMemoryRetriever.from_fixtures(),
            agent=AnalysisAgent(FixtureModelClient()),
            planner=FixtureSearchPlanner(FIXTURES),
        )

        response = service.analyze(load_fixture("analysis.request.json"))

        self.assertEqual(response["status"], "failed")
        self.assertEqual(response["error"]["code"], "VERIFICATION_FAILED")

    def test_semantic_verifier_rejection_fails_closed(self) -> None:
        service = AnalysisService(
            retriever=InMemoryRetriever.from_fixtures(),
            agent=AnalysisAgent(
                FixtureModelClient(),
                semantic_verifier=lambda _request, _response, _chunks, _timeout: (
                    False, "Claim is not supported by its cited quote"
                ),
            ),
            planner=FixtureSearchPlanner(FIXTURES),
        )

        response = service.analyze(load_fixture("analysis.request.json"))

        self.assertEqual(response["status"], "failed")
        self.assertEqual(response["error"]["code"], "VERIFICATION_FAILED")

    def test_second_model_pass_rejects_invented_amount_before_publication(self) -> None:
        client = HallucinatingTwoPassClient()
        service = AnalysisService(
            retriever=InMemoryRetriever.from_fixtures(),
            agent=AnalysisAgent(
                client,
                semantic_verifier=LLMSemanticVerifier(client, DEFAULT_VERIFIER_MODEL),
            ),
            planner=FixtureSearchPlanner(FIXTURES),
        )

        response = service.analyze(load_fixture("analysis.request.json"))

        self.assertEqual(response["status"], "failed")
        self.assertEqual(response["error"]["code"], "VERIFICATION_FAILED")
        self.assertIsNone(response["insight"])
        self.assertEqual(client.calls, 2)


if __name__ == "__main__":
    unittest.main()
