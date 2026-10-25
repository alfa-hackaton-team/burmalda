"""Offline tests for event candidate extraction and evidence validation."""

from __future__ import annotations

import unittest
from typing import Any

from ml_runtime.article_processing import parse_article_processing
from ml_runtime.event_extraction import EventExtractionError, LLMEventExtractor, validate_candidate_events
from ml_runtime.tests.test_article_processing_r02 import PIPELINE_VERSION, payload


QUOTE = "С 1 ноября новые договоры продукта X заключаются по обновленным условиям."


def candidate(**updates: Any) -> dict[str, Any]:
    event = {
        "event_type": "credit_terms_change",
        "title": "Обновление условий новых договоров продукта X",
        "summary": "С 1 ноября новые договоры продукта X заключаются по обновленным условиям.",
        "occurred_at": None,
        "geography": [],
        "evidence_quotes": [QUOTE],
    }
    event.update(updates)
    return event


class FakeClient:
    def __init__(self, *values: dict[str, Any]) -> None:
        self.values = list(values)
        self.calls: list[dict[str, Any]] = []

    def generate_json(self, **kwargs):
        self.calls.append(kwargs)
        return self.values.pop(0)


class EventExtractionTests(unittest.TestCase):
    def setUp(self) -> None:
        request_payload = payload(body=QUOTE)
        self.request = parse_article_processing(
            request_payload,
            f"{request_payload['article']['article_version_id']}:{PIPELINE_VERSION}",
            max_text_bytes=131072,
            supported_pipeline_versions={PIPELINE_VERSION},
        )

    def test_extracts_a_candidate_with_exact_evidence_and_no_persisted_id(self):
        client = FakeClient(
            {"events": [candidate()]},
            {"event_checks": [{"index": 0, "supported": True, "reason": ""}], "overall_supported": True},
        )
        extractor = LLMEventExtractor(client)

        result = extractor.extract(self.request, timeout_seconds=12)

        self.assertEqual(len(result), 1)
        self.assertEqual(result[0].status, "candidate")
        self.assertEqual(result[0].evidence_quotes, (QUOTE,))
        self.assertNotIn("id", result[0].to_dict())
        self.assertEqual(client.calls[0]["input_payload"]["article_version_id"], self.request.article["article_version_id"])
        self.assertEqual(client.calls[0]["timeout_seconds"], 12)
        self.assertEqual(client.calls[1]["model_name"], "qwen/qwen3.6-35b-a3b")
        self.assertLess(client.calls[1]["timeout_seconds"], 12)

    def test_empty_event_list_is_valid(self):
        client = FakeClient({"events": []})
        extractor = LLMEventExtractor(client)
        self.assertEqual(extractor.extract(self.request), [])
        self.assertEqual(len(client.calls), 1)

    def test_rejects_quote_not_found_in_saved_body(self):
        with self.assertRaises(EventExtractionError):
            validate_candidate_events({"events": [candidate(evidence_quotes=["10 млн рублей"])]}, QUOTE)

    def test_rejects_duplicate_candidate_event(self):
        with self.assertRaises(EventExtractionError):
            validate_candidate_events({"events": [candidate(), candidate()]}, QUOTE)

    def test_duplicate_identity_normalizes_title_spacing_and_timezone(self):
        first = candidate(occurred_at="2026-11-01T00:00:00+03:00")
        second = candidate(
            title="  ОБНОВЛЕНИЕ   условий новых договоров продукта X ",
            occurred_at="2026-10-31T21:00:00Z",
        )
        with self.assertRaisesRegex(EventExtractionError, "duplicate candidate"):
            validate_candidate_events({"events": [first, second]}, QUOTE)

    def test_semantic_verifier_rejection_prevents_candidates_from_returning(self):
        client = FakeClient(
            {"events": [candidate()]},
            {"event_checks": [{"index": 0, "supported": False, "reason": "Summary adds an unsupported event."}], "overall_supported": False},
        )
        with self.assertRaisesRegex(EventExtractionError, "unsupported event"):
            LLMEventExtractor(client).extract(self.request)

    def test_incomplete_verifier_response_fails_closed(self):
        client = FakeClient(
            {"events": [candidate()]},
            {"event_checks": [], "overall_supported": True},
        )
        with self.assertRaisesRegex(EventExtractionError, "every candidate"):
            LLMEventExtractor(client).extract(self.request)

    def test_rejects_unconfigured_confidence_or_timezone_less_event_date(self):
        with self.assertRaises(EventExtractionError):
            validate_candidate_events({"events": [candidate(confidence=1.1)]}, QUOTE)
        with self.assertRaises(EventExtractionError):
            validate_candidate_events({"events": [candidate(occurred_at="2026-11-01T00:00:00")]}, QUOTE)


if __name__ == "__main__":
    unittest.main()
