"""R02 input and quality-first processing checks without external services."""

from __future__ import annotations

import unittest
from typing import Any

from ml_runtime.article_processing import ProcessingContractError, parse_article_processing
from ml_runtime.event_extraction import EventTextChunk
from ml_runtime.operations import (
    ArticleProcessingCoordinator,
    InMemoryOperationStore,
    IndexedArticleVersion,
)


SOURCE_ID = "10000000-0000-4000-8000-000000000001"
ARTICLE_VERSION_ID = "10000000-0000-4000-8000-000000000003"
PIPELINE_VERSION = "ml-pipeline-v1"


def payload(body: str = "Synthetic article text.") -> dict[str, Any]:
    return {
        "article": {
            "article_id": "10000000-0000-4000-8000-000000000002",
            "article_version_id": ARTICLE_VERSION_ID,
            "source_id": SOURCE_ID,
            "title": "Synthetic title",
            "body": body,
            "fetched_at": "2026-10-25T10:00:00Z",
            "raw_ref": "raw-artifact:10000000-0000-4000-8000-000000000004",
            "content_hash": "a" * 64,
            "metadata": {"extraction_method": "full_article", "extraction_errors": []},
        },
        "source": {
            "source_id": SOURCE_ID,
            "name": "Synthetic source",
            "source_role": "external",
            "source_type": "news",
            "trust_level": "primary",
            "config_version": "1",
        },
        "pipeline_version": PIPELINE_VERSION,
        "trace_id": "10000000-0000-4000-8000-000000000005",
    }


def parse(value: dict[str, Any]):
    return parse_article_processing(
        value,
        f"{ARTICLE_VERSION_ID}:{PIPELINE_VERSION}",
        max_text_bytes=131072,
        supported_pipeline_versions={PIPELINE_VERSION},
    )


class FakeQualityGate:
    def __init__(self, status: str, **overrides: Any) -> None:
        self.status = status
        self.calls = 0
        self.overrides = overrides

    def assess(self, request):
        self.calls += 1
        result = {
            "document_id": request.article["article_id"],
            "document_version_id": request.article["article_version_id"],
            "pipeline_version": request.pipeline_version,
            "quality_rules_version": "ml1-quality-fixture-v1",
            "status": self.status,
            "failures": [],
            "warnings": [],
            "text_utf8_bytes": len(request.article["body"].encode("utf-8")),
        }
        result.update(self.overrides)
        return result


class FakeIndexer:
    def __init__(self, calls: list[str], document_version_id: str | None = None) -> None:
        self.calls = calls
        self.document_version_id = document_version_id

    def index(self, request):
        self.calls.append("index")
        return IndexedArticleVersion(
            self.document_version_id or request.article["article_version_id"],
            (EventTextChunk("fixture-chunk", request.article["body"] or "fixture"),),
        )


class FakeEventProcessor:
    def __init__(self, calls: list[str]) -> None:
        self.calls = calls

    def process(self, request, indexed):
        self.calls.append("events")
        return []


class ArticleProcessingR02Tests(unittest.TestCase):
    def test_requires_closed_r02_extraction_metadata(self):
        for metadata in (
            {},
            {"extraction_method": "full_article"},
            {"extraction_method": "unknown", "extraction_errors": []},
            {"extraction_method": "full_article", "extraction_errors": ["UNKNOWN"]},
            {"extraction_method": "full_article", "extraction_errors": ["MISSING_PAGES", "MISSING_PAGES"]},
            {"extraction_method": "full_article", "extraction_errors": [], "extra": True},
        ):
            candidate = payload()
            candidate["article"]["metadata"] = metadata
            with self.subTest(metadata=metadata), self.assertRaises(ProcessingContractError):
                parse(candidate)

    def test_empty_body_reaches_quality_gate_and_oversize_is_413(self):
        request = parse(payload(body=""))
        self.assertEqual(request.article["body"], "")

        malformed_unicode = parse(payload(body="\ud800"))
        self.assertEqual(malformed_unicode.article["body"], "\ud800")

        candidate = payload(body="я" * 65537)
        with self.assertRaises(ProcessingContractError) as caught:
            parse(candidate)
        self.assertEqual(caught.exception.status, 413)
        self.assertEqual(caught.exception.code, "PAYLOAD_TOO_LARGE")

    def test_quality_fail_stops_indexing_and_event_processing(self):
        calls: list[str] = []
        gate = FakeQualityGate("FAIL")
        store = InMemoryOperationStore()
        coordinator = ArticleProcessingCoordinator(
            store=store,
            quality_gate=gate,
            indexer=FakeIndexer(calls),
            event_processor=FakeEventProcessor(calls),
        )
        accepted, created = coordinator.submit(parse(payload()))
        result = coordinator.execute(accepted["processing_id"])

        self.assertTrue(created)
        self.assertEqual(result["state"], "FAILED")
        self.assertEqual(result["error"]["code"], "ML_PROCESSING_ERROR")
        self.assertEqual(calls, [])
        self.assertEqual(store.get(accepted["processing_id"]).quality_result["status"], "FAIL")
        self.assertNotIn("quality_result", result)

    def test_quality_result_for_another_version_stops_processing(self):
        calls: list[str] = []
        store = InMemoryOperationStore()
        gate = FakeQualityGate("PASS", document_version_id="10000000-0000-4000-8000-000000000099")
        coordinator = ArticleProcessingCoordinator(
            store=store,
            quality_gate=gate,
            indexer=FakeIndexer(calls),
            event_processor=FakeEventProcessor(calls),
        )
        accepted, _ = coordinator.submit(parse(payload()))

        result = coordinator.execute(accepted["processing_id"])

        self.assertEqual(result["state"], "FAILED")
        self.assertEqual(calls, [])

    def test_quality_pass_runs_index_then_events_before_succeeded(self):
        calls: list[str] = []
        store = InMemoryOperationStore()
        coordinator = ArticleProcessingCoordinator(
            store=store,
            quality_gate=FakeQualityGate("PASS"),
            indexer=FakeIndexer(calls),
            event_processor=FakeEventProcessor(calls),
        )
        accepted, _ = coordinator.submit(parse(payload()))
        result = coordinator.execute(accepted["processing_id"])

        self.assertEqual(result["state"], "SUCCEEDED")
        self.assertEqual(result["document_version_id"], ARTICLE_VERSION_ID)
        self.assertEqual(result["event_ids"], [])
        self.assertEqual(calls, ["index", "events"])
        self.assertEqual(store.get(accepted["processing_id"]).quality_result["status"], "PASS")

    def test_worker_can_drain_a_persisted_queued_operation(self):
        calls: list[str] = []
        store = InMemoryOperationStore()
        coordinator = ArticleProcessingCoordinator(
            store=store,
            quality_gate=FakeQualityGate("PASS"),
            indexer=FakeIndexer(calls),
            event_processor=FakeEventProcessor(calls),
        )
        coordinator.submit(parse(payload()))

        result = coordinator.execute_next()

        self.assertEqual(result["state"], "SUCCEEDED")
        self.assertEqual(coordinator.execute_next(), None)
        self.assertEqual(calls, ["index", "events"])

    def test_document_version_id_must_match_submitted_article_version(self):
        calls: list[str] = []
        coordinator = ArticleProcessingCoordinator(
            store=InMemoryOperationStore(),
            quality_gate=FakeQualityGate("PASS"),
            indexer=FakeIndexer(calls, "10000000-0000-4000-8000-000000000099"),
            event_processor=FakeEventProcessor(calls),
        )
        accepted, _ = coordinator.submit(parse(payload()))

        result = coordinator.execute(accepted["processing_id"])

        self.assertEqual(result["state"], "FAILED")
        self.assertEqual(result["error"]["code"], "ML_PROCESSING_ERROR")
        self.assertEqual(calls, ["index"])


if __name__ == "__main__":
    unittest.main()
