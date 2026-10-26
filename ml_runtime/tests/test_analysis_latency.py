"""Regression for real ML1 latency at the evidence handoff.

The frozen holdout measured 15–19 seconds per rerank. Advance a clock instead
of sleeping or loading models; the agent and DTO verification are exercised.
"""
import unittest
from unittest.mock import patch

from ml.adapters.in_memory import InMemoryRetriever
from ml_runtime.agent import AnalysisAgent
from ml_runtime.service import AnalysisService
from ml_runtime.tests.test_analysis_contract import FixtureModelClient, load_fixture


class AnalysisLatencyTests(unittest.TestCase):
    def run_case(self, delay):
        clock = [0.0]
        requests = []
        wrapped = InMemoryRetriever.from_fixtures()
        registered_query = load_fixture('search.request.json')['query']

        class MeasuredLatencyRetriever:
            def search(self, request):
                requests.append(request)
                clock[0] += delay
                # Preserve the default planner's K/context/scope. Only translate
                # the query for the exact-string fixture adapter.
                return wrapped.search({**request, 'query': registered_query})

            def get_chunks(self, request):
                return wrapped.get_chunks(request)

        service = AnalysisService(MeasuredLatencyRetriever(), AnalysisAgent(
            FixtureModelClient(), semantic_verifier=lambda *args: (True, None)))
        with patch('ml_runtime.search_plan.monotonic', side_effect=lambda: clock[0]), \
                patch('ml_runtime.service.monotonic', side_effect=lambda: clock[0]):
            result = service.analyze(load_fixture('analysis.request.json'))
        return result, requests

    def test_measured_reranker_latency_reaches_verified_agent(self):
        result, requests = self.run_case(19)
        self.assertEqual(result['status'], 'succeeded')
        self.assertEqual(requests[0]['top_k'], 5)
        self.assertTrue(result['insight']['claims'])

    def test_search_beyond_budget_still_fails_closed(self):
        result, _ = self.run_case(91)
        self.assertEqual(result['status'], 'failed')
        self.assertEqual(result['error']['code'], 'SEARCH_UNAVAILABLE')


if __name__ == '__main__':
    unittest.main()
