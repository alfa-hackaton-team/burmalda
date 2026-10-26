"""Composition tests for the article-processing runtime factory."""

from __future__ import annotations

import os
import unittest
from types import SimpleNamespace
from unittest.mock import patch

from fastapi import FastAPI

from ml_runtime.article_processing_runtime import (
    create_article_processing_api_runtime,
    create_article_processing_runtime,
    create_article_processing_worker_runtime,
    load_indexer_from_env,
)
from ml_runtime.event_store import ML2EventProcessor
from ml_runtime.ml1_indexer import ML1IndexerAdapter
from ml_runtime.worker import ArticleProcessingWorker


class RuntimeFactoryTests(unittest.TestCase):
    def test_api_runtime_does_not_load_models_or_indexer(self):
        class FakeQualityGate:
            max_text_bytes = 131072

        operation_store = object()
        app = FastAPI()
        with (
            patch.dict(os.environ, {
                "ML_SERVICE_TOKEN": "test-token",
                "ML_MAX_TEXT_BYTES": "131072",
                "ML_PIPELINE_VERSIONS": "ml-pipeline-v1",
            }),
            patch("ml_runtime.article_processing_runtime.create_article_processing_app_from_env", return_value=app),
            patch("ml_runtime.article_processing_runtime.OpenAICompatibleModelClient") as model_client,
        ):
            runtime = create_article_processing_api_runtime(
                operation_store=operation_store,
                quality_gate=FakeQualityGate(),
            )

        self.assertIs(runtime.app, app)
        self.assertIs(runtime.service.store, operation_store)
        model_client.assert_not_called()

    def test_worker_runtime_does_not_require_http_service_configuration(self):
        class FakeIndexer:
            def index(self, payload, **kwargs):
                raise AssertionError("indexer must not run while composing worker")

            def get_chunks(self, request):
                raise AssertionError("retriever must not run while composing worker")

        with (
            patch.dict(os.environ, {}, clear=True),
            patch("ml_runtime.article_processing_runtime.PostgresOperationStore", return_value=object()),
            patch("ml_runtime.article_processing_runtime.PostgresEventStore", return_value=object()),
            patch("ml_runtime.article_processing_runtime.OpenAICompatibleModelClient", return_value=object()) as client_factory,
        ):
            runtime = create_article_processing_worker_runtime(FakeIndexer())

        self.assertIsInstance(runtime.worker, ArticleProcessingWorker)
        self.assertEqual(client_factory.call_args.kwargs['reasoning_effort'], 'low')

    def test_loads_an_indexer_factory_from_the_configured_module_path(self):
        indexer = type("Indexer", (), {
            "index": lambda self, payload, **kwargs: None,
            "get_chunks": lambda self, request: None,
        })()
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
            with self.assertRaisesRegex(ValueError, "ML1Pipeline"):
                load_indexer_from_env()

    def test_wires_injected_ml1_indexer_and_event_model_into_api_and_worker(self):
        class FakeIndexer:
            def index(self, payload, **kwargs):
                raise AssertionError("indexer should not run while composing the service")

            def get_chunks(self, request):
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
