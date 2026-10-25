"""Holdout judgments, no-answer accounting, aspect fusion, and profile isolation."""
from copy import deepcopy
import hashlib
import json
import os
from pathlib import Path
import unittest
from unittest.mock import Mock

from ml.evaluation.dataset import load_dataset, validate_dataset
from ml.evaluation.qwen_reranker import INFERENCE_PROFILES, PROFILE, QwenReranker, RerankerError
from ml.evaluation.reranking import metrics, rerank_experiment
from ml.evaluation.validation_study import HOLDOUT, aspect_coverage, aspect_experiment, balanced_ranking
from ml.evaluation.retrieval import evaluate
import ml.tests.test_retrieval_evaluation as evaluation_fixture


class HoldoutTests(unittest.TestCase):
    def test_sources_hashes_and_exact_gold_evidence(self):
        dataset, _ = load_dataset(HOLDOUT)
        self.assertEqual(len(dataset['documents']), 12)
        self.assertEqual(sum(len(d['chunks']) for d in dataset['documents']), 51)
        self.assertEqual(sum(q.get('answerable') is False for q in dataset['queries']), 4)
        self.assertEqual(len(dataset['queries']), 18)
        docs = {d['key']: d for d in dataset['documents']}
        for doc in docs.values():
            self.assertEqual(hashlib.sha256(doc['text'].encode()).hexdigest(), doc['provenance']['text_sha256'])
            self.assertTrue(doc['provenance']['url'].startswith(('https://www.cbr.ru/', 'https://www.federalreserve.gov/')))
            self.assertTrue(doc['provenance']['reuse_policy_url'])
        for query in dataset['queries']:
            for evidence in query.get('evidence', []):
                doc = docs[evidence['document_key']]
                self.assertEqual(doc['text'][evidence['document_char_start']:evidence['document_char_end']], evidence['quote'])
                self.assertIn(evidence['chunk_id'], [c['chunk_id'] for c in doc['chunks']])
                self.assertIn(evidence['chunk_id'], query['relevant_chunk_ids'])

    def test_no_answer_requires_explicit_reason_and_cannot_have_positives(self):
        dataset, _ = load_dataset(HOLDOUT)
        for change in ('reason', 'positive'):
            invalid = deepcopy(dataset)
            q = invalid['queries'][-1]
            if change == 'reason':
                q['no_answer_reason'] = ''
            else:
                q['relevant_chunk_ids'] = invalid['queries'][0]['relevant_chunk_ids']
            with self.assertRaises(ValueError):
                validate_dataset(invalid)

    @unittest.skipUnless(os.environ.get('ML1_TEST_QWEN_TOKENIZER') == '1', 'Cached pinned tokenizer opt-in')
    def test_holdout_chunk_manifest_and_evidence_containment(self):
        from ml.chunking.tokenization import QwenTokenCounter
        from ml.evaluation.dataset import build_corpus
        dataset, _ = load_dataset(HOLDOUT)
        corpus = build_corpus(dataset, QwenTokenCounter(cache_dir=os.environ.get('ML1_MODEL_CACHE')))
        chunks = {c.id: c for _, group in corpus for c in group}
        self.assertEqual(len(chunks), 51)
        for query in dataset['queries']:
            for evidence in query.get('evidence', []):
                self.assertIn(evidence['quote'], chunks[evidence['chunk_id']].chunk_text)

    def test_profiles_change_only_inference_not_prompt_model_or_revision(self):
        self.assertEqual(INFERENCE_PROFILES['cpu-eager-b4'], PROFILE)
        for profile in INFERENCE_PROFILES.values():
            self.assertEqual(profile.model, PROFILE.model)
            self.assertEqual(profile.revision, PROFILE.revision)
            self.assertEqual(profile.instruction, PROFILE.instruction)
            self.assertEqual(profile.dtype, 'float32')
        with self.assertRaises(RerankerError):
            QwenReranker(profile=None)


class NoAnswerTests(unittest.TestCase):
    def test_empty_relevance_is_undefined_not_a_zero_recall(self):
        self.assertTrue(all(x is None for x in metrics([], ['a']).values()))
        fixture = evaluation_fixture.EvaluationTests()
        fixture.setUp()
        negative = deepcopy(fixture.dataset['queries'][0])
        negative.update(query_id='no-answer', relevant_chunk_ids=[], answerable=False, no_answer_reason='Absent')
        fixture.dataset['queries'].append(negative)
        result = evaluate(fixture.retriever, fixture.dataset, top_k=20)
        self.assertEqual(result['answerable_query_count'], 2)
        self.assertEqual(result['no_answer_query_count'], 1)
        self.assertEqual(result['recall_at_5'], .75)
        self.assertIsNone(result['queries'][-1]['recall_at_5'])
        # Successful forced ranking does not mean the question has an answer.
        self.assertTrue(result['queries'][-1]['results'])

    def test_rerank_does_not_invent_no_answer_threshold_or_proof(self):
        vector = json.loads((Path(__file__).parents[1] / 'evaluation/reports/vector_v1_diagnostics.json').read_text())
        vector['queries'] = vector['queries'][:1]
        vector['queries'][0].update(relevant_chunk_ids=[], expected=[], answerable=False, no_answer_reason='Absent')
        vector['queries'][0]['results'] = [dict(r, relevant=False) for r in vector['queries'][0]['results'][:20]]
        scorer = Mock()
        scorer.score.side_effect = lambda q, rows: [(r['chunk_id'], 100.0) for r in rows]
        result = rerank_experiment(vector, scorer)
        self.assertEqual(result['answerable_query_count'], 0)
        self.assertEqual(result['no_answer_query_count'], 1)
        self.assertIsNone(result['metrics']['reranked']['recall_at_5'])
        self.assertEqual(len(result['queries'][0]['results']), 20)
        self.assertFalse(result['queries'][0]['answerable'])


class AspectTests(unittest.TestCase):
    def test_round_robin_deduplicates_and_preserves_membership(self):
        self.assertEqual(balanced_ranking([['a', 'b', 'c'], ['a', 'c', 'b']]), ['a', 'b', 'c'])
        self.assertEqual(balanced_ranking([['a', 'b', 'c'], ['c', 'b', 'a']]), ['a', 'c', 'b'])
        self.assertEqual(balanced_ranking([]), [])
        aspects = [{'relevant_chunk_ids': ['a']}, {'relevant_chunk_ids': ['c']}]
        self.assertEqual(aspect_coverage(aspects, ['a', 'b', 'c'], 2), .5)
        self.assertEqual(aspect_coverage(aspects, ['a', 'c', 'b'], 2), 1)

    def test_generic_fusion_no_query_id_or_qrels_in_model_input(self):
        query = {'query_id': 'arbitrary-unseen-id', 'text': 'Two questions',
                 'relevant_chunk_ids': ['a', 'c'], 'aspects': [
                     {'text': 'first question', 'relevant_chunk_ids': ['a']},
                     {'text': 'second question', 'relevant_chunk_ids': ['c']}],
                 'results': [{'chunk_id': x, 'chunk_text': x.upper(), 'rank': i}
                             for i, x in enumerate(('a', 'b', 'c'), 1)]}
        vector = {'queries': [query]}
        reference = {'queries': [{'query_id': query['query_id'], 'results': [{'chunk_id': x} for x in ('a', 'b', 'c')]}]}
        scorer = Mock()
        def score(text, rows):
            self.assertTrue(all(set(r) == {'chunk_id', 'chunk_text'} for r in rows))
            scores = {'a': 3., 'b': 2., 'c': 1.} if text == 'first question' else {'a': 1., 'b': 2., 'c': 3.}
            return list(scores.items())
        scorer.score.side_effect = score
        result = aspect_experiment(vector, reference, scorer)
        self.assertEqual(result['queries'][0]['ranking'], ['a', 'c', 'b'])
        self.assertEqual(result['coverage_at_2'], {'reference': .5, 'multi_aspect': 1})
        self.assertEqual(query['results'][1]['chunk_id'], 'b')
