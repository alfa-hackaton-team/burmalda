"""Event-result boundary and PostgreSQL persistence tests."""

from __future__ import annotations

import os
import unittest
from uuid import uuid4

from ml_runtime.event_extraction import CandidateEvent, EventExtractionResult, EventTextChunk
from ml_runtime.event_store import ML2EventProcessor, PostgresEventStore
from ml_runtime.migrate import apply_migrations
from ml_runtime.tests.test_article_processing_r02 import PIPELINE_VERSION, payload
from ml_runtime.article_processing import parse_article_processing


DATABASE_URL = os.environ.get("ML_TEST_DATABASE_URL")


def event(
    *,
    title: str = "Обновление условий новых договоров продукта X",
    summary: str = "Новые условия для новых договоров.",
    occurred_at: str | None = "2026-11-01T00:00:00+03:00",
) -> CandidateEvent:
    return CandidateEvent(
        event_type="credit_terms_change",
        title=title,
        summary=summary,
        occurred_at=occurred_at,
        geography=({"type": "country", "code": "RU", "name": "Россия"},),
        confidence=None,
        evidence_quotes=("Новые договоры продукта X заключаются по обновленным условиям.",),
    )


class EventResultBoundaryTests(unittest.TestCase):
    def test_processor_persists_extracted_candidates_for_matching_version(self):
        article = payload()
        request = parse_article_processing(
            article,
            f"{article['article']['article_version_id']}:{PIPELINE_VERSION}",
            max_text_bytes=131072,
            supported_pipeline_versions={PIPELINE_VERSION},
        )

        class Extractor:
            event_pipeline_config_version = "event-extraction-v1"

            def extract(self, _request, *, chunks=None):
                return EventExtractionResult(
                    _request.article["article_version_id"],
                    self.event_pipeline_config_version,
                    (event(),),
                )

        class Store:
            def __init__(self):
                self.saved = None
                self.prior = None

            def get_saved_event_ids(self, _version_id, _config_version):
                return self.prior

            def save_result(self, result):
                self.saved = result
                return ["event-id-1"]

        store = Store()
        class ChunkProvider:
            def get_chunks(self, document_version_id):
                return [EventTextChunk("chunk-1", "Новые договоры продукта X заключаются по обновленным условиям.")]

        processor = ML2EventProcessor(Extractor(), store, ChunkProvider())

        result = processor.process(request, request.article["article_version_id"])

        self.assertEqual(result, ["event-id-1"])
        self.assertEqual(store.saved.document_version_id, request.article["article_version_id"])
        self.assertEqual(store.saved.event_pipeline_config_version, "event-extraction-v1")

    def test_processor_rejects_different_document_version(self):
        article = payload()
        request = parse_article_processing(
            article,
            f"{article['article']['article_version_id']}:{PIPELINE_VERSION}",
            max_text_bytes=131072,
            supported_pipeline_versions={PIPELINE_VERSION},
        )
        with self.assertRaisesRegex(ValueError, "must match"):
            ML2EventProcessor(None, None, None).process(request, str(uuid4()))


@unittest.skipUnless(DATABASE_URL, "Set ML_TEST_DATABASE_URL to an isolated PostgreSQL test database")
class PostgresEventStoreTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        apply_migrations(DATABASE_URL)
        cls.store = PostgresEventStore(DATABASE_URL)

    def setUp(self) -> None:
        self.version_a = str(uuid4())
        self.version_b = str(uuid4())
        self.pipeline = "event-extraction-v1"

    def tearDown(self) -> None:
        import psycopg

        with psycopg.connect(DATABASE_URL) as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    """DELETE FROM ml.event_extraction_runs
                       WHERE document_version_id IN (%s, %s)""",
                    (self.version_a, self.version_b),
                )
                cursor.execute(
                    """DELETE FROM ml.event_documents WHERE document_version_id IN (%s, %s)""",
                    (self.version_a, self.version_b),
                )
                cursor.execute(
                    """DELETE FROM ml.events e WHERE NOT EXISTS (
                           SELECT 1 FROM ml.event_documents d WHERE d.event_id = e.event_id
                       )"""
                )

    def test_replay_is_idempotent_without_cross_document_auto_merge(self):
        first_result = EventExtractionResult(self.version_a, self.pipeline, (event(),))
        second_result = EventExtractionResult(self.version_b, self.pipeline, (event(),))
        first = self.store.save_result(first_result)
        replay = self.store.save_result(first_result)
        second_article = self.store.save_result(second_result)

        self.assertEqual(replay, first)
        self.assertNotEqual(second_article, first)

    def test_changed_model_result_for_same_key_returns_first_saved_result(self):
        first_result = EventExtractionResult(self.version_a, self.pipeline, (event(),))
        changed_result = EventExtractionResult(
            self.version_a, self.pipeline, (event(title="Совершенно другое событие"),)
        )
        first = self.store.save_result(first_result)
        replay = self.store.save_result(changed_result)
        self.assertEqual(replay, first)


if __name__ == "__main__":
    unittest.main()
