"""Evaluation bookkeeping tests: no quality threshold and no pretrained weights."""
from copy import deepcopy
import importlib.util
import json
import math
import os
import unittest
from types import SimpleNamespace
from unittest.mock import Mock, patch

from ml.evaluation.dataset import build_corpus, identity, load_dataset, validate_dataset
from ml.evaluation.retrieval import (
    diagnostic_markdown, evaluate, markdown_report, ndcg_at, recall_at, reciprocal_rank_at, run_pipeline,
)
from ml.tests.test_chunking import CharacterCounter
from ml.tests.test_retrieval import load


class RecallTests(unittest.TestCase):
    def test_recall_denominator_and_cutoff(self):
        expected = ['a', 'b', 'c']
        self.assertAlmostEqual(recall_at(expected, ['x', 'a', 'b', 'c'], 3), 2 / 3)
        self.assertEqual(recall_at(expected, [], 10), 0)
        self.assertEqual(recall_at(expected, ['x', 'a', 'b', 'c'], 10), 1)

    def test_invalid_labels_duplicates_and_k(self):
        for expected, retrieved, k in (([], [], 5), (['a', 'a'], [], 5),
                                       (['a'], ['a', 'a'], 5), (['a'], [], 0), (['a'], [], True)):
            with self.assertRaises(ValueError):
                recall_at(expected, retrieved, k)

    def test_reciprocal_rank_uses_first_positive_with_cutoff(self):
        self.assertEqual(reciprocal_rank_at(['a', 'b'], ['x', 'a', 'b'], 3), .5)
        self.assertEqual(reciprocal_rank_at(['a'], ['x', 'a'], 1), 0)
        self.assertEqual(reciprocal_rank_at(['a'], [], 10), 0)

    def test_binary_ndcg_discount_ideal_and_missing_positive(self):
        self.assertEqual(ndcg_at(['a', 'b'], ['a', 'b'], 10), 1)
        self.assertEqual(ndcg_at(['a'], [], 10), 0)
        self.assertAlmostEqual(ndcg_at(['a'], ['x', 'a'], 10), 1 / math.log2(3))
        self.assertAlmostEqual(ndcg_at(['a', 'b'], ['a'], 10), 1 / (1 + 1 / math.log2(3)))
        self.assertEqual(ndcg_at(['a'], ['x', 'a'], 1), 0)


class DatasetTests(unittest.TestCase):
    def setUp(self):
        self.dataset, self.checksum = load_dataset()

    def test_fixed_corpus_and_explicit_judgments(self):
        self.assertEqual(len(self.dataset['documents']), 40)
        self.assertEqual(len(self.dataset['queries']), 24)
        self.assertEqual(len(self.checksum), 64)
        self.assertTrue(any(len(q['relevant_chunk_ids']) > 1 for q in self.dataset['queries']))
        self.assertTrue(all(q['hard_negative_chunk_ids'] for q in self.dataset['queries']))
        self.assertEqual(load_dataset()[1], self.checksum)

    def test_invalid_or_overlapping_judgments_fail(self):
        for field, replacement in (
            ('relevant_chunk_ids', []), ('relevant_chunk_ids', [identity('missing')]),
            ('relevant_chunk_ids', self.dataset['queries'][0]['relevant_chunk_ids'] * 2),
            ('hard_negative_chunk_ids', self.dataset['queries'][0]['relevant_chunk_ids']),
        ):
            dataset = deepcopy(self.dataset)
            dataset['queries'][0][field] = replacement
            with self.assertRaises(ValueError):
                validate_dataset(dataset)

    def test_duplicate_documents_and_queries_fail(self):
        for key in ('documents', 'queries'):
            dataset = deepcopy(self.dataset)
            dataset[key].append(dataset[key][0])
            with self.assertRaises(ValueError):
                validate_dataset(dataset)

    def test_stale_manifest_is_not_silently_regenerated(self):
        # Wrong tokenizer is rejected before inference/storage.
        with self.assertRaises(ValueError):
            build_corpus(self.dataset, CharacterCounter())
        item = self.dataset['documents'][0]
        fake_chunk = SimpleNamespace(id=identity('changed-chunk'), chunk_no=0, chunk_text=item['text'])
        with patch('ml.chunking.chunk_document', return_value=(fake_chunk,)):
            with self.assertRaisesRegex(ValueError, 'Frozen chunk manifest changed'):
                build_corpus(self.dataset, Mock())


class EvaluationTests(unittest.TestCase):
    def setUp(self):
        # Reuse contract-valid R01 fixture DTO; only ranking/metrics are stubbed here.
        response = load('search.ok.json')
        first = response['results'][0]
        self.rows = [first, {**first, 'chunk_id': identity('metric-second-chunk'), 'rank': 2}]
        self.response = response
        self.assertGreaterEqual(len(self.rows), 2)
        self.first, self.second = [r['chunk_id'] for r in self.rows[:2]]
        self.dataset = {
            'dataset_version': 'metric-fixture',
            'documents': [{'key': str(i), 'chunks': [{'chunk_id': r['chunk_id']}]} for i, r in enumerate(self.rows)],
            'queries': [
                {'query_id': 'one', 'text': 'first', 'relevant_chunk_ids': [self.first],
                 'hard_negative_chunk_ids': [], 'rationale': 'one positive'},
                {'query_id': 'two', 'text': 'both', 'relevant_chunk_ids': [self.first, self.second],
                 'hard_negative_chunk_ids': [], 'rationale': 'two positives'},
            ],
        }
        self.retriever = Mock()

        def search(request):
            result = deepcopy(response)
            result['trace_id'] = request['trace_id']
            result['results'] = [{**self.rows[0], 'rank': 1, 'retrieval_score': .5}]
            return result

        self.retriever.search.side_effect = search

    def test_macro_mean_not_micro_and_fixed_top_ten(self):
        report = evaluate(self.retriever, self.dataset)
        self.assertEqual(report['recall_at_5'], .75)
        self.assertEqual(report['recall_at_10'], .75)
        self.assertEqual(report['positive_judgment_count'], 3)
        self.assertEqual(report['queries'][1]['missing_at_10'], [self.second])
        for call in self.retriever.search.call_args_list:
            self.assertEqual(call.args[0]['top_k'], 10)
            self.assertEqual(call.args[0]['business_context_id'], identity('business-context'))
        report['dataset_sha256'] = 'fixture'
        self.assertIn(self.first, markdown_report(report))
        self.assertEqual(json.loads(json.dumps(report)), report)

    def test_top_twenty_is_explicit_and_default_contract_limit_is_preserved(self):
        report = evaluate(self.retriever, self.dataset, top_k=20)
        self.assertEqual(report['retrieved_k'], 20)
        self.assertEqual(report['recall_at_20'], .75)
        for call in self.retriever.search.call_args_list:
            self.assertEqual(call.args[0]['top_k'], 20)
        for bad in (0, 51, True, 20.0):
            with self.assertRaises(ValueError):
                evaluate(self.retriever, self.dataset, top_k=bad)

    def test_model_or_database_error_is_not_scored_as_zero_recall(self):
        self.retriever.search.side_effect = RuntimeError('inference unavailable')
        with self.assertRaises(RuntimeError):
            evaluate(self.retriever, self.dataset)

    def test_full_ranking_requires_complete_corpus(self):
        with self.assertRaisesRegex(ValueError, 'complete allowed fixture corpus'):
            evaluate(self.retriever, self.dataset, full_ranking=True)

    def test_full_ranking_positions_and_candidate_cutoffs(self):
        def search(request):
            response = deepcopy(self.response)
            response['trace_id'] = request['trace_id']
            response['results'] = [{**row, 'rank': i, 'retrieval_score': 1 / i}
                                   for i, row in enumerate(self.rows, 1)]
            return response
        self.retriever.search.side_effect = search
        report = evaluate(self.retriever, self.dataset, full_ranking=True)
        self.assertEqual(report['retrieved_k'], 2)
        self.assertEqual(report['recall_at_40'], 1)
        self.assertEqual(report['mrr_at_10'], 1)
        self.assertEqual(report['ndcg_at_10'], 1)
        self.assertEqual(report['queries'][1]['required_candidate_k'], 2)
        self.assertEqual([p['rank'] for p in report['queries'][1]['relevant_positions']], [1, 2])
        report['dataset_sha256'] = 'fixture'
        self.assertIn('MRR', diagnostic_markdown(report))

    def test_full_ranking_over_contract_limit_rejected_before_search(self):
        self.dataset['documents'] = [{'key': str(i), 'chunks': [{'chunk_id': identity(f'chunk:{i}')}]} for i in range(51)]
        with self.assertRaisesRegex(ValueError, 'at most 50'):
            evaluate(self.retriever, self.dataset, full_ranking=True)
        self.retriever.search.assert_not_called()

    def test_diagnostics_identifies_miss_recovered_beyond_ten_and_score_gap(self):
        rows = [self.rows[1]] + [{**self.rows[0], 'chunk_id': identity(f'diagnostic-extra:{i}')}
                                 for i in range(10)] + [self.rows[0]]
        self.dataset['documents'] = [{'key': str(i), 'chunks': [{'chunk_id': r['chunk_id']}]} for i, r in enumerate(rows)]
        self.dataset['queries'][0]['hard_negative_chunk_ids'] = [self.second]

        def search(request):
            response = deepcopy(self.response)
            response['trace_id'] = request['trace_id']
            response['results'] = [{**row, 'rank': i, 'retrieval_score': 1 / i}
                                   for i, row in enumerate(rows, 1)]
            return response

        self.retriever.search.side_effect = search
        report = evaluate(self.retriever, self.dataset, full_ranking=True)
        query = report['queries'][0]
        self.assertEqual(query['recall_at_10'], 0)
        self.assertEqual(query['recall_at_20'], 1)
        self.assertEqual(query['required_candidate_k'], 12)
        position = query['relevant_positions'][0]
        self.assertEqual(position['rank'], 12)
        self.assertAlmostEqual(position['score_gap_from_top_10'], .1 - 1 / 12)
        self.assertEqual(position['outranking_hard_negatives'][0]['chunk_id'], self.second)
        self.assertEqual(position['outranking_hard_negatives'][0]['rank'], 1)

    def test_error_and_partial_responses_abort_evaluation(self):
        for status in ('error', 'partial'):
            with self.subTest(status=status):
                def search(request):
                    response = deepcopy(self.response)
                    response.update(trace_id=request['trace_id'], status=status, warnings=['Fixture limitation'])
                    if status == 'error':
                        response.update(results=[], error={'code': 'SEARCH_UNAVAILABLE',
                                        'message': 'Unavailable', 'retryable': True})
                    return response
                self.retriever.search.side_effect = search
                with self.assertRaises(RuntimeError):
                    evaluate(self.retriever, self.dataset)

    def test_nondescending_scores_fail(self):
        def search(request):
            response = deepcopy(self.response)
            response['trace_id'] = request['trace_id']
            response['results'] = [{**self.rows[0], 'rank': 1, 'retrieval_score': .1},
                                   {**self.rows[1], 'rank': 2, 'retrieval_score': .9}]
            return response
        self.retriever.search.side_effect = search
        with self.assertRaises(ValueError):
            evaluate(self.retriever, self.dataset)


HAS_EXTRAS = all(importlib.util.find_spec(p) is not None for p in ('psycopg', 'torch', 'transformers', 'tokenizers'))


@unittest.skipUnless(HAS_EXTRAS and os.environ.get('ML1_TEST_POSTGRES_DSN'), 'Explicit PostgreSQL + optional ML extras')
class EvaluationPostgresTests(unittest.TestCase):
    def test_failure_cleans_only_own_schema(self):
        import psycopg
        from ml.chunking.tokenization import QwenTokenCounter
        from ml.embeddings import EmbeddingError

        # This is a cleanup test, not an embedding-quality test; no weights are loaded.
        # Require the pinned tokenizer acceptance opt-in, as in the existing suite.
        if os.environ.get('ML1_TEST_QWEN_TOKENIZER') != '1':
            self.skipTest('Cached tokenizer opt-in')
        dataset, _ = load_dataset()
        counter = QwenTokenCounter(cache_dir=os.environ.get('ML1_MODEL_CACHE'))
        with psycopg.connect(os.environ['ML1_TEST_POSTGRES_DSN'], autocommit=True) as connection:
            connection.execute('CREATE EXTENSION IF NOT EXISTS vector')  # Disposable test infrastructure.
            before = connection.execute("SELECT nspname FROM pg_namespace WHERE nspname LIKE 'ml1_eval_%'").fetchall()
            embedder = Mock()
            embedder.encode_chunks.side_effect = EmbeddingError('inference unavailable')
            with self.assertRaises(EmbeddingError):
                run_pipeline(connection, dataset, embedder, counter)
            after = connection.execute("SELECT nspname FROM pg_namespace WHERE nspname LIKE 'ml1_eval_%'").fetchall()
            self.assertEqual(before, after)

    def test_manifest_matches_real_chunker_and_pinned_tokenizer(self):
        if os.environ.get('ML1_TEST_QWEN_TOKENIZER') != '1':
            self.skipTest('Cached tokenizer opt-in')
        from ml.chunking.tokenization import QwenTokenCounter
        dataset, _ = load_dataset()
        corpus = build_corpus(dataset, QwenTokenCounter(cache_dir=os.environ.get('ML1_MODEL_CACHE')))
        self.assertEqual(sum(len(chunks) for _, chunks in corpus), 40)
