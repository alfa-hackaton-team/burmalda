"""Grounding inputs preserve source text; bad generation still fails closed."""
import copy
import unittest

from ml_runtime.agent import AnalysisAgent, _citation_candidates, ground_insight_summary
from ml_runtime.retrieval import FixtureRetriever
from ml_runtime.search_plan import FixtureSearchPlanner
from ml_runtime.service import AnalysisService
from ml_runtime.tests.test_analysis_contract import FixtureModelClient, load_fixture


class GenerationGroundingTests(unittest.TestCase):
    def test_summary_cannot_add_a_statement_outside_candidate_claims(self):
        response = load_fixture('analysis.ok.json')
        claims = copy.deepcopy(response['insight']['claims'])
        response['insight']['summary'] = 'У компании 10 млн свободных средств. Влияние не подтверждено.'
        ground_insight_summary(response)
        self.assertEqual(response['insight']['claims'], claims)
        self.assertEqual(response['insight']['summary'], ' '.join(c['claim_text'] for c in claims))

    def test_semantic_verifier_checks_the_final_summary_not_free_text_draft(self):
        class AddingSummaryClient(FixtureModelClient):
            def generate_json(self, **kwargs):
                out = super().generate_json(**kwargs)
                out['insight']['summary'] = 'Неподтверждённый эффект на компанию.'
                return out
        checked = []
        def verifier(request, response, chunks, timeout):
            checked.append(copy.deepcopy(response))
            return True, None
        agent = AnalysisAgent(AddingSummaryClient(), semantic_verifier=verifier)
        result = AnalysisService(FixtureRetriever(), agent, FixtureSearchPlanner()).analyze(load_fixture('analysis.request.json'))
        self.assertEqual(result['status'], 'succeeded')
        self.assertEqual(len(checked), 1)
        self.assertEqual(result['insight']['summary'], checked[0]['insight']['summary'])
        self.assertNotIn('Неподтверждённый эффект', result['insight']['summary'])

    def test_quote_choices_preserve_ocr_numbers_negation_and_newlines(self):
        text = '  Банк НЕ объявлял ставку 6,32%.\nПеренос\u00a0строки.\n\n• Мы ожидаем −0,07% — прогноз.  '
        chunks = [{'chunk_id': 'one', 'chunk_text': text}]
        before = copy.deepcopy(chunks)
        quotes = _citation_candidates(chunks)
        self.assertEqual(chunks, before)
        self.assertEqual(len(quotes), 2)
        self.assertIn('НЕ объявлял', quotes[0]['quote_text'])
        self.assertIn('\nПеренос\u00a0строки', quotes[0]['quote_text'])
        self.assertEqual(quotes[1]['quote_text'], '• Мы ожидаем −0,07% — прогноз.')
        self.assertTrue(all(q['quote_text'] in text for q in quotes))

    def test_paraphrased_quote_is_rejected_not_automatically_repaired(self):
        class RewordingClient(FixtureModelClient):
            def generate_json(self, *, input_payload, **kwargs):
                self_payload = input_payload
                assert self_payload['citation_candidates']
                out = super().generate_json(input_payload=input_payload, **kwargs)
                evidence = out['insight']['claims'][0]['evidence'][0]
                evidence['quote_text'] = 'С первого ноября новые соглашения продукта X доступны.'
                return out

        verifier_calls = []
        agent = AnalysisAgent(RewordingClient(), semantic_verifier=lambda *args: verifier_calls.append(args) or (True, None))
        result = AnalysisService(FixtureRetriever(), agent, FixtureSearchPlanner()).analyze(load_fixture('analysis.request.json'))
        self.assertEqual(result['status'], 'failed')
        self.assertEqual(result['error']['code'], 'VERIFICATION_FAILED')
        self.assertIsNone(result['insight'])
        self.assertEqual(verifier_calls, [])

    def test_exact_quote_still_requires_semantic_support(self):
        agent = AnalysisAgent(FixtureModelClient(), semantic_verifier=lambda *args: (False, 'Unsupported applicability'))
        result = AnalysisService(FixtureRetriever(), agent, FixtureSearchPlanner()).analyze(load_fixture('analysis.request.json'))
        self.assertEqual(result['status'], 'failed')
        self.assertIn('Unsupported applicability', result['error']['message'])
        self.assertIsNone(result['insight'])
