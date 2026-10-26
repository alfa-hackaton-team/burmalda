"""Tests for the adapter to ML1Pipeline.index() and get_chunks()."""

from __future__ import annotations

import unittest
from types import SimpleNamespace
from typing import Any

from ml_runtime.article_processing import parse_article_processing
from ml_runtime.event_extraction import EventTextChunk
from ml_runtime.ml1_indexer import ML1IndexerAdapter
from ml_runtime.tests.test_article_processing_r02 import PIPELINE_VERSION, payload


ARTICLE_VERSION_ID = "10000000-0000-4000-8000-000000000003"
CHUNK_ID = "10000000-0000-4000-8000-000000000006"


def request():
    body = payload()
    return parse_article_processing(
        body,
        f"{ARTICLE_VERSION_ID}:{PIPELINE_VERSION}",
        max_text_bytes=131072,
        supported_pipeline_versions={PIPELINE_VERSION},
    )


class FakeML1Pipeline:
    def __init__(self, *, quality_status="PASS", indexed=True, response=None):
        self.quality_status = quality_status
        self.indexed = indexed
        self.response = response
        self.payload = None
        self.service_access = False
        self.chunk_request = None

    def index(self, value: dict[str, Any], *, service_access: bool = False):
        self.payload = value
        self.service_access = service_access
        version_id = value["article"]["article_version_id"]
        return SimpleNamespace(
            quality=SimpleNamespace(status=self.quality_status, document_version_id=version_id),
            indexed=self.indexed,
            chunk_ids=(CHUNK_ID,),
        )

    def get_chunks(self, value):
        self.chunk_request = value
        if self.response is not None:
            return self.response
        version_id = self.payload["article"]["article_version_id"]
        return {
            "status": "ok",
            "error": None,
            "unresolved_chunk_ids": [],
            "results": [{
                "chunk_id": CHUNK_ID,
                "document_version_id": version_id,
                "chunk_text": "Indexed source text.",
            }],
        }


class ML1IndexerAdapterTests(unittest.TestCase):
    def test_indexes_backend_payload_and_resolves_persisted_text_through_ml1(self):
        pipeline = FakeML1Pipeline()
        result = ML1IndexerAdapter(pipeline).index(request())

        self.assertEqual(result.document_version_id, ARTICLE_VERSION_ID)
        self.assertEqual(result.chunks, (EventTextChunk(CHUNK_ID, "Indexed source text."),))
        self.assertEqual(pipeline.payload["article"]["version_no"], 1)
        self.assertTrue(pipeline.service_access)
        self.assertEqual(pipeline.chunk_request["business_context_id"], None)
        self.assertEqual(pipeline.chunk_request["chunk_ids"], [CHUNK_ID])

    def test_rejects_quality_failure_or_unindexed_result(self):
        for pipeline in (
            FakeML1Pipeline(quality_status="FAIL", indexed=False),
            FakeML1Pipeline(indexed=False),
        ):
            with self.subTest(pipeline=pipeline), self.assertRaisesRegex(RuntimeError, "quality-approved"):
                ML1IndexerAdapter(pipeline).index(request())

    def test_rejects_incomplete_or_wrong_version_chunks(self):
        cases = (
            {
                "status": "partial", "error": None,
                "unresolved_chunk_ids": [CHUNK_ID], "results": [],
            },
            {
                "status": "ok", "error": None,
                "unresolved_chunk_ids": [],
                "results": [{
                    "chunk_id": CHUNK_ID,
                    "document_version_id": "10000000-0000-4000-8000-000000000099",
                    "chunk_text": "Other version.",
                }],
            },
        )
        for response in cases:
            pipeline = FakeML1Pipeline(response=response)
            with self.subTest(response=response), self.assertRaises(RuntimeError):
                ML1IndexerAdapter(pipeline).index(request())

    def test_rejects_missing_or_duplicate_chunk_identity(self):
        pipeline = FakeML1Pipeline()
        pipeline.index = lambda value, *, service_access=False: SimpleNamespace(
            quality=SimpleNamespace(status="PASS", document_version_id=ARTICLE_VERSION_ID),
            indexed=True,
            chunk_ids=(CHUNK_ID, CHUNK_ID),
        )
        with self.assertRaisesRegex(RuntimeError, "duplicate chunk IDs"):
            ML1IndexerAdapter(pipeline).index(request())


if __name__ == "__main__":
    unittest.main()
