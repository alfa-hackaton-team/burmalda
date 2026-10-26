"""Follow-up consumes only the saved card; no search/reranking fallback."""
import copy
import unittest
from unittest.mock import Mock, patch

from ml_runtime.agent import AnalysisAgent
from ml_runtime.analysis_validation import validate_contract_message
from ml_runtime.citation_support import ensure_attributed_support
from ml_runtime.semantic_verifier import LLMSemanticVerifier
from ml_runtime.service import AnalysisService
from ml_runtime.tests.test_analysis_contract import FixtureModelClient, load_fixture


class SnapshotClient(FixtureModelClient):
    """Deterministic generation and verdict through the production adapter."""
    def __init__(self, edit=None, supported=True):
        self.inputs = []
        self.edit = edit
        self.supported = supported

    def generate_json(self, *, input_payload, **kwargs):
        self.inputs.append(copy.deepcopy(input_payload))
        if 'claim_records' in input_payload:
            return {'claim_checks': [
                {'claim_id': r['claim']['id'], 'supported': self.supported,
                 'reason': 'Exact saved evidence' if self.supported else 'Unsupported claim'}
                for r in input_payload['claim_records']],
                'unattributed_assertions': [], 'overall_supported': self.supported}
        result = super().generate_json(input_payload=input_payload, **kwargs)
        claim = result['message']['claims'][0]
        claim['evidence'] = claim['evidence'][:1]
        claim.update(claim_text=claim['evidence'][0]['quote_text'],
                     knowledge_type='FACT', assumptions=[])
        result['message']['content'] = claim['claim_text']
        result['message']['citations'] = result['message']['citations'][:1]
        if self.edit:
            self.edit(result)
        return result


class FollowUpSnapshotTests(unittest.TestCase):
    def setUp(self):
        self.request = load_fixture('follow_up.request.json')
        self.retriever = Mock(spec=['search', 'get_chunks'])
        self.retriever.search.side_effect = AssertionError('Follow-up must not search')
        self.retriever.get_chunks.side_effect = AssertionError('Follow-up must not reload chunks')
        self.planner = Mock(spec=['plan'])
        self.planner.plan.side_effect = AssertionError('Follow-up must not plan retrieval')

    def run_follow_up(self, client):
        verifier = LLMSemanticVerifier(client)
        agent = AnalysisAgent(client, semantic_verifier=verifier)
        service = AnalysisService(self.retriever, agent, self.planner)
        before = copy.deepcopy(self.request)
        with patch('ml_runtime.service.BoundedSearchRunner',
                   side_effect=AssertionError('Search/reranking path must not be entered')) as search, \
             patch('ml_runtime.agent.ensure_attributed_support',
                   wraps=ensure_attributed_support) as guard:
            result = service.analyze(self.request)
            search.assert_not_called()
            guard.assert_called_once()
        self.retriever.search.assert_not_called()
        self.retriever.get_chunks.assert_not_called()
        self.planner.plan.assert_not_called()
        self.assertEqual(self.request, before)
        validate_contract_message(result, 'AnalysisResponse')
        return result

    def test_grounded_answer_uses_saved_claims_and_chunks_with_zero_search(self):
        client = SnapshotClient()
        result = self.run_follow_up(client)
        self.assertEqual(result['status'], 'succeeded')
        saved = self.request['follow_up']['initial_insight']
        self.assertEqual(client.inputs[0]['evidence_chunks'], saved['chunks'])
        self.assertEqual(client.inputs[0]['follow_up']['initial_insight'], saved)
        self.assertEqual(len(client.inputs), 2)  # generation + production verifier adapter
        checked = client.inputs[1]['claim_records'][0]['evidence'][0]
        self.assertEqual(checked['chunk_text'], saved['chunks'][0]['chunk_text'])
        self.assertIn(checked['quote_text'], checked['chunk_text'])

    def test_insufficient_evidence_has_no_search_fallback_or_published_answer(self):
        def insufficient(response):
            response.update(status='insufficient_evidence', message=None,
                            gaps=[{'code': 'NO_EVENT_EVIDENCE',
                                   'description': 'Saved card does not establish the requested amount.',
                                   'required_feature_key': None}])
        client = SnapshotClient(edit=insufficient)
        result = self.run_follow_up(client)
        self.assertEqual(result['status'], 'insufficient_evidence')
        self.assertIsNone(result['message'])
        self.assertIsNone(result['insight'])
        self.assertEqual(len(client.inputs), 1)  # no candidate claims to verify

    def test_verifier_rejection_has_no_search_fallback(self):
        client = SnapshotClient(supported=False)
        result = self.run_follow_up(client)
        self.assertEqual(result['status'], 'failed')
        self.assertEqual(result['error']['code'], 'VERIFICATION_FAILED')
        self.assertIsNone(result['message'])
        self.assertEqual(len(client.inputs), 2)

    def test_nonexact_quote_is_rejected_before_semantic_verifier(self):
        def paraphrase(response):
            response['message']['claims'][0]['evidence'][0]['quote_text'] = 'Invented quotation'
        client = SnapshotClient(edit=paraphrase)
        result = self.run_follow_up(client)
        self.assertEqual(result['status'], 'failed')
        self.assertEqual(result['error']['code'], 'VERIFICATION_FAILED')
        self.assertEqual(len(client.inputs), 1)

    def test_chunk_outside_saved_card_is_rejected_without_loading_it(self):
        def outside(response):
            response['message']['claims'][0]['evidence'][0]['chunk_id'] = '11111111-1111-4111-8111-111111111111'
        client = SnapshotClient(edit=outside)
        result = self.run_follow_up(client)
        self.assertEqual(result['status'], 'failed')
        self.assertEqual(result['error']['code'], 'VERIFICATION_FAILED')
        self.assertEqual(len(client.inputs), 1)

    def test_attribution_guard_expands_saved_span_before_verifier(self):
        author = 'Банк России представил базовый сценарий.'
        fact = 'В базовом сценарии это означает ставку 13,0–15,0% в 2026 году.'
        self.request['follow_up']['initial_insight']['chunks'][0]['chunk_text'] += '\n\n' + author + ' ' + fact
        def forecast(response):
            claim = response['message']['claims'][0]
            claim.update(claim_text='Базовый сценарий Банка России предполагает ставку 13,0–15,0% в 2026 году.',
                         knowledge_type='FORECAST')
            claim['evidence'][0]['quote_text'] = fact
            response['message']['content'] = claim['claim_text']
        client = SnapshotClient(edit=forecast)
        result = self.run_follow_up(client)
        self.assertEqual(result['status'], 'succeeded')
        checked = client.inputs[1]['claim_records'][0]['evidence'][0]
        self.assertEqual(checked['quote_text'], author + ' ' + fact)
        self.assertIn(checked['quote_text'], checked['chunk_text'])
