"""Exact event identity and PostgreSQL persistence tests."""

from __future__ import annotations

import os
import unittest
from uuid import uuid4

from ml_runtime.event_extraction import CandidateEvent
from ml_runtime.event_store import ML2EventProcessor, PostgresEventStore, event_dedupe_key
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


class EventDedupeKeyTests(unittest.TestCase):
    def test_exact_identity_normalizes_case_whitespace_and_timezone(self):
        left = event()
        right = event(
            title="  обновление   условий новых договоров продукта x ",
            occurred_at="2026-10-31T21:00:00Z",
        )
        self.assertEqual(event_dedupe_key(left), event_dedupe_key(right))

    def test_different_date_or_geography_remains_a_separate_event(self):
        base_key = event_dedupe_key(event())
        self.assertNotEqual(
            base_key,
            event_dedupe_key(event(occurred_at="2026-12-01T00:00:00Z")),
        )
        other_geography = CandidateEvent(
            **{**event().__dict__, "geography": ({"type": "country", "code": "KZ", "name": "Казахстан"},)}
        )
        self.assertNotEqual(base_key, event_dedupe_key(other_geography))

    def test_processor_persists_extracted_candidates_for_matching_version(self):
        article = payload()
        request = parse_article_processing(
            article,
            f"{article['article']['article_version_id']}:{PIPELINE_VERSION}",
            max_text_bytes=131072,
            supported_pipeline_versions={PIPELINE_VERSION},
        )

        class Extractor:
            def extract(self, _request):
                return [event()]

        class Store:
            def __init__(self):
                self.saved = None

            def save_candidates(self, version_id, pipeline_version, candidates):
                self.saved = (version_id, pipeline_version, candidates)
                return ["event-id-1"]

        store = Store()
        processor = ML2EventProcessor(Extractor(), store)

        result = processor.process(request, request.article["article_version_id"])

        self.assertEqual(result, ["event-id-1"])
        self.assertEqual(store.saved[:2], (request.article["article_version_id"], PIPELINE_VERSION))

    def test_processor_rejects_different_document_version(self):
        article = payload()
        request = parse_article_processing(
            article,
            f"{article['article']['article_version_id']}:{PIPELINE_VERSION}",
            max_text_bytes=131072,
            supported_pipeline_versions={PIPELINE_VERSION},
        )
        with self.assertRaisesRegex(ValueError, "must match"):
            ML2EventProcessor(None, None).process(request, str(uuid4()))


@unittest.skipUnless(DATABASE_URL, "Set ML_TEST_DATABASE_URL to an isolated PostgreSQL test database")
class PostgresEventStoreTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        apply_migrations(DATABASE_URL)
        cls.store = PostgresEventStore(DATABASE_URL)

    def setUp(self) -> None:
        self.version_a = str(uuid4())
        self.version_b = str(uuid4())
        self.pipeline = "ml-pipeline-v1"

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
                           SELECT 1 FROM ml.event_documents d WHERE d.event_id = e.event_id
                       )"""
                )

    def test_replay_is_idempotent_and_exact_cross_article_identity_reuses_event(self):
        first = self.store.save_candidates(self.version_a, self.pipeline, [event()])
        replay = self.store.save_candidates(self.version_a, self.pipeline, [event()])
        second_article = self.store.save_candidates(
            self.version_b,
            self.pipeline,
            [event(title="обновление условий новых договоров продукта x", occurred_at="2026-10-31T21:00:00Z")],
        )

        self.assertEqual(replay, first)
        self.assertEqual(second_article, first)

    def test_changed_candidate_for_same_version_pipeline_fails_closed(self):
        self.store.save_candidates(self.version_a, self.pipeline, [event()])

        with self.assertRaisesRegex(RuntimeError, "changed on replay"):
            self.store.save_candidates(
                self.version_a,
                self.pipeline,
                [event(title="Совершенно другое событие")],
            )


if __name__ == "__main__":
    unittest.main()
