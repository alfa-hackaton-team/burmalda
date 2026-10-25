"""Unit tests need psycopg only; PostgreSQL acceptance is explicitly opt-in.

ML1_TEST_POSTGRES_DSN must point to a disposable test database with CREATE SCHEMA
permission. Each test owns an isolated randomly named schema; no public tables
are read or changed. pgvector is deliberately not required.
"""
import os
import unittest
from concurrent.futures import ThreadPoolExecutor
from dataclasses import replace
from pathlib import Path
from threading import Barrier
from unittest.mock import MagicMock
from uuid import UUID, uuid4

try:
    import psycopg
except ImportError:
    psycopg = None

if psycopg is not None:
    from psycopg import sql
    from psycopg.pq import TransactionStatus
    from ml.adapters.postgres_chunks import (
        ChunkPersistenceConflict, ChunkPersistenceError, save_document_chunks,
    )

from ml.chunking.dto import DocumentChunk


def chunks(version=None, profile='v1'):
    version = version or str(uuid4())
    # Persistence must preserve arbitrary DTO values, not regenerate UUIDs or
    # tokenize. Unicode offsets here count code points, including combining marks.
    return (
        DocumentChunk(str(uuid4()), version, profile, 0, 'Привет 🙂. ', 0, 10, 8),
        DocumentChunk(str(uuid4()), version, profile, 1, '🙂. e\u0301 end', 7, 16, 7),
    )


@unittest.skipIf(psycopg is None, 'Install ml/requirements-persistence.txt')
class ChunkPersistenceUnitTests(unittest.TestCase):
    def connection(self):
        connection = MagicMock()
        connection.info.transaction_status = TransactionStatus.IDLE
        connection.cursor.return_value.__enter__.return_value.fetchall.return_value = []
        return connection

    def test_invalid_sets_do_not_start_transaction(self):
        first, second = chunks()
        bad_sets = (
            (), ({},), (second, first), (replace(first, chunk_no=1),),
            (first, replace(second, document_version_id=str(uuid4()))),
            (first, replace(second, chunking_profile_id='v2')),
            (first, replace(second, id=first.id)),
            (first, replace(second, char_start=11, char_end=20)),
            (first, replace(second, chunk_text='X. e\u0301 end')),
        )
        for bad in bad_sets:
            with self.subTest(bad=bad):
                connection = self.connection()
                with self.assertRaises(ValueError):
                    save_document_chunks(connection, bad)
                connection.transaction.assert_not_called()

    def test_existing_transaction_is_not_committed_or_rolled_back(self):
        for status in (TransactionStatus.INTRANS, TransactionStatus.INERROR, TransactionStatus.ACTIVE):
            connection = self.connection()
            connection.info.transaction_status = status
            with self.assertRaisesRegex(ValueError, 'idle connection'):
                save_document_chunks(connection, chunks())
            connection.transaction.assert_not_called()
            connection.commit.assert_not_called()
            connection.rollback.assert_not_called()

    def test_safe_error_on_database_failure(self):
        connection = self.connection()
        cursor = connection.cursor.return_value.__enter__.return_value
        cursor.execute.side_effect = psycopg.OperationalError('password secret document')
        with self.assertRaisesRegex(ChunkPersistenceError, '^PostgreSQL chunk persistence failed$'):
            save_document_chunks(connection, chunks())
        connection.transaction.return_value.__exit__.assert_called_once()
        self.assertIs(connection.transaction.return_value.__exit__.call_args.args[0], psycopg.OperationalError)
        connection.close.assert_not_called()

    def test_invalid_schema_before_database_access(self):
        for schema in ('', ' ', None):
            connection = self.connection()
            with self.assertRaises(ValueError):
                save_document_chunks(connection, chunks(), schema=schema)
            connection.transaction.assert_not_called()


@unittest.skipUnless(psycopg is not None and os.environ.get('ML1_TEST_POSTGRES_DSN'),
                     'Set ML1_TEST_POSTGRES_DSN and install persistence dependencies')
class ChunkPersistencePostgresTests(unittest.TestCase):
    def setUp(self):
        self.dsn = os.environ['ML1_TEST_POSTGRES_DSN']
        self.schema = 'ml1_test_chunks_' + uuid4().hex
        self.connection = psycopg.connect(self.dsn, autocommit=True, options='-c statement_timeout=10000')
        self.addCleanup(self.connection.close)
        self.connection.execute(sql.SQL('CREATE SCHEMA {}').format(sql.Identifier(self.schema)))
        self.addCleanup(self.drop_schema)
        with self.connection.transaction():
            self.connection.execute(sql.SQL('SET LOCAL search_path TO {}').format(sql.Identifier(self.schema)))
            self.connection.execute((Path(__file__).parent / 'fixtures/persistence.sql').read_text())
        self.rows = chunks()
        self.parent(self.rows[0].document_version_id)

    def drop_schema(self):
        self.connection.rollback()
        self.connection.execute(sql.SQL('DROP SCHEMA {} CASCADE').format(sql.Identifier(self.schema)))

    def parent(self, version):
        self.connection.execute(sql.SQL('INSERT INTO {} (id) VALUES (%s)').format(
            sql.Identifier(self.schema, 'document_versions')), (UUID(version),))

    def save(self, rows=None, connection=None):
        save_document_chunks(connection or self.connection, self.rows if rows is None else rows, schema=self.schema)

    def read(self):
        return self.connection.execute(sql.SQL(
            'SELECT id, document_version_id, chunking_profile_id, chunk_no, '
            'chunk_text, char_start, char_end, token_count, created_at, '
            'search_vector::text, embedding, embedding_model, embedding_version '
            'FROM {} ORDER BY document_version_id, chunking_profile_id, chunk_no'
        ).format(sql.Identifier(self.schema, 'document_chunks'))).fetchall()

    def test_success_all_fields_and_database_defaults(self):
        self.save()
        persisted = self.read()
        self.assertEqual(len(persisted), 2)
        for chunk, row in zip(self.rows, persisted):
            self.assertEqual(row[:8], (UUID(chunk.id), UUID(chunk.document_version_id), chunk.chunking_profile_id,
                                      chunk.chunk_no, chunk.chunk_text, chunk.char_start, chunk.char_end, chunk.token_count))
            self.assertIsNotNone(row[8])
            self.assertTrue(row[9])
            self.assertEqual(row[10:], (None, None, None))

    def test_chunker_output_roundtrip(self):
        from ml.chunking import chunk_document
        from ml.tests.test_chunking import CharacterCounter, TEST_PROFILE, document

        saved = document(('Текст 🙂. Данные e\u0301!\r\n\r\n') * 20)
        generated = chunk_document(saved, CharacterCounter(), TEST_PROFILE)
        self.parent(saved.document_version_id)
        self.save(generated)
        persisted = tuple(DocumentChunk(str(row[0]), str(row[1]), *row[2:8]) for row in self.read())
        self.assertEqual(persisted, generated)
        self.save(generated)
        self.assertEqual(len(self.read()), len(generated))

    def test_identical_retry_preserves_timestamp_fts_and_embedding(self):
        self.save()
        self.connection.execute(sql.SQL(
            "UPDATE {} SET embedding = ARRAY[0.25, 0.5], embedding_model = 'test-only', "
            "embedding_version = 'fixture' WHERE id = %s"
        ).format(sql.Identifier(self.schema, 'document_chunks')), (UUID(self.rows[0].id),))
        before = self.read()
        self.save()
        self.assertEqual(before, self.read())

    def test_conflicting_payload_fields_never_overwrite(self):
        self.save()
        before = self.read()
        for updates in ({'id': str(uuid4())}, {'chunk_text': 'ПРИВЕТ 🙂. '}, {'token_count': 99}):
            with self.subTest(updates=updates):
                with self.assertRaises(ChunkPersistenceConflict):
                    self.save((replace(self.rows[0], **updates), self.rows[1]))
                self.assertEqual(before, self.read())
        moved = (replace(self.rows[0], char_end=11, chunk_text='Привет! 🙂. '),
                 replace(self.rows[1], char_start=8, char_end=17))
        with self.assertRaises(ChunkPersistenceConflict):
            self.save(moved)
        self.assertEqual(before, self.read())

    def test_smaller_or_larger_set_is_conflict(self):
        self.save()
        before = self.read()
        for other in (self.rows[:1], self.rows + (
                DocumentChunk(str(uuid4()), self.rows[0].document_version_id, 'v1', 2, 'More', 16, 20, 1),)):
            with self.assertRaises(ChunkPersistenceConflict):
                self.save(other)
            self.assertEqual(before, self.read())

    def test_existing_partial_set_is_not_silently_completed(self):
        self.save(self.rows[:1])
        before = self.read()
        with self.assertRaises(ChunkPersistenceConflict):
            self.save()
        self.assertEqual(before, self.read())

    def test_error_on_second_insert_rolls_back_first(self):
        bad = (self.rows[0], replace(self.rows[1], token_count=2**31))
        with self.assertRaises(ChunkPersistenceError):
            self.save(bad)
        self.assertEqual(self.read(), [])
        self.assertEqual(self.connection.info.transaction_status, TransactionStatus.IDLE)
        self.save()
        self.assertEqual(len(self.read()), 2)

    def test_missing_parent_rolls_back(self):
        other = chunks()
        with self.assertRaises(ChunkPersistenceError):
            self.save(other)
        self.assertEqual(self.read(), [])

    def test_global_uuid_collision_rolls_back_entire_new_set(self):
        self.save()
        before = self.read()
        other = chunks()
        self.parent(other[0].document_version_id)
        with self.assertRaises(ChunkPersistenceConflict):
            self.save((other[0], replace(other[1], id=self.rows[0].id)))
        self.assertEqual(before, self.read())

    def test_distinct_profiles_and_versions_coexist(self):
        self.save()
        self.save(chunks(self.rows[0].document_version_id, 'v2'))
        other = chunks()
        self.parent(other[0].document_version_id)
        self.save(other)
        self.assertEqual(len(self.read()), 6)

    def test_non_autocommit_connection_commits_complete_set(self):
        with psycopg.connect(self.dsn) as connection:
            self.save(connection=connection)
            # Read on independent connection before exiting outer context.
            self.assertEqual(len(self.read()), 2)

    def test_connection_row_factory_does_not_change_comparison(self):
        with psycopg.connect(self.dsn, row_factory=psycopg.rows.dict_row) as connection:
            self.save(connection=connection)
            self.save(connection=connection)
        self.assertEqual(len(self.read()), 2)

    def concurrent(self, other):
        barrier = Barrier(2)

        def write(rows):
            with psycopg.connect(self.dsn, options='-c statement_timeout=10000') as connection:
                barrier.wait(timeout=5)
                try:
                    self.save(rows, connection)
                    return 'ok'
                except ChunkPersistenceConflict:
                    return 'conflict'

        with ThreadPoolExecutor(max_workers=2) as pool:
            futures = [pool.submit(write, rows) for rows in (self.rows, other)]
            return sorted(f.result(timeout=15) for f in futures)

    def test_concurrent_identical_writers(self):
        self.assertEqual(self.concurrent(self.rows), ['ok', 'ok'])
        self.assertEqual(len(self.read()), 2)

    def test_concurrent_conflicting_writers(self):
        other = (replace(self.rows[0], token_count=9), self.rows[1])
        self.assertEqual(self.concurrent(other), ['conflict', 'ok'])
        self.assertEqual(len(self.read()), 2)
