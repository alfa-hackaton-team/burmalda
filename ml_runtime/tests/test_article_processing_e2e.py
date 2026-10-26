"""PostgreSQL-backed HTTP-to-event integration against the ML1 PR #24 contracts.

Set ML_TEST_DATABASE_URL to an isolated PostgreSQL database. The test uses the
real ML1 DTO, quality gate, ML2 API, operation/event stores, and worker; only the
embedding/index computation and model extraction are deterministic fakes.
"""

from __future__ import annotations

import importlib.util
import os
import unittest
from types import SimpleNamespace
from uuid import uuid4

from fastapi.testclient import TestClient

from ml_runtime.article_processing_api import create_article_processing_app
from ml_runtime.event_extraction import CandidateEvent, EventExtractionResult
from ml_runtime.event_store import ML2EventProcessor, PostgresEventStore
from ml_runtime.ml1_indexer import ML1IndexerAdapter
from ml_runtime.operations import ArticleProcessingCoordinator
from ml_runtime.postgres_store import PostgresOperationStore
from ml_runtime.quality_gate import ML1R02QualityGate
from ml_runtime.tests.postgres_test_schema import (
    cleanup_test_versions,
    prepare_ml_test_schema,
    seed_document_version,
)
from ml_runtime.tests.test_article_processing_r02 import PIPELINE_VERSION, payload


DATABASE_URL = os.environ.get("ML_TEST_DATABASE_URL")
HAS_ML1 = importlib.util.find_spec("ml") is not None


class ArticleProcessingEndToEndTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        if DATABASE_URL:
            prepare_ml_test_schema(DATABASE_URL)

    @unittest.skipUnless(DATABASE_URL and HAS_ML1, "Requires PR #24 code and isolated PostgreSQL")
    def test_http_worker_index_adapter_event_persistence_and_get(self) -> None:
        article = payload()
        version_id = article["article"]["article_version_id"]
        chunk_id = str(uuid4())
        seed_document_version(DATABASE_URL, version_id, article["article"]["article_id"])

        class FakeProductionPipeline:
            """Match ML1Pipeline.index and get_chunks DTOs without model downloads."""

            def index(self, backend_input, *, service_access=False):
                self.assertions = (backend_input, service_access)
                from ml.adapters.backend_documents import document_from_backend
                from ml.ingestion.quality import check_document

                quality = check_document(document_from_backend(backend_input))
                return SimpleNamespace(quality=quality, chunk_ids=(chunk_id,), indexed=quality.status == "PASS")

            def get_chunks(self, request):
                self.request = request
                return {
                    "contract_version": "v1",
                    "trace_id": request["trace_id"],
                    "status": "ok",
                    "results": [{
                        "chunk_id": chunk_id,
                        "document_id": article["article"]["article_id"],
                        "document_version_id": version_id,
                        "chunk_text": article["article"]["body"],
                    }],
                    "unresolved_chunk_ids": [],
                    "warnings": [],
                    "error": None,
                }

        class FakeExtractor:
            event_pipeline_config_version = "event-extraction-e2e-v1"

            def __init__(self):
                self.calls = 0

            def extract(self, request, *, chunks):
                self.calls += 1
                return EventExtractionResult(
                    document_version_id=request.article["article_version_id"],
                    event_pipeline_config_version=self.event_pipeline_config_version,
                    events=(CandidateEvent(
                        event_type="terms_change",
                        title="Изменение условий",
                        summary="В продукте X изменились условия.",
                        occurred_at=None,
                        geography=(),
                        confidence=None,
                        evidence_quotes=(chunks[0].text,),
                        evidence_chunk_ids=((chunk_id,),),
                    ),),
                )

        extractor = FakeExtractor()
        coordinator = ArticleProcessingCoordinator(
            store=PostgresOperationStore(DATABASE_URL),
            quality_gate=ML1R02QualityGate(),
            indexer=ML1IndexerAdapter(FakeProductionPipeline()),
            event_processor=ML2EventProcessor(extractor, PostgresEventStore(DATABASE_URL)),
        )
        client = TestClient(create_article_processing_app(
            coordinator,
            service_token="integration-test-token",
            max_text_bytes=131072,
            supported_pipeline_versions={PIPELINE_VERSION},
        ))
        headers = {
            "Authorization": "Bearer integration-test-token",
            "Idempotency-Key": f"{version_id}:{PIPELINE_VERSION}",
        }

        try:
            accepted = client.post("/internal/v0.1/article-processings", json=article, headers=headers)
            self.assertEqual(accepted.status_code, 202, accepted.text)
            processing_id = accepted.json()["processing_id"]
            replay = client.post("/internal/v0.1/article-processings", json=article, headers=headers)
            self.assertEqual(replay.json()["processing_id"], processing_id)

            completed = coordinator.execute_next()
            self.assertEqual(completed["state"], "SUCCEEDED", completed)
            self.assertEqual(completed["document_version_id"], version_id)
            self.assertEqual(len(completed["event_ids"]), 1)
            self.assertEqual(extractor.calls, 1)

            fetched = client.get(
                f"/internal/v0.1/article-processings/{processing_id}",
                headers={"Authorization": "Bearer integration-test-token"},
            )
            self.assertEqual(fetched.status_code, 200, fetched.text)
            self.assertEqual(fetched.json(), completed)

            import psycopg

            with psycopg.connect(DATABASE_URL) as connection:
                with connection.cursor() as cursor:
                    cursor.execute(
                        "SELECT event_type, title FROM ml.events WHERE id = %s",
                        (completed["event_ids"][0],),
                    )
                    self.assertEqual(cursor.fetchone(), ("terms_change", "Изменение условий"))
                    cursor.execute(
                        "SELECT evidence_quotes FROM ml.event_documents WHERE event_id = %s",
                        (completed["event_ids"][0],),
                    )
                    evidence = cursor.fetchone()[0]
                    self.assertEqual(evidence[0]["chunk_id"], chunk_id)
                    self.assertEqual(evidence[0]["quote_text"], article["article"]["body"])
        finally:
            cleanup_test_versions(DATABASE_URL, (version_id,))


if __name__ == "__main__":
    unittest.main()
