"""Keep the analysis latency control out of verifier/event requests."""
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from ml_runtime.model_client import OpenAICompatibleModelClient
from ml_runtime.model_policy import DEFAULT_AGENT_MODEL, DEFAULT_VERIFIER_MODEL


class ModelClientLatencyTests(unittest.TestCase):
    def call_options(self, model, **config):
        with patch('ml_runtime.model_client.OpenAI') as sdk:
            sdk.return_value.chat.completions.create.return_value = SimpleNamespace(
                choices=[SimpleNamespace(message=SimpleNamespace(content='{}'))])
            client = OpenAICompatibleModelClient('test-key', **config)
            client.generate_json(model_name=model, system_prompt='test', input_payload={}, timeout_seconds=9)
            return sdk.return_value.chat.completions.create.call_args.kwargs

    def test_primary_analysis_passes_explicit_low_effort(self):
        self.assertEqual(self.call_options(DEFAULT_AGENT_MODEL, reasoning_effort='low')['reasoning_effort'], 'low')

    def test_verifier_does_not_receive_glm_effort(self):
        self.assertNotIn('reasoning_effort', self.call_options(DEFAULT_VERIFIER_MODEL, reasoning_effort='low'))

    def test_unconfigured_event_client_keeps_existing_request(self):
        self.assertNotIn('reasoning_effort', self.call_options(DEFAULT_AGENT_MODEL))


if __name__ == '__main__':
    unittest.main()
