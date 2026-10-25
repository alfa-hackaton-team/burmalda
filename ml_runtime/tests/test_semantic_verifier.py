"""Offline tests for the fail-closed semantic-verification adapter."""

from __future__ import annotations

import unittest
from typing import Any

from ml_runtime.model_policy import DEFAULT_AGENT_MODEL
from ml_runtime.semantic_verifier import LLMSemanticVerifier


class VerdictClient:
    def __init__(self, verdict: dict[str, Any]) -> None:
        self.verdict = verdict
        self.calls: list[dict[str, Any]] = []

    def generate_json(self, **kwargs):
        self.calls.append(kwargs)
        return self.verdict


def fixture_payload():
    request = {
        "request_type": "initial",
        "context": {
            "profile_features": [
                {"id": "feature-1", "feature_key": "has_loan", "feature_value": True}
            ]
        },
        "event": {"id": "event-1"},
    }
    claim = {
        "id": "claim-1",
        "claim_text": "У компании 10 млн рублей свободных средств.",
        "knowledge_type": "FACT",
        "verification_status": "SUPPORTED",
        "assumptions": [],
        "evidence": [{
            "chunk_id": "chunk-1",
            "profile_feature_id": None,
            "evidence_role": "support",
            "quote_text": "Новые договоры продукта X доступны с 1 ноября.",
        }],
    }
    response = {
        "insight": {
            "claims": [claim],
            "summary": claim["claim_text"],
            "consequences": "",
            "recommended_actions": "",
        },
        "message": None,
    }
    chunks = [{
        "chunk_id": "chunk-1",
        "chunk_text": "Новые договоры продукта X доступны с 1 ноября.",
        "document_version_id": "doc-version-1",
        "source_role": "external",
    }]
    return request, response, chunks


class LLMSemanticVerifierTests(unittest.TestCase):
    def test_unsupported_invented_amount_is_rejected(self):
        client = VerdictClient({
            "claim_checks": [{
                "claim_id": "claim-1",
                "supported": False,
                "reason": "The evidence says nothing about company funds or this amount.",
            }],
            "unattributed_assertions": [],
            "overall_supported": False,
        })
        verifier = LLMSemanticVerifier(client, DEFAULT_AGENT_MODEL)

        supported, reason = verifier(*fixture_payload(), timeout_seconds=7.0)

        self.assertFalse(supported)
        self.assertIn("funds", reason)
        self.assertEqual(client.calls[0]["timeout_seconds"], 7.0)
        self.assertEqual(
            client.calls[0]["input_payload"]["claim_records"][0]["evidence"][0]["chunk_text"],
            "Новые договоры продукта X доступны с 1 ноября.",
        )

    def test_missing_claim_check_fails_closed(self):
        client = VerdictClient({
            "claim_checks": [],
            "unattributed_assertions": [],
            "overall_supported": True,
        })

        supported, reason = LLMSemanticVerifier(client)(*fixture_payload(), timeout_seconds=5)

        self.assertFalse(supported)
        self.assertIn("each response claim", reason)


if __name__ == "__main__":
    unittest.main()
