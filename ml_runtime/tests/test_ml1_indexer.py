"""Tests for the ML1-to-ML2 indexed-chunk adapter."""

from __future__ import annotations

import unittest
from typing import Any

from ml_runtime.event_extraction import EventTextChunk
from ml_runtime.ml1_indexer import ML1IndexerAdapter
from ml_runtime.tests.test_article_processing_r02 import PIPELINE_VERSION, payload
from ml_runtime.article_processing import parse_article_processing


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


class FakeML1Indexer:
    def __init__(self, result: dict[str, Any]):
        self.result = result
        self.document = None

    def index(self, document):
        self.document = document
        return self.result


class ML1IndexerAdapterTests(unittest.TestCase):
    def test_converts_canonical_ml1_result_to_ml2_chunks(self):
        indexer = FakeML1Indexer({
            "document_version_id": ARTICLE_VERSION_ID,
            "chunks": [{"chunk_id": CHUNK_ID, "text": "Indexed source text."}],
        })
        seen_payload = {}

        def document_factory(value):
            seen_payload.update(value)
            return {"canonical_document": value["article"]["article_version_id"]}

        result = ML1IndexerAdapter(indexer, document_factory=document_factory).index(request())

        self.assertEqual(result.document_version_id, ARTICLE_VERSION_ID)
        self.assertEqual(result.chunks, (EventTextChunk(CHUNK_ID, "Indexed source text."),))
        self.assertEqual(indexer.document, {"canonical_document": ARTICLE_VERSION_ID})
        self.assertEqual(seen_payload["pipeline_version"], PIPELINE_VERSION)

    def test_rejects_a_different_document_version(self):
        indexer = FakeML1Indexer({
            "document_version_id": "10000000-0000-4000-8000-000000000099",
            "chunks": [{"chunk_id": CHUNK_ID, "text": "Indexed source text."}],
        })
        with self.assertRaisesRegex(ValueError, "document_version_id"):
            ML1IndexerAdapter(indexer, document_factory=lambda value: value).index(request())

    def test_rejects_missing_or_duplicate_chunk_identity(self):
        for chunks in (
            [{"text": "Indexed source text."}],
            [
                {"chunk_id": CHUNK_ID, "text": "first"},
                {"chunk_id": CHUNK_ID, "text": "duplicate"},
            ],
        ):
            with self.subTest(chunks=chunks):
                indexer = FakeML1Indexer({"document_version_id": ARTICLE_VERSION_ID, "chunks": chunks})
                with self.assertRaises(ValueError):
                    ML1IndexerAdapter(indexer, document_factory=lambda value: value).index(request())


if __name__ == "__main__":
    unittest.main()
