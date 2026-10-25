"""Composition tests for the article-processing runtime factory."""

from __future__ import annotations

import unittest
from unittest.mock import patch

from fastapi import FastAPI

from ml_runtime.article_processing_runtime import create_article_processing_runtime
from ml_runtime.event_store import ML2EventProcessor
from ml_runtime.ml1_indexer import ML1IndexerAdapter
from ml_runtime.worker import ArticleProcessingWorker


class RuntimeFactoryTests(unittest.TestCase):
    def test_wires_injected_ml1_indexer_and_event_model_into_api_and_worker(self):
        class FakeIndexer:
            def index(self, document):
                raise AssertionError("indexer should not run while composing the service")

        class FakeQualityGate:
            max_text_bytes = 131072

            def assess(self, request):
                raise AssertionError("quality gate should not run while composing the service")

        operation_store = object()
        event_store = object()
        client = object()
        app = FastAPI()
        with (
            patch("ml_runtime.article_processing_runtime.ML1R02QualityGate", return_value=FakeQualityGate()),
            patch("ml_runtime.article_processing_runtime.create_article_processing_app_from_env", return_value=app),
        ):
            runtime = create_article_processing_runtime(
                FakeIndexer(),
                model_client=client,
                operation_store=operation_store,
                event_store=event_store,
            )

        self.assertIs(runtime.app, app)
        self.assertIsInstance(runtime.worker, ArticleProcessingWorker)
        self.assertIs(runtime.worker.coordinator, runtime.coordinator)
        self.assertIs(runtime.coordinator.store, operation_store)
        self.assertIsInstance(runtime.coordinator.indexer, ML1IndexerAdapter)
        self.assertIsInstance(runtime.coordinator.event_processor, ML2EventProcessor)
        self.assertIs(runtime.coordinator.event_processor.store, event_store)
        self.assertIs(runtime.coordinator.event_processor.extractor.client, client)
        self.assertIs(app.state.article_processing_worker, runtime.worker)


if __name__ == "__main__":
    unittest.main()
