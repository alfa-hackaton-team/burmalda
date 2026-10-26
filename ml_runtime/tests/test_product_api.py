"""Product HTTP boundaries and durable queue integration (optional PostgreSQL)."""
import copy
import os
import unittest
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from datetime import datetime, timezone
from uuid import uuid4

from fastapi import FastAPI
from fastapi.testclient import TestClient

from ml_runtime.product_api import FeedRankingService, public_validator, register_product_routes
from ml_runtime.product_store import ProductStore, fingerprint
from ml_runtime.run_analysis_worker import process_next
from ml_runtime.tests.test_analysis_contract import load_fixture


class MemoryStore:
    def __init__(self):
        self.rankings = {}
        self.requests = []

    def save_ranking(self, digest, items):
        identity = str(uuid4())
        self.rankings[identity] = (digest, copy.deepcopy(items))
        return identity

    def get_ranking(self, identity, digest):
        saved = self.rankings.get(identity)
        return saved[1] if saved and saved[0] == digest else None

    def submit(self, request):
        self.requests.append(request)
        return {'analysis_id': str(uuid4()), 'state': 'QUEUED', 'trace_id': request['trace_id'],
                'created_at': datetime.now(timezone.utc).isoformat()}

    def get(self, identity):
        return None


class Corpus:
    def __init__(self):
        self.calls = 0

    def feed_items(self, topic):
        self.calls += 1
        item = load_public_feed()
        return [{**item, 'preview_id': str(i)} for i in range(3)]


def load_public_feed():
    import json
    from pathlib import Path
    return json.loads((Path(__file__).resolve().parents[2] /
                      'docs/api/examples/feed.json').read_text())['items'][0]


def feed_request():
    return {'request_context': {'request_id': 'request', 'trace_id': 'trace', 'user_id': 'user',
        'context': {'context_id': 'context', 'kind': 'COMPANY', 'company_id': 'company'},
        'role_id': 'cfo', 'profile': {'profile_id': 'profile', 'version': '1'}}, 'limit': 2}


class ProductAPITests(unittest.TestCase):
    def setUp(self):
        self.store, self.corpus = MemoryStore(), Corpus()
        self.ranking = FeedRankingService(self.store, self.corpus, 'test-token')
        self.app = FastAPI()
        register_product_routes(self.app, store=self.store, ranking=self.ranking,
            service_token='test-token', analysis_version='processing-v1')
        self.client = TestClient(self.app)
        self.headers = {'Authorization': 'Bearer test-token'}

    def test_unauthorized_requests_cannot_read_corpus_or_enqueue_paid_work(self):
        for path in ('feed-rankings', 'analyses', 'follow-ups'):
            response = self.client.post('/internal/v0.1/' + path, json={})
            self.assertEqual(response.status_code, 401)
        self.assertEqual(self.corpus.calls, 0)
        self.assertEqual(self.store.requests, [])

    def test_analysis_validates_version_idempotency_key_and_endpoint_before_queue(self):
        request = load_fixture('analysis.request.json')
        path = '/internal/v0.1/analyses'
        self.assertEqual(self.client.post(path, json=request, headers=self.headers).status_code, 400)
        headers = {**self.headers, 'Idempotency-Key': request['request_operation_id']}
        wrong = {**request, 'processing_config_version': 'unsupported'}
        self.assertEqual(self.client.post(path, json=wrong, headers=headers).status_code, 422)
        self.assertEqual(self.client.post('/internal/v0.1/follow-ups', json=request, headers=headers).status_code, 400)
        response = self.client.post(path, json=request, headers=headers)
        self.assertEqual(response.status_code, 202)
        public_validator('AnalysisOperation').validate(response.json())
        self.assertEqual(len(self.store.requests), 1)

    def test_follow_up_accepts_only_consistent_original_evidence_snapshot(self):
        request = load_fixture('follow_up.request.json')
        headers = {**self.headers, 'Idempotency-Key': request['request_operation_id']}
        self.assertEqual(self.client.post('/internal/v0.1/follow-ups', json=request, headers=headers).status_code, 202)
        request['follow_up']['initial_insight']['context']['business_context_id'] = str(uuid4())
        self.assertEqual(self.client.post('/internal/v0.1/follow-ups', json=request, headers=headers).status_code, 400)

    def test_ranking_pages_reuse_snapshot_and_reject_cross_context_or_tampered_cursors(self):
        request = feed_request()
        first = self.ranking.rank(request)
        self.assertEqual(len(first['items']), 2)
        # Live events can change without shifting pages in an existing snapshot.
        second_request = {**request, 'cursor': first['next_cursor']}
        second = self.ranking.rank(second_request)
        self.assertEqual([i['preview_id'] for i in second['items']], ['2'])
        self.assertIsNone(second['next_cursor'])
        self.assertEqual(self.corpus.calls, 1)
        other_user = copy.deepcopy(second_request)
        other_user['request_context']['user_id'] = 'another-user'
        with self.assertRaises(ValueError):
            self.ranking.rank(other_user)
        with self.assertRaises(ValueError):
            self.ranking.rank({**second_request, 'cursor': first['next_cursor'] + 'x'})

    def test_invalid_ranking_input_is_rejected(self):
        for value in ({}, {**feed_request(), 'limit': True}, {**feed_request(), 'limit': 101}):
            self.assertEqual(self.client.post('/internal/v0.1/feed-rankings', json=value,
                                             headers=self.headers).status_code, 400)

    def test_fingerprint_ignores_transport_trace_but_binds_company_and_question(self):
        original = load_fixture('follow_up.request.json')
        self.assertEqual(fingerprint(original), fingerprint({**original, 'trace_id': 'retry'}))
        changed = copy.deepcopy(original)
        changed['follow_up']['question'] += '?'
        self.assertNotEqual(fingerprint(original), fingerprint(changed))


class EventEvidenceTests(unittest.TestCase):
    def test_analysis_uses_actual_event_citations_without_a_second_embedding_call(self):
        from ml.adapters.in_memory import InMemoryRetriever
        from ml_runtime.product_corpus import EventEvidenceRetriever
        from ml_runtime.retrieval import build_search_request
        from ml_runtime.service import AnalysisService
        from ml_runtime.agent import AnalysisAgent
        from ml_runtime.tests.test_analysis_contract import FixtureModelClient
        chunks = load_fixture('chunks.ok.json')['results']
        reader = InMemoryRetriever.from_fixtures()
        retriever = EventEvidenceRetriever(reader, [c['chunk_id'] for c in chunks])
        request = load_fixture('analysis.request.json')
        response = retriever.search(build_search_request(request))
        self.assertEqual(response['status'], 'ok')
        self.assertTrue(all(item['retrieval_score'] is None for item in response['results']))
        result = AnalysisService(retriever, AnalysisAgent(FixtureModelClient(),
            semantic_verifier=lambda *args: (True, None))).analyze(request)
        self.assertEqual(result['status'], 'succeeded')
        # The same IDs must not become readable in a different company context.
        other = copy.deepcopy(request)
        other['context']['business_context_id'] = str(uuid4())
        self.assertEqual(retriever.search(build_search_request(other))['status'], 'error')

    def test_missing_event_citations_produce_insufficient_evidence_without_model_call(self):
        from ml_runtime.product_corpus import EventEvidenceRetriever
        from ml_runtime.retrieval import build_search_request
        result = EventEvidenceRetriever(None, []).search(build_search_request(load_fixture('analysis.request.json')))
        self.assertEqual(result['status'], 'empty')


DATABASE_URL = os.environ.get('ML_TEST_DATABASE_URL')


@unittest.skipUnless(DATABASE_URL, 'Set ML_TEST_DATABASE_URL to an isolated PostgreSQL database')
class ProductPersistenceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        from ml_runtime.tests.postgres_test_schema import prepare_ml_test_schema
        prepare_ml_test_schema(DATABASE_URL)

    def setUp(self):
        self.store = ProductStore(DATABASE_URL)
        self.request = load_fixture('analysis.request.json')
        self.request['request_operation_id'] = str(uuid4())

    def tearDown(self):
        with self.store.connect() as connection:
            connection.execute('DELETE FROM ml.analysis_operations WHERE request_operation_id=%s',
                               (self.request['request_operation_id'],))

    def test_concurrent_retries_and_restarts_do_not_create_a_second_model_run(self):
        with ThreadPoolExecutor(max_workers=2) as workers:
            results = list(workers.map(lambda _: self.store.submit(self.request), range(2)))
        self.assertEqual(results[0]['analysis_id'], results[1]['analysis_id'])
        from ml_runtime.product_store import OperationConflict
        with self.assertRaises(OperationConflict):
            self.store.submit({**self.request, 'processing_config_version': 'different'})
        identity = results[0]['analysis_id']
        self.assertEqual(ProductStore(DATABASE_URL).get(identity)['state'], 'QUEUED')
        self.assertEqual(self.store.claim()['analysis_id'].__str__(), identity)
        self.assertIsNone(self.store.claim())

    def test_real_service_preserves_queued_identity_and_result_survives_restart(self):
        from ml.adapters.in_memory import InMemoryRetriever
        from ml_runtime.agent import AnalysisAgent
        from ml_runtime.search_plan import FixtureSearchPlanner
        from ml_runtime.service import AnalysisService
        from ml_runtime.tests.test_analysis_contract import FixtureModelClient
        service = AnalysisService(InMemoryRetriever.from_fixtures(), AnalysisAgent(
            FixtureModelClient(), semantic_verifier=lambda *args: (True, None)), FixtureSearchPlanner())

        class TestCorpus:
            @contextmanager
            def analysis_service(self, request):
                yield service

        operation = self.store.submit(self.request)
        self.assertTrue(process_next(self.store, TestCorpus()))
        saved = ProductStore(DATABASE_URL).get(operation['analysis_id'])
        public_validator('AnalysisOperation').validate(saved)
        self.assertEqual(saved['state'], 'SUCCEEDED')
        self.assertEqual(saved['response']['analysis_run_id'], operation['analysis_id'])
        self.assertEqual(saved['response']['insight']['analysis_run_id'], operation['analysis_id'])
        self.assertFalse(process_next(self.store, TestCorpus()))

    def test_stale_worker_is_failed_without_a_second_paid_attempt(self):
        operation = self.store.submit(self.request)
        self.store.claim()
        with self.store.connect() as connection:
            connection.execute("UPDATE ml.analysis_operations SET started_at=now()-interval '6 minutes' WHERE analysis_id=%s",
                               (operation['analysis_id'],))
        self.store.expire_running()
        result = self.store.get(operation['analysis_id'])
        public_validator('AnalysisOperation').validate(result)
        self.assertEqual(result['state'], 'FAILED')
        self.assertIsNone(self.store.claim())

    def test_ranking_snapshot_survives_restart_and_enforces_context_expiry(self):
        request = feed_request()
        ranking = FeedRankingService(self.store, Corpus(), 'token')
        first = ranking.rank(request)
        restarted = FeedRankingService(ProductStore(DATABASE_URL), Corpus(), 'token')
        second = restarted.rank({**request, 'cursor': first['next_cursor']})
        self.assertEqual([item['preview_id'] for item in second['items']], ['2'])
        with self.store.connect() as connection:
            connection.execute("UPDATE ml.ranking_snapshots SET expires_at=now()-interval '1 second'")
        with self.assertRaises(ValueError):
            restarted.rank({**request, 'cursor': first['next_cursor']})
