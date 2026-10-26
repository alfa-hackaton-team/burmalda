"""PostgreSQL persistence tests; set ML_TEST_DATABASE_URL to run integration cases."""

from __future__ import annotations

import os
import unittest
from threading import Barrier
from concurrent.futures import ThreadPoolExecutor
from copy import deepcopy
from uuid import uuid4

from ml_runtime.article_processing import ProcessingContractError, parse_article_processing
from ml_runtime.postgres_store import PostgresOperationStore
from ml_runtime.tests.test_article_processing_r02 import PIPELINE_VERSION, payload
from ml_runtime.tests.postgres_test_schema import (
    cleanup_test_versions,
    prepare_ml_test_schema,
    seed_document_version,
)


DATABASE_URL = os.environ.get("ML_TEST_DATABASE_URL")


@unittest.skipUnless(DATABASE_URL, "Set ML_TEST_DATABASE_URL to an isolated PostgreSQL test database")
class PostgresOperationStoreTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        prepare_ml_test_schema(DATABASE_URL)
        cls.store = PostgresOperationStore(DATABASE_URL)

    def setUp(self) -> None:
        self.article_version_id = str(uuid4())
        self.idempotency_key = f"{self.article_version_id}:{PIPELINE_VERSION}"
        self.payload = payload()
        self.payload["article"]["article_version_id"] = self.article_version_id
        self.request = parse_article_processing(
            self.payload,
            self.idempotency_key,
            max_text_bytes=131072,
            supported_pipeline_versions={PIPELINE_VERSION},
        )
        seed_document_version(
            DATABASE_URL,
            self.article_version_id,
            self.payload["article"]["article_id"],
        )

    def tearDown(self) -> None:
        cleanup_test_versions(DATABASE_URL, (self.article_version_id,))

    def test_input_operation_and_terminal_result_survive_store_instances(self) -> None:
        operation, created = self.store.create_or_get(self.request)
        self.assertTrue(created)
        restored = PostgresOperationStore(DATABASE_URL)
        stored_input = restored.get_input(operation.processing_id)
        self.assertEqual(stored_input.article, self.request.article)
        self.assertEqual(stored_input.fingerprint, self.request.fingerprint)

        claimed, did_claim = restored.claim(operation.processing_id)
        self.assertTrue(did_claim)
        self.assertEqual(claimed.state, "RUNNING")
        repeated_claim, did_claim_again = self.store.claim(operation.processing_id)
        self.assertFalse(did_claim_again)
        self.assertEqual(repeated_claim.state, "RUNNING")

        failed = self.store.transition(
            operation.processing_id,
            "RUNNING",
            "FAILED",
            error={"code": "ML_PROCESSING_ERROR", "message": "Synthetic failure", "retryable": False},
            quality_result={"status": "FAIL", "failures": []},
        )
        self.assertEqual(failed.state, "FAILED")
        self.assertEqual(restored.get(operation.processing_id).error["code"], "ML_PROCESSING_ERROR")
        self.assertEqual(restored.get(operation.processing_id).quality_result["status"], "FAIL")

    def test_saved_input_preserves_text_that_r02_must_reject(self) -> None:
        invalid_body = "NUL:\x00 invalid Unicode:\ud800"
        candidate = payload(body=invalid_body)
        candidate["article"]["article_version_id"] = self.article_version_id
        request = parse_article_processing(
            candidate,
            self.idempotency_key,
            max_text_bytes=131072,
            supported_pipeline_versions={PIPELINE_VERSION},
        )
        operation, _ = self.store.create_or_get(request)
        restored = self.store.get_input(operation.processing_id)
        self.assertEqual(restored.article["body"], invalid_body)

    def test_same_key_replays_operation_but_changed_parameters_conflict(self) -> None:
        first, created = self.store.create_or_get(self.request)
        self.assertTrue(created)

        replay_payload = deepcopy(self.payload)
        replay_payload["trace_id"] = str(uuid4())
        replay_request = parse_article_processing(
            replay_payload,
            self.idempotency_key,
            max_text_bytes=131072,
            supported_pipeline_versions={PIPELINE_VERSION},
        )
        replay, replay_created = self.store.create_or_get(replay_request)
        self.assertFalse(replay_created)
        self.assertEqual(replay.processing_id, first.processing_id)
        self.assertEqual(replay.trace_id, self.request.trace_id)

        changed_payload = deepcopy(self.payload)
        changed_payload["article"]["metadata"]["extraction_errors"] = ["LAYOUT_LOSS"]
        changed = parse_article_processing(
            changed_payload,
            self.idempotency_key,
            max_text_bytes=131072,
            supported_pipeline_versions={PIPELINE_VERSION},
        )
        with self.assertRaises(ProcessingContractError) as caught:
            self.store.create_or_get(changed)
        self.assertEqual(caught.exception.status, 409)

    def test_parallel_posts_create_only_one_operation(self) -> None:
        barrier_request = self.request
        barrier = Barrier(2)

        def submit():
            barrier.wait()
            return PostgresOperationStore(DATABASE_URL).create_or_get(barrier_request)

        with ThreadPoolExecutor(max_workers=2) as workers:
            results = list(workers.map(lambda _: submit(), range(2)))
        self.assertEqual(sum(created for _, created in results), 1)
        self.assertEqual(results[0][0].processing_id, results[1][0].processing_id)

    def test_succeeded_result_including_empty_event_ids_is_persistent(self) -> None:
        operation, _ = self.store.create_or_get(self.request)
        _, claimed = self.store.claim(operation.processing_id)
        self.assertTrue(claimed)
        completed = self.store.transition(
            operation.processing_id,
            "RUNNING",
            "SUCCEEDED",
            document_version_id=self.article_version_id,
            event_ids=[],
            quality_result={"status": "PASS", "failures": [], "warnings": []},
        )
        restored = PostgresOperationStore(DATABASE_URL).get(operation.processing_id)
        self.assertEqual(completed.to_wire(), restored.to_wire())
        self.assertEqual(restored.event_ids, [])


class PostgresMigrationUnitTests(unittest.TestCase):
    def test_current_migration_files_are_separately_versioned(self) -> None:
        from pathlib import Path
        import re

        directory = Path(__file__).resolve().parents[1] / "migrations"
        files = sorted(directory.glob("*.sql"))
        self.assertTrue(files)
        self.assertEqual(len(files), len({path.name[:6] for path in files}))
        self.assertTrue(all(re.fullmatch(r"\d{6}_[a-z0-9_]+\.sql", path.name) for path in files))
        migration = files[0].read_text(encoding="utf-8")
        self.assertIn("ALTER TABLE ml.article_processings", migration)
        self.assertNotIn("CREATE TABLE ml.events", migration)
        self.assertIn("ml.ml2_schema_migrations", Path(__file__).resolve().parents[1].joinpath("migrate.py").read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
