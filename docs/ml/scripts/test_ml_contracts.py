"""Regression tests for the review findings; no network or paid model calls."""
import copy
import unittest

from check_ml_contracts import SCHEMA, check, load_fixtures, validate_scenario


class ContractRegressionTests(unittest.TestCase):
    def setUp(self):
        self.fixtures = copy.deepcopy(load_fixtures())

    def test_valid_analysis_then_follow_up(self):
        validate_scenario(self.fixtures)

    def test_missing_original_insight_is_rejected(self):
        request = self.fixtures['follow_up.request.json']
        del request['follow_up']['initial_insight']
        with self.assertRaises(ValueError):
            check(request, SCHEMA['$defs']['AnalysisRequest'])

    def test_duplicate_trace_for_different_runs_is_rejected(self):
        # Keep the parent distinct so the cross-run uniqueness check is exercised.
        for name in ('follow_up.request.json', 'follow_up.ok.json'):
            self.fixtures[name]['trace_id'] = self.fixtures['analysis.ok.json']['trace_id']
        self.fixtures['follow_up.request.json']['parent_trace_id'] = '00000000-0000-4000-8000-000000000099'
        with self.assertRaisesRegex(ValueError, 'duplicate trace_id across runs'):
            validate_scenario(self.fixtures)

    def test_foreign_original_context_is_rejected(self):
        self.fixtures['follow_up.request.json']['follow_up']['initial_insight']['context']['profile_version'] = 2
        with self.assertRaisesRegex(ValueError, 'context/profile mismatch'):
            validate_scenario(self.fixtures)

    def test_modified_saved_insight_is_rejected(self):
        self.fixtures['follow_up.request.json']['follow_up']['initial_insight']['insight']['summary'] = 'Подменённый вывод'
        with self.assertRaisesRegex(ValueError, 'snapshot differs'):
            validate_scenario(self.fixtures)

    def test_foreign_claim_citation_is_rejected(self):
        self.fixtures['follow_up.ok.json']['message']['citations'][1]['claim_id'] = '00000000-0000-4000-8000-000000000099'
        with self.assertRaisesRegex(ValueError, 'foreign citation claim'):
            validate_scenario(self.fixtures)

    def test_original_quote_must_match_snapshot(self):
        original = self.fixtures['follow_up.request.json']['follow_up']['initial_insight']['insight']
        original['claims'][0]['evidence'][0]['quote_text'] = 'Платежи по действующим договорам выросли.'
        with self.assertRaisesRegex(ValueError, 'quote mismatch'):
            validate_scenario(self.fixtures)

    def test_history_cannot_extend_beyond_boundary(self):
        follow = self.fixtures['follow_up.request.json']['follow_up']
        extra = copy.deepcopy(follow['history'][-1])
        extra['id'] = '00000000-0000-4000-8000-000000000099'
        follow['history'].append(extra)
        with self.assertRaisesRegex(ValueError, 'history boundary mismatch'):
            validate_scenario(self.fixtures)

    def test_response_must_belong_to_request(self):
        self.fixtures['follow_up.ok.json']['request_operation_id'] = self.fixtures['analysis.ok.json']['request_operation_id']
        with self.assertRaisesRegex(ValueError, 'request_operation_id mismatch'):
            validate_scenario(self.fixtures)

    def test_new_message_cannot_reuse_original_claim(self):
        self.fixtures['follow_up.ok.json']['message']['claims'][0]['id'] = self.fixtures['analysis.ok.json']['insight']['claims'][0]['id']
        with self.assertRaisesRegex(ValueError, 'reuses existing claim'):
            validate_scenario(self.fixtures)

    def test_business_purpose_is_not_knowledge_type(self):
        response = self.fixtures['analysis.ok.json']
        response['insight']['claims'][0]['knowledge_type'] = 'action'
        with self.assertRaises(ValueError):
            check(response, SCHEMA['$defs']['AnalysisResponse'])

    def test_partial_support_requires_assumptions(self):
        claim = self.fixtures['analysis.ok.json']['insight']['claims'][0]
        claim['verification_status'] = 'PARTIALLY_SUPPORTED'
        claim['assumptions'] = []
        with self.assertRaisesRegex(ValueError, 'requires explicit assumptions'):
            validate_scenario(self.fixtures)

    def test_model_inference_cannot_become_bank_view(self):
        self.fixtures['analysis.ok.json']['insight']['claims'][0]['knowledge_type'] = 'ALFA_ANALYTICS'
        with self.assertRaisesRegex(ValueError, 'bank claim has no bank support'):
            validate_scenario(self.fixtures)


if __name__ == '__main__':
    unittest.main()
