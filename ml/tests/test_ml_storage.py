"""Production ML migrations + snapshot/chunk persistence on isolated schemas."""
import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from dataclasses import replace
from threading import Barrier
from unittest.mock import MagicMock, patch
from uuid import UUID, uuid4

from ml.chunking import chunk_document
from ml.tests.test_chunking import CharacterCounter, TEST_PROFILE, document

HAS_PSYCOPG = importlib.util.find_spec('psycopg') is not None
if HAS_PSYCOPG:
    import psycopg
    from psycopg import sql
    from psycopg.pq import TransactionStatus
    from ml.database import connect_runtime
    from ml.migrate import MigrationError, apply_migrations, grant_runtime_access
    from ml.adapters.postgres_chunks import (
        ChunkPersistenceConflict, ChunkPersistenceError, save_document_version_and_chunks,
        save_document_chunks, save_document_embeddings,
    )


@unittest.skipUnless(HAS_PSYCOPG, 'Optional persistence dependencies')
class MLStorageUnitTests(unittest.TestCase):
    def test_runtime_dsn_is_separate_and_required(self):
        with patch.dict(os.environ, {'DATABASE_URL': 'go-database'}, clear=True):
            with self.assertRaisesRegex(ValueError, 'ML_DATABASE_URL'):
                connect_runtime()
        with patch.dict(os.environ, {'ML_DATABASE_URL': 'ml-database'}, clear=True), \
                patch('ml.database.psycopg.connect') as connect:
            self.assertIs(connect_runtime(), connect.return_value)
            connect.assert_called_once_with('ml-database', autocommit=True)

    def test_invalid_document_or_chunks_before_transaction(self):
        saved = document('Data. ' * 20)
        chunks = chunk_document(saved, CharacterCounter(), TEST_PROFILE)
        for bad_doc, bad_chunks in (
            ({}, chunks), (replace(saved, normalized_text=''), chunks),
            (saved, chunks[:1]), (replace(saved, document_version_id=str(uuid4())), chunks),
            (saved, (replace(chunks[0], chunk_text='x' + chunks[0].chunk_text[1:]),) + chunks[1:]),
        ):
            connection = MagicMock()
            with self.assertRaises((ValueError, TypeError)):
                save_document_version_and_chunks(connection, bad_doc, bad_chunks)
            connection.transaction.assert_not_called()

    def test_no_existing_caller_transaction_is_modified(self):
        saved = document('Data')
        chunks = chunk_document(saved, CharacterCounter(), TEST_PROFILE)
        connection = MagicMock()
        connection.info.transaction_status = TransactionStatus.INTRANS
        for call in (lambda: save_document_version_and_chunks(connection, saved, chunks),
                     lambda: apply_migrations(connection),
                     lambda: grant_runtime_access(connection, 'runtime')):
            with self.assertRaises(ValueError):
                call()
        connection.transaction.assert_not_called()
        connection.commit.assert_not_called()
        connection.rollback.assert_not_called()


@unittest.skipUnless(HAS_PSYCOPG and os.environ.get('ML1_TEST_POSTGRES_DSN'), 'Explicit disposable PostgreSQL with pgvector')
class MLStoragePostgresTests(unittest.TestCase):
    def setUp(self):
        self.dsn = os.environ['ML1_TEST_POSTGRES_DSN']
        self.schema = 'ml1_storage_' + uuid4().hex
        self.connection = psycopg.connect(self.dsn, autocommit=True, options='-c statement_timeout=10000')
        self.addCleanup(self.connection.close)
        # Extension prerequisite is test infrastructure, not part of migration.
        self.connection.execute(sql.SQL('CREATE SCHEMA {}').format(sql.Identifier(self.schema)))
        self.addCleanup(self.drop_schema)
        if not self.connection.execute("SELECT 1 FROM pg_extension WHERE extname='vector'").fetchone():
            self.connection.execute(sql.SQL('CREATE EXTENSION vector WITH SCHEMA {}').format(sql.Identifier(self.schema)))
        self.assertEqual(apply_migrations(self.connection, schema=self.schema), ('000001_document_storage', '000002_pipeline_state_and_events'))
        self.document = document('Привет 🙂. Данные e\u0301.\r\n\r\n' * 10)
        self.chunks = chunk_document(self.document, CharacterCounter(), TEST_PROFILE)

    def drop_schema(self):
        self.connection.rollback()
        self.connection.execute(sql.SQL('DROP SCHEMA {} CASCADE').format(sql.Identifier(self.schema)))

    def save(self, saved=None, chunks=None, connection=None):
        save_document_version_and_chunks(
            connection or self.connection, self.document if saved is None else saved,
            self.chunks if chunks is None else chunks, schema=self.schema,
        )

    def snapshots(self):
        return self.connection.execute(sql.SQL('SELECT id, document_id, snapshot, created_at FROM {} ORDER BY id').format(
            sql.Identifier(self.schema, 'document_versions'))).fetchall()

    def rows(self):
        return self.connection.execute(sql.SQL('SELECT id, chunk_text, char_start, char_end, token_count, created_at FROM {} ORDER BY chunk_no').format(
            sql.Identifier(self.schema, 'document_chunks'))).fetchall()

    def test_real_migration_schema_fk_vector_and_journal_idempotency(self):
        self.assertEqual(apply_migrations(self.connection, schema=self.schema), ())
        table = f'{self.schema}.document_chunks'
        constraints = self.connection.execute(
            "SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid=%s::regclass AND contype='f'", (table,)
        ).fetchall()
        self.assertEqual(len(constraints), 1)
        self.assertIn(self.schema + '.document_versions', constraints[0][0])
        self.assertNotIn('public.', constraints[0][0])
        vector_type = self.connection.execute(
            "SELECT format_type(atttypid, atttypmod) FROM pg_attribute WHERE attrelid=%s::regclass AND attname='embedding'", (table,)
        ).fetchone()[0]
        self.assertTrue(vector_type.endswith('vector(1024)'))
        journal = self.connection.execute(sql.SQL('SELECT version, length(checksum) FROM {}').format(
            sql.Identifier(self.schema, 'schema_migrations'))).fetchall()
        self.assertEqual(journal, [('000001_document_storage', 64), ('000002_pipeline_state_and_events', 64)])

    def test_snapshot_exact_payload_all_provenance_and_empty_title(self):
        saved = replace(self.document, title='', published_at=None)
        self.save(saved)
        stored = self.snapshots()[0]
        expected = saved.to_dict()
        expected.pop('trace_id')
        expected.pop('pipeline_version')
        self.assertEqual(stored[:3], (UUID(saved.document_version_id), UUID(saved.document_id), expected))
        self.assertEqual(len(self.rows()), len(self.chunks))
        for chunk, row in zip(self.chunks, self.rows()):
            self.assertEqual(row[:5], (UUID(chunk.id), chunk.chunk_text, chunk.char_start, chunk.char_end, chunk.token_count))

    def test_retry_changed_trace_pipeline_no_snapshot_rewrite(self):
        self.save()
        before = (self.snapshots(), self.rows())
        self.save(replace(self.document, trace_id='another-trace', pipeline_version='another-pipeline'))
        self.assertEqual((self.snapshots(), self.rows()), before)

    def test_snapshot_metadata_conflicts_even_with_identical_chunks(self):
        self.save()
        before = (self.snapshots(), self.rows())
        for saved in (replace(self.document, title='Changed'),
                      replace(self.document, content_hash='a' * 64),
                      replace(self.document, source=replace(self.document.source, config_version='999')),
                      replace(self.document, retrieved_at='2026-01-01T00:00:00Z')):
            with self.assertRaises(ChunkPersistenceConflict):
                self.save(saved)
            self.assertEqual((self.snapshots(), self.rows()), before)

    def test_insert_error_rolls_back_new_snapshot_and_all_chunks(self):
        bad = (self.chunks[0], replace(self.chunks[1], token_count=2**31)) + self.chunks[2:]
        with self.assertRaises(ChunkPersistenceError):
            self.save(chunks=bad)
        self.assertEqual(self.snapshots(), [])
        self.assertEqual(self.rows(), [])
        self.save()

    def test_conflicting_chunk_does_not_change_existing_snapshot(self):
        self.save()
        before = (self.snapshots(), self.rows())
        bad = (replace(self.chunks[0], token_count=self.chunks[0].token_count + 1),) + self.chunks[1:]
        with self.assertRaises(ChunkPersistenceConflict):
            self.save(chunks=bad)
        self.assertEqual((self.snapshots(), self.rows()), before)

    def test_different_profiles_share_one_snapshot(self):
        self.save()
        other = tuple(replace(chunk, id=str(uuid4()), chunking_profile_id='test-v2') for chunk in self.chunks)
        self.save(chunks=other)
        self.assertEqual(len(self.snapshots()), 1)
        self.assertEqual(len(self.rows()), 2 * len(self.chunks))

    def test_migration_checksum_conflict_preserves_schema(self):
        self.connection.execute(sql.SQL("UPDATE {} SET checksum='changed'").format(
            sql.Identifier(self.schema, 'schema_migrations')))
        with self.assertRaisesRegex(MigrationError, 'checksum'):
            apply_migrations(self.connection, schema=self.schema)
        self.assertEqual(self.snapshots(), [])

    def test_failed_pending_migration_rolls_back_ddl_and_journal(self):
        from ml.migrate import MIGRATIONS
        with tempfile.TemporaryDirectory() as directory:
            for original in MIGRATIONS.glob('*.sql'):
                Path(directory, original.name).write_bytes(original.read_bytes())
            Path(directory, '000003_failure.sql').write_text('CREATE TABLE rollback_marker(id integer); SELECT 1/0;')
            with patch('ml.migrate.MIGRATIONS', Path(directory)):
                with self.assertRaises(MigrationError):
                    apply_migrations(self.connection, schema=self.schema)
        self.assertIsNone(self.connection.execute('SELECT to_regclass(%s)', (self.schema + '.rollback_marker',)).fetchone()[0])
        self.assertEqual(apply_migrations(self.connection, schema=self.schema), ())

    def test_initial_migration_error_removes_new_schema_and_journal(self):
        target = 'ml1_rollback_' + uuid4().hex
        with tempfile.TemporaryDirectory() as directory:
            Path(directory, '000001_failure.sql').write_text('CREATE TABLE marker(id integer); SELECT 1/0;')
            with patch('ml.migrate.MIGRATIONS', Path(directory)):
                with self.assertRaises(MigrationError):
                    apply_migrations(self.connection, schema=target)
        self.assertIsNone(self.connection.execute('SELECT to_regnamespace(%s)', (target,)).fetchone()[0])

    def test_concurrent_identical_snapshots(self):
        barrier = Barrier(2)

        def write():
            with psycopg.connect(self.dsn) as connection:
                barrier.wait(timeout=5)
                self.save(connection=connection)

        with ThreadPoolExecutor(max_workers=2) as pool:
            futures = [pool.submit(write) for _ in range(2)]
            for future in futures:
                future.result(timeout=15)
        self.assertEqual(len(self.snapshots()), 1)
        self.assertEqual(len(self.rows()), len(self.chunks))

    def test_concurrent_profiles_cannot_disagree_on_parent_snapshot(self):
        barrier = Barrier(2)
        other = tuple(replace(chunk, id=str(uuid4()), chunking_profile_id='test-v2') for chunk in self.chunks)

        def write(saved, chunks):
            with psycopg.connect(self.dsn) as connection:
                barrier.wait(timeout=5)
                try:
                    self.save(saved=saved, chunks=chunks, connection=connection)
                    return 'ok'
                except ChunkPersistenceConflict:
                    return 'conflict'

        with ThreadPoolExecutor(max_workers=2) as pool:
            futures = [pool.submit(write, self.document, self.chunks),
                       pool.submit(write, replace(self.document, title='Other snapshot'), other)]
            self.assertEqual(sorted(f.result(timeout=15) for f in futures), ['conflict', 'ok'])
        self.assertEqual(len(self.snapshots()), 1)
        self.assertEqual(len(self.rows()), len(self.chunks))

    def test_default_ml_schema_and_snapshot_embedding_roundtrip(self):
        if self.connection.execute("SELECT 1 FROM pg_namespace WHERE nspname='ml'").fetchone():
            self.skipTest('Default ml schema already exists; never modify existing deployment data')
        from ml.chunking.dto import DocumentChunk
        from ml.embeddings import ChunkEmbedding
        from ml.embeddings.core import text_hash
        from ml.tests.test_embeddings import unit_vector

        self.assertEqual(apply_migrations(self.connection), ('000001_document_storage', '000002_pipeline_state_and_events'))
        self.addCleanup(lambda: self.connection.execute('DROP SCHEMA ml CASCADE'))
        saved = document('Data')
        # Synthetic DTO fixture, not a replacement for the production chunker.
        chunk = DocumentChunk(str(uuid4()), saved.document_version_id, 'v1', 0, 'Data', 0, 4, 1)
        item = ChunkEmbedding(chunk.id, text_hash(chunk.chunk_text), unit_vector())
        save_document_version_and_chunks(self.connection, saved, (chunk,))
        save_document_embeddings(self.connection, (chunk,), (item,))
        save_document_chunks(self.connection, (chunk,))
        row = self.connection.execute('SELECT embedding::text, embedding_model, embedding_version FROM ml.document_chunks').fetchone()
        self.assertTrue(row[0].startswith('[1,0,'))
        self.assertEqual(row[1:], (item.embedding_model, item.embedding_version))

    def new_role(self, prefix):
        role = prefix + uuid4().hex
        self.connection.execute(sql.SQL('CREATE ROLE {} NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT').format(sql.Identifier(role)))

        def drop_role():
            self.connection.execute(sql.SQL('DROP OWNED BY {}').format(sql.Identifier(role)))
            self.connection.execute(sql.SQL('DROP ROLE {}').format(sql.Identifier(role)))
        self.addCleanup(drop_role)
        return role

    def test_runtime_role_can_write_ml_but_not_ddl_journal_or_go(self):
        role = self.new_role('ml_runtime_test_')
        grant_runtime_access(self.connection, role, schema=self.schema)
        go_schema = 'go_test_' + uuid4().hex
        self.connection.execute(sql.SQL('CREATE SCHEMA {}').format(sql.Identifier(go_schema)))
        self.addCleanup(lambda: self.connection.execute(sql.SQL('DROP SCHEMA {} CASCADE').format(sql.Identifier(go_schema))))
        self.connection.execute(sql.SQL('CREATE TABLE {} (id int)').format(sql.Identifier(go_schema, 'processing_jobs')))
        with psycopg.connect(self.dsn, autocommit=True) as runtime:
            runtime.execute(sql.SQL('SET ROLE {}').format(sql.Identifier(role)))
            self.save(connection=runtime)
            self.save(connection=runtime)
            for query in (
                sql.SQL('SELECT * FROM {}').format(sql.Identifier(self.schema, 'schema_migrations')),
                sql.SQL('UPDATE {} SET snapshot=snapshot').format(sql.Identifier(self.schema, 'document_versions')),
                sql.SQL('UPDATE {} SET chunk_text=chunk_text').format(sql.Identifier(self.schema, 'document_chunks')),
                sql.SQL('DELETE FROM {}').format(sql.Identifier(self.schema, 'document_chunks')),
                sql.SQL('CREATE TABLE {} (id int)').format(sql.Identifier(self.schema, 'forbidden')),
                sql.SQL('SELECT * FROM {}').format(sql.Identifier(go_schema, 'processing_jobs')),
            ):
                with self.assertRaises(psycopg.errors.InsufficientPrivilege):
                    runtime.execute(query)
            # Column-specific UPDATE rights permit the real embedding adapter.
            from ml.chunking.dto import DocumentChunk
            from ml.embeddings import ChunkEmbedding
            from ml.embeddings.core import text_hash
            from ml.tests.test_embeddings import unit_vector

            saved = replace(document('Data'), document_version_id=str(uuid4()))
            chunk = DocumentChunk(str(uuid4()), saved.document_version_id, 'v1', 0, 'Data', 0, 4, 1)
            item = ChunkEmbedding(chunk.id, text_hash(chunk.chunk_text), unit_vector())
            save_document_version_and_chunks(runtime, saved, (chunk,), schema=self.schema)
            save_document_embeddings(runtime, (chunk,), (item,), schema=self.schema)
            save_document_embeddings(runtime, (chunk,), (item,), schema=self.schema)

    def test_migration_role_owns_its_schema_and_journal(self):
        role = self.new_role('ml_migrator_test_')
        target = 'ml1_owned_' + uuid4().hex
        database = self.connection.execute('SELECT current_database()').fetchone()[0]
        self.connection.execute(sql.SQL('GRANT CREATE ON DATABASE {} TO {}').format(sql.Identifier(database), sql.Identifier(role)))
        extension_schema = self.connection.execute(
            "SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='vector'"
        ).fetchone()[0]
        self.connection.execute(sql.SQL('GRANT USAGE ON SCHEMA {} TO {}').format(sql.Identifier(extension_schema), sql.Identifier(role)))
        with psycopg.connect(self.dsn, autocommit=True) as migration:
            migration.execute(sql.SQL('SET ROLE {}').format(sql.Identifier(role)))
            self.assertEqual(apply_migrations(migration, schema=target), ('000001_document_storage', '000002_pipeline_state_and_events'))
            self.assertEqual(apply_migrations(migration, schema=target), ())
        # DROP OWNED in role cleanup owns/removes only this new isolated schema.
        owner = self.connection.execute('SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname=%s', (target,)).fetchone()[0]
        self.assertEqual(owner, role)
