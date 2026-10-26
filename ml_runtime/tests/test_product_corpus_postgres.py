"""Real SQL checks for source URLs and server-selected bank context scope."""
import os
import unittest
from contextlib import ExitStack
from unittest.mock import patch, Mock
from uuid import uuid4, uuid5, NAMESPACE_URL
import psycopg
from psycopg import sql
from psycopg.conninfo import make_conninfo
from psycopg.types.json import Jsonb

from ml_runtime.product_corpus import ProductCorpus
from ml_runtime.product_store import ProductStore
from ml_runtime.tests.test_analysis_contract import load_fixture


@unittest.skipUnless(os.environ.get('ML_TEST_DATABASE_URL'), 'Requires isolated PostgreSQL')
class ProductCorpusPostgresTests(unittest.TestCase):
    def setUp(self):
        self.admin_dsn = os.environ['ML_TEST_DATABASE_URL']
        self.name = 'corpus_' + uuid4().hex
        with psycopg.connect(self.admin_dsn, autocommit=True) as connection:
            connection.execute(sql.SQL('CREATE DATABASE {}').format(sql.Identifier(self.name)))
        self.addCleanup(self.drop_database)
        self.dsn = make_conninfo(self.admin_dsn, dbname=self.name)
        self.store = ProductStore(self.dsn)
        self.request = load_fixture('analysis.request.json')
        self.event_id = self.request['event']['id']
        self.event_version = self.request['event']['document_version_ids'][0]
        self.bank_version, self.bank_chunk = str(uuid4()), str(uuid4())
        self.event_chunk = str(uuid4())
        self.outside_version = str(uuid4())
        with self.store.connect() as connection:
            connection.execute('''CREATE SCHEMA ml;
                CREATE TABLE ml.events(id uuid PRIMARY KEY,event_type text,title text,summary text,status text,created_at timestamptz DEFAULT now());
                CREATE TABLE ml.event_documents(event_id uuid,document_version_id uuid,evidence_quotes jsonb);
                CREATE TABLE ml.document_versions(id uuid PRIMARY KEY,snapshot jsonb);
                CREATE TABLE ml.document_version_catalog(document_version_id uuid PRIMARY KEY,document_id uuid,version_no int,ready boolean);
                CREATE TABLE ml.document_chunks(id uuid PRIMARY KEY,document_version_id uuid)''')
            connection.execute("INSERT INTO ml.events VALUES(%s,'monetary policy decision','Title','Summary','confirmed',now())", (self.event_id,))
            connection.execute('INSERT INTO ml.event_documents VALUES(%s,%s,%s)',
                (self.event_id, self.event_version, Jsonb([{'chunk_id': self.event_chunk}])))
            for version, role, source, chunk in (
                (self.event_version, 'external', str(uuid4()), self.event_chunk),
                (self.bank_version, 'bank', str(uuid5(NAMESPACE_URL,'alfa-hackathon-reference-pdfs')), self.bank_chunk),
                (self.outside_version, 'bank', str(uuid4()), str(uuid4())),
            ):
                snapshot = {'source': {'source_id': source, 'name': role, 'source_role': role, 'trust_level': 'primary'},
                    'canonical_url': 'https://example.org/article' if role == 'external' else None,
                    'published_at': None}
                connection.execute('INSERT INTO ml.document_versions VALUES(%s,%s)', (version, Jsonb(snapshot)))
                connection.execute('INSERT INTO ml.document_version_catalog VALUES(%s,%s,1,true)', (version,str(uuid4())))
                connection.execute('INSERT INTO ml.document_chunks VALUES(%s,%s)', (chunk,version))

    def drop_database(self):
        with psycopg.connect(self.admin_dsn, autocommit=True) as connection:
            connection.execute(sql.SQL('DROP DATABASE {} WITH (FORCE)').format(sql.Identifier(self.name)))

    def test_feed_preserves_public_url_and_omits_absent_url(self):
        items = ProductCorpus(self.store).feed_items()
        self.assertEqual(items[0]['topic'], 'financing')
        self.assertEqual(items[0]['sources'][0]['url'], 'https://example.org/article')
        with self.store.connect() as connection:
            connection.execute("UPDATE ml.document_versions SET snapshot=jsonb_set(snapshot,'{canonical_url}','null') WHERE id=%s", (self.event_version,))
        self.assertNotIn('url', ProductCorpus(self.store).feed_items()[0]['sources'][0])

    def test_only_registered_ready_bank_source_can_extend_scope(self):
        agent = Mock()
        with ExitStack() as stack:
            stack.enter_context(patch('ml_runtime.model_client.create_agent_from_env', return_value=agent))
            stack.enter_context(patch('ml.database.connect_runtime', side_effect=self.store.connect))
            stack.enter_context(patch('ml.embeddings.Embedder'))
            stack.enter_context(patch('ml.pipeline.ML1Pipeline'))
            stack.enter_context(patch('ml.adapters.postgres_retrieval.PostgresRetriever'))
            with ProductCorpus(self.store).analysis_service(self.request) as service:
                queries = service.planner.plan(self.request)
                self.assertEqual(queries[1]['filters']['document_version_ids'], [self.bank_version])
                self.assertNotIn(self.outside_version, queries[1]['filters']['document_version_ids'])
                self.assertEqual(agent.context_document_version_ids, frozenset([self.bank_version]))
            with self.store.connect() as connection:
                connection.execute('DELETE FROM ml.event_documents WHERE event_id=%s',(self.event_id,))
            with ProductCorpus(self.store).analysis_service(self.request) as service:
                self.assertEqual(len(service.planner.plan(self.request)),1)


    def test_unready_future_and_superseded_bank_versions_are_not_authorized(self):
        self.request['event']['occurred_at'] = '2026-09-11T00:00:00Z'
        def plan():
            with ExitStack() as stack:
                stack.enter_context(patch('ml_runtime.model_client.create_agent_from_env', return_value=Mock()))
                stack.enter_context(patch('ml.database.connect_runtime', side_effect=self.store.connect))
                stack.enter_context(patch('ml.embeddings.Embedder'))
                stack.enter_context(patch('ml.pipeline.ML1Pipeline'))
                stack.enter_context(patch('ml.adapters.postgres_retrieval.PostgresRetriever'))
                with ProductCorpus(self.store).analysis_service(self.request) as service:
                    return service.planner.plan(self.request)
        self.assertEqual(len(plan()),2)
        with self.store.connect() as connection:
            connection.execute('UPDATE ml.document_version_catalog SET ready=false WHERE document_version_id=%s',(self.bank_version,))
        self.assertEqual(len(plan()),1)
        with self.store.connect() as connection:
            connection.execute('UPDATE ml.document_version_catalog SET ready=true WHERE document_version_id=%s',(self.bank_version,))
            connection.execute("UPDATE ml.document_versions SET snapshot=jsonb_set(snapshot,'{published_at}',%s) WHERE id=%s",(Jsonb('2026-09-12T00:00:00Z'),self.bank_version))
        self.assertEqual(len(plan()),1)
        with self.store.connect() as connection:
            connection.execute("UPDATE ml.document_versions SET snapshot=jsonb_set(snapshot,'{published_at}','null') WHERE id=%s",(self.bank_version,))
        self.assertEqual(len(plan()),2)
        with self.store.connect() as connection:
            connection.execute('INSERT INTO ml.document_version_catalog SELECT %s,document_id,2,false FROM ml.document_version_catalog WHERE document_version_id=%s',(str(uuid4()),self.bank_version))
        self.assertEqual(len(plan()),1)

    def test_follow_up_never_opens_corpus_or_retriever(self):
        with patch('ml_runtime.model_client.create_agent_from_env', return_value=Mock()), \
             patch.object(self.store, 'connect', side_effect=AssertionError('corpus access')):
            with ProductCorpus(self.store).analysis_service(load_fixture('follow_up.request.json')) as service:
                self.assertIsNone(service.retriever)
