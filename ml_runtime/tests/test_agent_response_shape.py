"""The model sees the same request-specific response shape as the validator."""
import unittest

from jsonschema import Draft202012Validator
from ml_runtime.agent import _analysis_response_schema
from ml_runtime.tests.test_analysis_contract import load_fixture


class AgentResponseShapeTests(unittest.TestCase):
    def test_initial_cannot_return_follow_up_message(self):
        validator = Draft202012Validator(_analysis_response_schema('initial'))
        self.assertTrue(validator.is_valid(load_fixture('analysis.ok.json')))
        self.assertFalse(validator.is_valid(load_fixture('follow_up.ok.json')))

    def test_follow_up_cannot_replace_initial_insight(self):
        validator = Draft202012Validator(_analysis_response_schema('follow_up'))
        self.assertTrue(validator.is_valid(load_fixture('follow_up.ok.json')))
        self.assertFalse(validator.is_valid(load_fixture('analysis.ok.json')))

    def test_insufficient_and_failed_remain_valid_for_both_request_types(self):
        for request_type in ('initial', 'reanalyze', 'follow_up'):
            validator = Draft202012Validator(_analysis_response_schema(request_type))
            for name in ('analysis.insufficient.json', 'analysis.failed.json'):
                self.assertTrue(validator.is_valid(load_fixture(name)), (request_type, name))


if __name__ == '__main__':
    unittest.main()
