"""Backend version_no, durable readiness and ML-owned operation/event schema."""
import json
import os
from pathlib import Path
import unittest
from uuid import uuid4

import psycopg
from psycopg import sql
from psycopg.types.json import Jsonb

from ml.adapters.backend_documents import document_from_backend
from ml.adapters.postgres_catalog import load_versions
from ml.adapters.postgres_chunks import ChunkPersistenceConflict
from ml.ingestion.validation import DocumentInputError
from ml.pipeline import ML1Pipeline, PipelineConfig
from ml.embeddings import Embedder
from ml.tests.test_pipeline import Counter, Encoder
from ml.tests.test_document_quality import fixture
import ml.tests.test_ml_storage as storage_fixture


class BackendVersionTests(unittest.TestCase):
    def test_current_backend_payloads_are_accepted(self):
        root = Path(__file__).resolve().parents[2]
        for relative in ('docs/api/examples/article-processing.request.json',):
            payload = json.loads((root / relative).read_text())
            self.assertGreater(payload['article']['version_no'], 0)
            saved = document_from_backend(payload)
            self.assertEqual(saved.document_version_id, payload['article']['article_version_id'])
            self.assertEqual(saved.normalized_text, payload['article']['body'])

    def test_invalid_version_is_rejected_without_changing_saved_dto(self):
        payload = fixture()
        for value in (0, -1, True, 1.0, '1', None):
            payload['article']['version_no'] = value
            with self.assertRaises(DocumentInputError):
                document_from_backend(payload)
        payload['article']['version_no'] = 3
        self.assertNotIn('version_no', document_from_backend(payload).to_dict())


@unittest.skipUnless(os.environ.get('ML1_TEST_POSTGRES_DSN'), 'Disposable PostgreSQL + pgvector')
class PipelineStateTests(unittest.TestCase):
    drop_schema = storage_fixture.MLStoragePostgresTests.drop_schema

    def setUp(self):
        storage_fixture.MLStoragePostgresTests.setUp(self)
        self.payload = fixture()
        self.payload['article']['version_no'] = 1
        self.context = str(uuid4())
        self.pipeline = ML1Pipeline(self.connection, Embedder(Encoder()), Counter(), schema=self.schema,
                                    context_chunks={self.context: []}, config=PipelineConfig(reranker_enabled=False))

    def table(self, name):
        return sql.Identifier(self.schema, name)

    def index(self):
        return self.pipeline.index(self.payload, business_context_ids=[self.context])

    def test_backend_version_and_readiness_survive_restart_and_retry(self):
        indexed = self.index()
        versions = load_versions(self.connection, schema=self.schema)
        self.assertEqual(len(versions), 1)
        self.assertTrue(versions[0].ready)
        self.assertEqual(versions[0].version_no, 1)
        before = self.connection.execute(sql.SQL('SELECT indexed_at FROM {}').format(
            self.table('document_version_catalog'))).fetchone()
        self.index()
        self.assertEqual(before, self.connection.execute(sql.SQL('SELECT indexed_at FROM {}').format(
            self.table('document_version_catalog'))).fetchone())
        restored = ML1Pipeline(self.connection, Embedder(Encoder()), Counter(), schema=self.schema,
                               versions=versions, context_chunks={self.context: indexed.chunk_ids},
                               config=PipelineConfig(reranker_enabled=False))
        self.assertEqual(restored.search('Условия', business_context_id=self.context)['status'], 'ok')
        self.payload['article']['version_no'] = 2
        with self.assertRaises((ChunkPersistenceConflict, ValueError)):
            self.index()
        self.assertEqual(load_versions(self.connection, schema=self.schema), versions)

    def test_failed_latest_is_durable_and_does_not_index(self):
        indexed = self.index()
        self.payload['article']['article_version_id'] = str(uuid4())
        self.payload['article']['version_no'] = 2
        self.payload['article']['body'] = ' '
        self.assertFalse(self.index().indexed)
        versions = load_versions(self.connection, schema=self.schema)
        self.assertFalse(versions[-1].ready)
        restored = ML1Pipeline(self.connection, Embedder(Encoder()), Counter(), schema=self.schema,
                               versions=versions, context_chunks={self.context: indexed.chunk_ids},
                               config=PipelineConfig(reranker_enabled=False))
        self.assertEqual(restored.search('Условия', business_context_id=self.context)['status'], 'empty')

    def test_operation_key_fingerprint_and_terminal_state_constraints(self):
        processing_id = uuid4()
        version = self.payload['article']['article_version_id']
        insert = sql.SQL('INSERT INTO {} (processing_id, article_version_id, pipeline_version, '
                         'idempotency_key, request_fingerprint, request_snapshot, trace_id) '
                         'VALUES (%s,%s,%s,%s,%s,%s,%s)').format(self.table('article_processings'))
        values = (processing_id, version, 'ml-pipeline-v1', version + ':ml-pipeline-v1',
                  b'x' * 32, Jsonb(self.payload), self.payload['trace_id'])
        self.connection.execute(insert, values)
        with self.assertRaises(psycopg.errors.UniqueViolation):
            self.connection.execute(insert, (uuid4(),) + values[1:])
        with self.assertRaises(psycopg.errors.CheckViolation):
            self.connection.execute(sql.SQL("UPDATE {} SET state='SUCCEEDED' WHERE processing_id=%s").format(
                self.table('article_processings')), (processing_id,))
        self.index()
        self.connection.execute(sql.SQL("UPDATE {} SET state='SUCCEEDED', document_version_id=%s, result=%s "
                                        'WHERE processing_id=%s').format(self.table('article_processings')),
                                (version, Jsonb({'event_ids': []}), processing_id))
        state, snapshot = self.connection.execute(sql.SQL('SELECT state, request_snapshot FROM {}').format(
            self.table('article_processings'))).fetchone()
        self.assertEqual(state, 'SUCCEEDED')
        self.assertEqual(snapshot, self.payload)

    def test_event_fact_evidence_roundtrip_and_local_foreign_keys(self):
        indexed = self.index()
        event, fact = uuid4(), uuid4()
        self.connection.execute(sql.SQL('INSERT INTO {} (id,event_type,title,summary) VALUES (%s,%s,%s,%s)').format(
            self.table('events')), (event, 'credit_terms', 'Условия договоров', 'Только новые договоры.'))
        link = sql.SQL('INSERT INTO {} (event_id,document_version_id,pipeline_version,event_pipeline_config_version,'
                       'candidate_fingerprint,evidence_quotes) VALUES (%s,%s,%s,%s,%s,%s)').format(self.table('event_documents'))
        values = (event, self.payload['article']['article_version_id'], 'ml-pipeline-v1', 'event-v1',
                  'candidate-1', Jsonb([{'chunk_id': indexed.chunk_ids[0], 'quote_text': 'Действующие договоры не изменяются.'}]))
        self.connection.execute(link, values)
        with self.assertRaises(psycopg.errors.UniqueViolation):
            self.connection.execute(link, values)
        self.connection.execute(sql.SQL('INSERT INTO {} (id,event_id,subject,predicate,object_value) '
                                        'VALUES (%s,%s,%s,%s,%s)').format(self.table('event_facts')),
                                (fact, event, 'Действующие договоры', 'условия', 'не изменяются'))
        evidence = sql.SQL('INSERT INTO {} (fact_id,chunk_id,quote_text) VALUES (%s,%s,%s)').format(self.table('fact_evidence'))
        quote = 'Действующие договоры не изменяются.'
        self.connection.execute(evidence, (fact, indexed.chunk_ids[0], quote))
        self.assertEqual(self.connection.execute(sql.SQL('SELECT quote_text FROM {}').format(
            self.table('fact_evidence'))).fetchone()[0], quote)
        with self.assertRaises(psycopg.errors.ForeignKeyViolation):
            self.connection.execute(evidence, (fact, uuid4(), quote))
