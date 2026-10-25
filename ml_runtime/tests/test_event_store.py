"""Event-result boundary and PostgreSQL persistence tests."""

from __future__ import annotations

import os
import unittest
from uuid import uuid4

from ml_runtime.event_extraction import CandidateEvent, EventExtractionResult, EventTextChunk
from ml_runtime.event_store import ML2EventProcessor, PostgresEventStore
from ml_runtime.operations import IndexedArticleVersion
from ml_runtime.migrate import apply_migrations
from ml_runtime.postgres_store import PostgresOperationStore
from ml_runtime.tests.test_article_processing_r02 import PIPELINE_VERSION, payload
from ml_runtime.article_processing import parse_article_processing


DATABASE_URL = os.environ.get("ML_TEST_DATABASE_URL")
CHUNK_ID = "10000000-0000-4000-8000-000000000006"


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
        evidence_chunk_ids=((CHUNK_ID,),),
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

            def get_saved_event_ids(self, _version_id, _pipeline_version, _config_version):
                return self.prior

            def save_result(self, result, indexed, _pipeline_version):
                self.saved = result
                return ["event-id-1"]

        store = Store()
        processor = ML2EventProcessor(Extractor(), store)

        indexed = IndexedArticleVersion(
            request.article["article_version_id"],
            (EventTextChunk(CHUNK_ID, "Новые договоры продукта X заключаются по обновленным условиям."),),
        )
        result = processor.process(request, indexed)

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
            ML2EventProcessor(None, None).process(
                request, IndexedArticleVersion(str(uuid4()), (EventTextChunk(CHUNK_ID, "text"),))
            )


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
        self._create_operation(self.version_a)
        self._create_operation(self.version_b)

    def _create_operation(self, version_id: str) -> None:
        request_payload = payload()
        request_payload["article"]["article_version_id"] = version_id
        request = parse_article_processing(
            request_payload,
            f"{version_id}:{PIPELINE_VERSION}",
            max_text_bytes=131072,
            supported_pipeline_versions={PIPELINE_VERSION},
        )
        PostgresOperationStore(DATABASE_URL).create_or_get(request)

    def tearDown(self) -> None:
        import psycopg

        with psycopg.connect(DATABASE_URL) as connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    """DELETE FROM ml.event_documents WHERE document_version_id IN (%s, %s)""",
                    (self.version_a, self.version_b),
                )
                cursor.execute(
                    """DELETE FROM ml.events e WHERE NOT EXISTS (
                           SELECT 1 FROM ml.event_documents d WHERE d.event_id = e.id
                       )"""
                )
                cursor.execute(
                    """DELETE FROM ml.article_processing_operations
                       WHERE article_version_id IN (%s, %s)""",
                    (self.version_a, self.version_b),
                )

    def test_replay_is_idempotent_without_cross_document_auto_merge(self):
        first_result = EventExtractionResult(self.version_a, self.pipeline, (event(),))
        second_result = EventExtractionResult(self.version_b, self.pipeline, (event(),))
        first = self.store.save_result(first_result, indexed(self.version_a), PIPELINE_VERSION)
        replay = self.store.save_result(first_result, indexed(self.version_a), PIPELINE_VERSION)
        second_article = self.store.save_result(second_result, indexed(self.version_b), PIPELINE_VERSION)

        self.assertEqual(replay, first)
        self.assertNotEqual(second_article, first)

    def test_changed_model_result_for_same_key_returns_first_saved_result(self):
        first_result = EventExtractionResult(self.version_a, self.pipeline, (event(),))
        changed_result = EventExtractionResult(
            self.version_a, self.pipeline, (event(title="Совершенно другое событие"),)
        )
        first = self.store.save_result(first_result, indexed(self.version_a), PIPELINE_VERSION)
        replay = self.store.save_result(changed_result, indexed(self.version_a), PIPELINE_VERSION)
        self.assertEqual(replay, first)

    def test_empty_event_result_is_replayed_from_the_operation_row(self):
        empty = EventExtractionResult(self.version_a, self.pipeline, ())
        first = self.store.save_result(empty, indexed(self.version_a), PIPELINE_VERSION)
        replay = self.store.get_saved_event_ids(
            self.version_a, PIPELINE_VERSION, self.pipeline
        )
        self.assertEqual(first, [])
        self.assertEqual(replay, [])


def indexed(document_version_id: str) -> IndexedArticleVersion:
    return IndexedArticleVersion(
        document_version_id,
        (EventTextChunk(CHUNK_ID, "Новые договоры продукта X заключаются по обновленным условиям."),),
    )


if __name__ == "__main__":
    unittest.main()
