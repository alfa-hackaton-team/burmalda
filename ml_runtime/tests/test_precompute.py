"""Capture boundaries; model reasoning/retrieval/verifier behavior stays unchanged."""
import copy
import unittest
from unittest.mock import patch

from ml_runtime.precompute import CapturingAgent, CapturingReranker, CapturingRetriever, capture_model_result, runtime_manifest
from ml_runtime.agent import AnalysisAgent
from ml_runtime.retrieval import FixtureRetriever
from ml_runtime.search_plan import FixtureSearchPlanner
from ml_runtime.service import AnalysisService
from ml_runtime.tests.test_analysis_contract import FixtureModelClient, load_fixture


class PrecomputeTests(unittest.TestCase):
    def test_requires_semantic_verifier(self):
        with self.assertRaises(ValueError):
            CapturingAgent(AnalysisAgent(FixtureModelClient()))

    def test_verified_response_captures_exact_agent_evidence(self):
        calls = []
        def verify(request, response, chunks, timeout):
            calls.append(copy.deepcopy(chunks))
            return True, None
        agent = CapturingAgent(AnalysisAgent(FixtureModelClient(), semantic_verifier=verify))
        service = AnalysisService(FixtureRetriever(), agent, FixtureSearchPlanner())
        identity = 'a17e6d41-30af-4777-9cc5-bb0743d80141'
        result = service.analyze(load_fixture('analysis.request.json'), analysis_run_id=identity)
        self.assertEqual(result['analysis_run_id'], identity)
        self.assertEqual(result['insight']['analysis_run_id'], identity)
        self.assertEqual(result['status'], 'succeeded')
        self.assertEqual(agent.chunks, calls[0])
        calls[0][0]['chunk_text'] = 'changed after run'
        self.assertNotEqual(agent.chunks, calls[0])

    def test_rejected_verification_cannot_publish_success(self):
        agent = CapturingAgent(AnalysisAgent(FixtureModelClient(), semantic_verifier=lambda *a: (False, 'rejected')))
        result = AnalysisService(FixtureRetriever(), agent, FixtureSearchPlanner()).analyze(load_fixture('analysis.request.json'))
        self.assertEqual(result['status'], 'failed')
        self.assertIsNone(result['insight'])

    def test_manifest_ignores_credentials(self):
        # Credential isolation is independent of optional ML1 packages installed
        # in the production image but intentionally absent from lightweight CI.
        versions = {'torch': 'test-torch', 'transformers': 'test-transformers',
                    'tokenizers': 'test-tokenizers', 'openai': 'test-openai',
                    'psycopg': 'test-psycopg'}
        with patch('ml_runtime.precompute.importlib.metadata.version', side_effect=versions.__getitem__):
            before = runtime_manifest()
            with patch.dict('os.environ', {'ML_LLM_API_KEY': 'do-not-persist', 'DATABASE_URL': 'private'}):
                after = runtime_manifest()
        self.assertEqual(before, after)
        self.assertEqual(before['dependencies'], versions)
        self.assertNotIn('do-not-persist', str(after))
        self.assertNotIn('private', str(after))

    def test_rejected_candidate_and_verdict_survive_as_diagnostics_only(self):
        candidate = load_fixture('analysis.ok.json')
        trace = {}
        capture_model_result(trace, {'response_schema': {}}, candidate)
        candidate['insight']['summary'] = 'mutated'
        self.assertNotEqual(trace['generated_response']['insight']['summary'], 'mutated')
        verdict = {'claim_checks': [], 'unattributed_assertions': ['Unsupported recommendation'], 'overall_supported': False}
        capture_model_result(trace, {'claim_records': []}, verdict)
        self.assertEqual(trace['verifier_verdict'], verdict)
        self.assertNotIn('reasoning_content', trace)
        capture_model_result(trace, {'claim_records': []}, {**verdict, 'reasoning_content': 'private'})
        self.assertEqual(trace['verifier_verdict'], verdict)

    def test_ranking_trace_is_observation_only(self):
        class Reranker:
            def score(self, query, candidates):
                self.query, self.candidates = query, candidates
                return [('second', 0.9), ('first', 0.1)]
        delegate = Reranker()
        recorder = CapturingReranker(delegate)
        rows = [{'chunk_id': 'first', 'chunk_text': 'A'}, {'chunk_id': 'second', 'chunk_text': 'B'}]
        result = recorder.score('query', rows)
        self.assertIs(delegate.candidates, rows)
        self.assertEqual(recorder.calls[0]['candidates'], rows)
        self.assertEqual(recorder.calls[0]['scores'], result)
        rows[0]['chunk_text'] = 'changed'
        self.assertEqual(recorder.calls[0]['candidates'][0]['chunk_text'], 'A')
