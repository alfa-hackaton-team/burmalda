"""Real PostgreSQL transport idempotency; no external model calls."""
import copy
from concurrent.futures import ThreadPoolExecutor
import os
from threading import Event
import unittest
from unittest.mock import Mock
from uuid import uuid4

from ml_runtime.agent import AnalysisAgent
from ml_runtime.follow_up_store import FollowUpConflict, PostgresFollowUpStore
from ml_runtime.semantic_verifier import LLMSemanticVerifier
from ml_runtime.service import AnalysisService
from ml_runtime.tests.postgres_test_schema import prepare_ml_test_schema
from ml_runtime.tests.test_analysis_contract import load_fixture
from ml_runtime.tests.test_follow_up_snapshot import SnapshotClient


DSN = os.environ.get('ML_TEST_DATABASE_URL')


@unittest.skipUnless(DSN, 'ML_TEST_DATABASE_URL must name an isolated test database')
class FollowUpStoreTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        prepare_ml_test_schema(DSN)

    def setUp(self):
        self.request = load_fixture('follow_up.request.json')
        self.request['request_operation_id'] = str(uuid4())
        self.key = self.request['request_operation_id']
        self.store = PostgresFollowUpStore(DSN)
        client = SnapshotClient()
        self.retriever = Mock(spec=['search', 'get_chunks'])
        self.service = AnalysisService(self.retriever, AnalysisAgent(
            client, semantic_verifier=LLMSemanticVerifier(client)))

    def tearDown(self):
        import psycopg
        with psycopg.connect(DSN) as conn:
            conn.execute('DELETE FROM ml.follow_up_operations WHERE idempotency_key=%s', (self.key,))
        self.retriever.search.assert_not_called()
        self.retriever.get_chunks.assert_not_called()

    def saved(self):
        import psycopg
        with psycopg.connect(DSN) as conn:
            return conn.execute('SELECT state,response,error FROM ml.follow_up_operations '
                                'WHERE idempotency_key=%s', (self.key,)).fetchone()

    def test_reservation_is_committed_before_analysis_and_result_survives_recreation(self):
        def analyze(request):
            self.assertEqual(self.saved(), ('RUNNING', None, None))
            return self.service.analyze(request)
        call = Mock(side_effect=analyze)
        first = self.store.execute(self.key, self.request, call)
        self.assertEqual(first['status'], 'succeeded')
        restored = PostgresFollowUpStore(DSN)
        self.assertEqual(restored.execute(self.key, self.request, call), first)
        call.assert_called_once()
        self.assertEqual(self.saved(), ('SUCCEEDED', first, None))

    def test_changed_question_context_profile_snapshot_or_config_conflicts(self):
        call = Mock(side_effect=self.service.analyze)
        self.store.execute(self.key, self.request, call)
        for field in ('question', 'context', 'snapshot', 'config'):
            changed = copy.deepcopy(self.request)
            if field == 'question': changed['follow_up']['question'] += '?'
            if field == 'context': changed['context']['role_code'] = 'ceo'
            if field == 'snapshot': changed['follow_up']['initial_insight']['chunks'][0]['chunk_text'] += '\nChanged'
            if field == 'config': changed['processing_config_version'] += '-new'
            with self.subTest(field=field), self.assertRaises(FollowUpConflict):
                PostgresFollowUpStore(DSN).execute(self.key, changed, call)
        call.assert_called_once()

    def test_technical_trace_change_replays_same_analysis_without_model_call(self):
        call = Mock(side_effect=self.service.analyze)
        first = self.store.execute(self.key, self.request, call)
        changed = copy.deepcopy(self.request)
        changed['trace_id'] = str(uuid4())
        changed['parent_trace_id'] = str(uuid4())
        replay = self.store.execute(self.key, changed, call)
        self.assertEqual(replay, {**first, 'trace_id': changed['trace_id']})
        call.assert_called_once()

    def test_concurrent_duplicate_waits_and_does_not_repeat_analysis(self):
        entered, release, second_started = Event(), Event(), Event()
        def analyze(request):
            entered.set()
            if not release.wait(5): raise RuntimeError('test release timeout')
            return self.service.analyze(request)
        call = Mock(side_effect=analyze)
        def second():
            second_started.set()
            return PostgresFollowUpStore(DSN).execute(self.key, self.request, call)
        with ThreadPoolExecutor(2) as pool:
            first = pool.submit(self.store.execute, self.key, self.request, call)
            try:
                self.assertTrue(entered.wait(5))
                duplicate = pool.submit(second)
                self.assertTrue(second_started.wait(5))
                self.assertFalse(duplicate.done())
            finally:
                release.set()
            self.assertEqual(first.result(5), duplicate.result(5))
        call.assert_called_once()

    def test_crash_leaves_unknown_outcome_and_retry_durably_fails_without_analysis(self):
        class SimulatedProcessDeath(BaseException): pass
        def die_after_analysis(request):
            self.service.analyze(request)
            raise SimulatedProcessDeath()
        call = Mock(side_effect=die_after_analysis)
        with self.assertRaises(SimulatedProcessDeath):
            self.store.execute(self.key, self.request, call)
        self.assertEqual(self.saved(), ('RUNNING', None, None))
        retry = Mock(side_effect=AssertionError('Unknown execution must not rerun'))
        failed = PostgresFollowUpStore(DSN).execute(self.key, self.request, retry)
        self.assertEqual(failed['status'], 'failed')
        self.assertIn('unknown', failed['error']['message'])
        self.assertFalse(failed['error']['retryable'])
        self.assertEqual(PostgresFollowUpStore(DSN).execute(self.key, self.request, retry), failed)
        retry.assert_not_called()
        self.assertEqual(len(self.service.agent.client.inputs), 2)
        self.assertEqual(self.saved(), ('FAILED', failed, failed['error']))

    def test_standalone_http_adapter_replays_and_conflicts_using_real_postgres(self):
        from types import SimpleNamespace
        from fastapi.testclient import TestClient
        from ml_runtime.article_processing_api import create_article_processing_app
        app = create_article_processing_app(
            SimpleNamespace(quality_gate=SimpleNamespace(max_text_bytes=131072)),
            service_token='test-only', max_text_bytes=131072,
            supported_pipeline_versions={'ml-pipeline-v1'}, synchronous_follow_up=True)
        def service():
            self.assertEqual(self.saved(), ('RUNNING', None, None))
            return self.service
        factory = Mock(side_effect=service)
        app.state.follow_up_store_factory = lambda: PostgresFollowUpStore(DSN)
        app.state.follow_up_service_factory = factory
        client = TestClient(app)
        headers = {'Authorization': 'Bearer test-only', 'Idempotency-Key': self.key}
        first = client.post('/internal/v0.1/follow-ups', json=self.request, headers=headers)
        self.assertEqual(first.status_code, 200)
        self.assertEqual(first.json()['status'], 'succeeded')
        replay = client.post('/internal/v0.1/follow-ups', json=self.request, headers=headers)
        self.assertEqual(replay.status_code, 200)
        self.assertEqual(replay.json(), first.json())
        changed = copy.deepcopy(self.request)
        changed['follow_up']['question'] += '?'
        changed['follow_up']['history'][-1]['content'] = changed['follow_up']['question']
        conflict = client.post('/internal/v0.1/follow-ups', json=changed, headers=headers)
        self.assertEqual(conflict.status_code, 409)
        factory.assert_called_once()

    def test_failed_and_insufficient_results_are_terminal_and_durable(self):
        for status in ('failed', 'insufficient_evidence'):
            with self.subTest(status=status):
                result = self.service.analyze(self.request)
                result.update(status=status, message=None)
                if status == 'failed':
                    result['error'] = {'code': 'VERIFICATION_FAILED', 'message': 'Rejected', 'retryable': False}
                else:
                    result['gaps'] = [{'code': 'NO_EVENT_EVIDENCE', 'description': 'Not in card',
                                       'required_feature_key': None}]
                call = Mock(return_value=result)
                first = self.store.execute(self.key, self.request, call)
                self.assertEqual(PostgresFollowUpStore(DSN).execute(self.key, self.request, call), first)
                call.assert_called_once()
                self.assertEqual(self.saved(), (status.upper(), first, first['error']))
                self.tearDown()

    def test_unexpected_analysis_exception_is_saved_without_sensitive_text(self):
        call = Mock(side_effect=RuntimeError('secret provider detail'))
        result = self.store.execute(self.key, self.request, call)
        self.assertEqual(result['status'], 'failed')
        self.assertNotIn('secret', str(result))
        self.assertEqual(PostgresFollowUpStore(DSN).execute(self.key, self.request, call), result)
        call.assert_called_once()
