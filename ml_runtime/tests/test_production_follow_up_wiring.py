"""Exercise the compose entrypoint and durable queue with deterministic model IO."""
import os
import unittest
from types import SimpleNamespace
from unittest.mock import Mock, patch
from uuid import uuid4

from fastapi.testclient import TestClient

from ml_runtime.agent import AnalysisAgent
from ml_runtime.article_processing_api import create_article_processing_app
from ml_runtime.citation_support import ensure_attributed_support
from ml_runtime.product_corpus import ProductCorpus
from ml_runtime.product_store import ProductStore
from ml_runtime.run_analysis_worker import process_next
from ml_runtime.semantic_verifier import LLMSemanticVerifier
from ml_runtime.serve_article_processing import main
from ml_runtime.tests.test_analysis_contract import load_fixture
from ml_runtime.tests.test_follow_up_snapshot import SnapshotClient


DSN = os.environ.get('ML_TEST_DATABASE_URL')


@unittest.skipUnless(DSN, 'Set ML_TEST_DATABASE_URL to an isolated PostgreSQL database')
class ProductionFollowUpWiringTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        from ml_runtime.tests.postgres_test_schema import prepare_ml_test_schema
        prepare_ml_test_schema(DSN)

    def test_entrypoint_has_one_queued_route_and_replays_verified_snapshot(self):
        store = ProductStore(DSN)
        request = load_fixture('follow_up.request.json')
        request['request_operation_id'] = str(uuid4())
        app = create_article_processing_app(
            SimpleNamespace(quality_gate=SimpleNamespace(max_text_bytes=131072)),
            service_token='test-token', max_text_bytes=131072,
            supported_pipeline_versions={'ml-pipeline-v1'})
        # This is the registration path invoked by compose's Python entrypoint.
        with patch('ml_runtime.serve_article_processing.create_article_processing_api_runtime',
                   return_value=SimpleNamespace(app=app)), \
             patch('ml_runtime.product_store.ProductStore', return_value=store), \
             patch.dict(os.environ, {'ML_SERVICE_TOKEN': 'test-token',
                                     'ML_ANALYSIS_CONFIG_VERSION': request['processing_config_version']}), \
             patch('ml_runtime.serve_article_processing.uvicorn.run') as serve:
            self.assertEqual(main(), 0)
            self.assertIs(serve.call_args.args[0], app)
        routes = [r for r in app.routes if getattr(r, 'path', '') == '/internal/v0.1/follow-ups'
                  and 'POST' in getattr(r, 'methods', ())]
        self.assertEqual(len(routes), 1)
        client = TestClient(app)
        headers = {'Authorization': 'Bearer test-token',
                   'Idempotency-Key': request['request_operation_id']}
        model = SnapshotClient()
        agent = AnalysisAgent(model, semantic_verifier=LLMSemanticVerifier(model))
        try:
            with patch('ml_runtime.model_client.create_agent_from_env', return_value=agent) as factory, \
                 patch('ml_runtime.service.BoundedSearchRunner',
                       side_effect=AssertionError('Follow-up entered retrieval/reranking')) as search, \
                 patch('ml_runtime.agent.ensure_attributed_support', wraps=ensure_attributed_support) as guard:
                submitted = client.post('/internal/v0.1/follow-ups', json=request, headers=headers)
                self.assertEqual(submitted.status_code, 202, submitted.text)
                operation = submitted.json()
                self.assertEqual(operation['state'], 'QUEUED')
                factory.assert_not_called()
                self.assertTrue(process_next(store, ProductCorpus(store)))
                identity = operation['analysis_id']
                result = client.get('/internal/v0.1/analyses/' + identity, headers=headers)
                self.assertEqual(result.status_code, 200)
                self.assertEqual(result.json()['state'], 'SUCCEEDED', result.text)
                self.assertEqual(result.json()['response']['analysis_run_id'], identity)
                guard.assert_called_once()
                search.assert_not_called()
                factory.assert_called_once()
                self.assertEqual(len(model.inputs), 2)  # generation + production verifier adapter
                self.assertIn('claim_records', model.inputs[1])
                replay = client.post('/internal/v0.1/follow-ups', json=request, headers=headers)
                self.assertEqual(replay.status_code, 202)
                self.assertEqual(replay.json(), result.json())
                self.assertEqual(ProductStore(DSN).get(identity), result.json())
                self.assertFalse(process_next(store, ProductCorpus(store)))
                self.assertEqual(len(model.inputs), 2)
        finally:
            with store.connect() as connection:
                connection.execute('DELETE FROM ml.analysis_operations WHERE request_operation_id=%s',
                                   (request['request_operation_id'],))
