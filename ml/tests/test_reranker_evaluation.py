"""Reranker experiment integrity, no quality threshold or mandatory weights."""
from copy import deepcopy
import importlib.util
import json
import math
import os
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import Mock

from ml.evaluation.qwen_reranker import PROFILE, QwenReranker, RerankerError, keyed_scores, pair_text
from ml.evaluation.reranking import rerank_experiment, verify_vector_baseline


class RerankingTests(unittest.TestCase):
    def test_key_mapping_validation(self):
        rows = [{'chunk_id': 'a'}, {'chunk_id': 'b'}]
        self.assertEqual(keyed_scores(rows, [('b', .5), ('a', -.5)]), {'a': -.5, 'b': .5})
        for output in ([('a', .5)], [('a', .5), ('a', .7)], [('a', .5), ('x', .7)],
                       [('a', float('nan')), ('b', .5)], [('a', float('inf')), ('b', .5)],
                       [('a', True), ('b', .5)]):
            with self.subTest(output=output), self.assertRaises(RerankerError):
                keyed_scores(rows, output)

    def test_instruction_is_fixed_and_text_is_exact(self):
        text = pair_text('Запрос?', '  Текст.\n')
        self.assertEqual(text, f'<Instruct>: {PROFILE.instruction}\n<Query>: Запрос?\n<Document>:   Текст.\n')
        for query, doc in (('', 'body'), ('query', ' \n')):
            with self.assertRaises(RerankerError):
                pair_text(query, doc)

    def fixture(self):
        data = json.loads((Path(__file__).parent / 'fixtures/reranking_case.json').read_text())
        data['queries'] = [deepcopy(data['queries'][0])]  # q13 has an actual missed positive at rank 15.
        data['queries'][0]['results'] = data['queries'][0]['results'][:20]
        return data

    def test_reranking_is_permutation_no_qrels_leak_no_input_mutation(self):
        vector = self.fixture()
        original = deepcopy(vector)
        positives = set(vector['queries'][0]['relevant_chunk_ids'])
        scorer = Mock()

        def score(query, candidates):
            self.assertTrue(all(set(c) == {'chunk_id', 'chunk_text'} for c in candidates))
            # Test-only oracle to exercise metric bookkeeping, never used in real evaluation.
            return [(c['chunk_id'], 1.0 if c['chunk_id'] in positives else 0.0) for c in reversed(candidates)]

        scorer.score.side_effect = score
        report = rerank_experiment(vector, scorer)
        query = report['queries'][0]
        self.assertEqual(query['vector']['recall_at_5'], .5)
        self.assertEqual(query['reranked']['recall_at_5'], 1)
        self.assertEqual(query['reranked']['ndcg_at_10'], 1)
        self.assertEqual({r['chunk_id'] for r in query['results']},
                         {r['chunk_id'] for r in vector['queries'][0]['results']})
        self.assertEqual(vector, original)
        by_id = {r['chunk_id']: r for r in original['queries'][0]['results']}
        for row in query['results']:
            self.assertEqual(row['retrieval_score'], by_id[row['chunk_id']]['retrieval_score'])
            self.assertEqual(row['chunk_text'], by_id[row['chunk_id']]['chunk_text'])

    def test_ties_keep_vector_order_and_failure_has_no_silent_fallback(self):
        vector = self.fixture()
        scorer = Mock()
        scorer.score.side_effect = lambda q, rows: [(r['chunk_id'], 0.0) for r in reversed(rows)]
        result = rerank_experiment(vector, scorer)
        self.assertEqual([r['chunk_id'] for r in result['queries'][0]['results']],
                         [r['chunk_id'] for r in vector['queries'][0]['results']])
        scorer.score.side_effect = RerankerError('model unavailable')
        with self.assertRaises(RerankerError):
            rerank_experiment(vector, scorer)

    def test_candidate_prefix_and_configuration_are_checked(self):
        vector = self.fixture()
        vector['dataset_sha256'] = 'fixed'
        baseline = deepcopy(vector)
        self.assertEqual(verify_vector_baseline(vector, baseline)['max_vector_score_difference'], 0)
        for mutation in ('text', 'order', 'scores', 'dataset', 'profile', 'implementation'):
            changed = deepcopy(vector)
            if mutation == 'text':
                changed['queries'][0]['results'][0]['chunk_text'] += ' changed'
            elif mutation == 'order':
                changed['queries'][0]['results'].reverse()
            elif mutation == 'scores':
                changed['queries'][0]['results'][0]['retrieval_score'] += .01
            elif mutation == 'dataset':
                changed['dataset_sha256'] = 'other'
            elif mutation == 'profile':
                changed['configuration']['embedding_profile']['revision'] = 'other'
            else:
                changed['configuration']['implementation_sha256']['ml/adapters/postgres_retrieval.py'] = 'other'
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                verify_vector_baseline(changed, baseline)


HAS_TORCH = importlib.util.find_spec('torch') is not None


@unittest.skipUnless(HAS_TORCH, 'Optional torch')
class RerankerInferenceTests(unittest.TestCase):
    def fake(self):
        import torch
        scorer = QwenReranker.__new__(QwenReranker)
        scorer.pad_id, scorer.yes_id, scorer.no_id = 0, 1, 2
        scorer.prefix, scorer.suffix = [3], [4]
        scorer.tokenizer = Mock()
        scorer.tokenizer.encode_batch.return_value = [SimpleNamespace(ids=[5]), SimpleNamespace(ids=[5, 6])]
        scorer.model = Mock(return_value=SimpleNamespace(logits=torch.tensor([[[0., 3., 1.]], [[0., 1., 2.]]])))
        return scorer

    def test_left_padding_yes_no_logits_and_chunk_ids(self):
        scorer = self.fake()
        rows = [{'chunk_id': 'a', 'chunk_text': 'A'}, {'chunk_id': 'b', 'chunk_text': 'B'}]
        self.assertEqual(scorer.score('query', rows), (('a', 2.0), ('b', -1.0)))
        args = scorer.model.call_args.kwargs
        self.assertEqual(args['input_ids'].tolist(), [[0, 3, 5, 4], [3, 5, 6, 4]])
        self.assertEqual(args['attention_mask'].tolist(), [[0, 1, 1, 1], [1, 1, 1, 1]])
        self.assertEqual(args['logits_to_keep'], 1)
        self.assertIs(args['use_cache'], False)

    def test_overlength_duplicate_empty_and_nonfinite(self):
        import torch
        scorer = self.fake()
        scorer.tokenizer.encode_batch.return_value = [SimpleNamespace(ids=[5] * PROFILE.max_input_tokens)]
        with self.assertRaisesRegex(RerankerError, 'truncation forbidden'):
            scorer.score('q', [{'chunk_id': 'a', 'chunk_text': 'A'}])
        scorer.model.assert_not_called()
        with self.assertRaises(RerankerError):
            scorer.score('q', [{'chunk_id': 'a', 'chunk_text': 'A'}] * 2)
        self.assertEqual(scorer.score('q', []), ())
        scorer.tokenizer.encode_batch.return_value = [SimpleNamespace(ids=[5])]
        scorer.model.return_value = SimpleNamespace(logits=torch.tensor([[[0., float('nan'), 1.]]]))
        with self.assertRaisesRegex(RerankerError, 'finite floats'):
            scorer.score('q', [{'chunk_id': 'a', 'chunk_text': 'A'}])

    def test_multiple_batches_preserve_chunk_mapping(self):
        import torch
        scorer = self.fake()
        rows = [{'chunk_id': str(i), 'chunk_text': str(i)} for i in range(6)]
        scorer.tokenizer.encode_batch.return_value = [SimpleNamespace(ids=[10 + i]) for i in range(6)]

        def model(**inputs):
            values = inputs['input_ids'][:, -2].float()
            logits = torch.zeros((len(values), 1, 3))
            logits[:, 0, 1] = values
            return SimpleNamespace(logits=logits)

        scorer.model.side_effect = model
        self.assertEqual(scorer.score('q', rows), tuple((str(i), float(10 + i)) for i in range(6)))
        self.assertEqual(scorer.model.call_count, 2)


@unittest.skipUnless(os.environ.get('ML1_TEST_QWEN_RERANKER') == '1', 'Explicit cached reranker weights')
class RealRerankerSmokeTests(unittest.TestCase):
    def test_offline_real_model_mapping_repeat_and_finite(self):
        scorer = QwenReranker(cache_dir=os.environ.get('ML1_MODEL_CACHE'))
        query = 'Банк повысил ключевую ставку?'
        rows = [{'chunk_id': str(i), 'chunk_text': text} for i, text in enumerate(
            ('Банк России повысил ключевую ставку.', 'Компания открыла завод.',
             'Ключевая ставка не изменилась.', 'Банк России повысил ключевую ставку.'))]
        first, second = scorer.score(query, rows), scorer.score(query, rows)
        self.assertEqual([i for i, _ in first], [r['chunk_id'] for r in rows])
        self.assertTrue(all(math.isfinite(s) for _, s in first))
        self.assertLessEqual(max(abs(a[1] - b[1]) for a, b in zip(first, second)), 1e-5)
        self.assertAlmostEqual(first[0][1], first[3][1], places=5)
        print('RERANKER SMOKE: repeat max score diff=', max(abs(a[1] - b[1]) for a, b in zip(first, second)), flush=True)
