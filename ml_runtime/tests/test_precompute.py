"""Capture boundaries; model reasoning/retrieval/verifier behavior stays unchanged."""
import copy
import unittest
from unittest.mock import patch

from ml_runtime.precompute import CapturingAgent, runtime_manifest
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
        result = service.analyze(load_fixture('analysis.request.json'))
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
        before = runtime_manifest()
        with patch.dict('os.environ', {'ML_LLM_API_KEY': 'do-not-persist', 'DATABASE_URL': 'private'}):
            after = runtime_manifest()
        self.assertEqual(before, after)
        self.assertNotIn('do-not-persist', str(after))
