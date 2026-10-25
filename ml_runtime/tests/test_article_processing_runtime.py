"""Composition tests for the article-processing runtime factory."""

from __future__ import annotations

import os
import unittest
from types import SimpleNamespace
from unittest.mock import patch

from fastapi import FastAPI

from ml_runtime.article_processing_runtime import (
    create_article_processing_runtime,
    load_indexer_from_env,
)
from ml_runtime.event_store import ML2EventProcessor
from ml_runtime.ml1_indexer import ML1IndexerAdapter
from ml_runtime.worker import ArticleProcessingWorker


class RuntimeFactoryTests(unittest.TestCase):
    def test_loads_an_indexer_factory_from_the_configured_module_path(self):
        indexer = type("Indexer", (), {"index": lambda self, document: None})()
        module = SimpleNamespace(indexer_factory=lambda: indexer)
        with (
            patch.dict(os.environ, {"ML_INDEXER_FACTORY": "ml_plugin:indexer_factory"}),
            patch("ml_runtime.article_processing_runtime.importlib.import_module", return_value=module) as load,
        ):
            loaded = load_indexer_from_env()
        self.assertIs(loaded, indexer)
        load.assert_called_once_with("ml_plugin")

    def test_rejects_missing_or_malformed_indexer_plugin_setting(self):
        for value in ("", "missing-separator", ":factory", "module:", "module:a:b"):
            with self.subTest(value=value), patch.dict(os.environ, {"ML_INDEXER_FACTORY": value}):
                with self.assertRaisesRegex(ValueError, "ML_INDEXER_FACTORY"):
                    load_indexer_from_env()

    def test_rejects_factory_without_an_index_method(self):
        module = SimpleNamespace(indexer_factory=lambda: object())
        with (
            patch.dict(os.environ, {"ML_INDEXER_FACTORY": "ml_plugin:indexer_factory"}),
            patch("ml_runtime.article_processing_runtime.importlib.import_module", return_value=module),
        ):
            with self.assertRaisesRegex(ValueError, r"index\(document\)"):
                load_indexer_from_env()

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
