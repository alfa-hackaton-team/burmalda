"""HTTP contract tests for the Go article-processing API."""

from __future__ import annotations

import unittest

from fastapi.testclient import TestClient

from ml_runtime.article_processing_api import create_article_processing_app
from ml_runtime.event_extraction import EventTextChunk
from ml_runtime.operations import (
    ArticleProcessingCoordinator,
    InMemoryOperationStore,
    IndexedArticleVersion,
)
from ml_runtime.tests.test_article_processing_r02 import PIPELINE_VERSION, payload


class FakeQualityGate:
    max_text_bytes = 131072

    def assess(self, request):
        return {
            "document_id": request.article["article_id"],
            "document_version_id": request.article["article_version_id"],
            "pipeline_version": request.pipeline_version,
            "quality_rules_version": "ml1-quality-fixture-v1",
            "status": "PASS",
            "failures": [],
            "warnings": [],
            "text_utf8_bytes": len(request.article["body"].encode("utf-8")),
        }


class FakeIndexer:
    def index(self, request):
        return IndexedArticleVersion(
            request.article["article_version_id"],
            (EventTextChunk("10000000-0000-4000-8000-000000000006", request.article["body"]),),
        )


class FakeEventProcessor:
    def process(self, request, indexed):
        return []


class ArticleProcessingAPITests(unittest.TestCase):
    def setUp(self) -> None:
        self.store = InMemoryOperationStore()
        coordinator = ArticleProcessingCoordinator(
            store=self.store,
            quality_gate=FakeQualityGate(),
            indexer=FakeIndexer(),
            event_processor=FakeEventProcessor(),
        )
        app = create_article_processing_app(
            coordinator,
            service_token="test-service-token",
            max_text_bytes=131072,
            supported_pipeline_versions={PIPELINE_VERSION},
        )
        self.client = TestClient(app)
        self.headers = {
            "Authorization": "Bearer test-service-token",
            "Idempotency-Key": f"10000000-0000-4000-8000-000000000003:{PIPELINE_VERSION}",
        }

    def test_post_persists_and_returns_202_then_worker_completes_and_get_returns_result(self):
        response = self.client.post(
            "/internal/v0.1/article-processings", json=payload(), headers=self.headers
        )

        self.assertEqual(response.status_code, 202)
        accepted = response.json()
        self.assertEqual(accepted["state"], "QUEUED")
        self.assertEqual(accepted["pipeline_version"], PIPELINE_VERSION)

        result = self.client.app.state.article_processing_coordinator.execute_next()
        self.assertEqual(result["state"], "SUCCEEDED")

        fetched = self.client.get(
            f"/internal/v0.1/article-processings/{accepted['processing_id']}",
            headers={"Authorization": "Bearer test-service-token"},
        )
        self.assertEqual(fetched.status_code, 200)
        self.assertEqual(fetched.json()["document_version_id"], payload()["article"]["article_version_id"])
        self.assertEqual(fetched.json()["event_ids"], [])

    def test_idempotent_post_replays_same_processing_id_and_changed_input_conflicts(self):
        first = self.client.post("/internal/v0.1/article-processings", json=payload(), headers=self.headers)
        replay = self.client.post("/internal/v0.1/article-processings", json=payload(), headers=self.headers)
        self.assertEqual(first.status_code, 202)
        self.assertEqual(replay.json()["processing_id"], first.json()["processing_id"])

        changed = payload()
        changed["article"]["metadata"]["extraction_errors"] = ["LAYOUT_LOSS"]
        conflict = self.client.post("/internal/v0.1/article-processings", json=changed, headers=self.headers)
        self.assertEqual(conflict.status_code, 409)
        self.assertEqual(conflict.json()["code"], "CONFLICT")

    def test_auth_and_get_not_found_use_contract_error_shape(self):
        denied = self.client.post("/internal/v0.1/article-processings", json=payload())
        self.assertEqual(denied.status_code, 401)
        self.assertEqual(denied.json()["code"], "UNAUTHENTICATED")

        missing = self.client.get(
            "/internal/v0.1/article-processings/10000000-0000-4000-8000-000000000099",
            headers={"Authorization": "Bearer test-service-token"},
        )
        self.assertEqual(missing.status_code, 404)
        self.assertEqual(missing.json()["code"], "NOT_FOUND")

    def test_payload_too_large_is_not_queued(self):
        oversized = payload(body="x" * 131073)
        response = self.client.post(
            "/internal/v0.1/article-processings", json=oversized, headers=self.headers
        )
        self.assertEqual(response.status_code, 413)
        self.assertEqual(response.json()["code"], "PAYLOAD_TOO_LARGE")
        self.assertEqual(self.store.claim_next(), None)

    def test_http_limit_must_match_ml1_quality_gate_limit(self):
        coordinator = ArticleProcessingCoordinator(
            store=InMemoryOperationStore(),
            quality_gate=FakeQualityGate(),
            indexer=FakeIndexer(),
            event_processor=FakeEventProcessor(),
        )
        with self.assertRaisesRegex(ValueError, "must match"):
            create_article_processing_app(
                coordinator,
                service_token="test-service-token",
                max_text_bytes=65536,
                supported_pipeline_versions={PIPELINE_VERSION},
            )


if __name__ == "__main__":
    unittest.main()
