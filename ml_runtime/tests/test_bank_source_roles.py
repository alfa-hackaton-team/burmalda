"""Bank names must never override the trusted source role."""
import unittest
from unittest.mock import Mock

from ml_runtime.tests.test_analysis_contract import load_fixture
from ml_runtime.verification import OutputVerificationError, verify_analysis_response


class BankSourceRoleTests(unittest.TestCase):
    def scenario(self):
        request = load_fixture('analysis.request.json')
        response = load_fixture('analysis.ok.json')
        chunks = load_fixture('chunks.ok.json')['results']
        claim = response['insight']['claims'][0]
        claim.update(claim_type='bank_view', knowledge_type='FACT', assumptions=[])
        claim['evidence'] = claim['evidence'][:1]
        claim['claim_text'] = claim['evidence'][0]['quote_text']
        response['insight'].update(bank_view=claim['claim_text'],
            bank_view_document_version_id=chunks[0]['document_version_id'])
        return request, response, chunks

    def test_regulator_named_bank_cannot_publish_bank_view(self):
        request, response, chunks = self.scenario()
        chunks[0]['source_name'] = 'Банк России'
        verifier = Mock(return_value=(True, None))
        with self.assertRaisesRegex(OutputVerificationError, 'bank source'):
            verify_analysis_response(request, response, chunks, verifier)
        verifier.assert_not_called()

    def test_real_bank_role_can_publish_verified_bank_view(self):
        request, response, chunks = self.scenario()
        chunks[0].update(source_role='bank', source_type='bank_analytics')
        verifier = Mock(return_value=(True, None))
        result = verify_analysis_response(request, response, chunks, verifier)
        self.assertTrue(result.semantic_verified)
        verifier.assert_called_once()


if __name__ == '__main__':
    unittest.main()
