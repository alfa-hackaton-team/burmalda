"""Grounding inputs preserve source text; bad generation still fails closed."""
import copy
import unittest

from ml_runtime.agent import AgentExecutionError, AnalysisAgent, _citation_candidates, ground_insight_summary
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

    def test_attributed_forecast_candidate_keeps_subject_and_value_together(self):
        paragraph = ('Банк России представил базовый сценарий. '
                     'В базовом сценарии средняя ставка составит 13,0–15,0% в 2026 году.')
        text = 'Решение по ставке.\n\n' + paragraph + '\n\nСледующее заседание.'
        quotes = _citation_candidates([{'chunk_id': 'forecast', 'chunk_text': text}])
        self.assertIn({'chunk_id': 'forecast', 'quote_text': paragraph}, quotes)
        self.assertTrue(all(q['quote_text'] in text for q in quotes))

    def test_partial_support_is_not_extended_or_published_when_fact_is_elsewhere(self):
        request = load_fixture('analysis.request.json')
        chunks = load_fixture('chunks.ok.json')['results']
        short_quote = 'Банк России снизил ключевую ставку до 16,50%.'
        missing_support = 'Банк России обновил среднесрочный прогноз.'
        chunks[0]['chunk_text'] = short_quote + '\n\n' + missing_support

        class PartialSupportClient(FixtureModelClient):
            def generate_json(self, **kwargs):
                response = super().generate_json(**kwargs)
                claim = response['insight']['claims'][0]
                claim.update(claim_type='relevance', knowledge_type='FACT', assumptions=[],
                             claim_text='Банк России снизил ставку и обновил прогноз.')
                claim['evidence'] = claim['evidence'][:1]
                claim['evidence'][0]['quote_text'] = short_quote
                return response

        checked = []
        def verifier(req, response, evidence, timeout):
            checked.append(response['insight']['claims'][0]['evidence'][0]['quote_text'])
            return False, 'Quote does not support the forecast update'

        agent = AnalysisAgent(PartialSupportClient(), semantic_verifier=verifier)
        with self.assertRaisesRegex(AgentExecutionError, 'forecast update'):
            agent.run(request, chunks)
        self.assertEqual(checked, [short_quote])
