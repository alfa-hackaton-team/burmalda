"""Minimal application acceptance: one path, existing DTOs and real DB rollback."""
from copy import deepcopy
from dataclasses import replace
import json
import os
from pathlib import Path
import unittest
from unittest.mock import Mock, patch
from uuid import uuid4

import psycopg
from psycopg import sql

from ml.adapters.postgres_chunks import ChunkPersistenceConflict, ChunkPersistenceError
from ml.chunking.profiles import V1 as CHUNKING
from ml.embeddings import Embedder
from ml.embeddings.core import EncodedVector, EmbeddingError
from ml.embeddings.profile import V1
from ml.pipeline import ML1Pipeline, PipelineConfig
from ml.retrieval.interfaces import DocumentVersion
from ml.shared.validation import validate_pair
import ml.tests.test_ml_storage as storage_fixture
from ml.tests.test_document_quality import fixture


class Counter:
    counter_id = CHUNKING.counter_id

    def count(self, text):
        return len(text)


class Encoder:
    profile = V1

    def encode_batch(self, inputs):
        return [EncodedVector(item.key, (1.,) + (0.,) * 1023) for item in inputs]


def metadata(payload, number=1):
    article = payload['article']
    return DocumentVersion(article['article_id'], article['article_version_id'], number, ready=False)


class PipelineUnitTests(unittest.TestCase):
    def setUp(self):
        self.payload, self.context = fixture(), str(uuid4())
        self.pipeline = ML1Pipeline(Mock(), Embedder(Encoder()), Counter())
        remember = patch("ml.pipeline.remember_version")
        remember.start()
        self.addCleanup(remember.stop)

    def test_quality_fail_never_calls_chunking_embedding_or_db(self):
        self.payload['article']['body'] = '  '
        with patch('ml.pipeline.chunk_document') as chunk, patch('ml.pipeline.save_indexed_document') as save:
            result = self.pipeline.index(self.payload, version=metadata(self.payload))
        self.assertFalse(result.indexed)
        self.assertEqual(result.quality.status, 'FAIL')
        self.assertFalse(result.chunk_ids)
        chunk.assert_not_called()
        save.assert_not_called()

    def test_inference_error_keeps_version_unready_and_access_unpublished(self):
        self.pipeline.embedder.encode_chunks = Mock(side_effect=EmbeddingError('not available'))
        with patch('ml.pipeline.save_indexed_document') as save, self.assertRaises(EmbeddingError):
            self.pipeline.index(self.payload, version=metadata(self.payload), business_context_ids=[self.context])
        save.assert_not_called()
        self.assertFalse(next(iter(self.pipeline._versions.values())).ready)
        self.assertEqual(self.pipeline._contexts, {})

    def test_configuration_and_metadata_validation(self):
        for kwargs in ({'candidate_k': 0}, {'final_k': 21}, {'final_k': True}, {'reranker_profile': 'mps-sdpa-b4'}):
            with self.assertRaises(ValueError):
                PipelineConfig(**kwargs)
        with self.assertRaises(ValueError):
            self.pipeline.index(self.payload, version=replace(metadata(self.payload), document_id=str(uuid4())))
        self.assertEqual(self.pipeline._versions, {})

    def response(self):
        # Reuse a schema-valid real DTO from the previously saved vector baseline.
        report = json.loads((Path(__file__).parent / 'fixtures/reranking_case.json').read_text())
        rows = deepcopy(report['queries'][0]['results'][:6])
        for i, row in enumerate(rows, 1):
            row['rank'] = i
            for key in ('document_key', 'relevant', 'hard_negative'):
                row.pop(key, None)
        return {'contract_version': 'v1', 'trace_id': str(uuid4()), 'status': 'ok', 'results': rows,
                'retrieval_config_version': 'vector', 'warnings': [], 'error': None}

    def test_rerank_key_mapping_scores_sidecar_and_no_dto_changes(self):
        response = self.response()
        original = deepcopy(response)
        scorer = Mock()
        scorer.score.side_effect = lambda q, rows: [(r['chunk_id'], float(i)) for i, r in enumerate(rows)]
        self.pipeline._reranker = scorer
        retriever = Mock()
        retriever.search.return_value = response
        with patch.object(self.pipeline, '_retriever', return_value=retriever):
            detailed = self.pipeline.search_with_scores('Запрос', trace_id=response['trace_id'])
        rows = detailed.response['results']
        self.assertTrue(detailed.reranker_applied)
        self.assertEqual(len(rows), 5)
        self.assertEqual([r['chunk_id'] for r in rows], [r['chunk_id'] for r in reversed(original['results'])][:5])
        self.assertEqual(retriever.search.call_args.args[0]['top_k'], 20)
        self.assertEqual(scorer.score.call_args.args[1][0].keys(), {'chunk_id', 'chunk_text'})
        by_id = {r['chunk_id']: r for r in original['results']}
        for row in rows:
            self.assertEqual(row['retrieval_score'], by_id[row['chunk_id']]['retrieval_score'])
            self.assertEqual(row.keys(), by_id[row['chunk_id']].keys())
        self.assertEqual(response, original)
        self.assertEqual(set(detailed.reranker_scores), {r['chunk_id'] for r in rows})

    def test_disabled_unavailable_and_invalid_scores_fall_back(self):
        for mode in ('disabled', 'load_failure', 'inference_failure', 'invalid_scores'):
            with self.subTest(mode=mode):
                response = self.response()
                self.pipeline.config = PipelineConfig(reranker_enabled=mode != 'disabled')
                self.pipeline._reranker = None if mode == 'load_failure' else Mock()
                if self.pipeline._reranker is not None:
                    self.pipeline._reranker.score.side_effect = RuntimeError('private model failure')
                    if mode == 'invalid_scores':
                        self.pipeline._reranker.score.side_effect = None
                        self.pipeline._reranker.score.return_value = [('wrong-id', float('nan'))]
                retriever = Mock()
                retriever.search.return_value = response
                with patch.object(self.pipeline, '_retriever', return_value=retriever), \
                        patch('ml.pipeline.QwenReranker', side_effect=OSError('private cache path')) as loader:
                    detailed = self.pipeline.search_with_scores('Запрос', top_k=2, trace_id=response['trace_id'])
                self.assertFalse(detailed.reranker_applied)
                self.assertEqual(detailed.reranker_scores, {})
                self.assertEqual(detailed.response['results'], response['results'][:2])
                self.assertEqual(bool(detailed.response['warnings']), mode != 'disabled')
                self.assertNotIn('private', str(detailed.response))
                if mode == 'disabled':
                    loader.assert_not_called()
                    self.pipeline._reranker.score.assert_not_called()


@unittest.skipUnless(os.environ.get('ML1_TEST_POSTGRES_DSN'), 'Disposable PostgreSQL + pgvector')
class PipelinePostgresTests(unittest.TestCase):
    drop_schema = storage_fixture.MLStoragePostgresTests.drop_schema

    def setUp(self):
        storage_fixture.MLStoragePostgresTests.setUp(self)
        self.payload, self.context = fixture(), str(uuid4())
        self.pipeline = ML1Pipeline(self.connection, Embedder(Encoder()), Counter(), schema=self.schema,
                                    context_chunks={self.context: []},
                                    config=PipelineConfig(reranker_enabled=False))

    def counts(self):
        return tuple(self.connection.execute(sql.SQL('SELECT count(*) FROM {}').format(
            sql.Identifier(self.schema, table))).fetchone()[0] for table in ('document_versions', 'document_chunks'))

    def index(self, payload=None, **kwargs):
        payload = payload or self.payload
        return self.pipeline.index(payload, version=metadata(payload), business_context_ids=[self.context], **kwargs)

    def test_index_retry_empty_and_disabled_search_historical_evidence(self):
        self.assertEqual(self.pipeline.search('Запрос', business_context_id=self.context)['status'], 'empty')
        first = self.index()
        self.assertTrue(first.indexed)
        self.assertEqual(self.counts(), (1, len(first.chunk_ids)))
        self.assertEqual(self.index().chunk_ids, first.chunk_ids)
        self.assertEqual(self.counts(), (1, len(first.chunk_ids)))
        result = self.pipeline.search_with_scores('Условия договоров', business_context_id=self.context)
        self.assertEqual(result.response['status'], 'ok', result)
        self.assertFalse(result.reranker_applied)
        self.assertEqual(result.response['results'][0]['chunk_text'], self.payload['article']['body'])
        request = {'contract_version': 'v1', 'trace_id': str(uuid4()), 'business_context_id': self.context,
                   'chunk_ids': list(first.chunk_ids)}
        historical = self.pipeline.get_chunks(request)
        validate_pair(request, historical, 'get_chunks')
        self.assertEqual(historical['status'], 'ok')

    def test_quality_fail_does_not_insert(self):
        self.payload['article']['metadata']['extraction_errors'] = ['TEXT_TRUNCATED']
        self.assertFalse(self.index().indexed)
        self.assertEqual(self.counts(), (0, 0))
        self.assertEqual(self.pipeline.search('Условия', business_context_id=self.context)['status'], 'empty')

    def test_atomic_failure_rolls_back_snapshot_and_chunks(self):
        from ml.adapters import postgres_chunks
        real_write = postgres_chunks._write_embeddings

        def fail_after_embeddings(*args):
            real_write(*args)
            # Another session sees none of the rows before the outer commit.
            with psycopg.connect(self.dsn, autocommit=True) as other:
                for table in ('document_versions', 'document_chunks'):
                    self.assertEqual(other.execute(sql.SQL('SELECT count(*) FROM {}').format(
                        sql.Identifier(self.schema, table))).fetchone()[0], 0)
            raise psycopg.OperationalError('simulated storage failure')

        with patch.object(postgres_chunks, '_write_embeddings', side_effect=fail_after_embeddings):
            with self.assertRaises(ChunkPersistenceError):
                self.index()
        self.assertEqual(self.counts(), (0, 0))
        self.assertFalse(next(iter(self.pipeline._versions.values())).ready)
        self.assertEqual(self.pipeline._contexts[self.context], set())
        self.assertTrue(self.index().indexed)

    def test_conflict_preserves_existing_rows(self):
        first = self.index()
        changed = deepcopy(self.payload)
        changed['article']['body'] += ' Другие условия.'
        with self.assertRaises(ChunkPersistenceConflict):
            self.index(changed)
        self.assertEqual(self.counts(), (1, len(first.chunk_ids)))
        result = self.pipeline.search('Условия', business_context_id=self.context)
        self.assertEqual(result['results'][0]['chunk_text'], self.payload['article']['body'])

    def test_failed_new_latest_does_not_expose_old_as_latest(self):
        self.index()
        new = deepcopy(self.payload)
        new['article']['article_version_id'] = str(uuid4())
        new['article']['version_no'] = 2
        new['article']['body'] = ''
        self.pipeline.index(new, version=metadata(new, number=2), business_context_ids=[self.context])
        self.assertEqual(self.pipeline.search('Условия', business_context_id=self.context)['status'], 'empty')


@unittest.skipUnless(os.environ.get('ML1_TEST_POSTGRES_DSN') and os.environ.get('ML1_TEST_PIPELINE_MODEL') == '1',
                     'Real cached Qwen models + PostgreSQL opt-in')
class PipelineRealSmokeTests(unittest.TestCase):
    drop_schema = storage_fixture.MLStoragePostgresTests.drop_schema

    def test_document_to_final_five_evidence(self):
        storage_fixture.MLStoragePostgresTests.setUp(self)
        # Existing frozen full publication; no dataset editing or new qrels.
        data = json.loads((Path(__file__).parents[1] / 'evaluation/data/official_holdout_v1.json').read_text())
        item = max(data['documents'], key=lambda d: len(d['text']))
        payload = fixture()
        payload['article'].update(body=item['text'], title=item['title'],
                                  canonical_url=item['provenance']['url'])
        context = str(uuid4())
        pipeline = ML1Pipeline.from_local_models(self.connection, schema=self.schema,
                                                context_chunks={context: []}, cache_dir=os.environ.get('ML1_MODEL_CACHE'))
        indexed = pipeline.index(payload, version=metadata(payload), business_context_ids=[context])
        self.assertTrue(indexed.indexed)
        self.assertEqual(indexed.quality.status, 'PASS')
        self.assertGreaterEqual(len(indexed.chunk_ids), 5)
        query = 'What did the September FOMC meeting say about foreign exchange interventions?'
        result = pipeline.search_with_scores(query, business_context_id=context)
        self.assertEqual(result.response['status'], 'ok', result.response)
        self.assertTrue(result.reranker_applied, result.response['warnings'])
        self.assertEqual(len(result.response['results']), 5)
        scores = [result.reranker_scores[r['chunk_id']] for r in result.response['results']]
        self.assertEqual(scores, sorted(scores, reverse=True))
        for row in result.response['results']:
            self.assertIn(row['chunk_id'], indexed.chunk_ids)
            self.assertEqual(row['document_version_id'], payload['article']['article_version_id'])
            self.assertEqual(row['canonical_url'], payload['article']['canonical_url'])
            self.assertIsInstance(row['retrieval_score'], float)
        print('\nML1 E2E:', len(indexed.chunk_ids), 'indexed chunks -> 20 candidates maximum -> 5 evidence;',
              result.response['retrieval_config_version'], flush=True)
        for row in result.response['results']:
            print('rank=', row['rank'], 'chunk=', row['chunk_id'], 'cosine=', row['retrieval_score'],
                  'reranker=', result.reranker_scores[row['chunk_id']], flush=True)
