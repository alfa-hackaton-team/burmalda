"""Offline regression for pre-verifier, contiguous attribution support."""
import copy
import unittest
from unittest.mock import Mock

from ml_runtime.agent import AgentExecutionError, AnalysisAgent
from ml_runtime.citation_support import ensure_attributed_support
from ml_runtime.tests.test_analysis_contract import FixtureModelClient, load_fixture
from ml_runtime.verification import OutputVerificationError


AUTHOR = ('Банк России будет поддерживать такую жесткость денежно-кредитных условий, '
          'которая необходима для возвращения инфляции к цели.')
FACT = ('В базовом сценарии это в том числе предполагает среднюю ключевую ставку '
        'в диапазоне 13,0–15,0% годовых в 2026 году')
CLAIM = ('В базовом сценарии Банка России средняя ключевая ставка на 2026 год '
         'предполагается в диапазоне 13,0–15,0% годовых.')


def scenario(text, quote=FACT, claim_text=CLAIM):
    response = load_fixture('analysis.ok.json')
    chunks = load_fixture('chunks.ok.json')['results']
    chunks[0]['chunk_text'] = text
    claim = response['insight']['claims'][0]
    claim.update(claim_text=claim_text, claim_type='relevance', knowledge_type='FACT', assumptions=[])
    claim['evidence'] = claim['evidence'][:1]
    claim['evidence'][0]['quote_text'] = quote
    return response, chunks


class AttributionSupportTests(unittest.TestCase):
    def test_h08_expands_to_adjacent_author_without_changing_claim_or_source(self):
        text = 'Другой абзац.\n\n' + AUTHOR + '\n' + FACT + '.\n\nИные сведения.'
        response, chunks = scenario(text)
        original_chunks = copy.deepcopy(chunks)
        ensure_attributed_support(response, chunks)
        claim = response['insight']['claims'][0]
        quote = claim['evidence'][0]['quote_text']
        self.assertEqual(quote, AUTHOR + '\n' + FACT)
        self.assertIn(quote, text)
        self.assertEqual(claim['claim_text'], CLAIM)
        self.assertEqual(chunks, original_chunks)

    def test_explicit_author_and_fact_are_unchanged(self):
        quote = 'По прогнозу Банка России, инфляция снизится до 4,0–5,0% в 2026 году.'
        response, chunks = scenario(quote, quote, quote)
        before = copy.deepcopy(response)
        ensure_attributed_support(response, chunks)
        self.assertEqual(response, before)

    def test_literal_named_forecaster_is_retained(self):
        text = 'По прогнозу Росстата, рост составит 2% в 2026 году.'
        response, chunks = scenario(text, 'рост составит 2% в 2026 году.', text)
        ensure_attributed_support(response, chunks)
        self.assertEqual(response['insight']['claims'][0]['evidence'][0]['quote_text'], text)

    def test_known_author_grammatical_cases_do_not_require_literal_metadata_name(self):
        quote = 'По прогнозу Банка России, инфляция снизится до 4,0–5,0%.'
        response, chunks = scenario(quote, quote, 'Банк России прогнозирует инфляцию 4,0–5,0%.')
        chunks[0]['source_name'] = 'Банк России'
        ensure_attributed_support(response, chunks)
        self.assertEqual(response['insight']['claims'][0]['evidence'][0]['quote_text'], quote)

    def test_explicit_following_attribution_can_complete_exact_span(self):
        text = FACT + '. Это прогноз Банка России.'
        response, chunks = scenario(text)
        ensure_attributed_support(response, chunks)
        self.assertEqual(response['insight']['claims'][0]['evidence'][0]['quote_text'], text)

    def test_missing_unlinked_or_conflicting_attribution_fails_closed(self):
        for text in (FACT + '.', AUTHOR + '\n\n' + FACT + '.',
                     AUTHOR + ' Производство выросло. ' + FACT + '.',
                     AUTHOR + ' Средняя ставка составит 13,0–15,0% в 2026 году.',
                     AUTHOR + ' В базовом сценарии Альфа-Банка это составляет 13,0–15,0%.'):
            quote = (FACT if FACT in text else
                     text[text.index('. ') + 2:])
            response, chunks = scenario(text, quote)
            chunks[0]['source_name'] = 'Банк России'  # Metadata cannot substitute for a quote.
            with self.subTest(text=text), self.assertRaises(OutputVerificationError):
                ensure_attributed_support(response, chunks)

    def test_another_chunk_cannot_supply_author(self):
        response, chunks = scenario(FACT + '.')
        chunks.append({**chunks[0], 'chunk_id': 'elsewhere', 'chunk_text': AUTHOR})
        with self.assertRaises(OutputVerificationError):
            ensure_attributed_support(response, chunks)

    def test_nonexact_or_ambiguous_quote_is_not_repaired(self):
        for text, quote in ((AUTHOR + ' ' + FACT, FACT.replace('13,0', '13.0')),
                            (AUTHOR + ' ' + FACT + '. ' + FACT, FACT)):
            response, chunks = scenario(text, quote)
            with self.subTest(quote=quote), self.assertRaises(OutputVerificationError):
                ensure_attributed_support(response, chunks)

    def test_agent_expands_before_verifier_and_rejects_unexpandable_support(self):
        for text, accepted in ((AUTHOR + ' ' + FACT + '.', True), (FACT + '.', False)):
            template, chunks = scenario(text)
            class Client(FixtureModelClient):
                def generate_json(self, **kwargs):
                    response = super().generate_json(**kwargs)
                    response['insight']['claims'] = copy.deepcopy(template['insight']['claims'])
                    return response
            verifier = Mock(return_value=(True, None))
            agent = AnalysisAgent(Client(), semantic_verifier=verifier)
            with self.subTest(accepted=accepted):
                if accepted:
                    agent.run(load_fixture('analysis.request.json'), chunks)
                    actual = verifier.call_args.args[1]['insight']['claims'][0]['evidence'][0]['quote_text']
                    self.assertEqual(actual, AUTHOR + ' ' + FACT)
                else:
                    with self.assertRaises(AgentExecutionError):
                        agent.run(load_fixture('analysis.request.json'), chunks)
                    verifier.assert_not_called()
