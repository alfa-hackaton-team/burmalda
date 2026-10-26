"""Service-owned identities must not change facts or evidence bindings."""
from copy import deepcopy
import unittest
from uuid import UUID, uuid4

from ml_runtime.agent import AnalysisAgent, _assign_result_ids
from ml_runtime.tests.test_analysis_contract import FixtureModelClient, load_fixture


class ResultIdentityTests(unittest.TestCase):
    def request(self, follow=False):
        request = load_fixture('analysis.request.json')
        if follow:
            request = load_fixture('follow_up.request.json')
        return request

    def test_invalid_model_label_is_replaced_without_changing_facts(self):
        request = self.request()
        response = load_fixture('analysis.ok.json')
        response['insight']['id'] = 'not-a-uuid'
        response['insight']['claims'][0]['id'] = 'claim-one'
        before = deepcopy(response['insight']['claims'][0])
        _assign_result_ids(request, response)
        UUID(response['insight']['id'])
        UUID(response['insight']['claims'][0]['id'])
        before['id'] = response['insight']['claims'][0]['id']
        self.assertEqual(before, response['insight']['claims'][0])

    def test_same_operation_stable_different_operations_distinct(self):
        request = self.request()
        a, b = load_fixture('analysis.ok.json'), load_fixture('analysis.ok.json')
        _assign_result_ids(request, a)
        _assign_result_ids(request, b)
        self.assertEqual(a, b)
        request['request_operation_id'] = str(uuid4())
        c = load_fixture('analysis.ok.json')
        _assign_result_ids(request, c)
        self.assertNotEqual(a['insight']['id'], c['insight']['id'])
        self.assertNotEqual(a['insight']['claims'][0]['id'], c['insight']['claims'][0]['id'])

    def test_follow_up_rebinds_only_new_claim_citations(self):
        request = self.request(True)
        response = load_fixture('follow_up.ok.json')
        original = deepcopy(response['message']['citations'][1])
        _assign_result_ids(request, response)
        self.assertEqual(response['message']['citations'][0]['claim_id'], response['message']['claims'][0]['id'])
        self.assertEqual(response['message']['citations'][1], original)
        UUID(response['message']['id'])

    def test_duplicate_labels_fail_instead_of_guessing_citation_target(self):
        request = self.request()
        response = load_fixture('analysis.ok.json')
        response['insight']['claims'].append(deepcopy(response['insight']['claims'][0]))
        with self.assertRaisesRegex(ValueError, 'ambiguous'):
            _assign_result_ids(request, response)

    def test_failure_and_insufficient_are_unchanged(self):
        for name in ('analysis.failed.json', 'analysis.insufficient.json'):
            response = load_fixture(name)
            before = deepcopy(response)
            _assign_result_ids(self.request(), response)
            self.assertEqual(response, before)

    def test_agent_validates_owned_ids_before_mandatory_verifier(self):
        class BadLabels(FixtureModelClient):
            def generate_json(self, **kwargs):
                response = super().generate_json(**kwargs)
                response['insight']['id'] = 'not-a-uuid'
                response['insight']['claims'][0]['id'] = 'claim-one'
                return response
        checked = []
        def verifier(request, response, chunks, timeout):
            UUID(response['insight']['id'])
            UUID(response['insight']['claims'][0]['id'])
            checked.append(response)
            return True, None
        response = AnalysisAgent(BadLabels(), semantic_verifier=verifier).run(
            self.request(), load_fixture('chunks.ok.json')['results'])
        self.assertEqual(response['status'], 'succeeded')
        self.assertEqual(len(checked), 1)
