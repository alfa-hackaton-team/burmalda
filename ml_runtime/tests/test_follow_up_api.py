"""HTTP validation/auth and wiring; the production model factory is mocked."""
from types import SimpleNamespace
import unittest
from unittest.mock import Mock

from fastapi.testclient import TestClient

from ml_runtime.agent import AnalysisAgent
from ml_runtime.article_processing_api import create_article_processing_app
from ml_runtime.follow_up_store import FollowUpConflict
from ml_runtime.semantic_verifier import LLMSemanticVerifier
from ml_runtime.service import AnalysisService
from ml_runtime.tests.test_analysis_contract import load_fixture
from ml_runtime.tests.test_follow_up_snapshot import SnapshotClient


class FollowUpAPITests(unittest.TestCase):
    def setUp(self):
        self.request = load_fixture('follow_up.request.json')
        coordinator = SimpleNamespace(quality_gate=SimpleNamespace(max_text_bytes=131072))
        self.app = create_article_processing_app(coordinator, service_token='internal-test',
            max_text_bytes=131072, supported_pipeline_versions={'ml-pipeline-v1'}, synchronous_follow_up=True)
        self.store = Mock()
        self.store.execute.side_effect = lambda key, req, analyze: analyze(req)
        client = SnapshotClient()
        self.retriever = Mock()
        service = AnalysisService(self.retriever, AnalysisAgent(
            client, semantic_verifier=LLMSemanticVerifier(client)))
        self.factory = Mock(return_value=service)
        self.app.state.follow_up_store_factory = lambda: self.store
        self.app.state.follow_up_service_factory = self.factory
        self.client = TestClient(self.app)
        self.headers = {'Authorization': 'Bearer internal-test',
                        'Idempotency-Key': self.request['request_operation_id']}

    def post(self, headers=None):
        return self.client.post('/internal/v0.1/follow-ups', json=self.request,
                                headers=self.headers if headers is None else headers)

    def test_explicit_standalone_adapter_calls_snapshot_service(self):
        response = self.post()
        self.assertEqual(response.status_code, 200)
        self.assertEqual(response.json()['status'], 'succeeded')
        self.store.execute.assert_called_once()
        self.factory.assert_called_once()
        self.retriever.assert_not_called()
        self.retriever.search.assert_not_called()
        self.retriever.get_chunks.assert_not_called()

    def test_missing_or_wrong_bearer_does_not_touch_storage_or_model(self):
        for auth in ('', 'Bearer wrong'):
            with self.subTest(auth=auth):
                response = self.post({**self.headers, 'Authorization': auth})
                self.assertEqual(response.status_code, 401)
        self.store.execute.assert_not_called()
        self.factory.assert_not_called()

    def test_missing_mismatched_key_or_wrong_request_type_is_400(self):
        for key in ('', 'wrong'):
            self.assertEqual(self.post({**self.headers, 'Idempotency-Key': key}).status_code, 400)
        self.request = load_fixture('analysis.request.json')
        self.assertEqual(self.post().status_code, 400)
        self.store.execute.assert_not_called()

    def test_malformed_json_and_schema_are_400(self):
        response = self.client.post('/internal/v0.1/follow-ups', content='{', headers=self.headers)
        self.assertEqual(response.status_code, 400)
        self.request = {}
        self.assertEqual(self.post().status_code, 400)

    def test_inconsistent_snapshot_is_403_without_storage_or_model(self):
        self.request['context']['profile_version'] = 2
        self.assertEqual(self.post().status_code, 403)
        self.store.execute.assert_not_called()

    def test_conflict_and_persistence_outage_map_to_safe_http_errors(self):
        for exc, status in ((FollowUpConflict(), 409), (RuntimeError('secret dsn'), 503)):
            self.store.execute.side_effect = exc
            response = self.post()
            self.assertEqual(response.status_code, status)
            self.assertNotIn('secret', response.text)
        self.factory.assert_not_called()

    def test_existing_result_is_returned_without_constructing_model(self):
        result = load_fixture('follow_up.ok.json')
        self.store.execute.side_effect = None
        self.store.execute.return_value = result
        self.assertEqual(self.post().json(), result)
        self.factory.assert_not_called()
