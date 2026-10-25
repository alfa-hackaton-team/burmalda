"""Persistence acceptance with real VECTOR(1024), never model weights/search."""
import importlib.util
import json
import os
import unittest
from dataclasses import replace
from threading import Barrier
from concurrent.futures import ThreadPoolExecutor
from unittest.mock import MagicMock
from uuid import UUID, uuid4

from ml.embeddings import Embedder, V1
from ml.embeddings.core import text_hash
from ml.tests.test_embeddings import FakeEncoder, unit_vector

HAS_PSYCOPG = importlib.util.find_spec('psycopg') is not None
if HAS_PSYCOPG:
    import psycopg
    from psycopg import sql
    from psycopg.pq import TransactionStatus
    from ml.adapters.postgres_chunks import (
        ChunkPersistenceConflict, ChunkPersistenceError, save_document_embeddings,
    )
    import ml.tests.test_chunk_persistence as fixture


@unittest.skipUnless(HAS_PSYCOPG, 'Optional persistence dependencies')
class EmbeddingPersistenceUnitTests(unittest.TestCase):
    def test_ids_and_text_hash_must_match_before_sql(self):
        chunks = fixture.chunks()
        items = Embedder(FakeEncoder()).encode_chunks(chunks)
        for bad in (items[:1], (items[0], items[0]),
                    (replace(items[0], chunk_id=str(uuid4())), items[1]),
                    (replace(items[0], text_sha256=text_hash('different')), items[1])):
            connection = MagicMock()
            with self.assertRaises(ValueError):
                save_document_embeddings(connection, chunks, bad)
            connection.transaction.assert_not_called()

    def test_active_transaction_and_bad_schema_rejected(self):
        chunks = fixture.chunks()
        items = Embedder(FakeEncoder()).encode_chunks(chunks)
        connection = MagicMock()
        connection.info.transaction_status = TransactionStatus.INTRANS
        with self.assertRaisesRegex(ValueError, 'idle connection'):
            save_document_embeddings(connection, chunks, items)
        connection.info.transaction_status = TransactionStatus.IDLE
        with self.assertRaises(ValueError):
            save_document_embeddings(connection, chunks, items, schema='')
        connection.transaction.assert_not_called()


@unittest.skipUnless(HAS_PSYCOPG and os.environ.get('ML1_TEST_POSTGRES_DSN'), 'Explicit test PostgreSQL with pgvector')
class EmbeddingPersistencePostgresTests(unittest.TestCase):
    # Reuse isolated-schema lifecycle; do not inherit/re-run chunk test methods.
    def setUp(self):
        fixture.ChunkPersistencePostgresTests.setUp(self)
        extension_schema = self.connection.execute(
            'SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace '
            "WHERE e.extname = 'vector'"
        ).fetchone()
        if extension_schema is None:
            # Only this test schema is changed. Cleanup drops its extension too.
            self.connection.execute(sql.SQL('CREATE EXTENSION vector WITH SCHEMA {}').format(sql.Identifier(self.schema)))
            vector_schema = self.schema
        else:
            vector_schema = extension_schema[0]
        self.connection.execute(sql.SQL('ALTER TABLE {} ALTER COLUMN embedding TYPE {}(1024) USING NULL').format(
            sql.Identifier(self.schema, 'document_chunks'), sql.Identifier(vector_schema, 'vector')))
        self.items = Embedder(FakeEncoder()).encode_chunks(self.rows)

    if HAS_PSYCOPG:
        drop_schema = fixture.ChunkPersistencePostgresTests.drop_schema
        parent = fixture.ChunkPersistencePostgresTests.parent
        save = fixture.ChunkPersistencePostgresTests.save

    def read(self):
        return self.connection.execute(sql.SQL(
            'SELECT id, chunk_text, char_start, char_end, token_count, created_at, search_vector::text, '
            'embedding::text, embedding_model, embedding_version FROM {} ORDER BY chunk_no'
        ).format(sql.Identifier(self.schema, 'document_chunks'))).fetchall()

    def save_vectors(self, items=None, chunks=None, connection=None):
        save_document_embeddings(connection or self.connection, self.rows if chunks is None else chunks,
                                 self.items if items is None else items, schema=self.schema)

    def test_vector1024_roundtrip_and_idempotent_retry_preserves_chunk_fields(self):
        self.save()
        before = self.read()
        self.save_vectors(tuple(reversed(self.items)))
        first = self.read()
        for row_before, row, item in zip(before, first, self.items):
            self.assertEqual(row[:7], row_before[:7])
            self.assertEqual(tuple(json.loads(row[7])), item.vector)
            self.assertEqual(row[8:], (V1.model, V1.version))
            self.assertEqual(row[0], UUID(item.chunk_id))
        self.save_vectors()
        self.assertEqual(self.read(), first)
        self.save()  # chunk-only retry must preserve the attached embeddings
        self.assertEqual(self.read(), first)

    def test_missing_or_changed_chunk_rows_rejected(self):
        with self.assertRaises(ChunkPersistenceConflict):
            self.save_vectors()
        self.save()
        changed = (replace(self.rows[0], token_count=999), self.rows[1])
        with self.assertRaises(ChunkPersistenceConflict):
            self.save_vectors(chunks=changed)
        self.assertTrue(all(row[7] is None for row in self.read()))

    def test_existing_vector_or_profile_conflict_never_overwrites(self):
        self.save()
        self.save_vectors()
        before = self.read()
        other = (replace(self.items[0], vector=unit_vector(9)), self.items[1])
        with self.assertRaises(ChunkPersistenceConflict):
            self.save_vectors(other)
        self.assertEqual(self.read(), before)
        self.connection.execute(sql.SQL("UPDATE {} SET embedding_version = 'older' WHERE chunk_no = 1").format(
            sql.Identifier(self.schema, 'document_chunks')))
        before = self.read()
        with self.assertRaises(ChunkPersistenceConflict):
            self.save_vectors()
        self.assertEqual(self.read(), before)

    def test_update_failure_rolls_back_all_embeddings(self):
        self.save()
        # First UPDATE succeeds; the second fails an independent DB constraint.
        self.connection.execute(sql.SQL(
            'ALTER TABLE {} ADD CHECK (chunk_no = 0 OR embedding IS NULL)'
        ).format(sql.Identifier(self.schema, 'document_chunks')))
        before = self.read()
        with self.assertRaises(ChunkPersistenceError):
            self.save_vectors()
        self.assertEqual(self.read(), before)

    def test_incomplete_input_set_is_conflict(self):
        self.save()
        with self.assertRaises(ChunkPersistenceConflict):
            self.save_vectors(self.items[:1], self.rows[:1])

    def test_completes_missing_vectors_without_touching_existing(self):
        self.save()
        self.save_vectors()
        before = self.read()
        self.connection.execute(sql.SQL(
            'UPDATE {} SET embedding = NULL, embedding_model = NULL, embedding_version = NULL WHERE chunk_no = 1'
        ).format(sql.Identifier(self.schema, 'document_chunks')))
        self.save_vectors()
        self.assertEqual(self.read(), before)

    def test_float32_roundoff_retry_is_noop(self):
        self.save()
        self.save_vectors()
        before = self.read()
        changed = list(self.items[0].vector)
        changed[9] = 5e-7
        retry = (replace(self.items[0], vector=tuple(changed)), self.items[1])
        self.save_vectors(retry)
        self.assertEqual(self.read(), before)

    def test_concurrent_different_vectors_one_success_one_conflict(self):
        self.save()
        barrier = Barrier(2)

        def write(items):
            with psycopg.connect(self.dsn, options='-c statement_timeout=10000') as connection:
                barrier.wait(timeout=5)
                try:
                    self.save_vectors(items=items, connection=connection)
                    return 'ok'
                except ChunkPersistenceConflict:
                    return 'conflict'

        other = (replace(self.items[0], vector=unit_vector(9)), self.items[1])
        with ThreadPoolExecutor(max_workers=2) as pool:
            futures = [pool.submit(write, items) for items in (self.items, other)]
            self.assertEqual(sorted(f.result(timeout=15) for f in futures), ['conflict', 'ok'])
