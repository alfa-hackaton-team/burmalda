"""Forward-only transactional ML migrations with their own checksummed journal."""
import hashlib
import argparse
import os
from pathlib import Path
import re

import psycopg
from psycopg import sql
from psycopg.pq import TransactionStatus
from psycopg.rows import tuple_row

from ml.database import ML_SCHEMA

MIGRATIONS = Path(__file__).parent / 'migrations'


class MigrationError(RuntimeError):
    pass


def apply_migrations(connection, *, schema: str = ML_SCHEMA) -> tuple[str, ...]:
    """Apply bundled SQL under a trusted schema. Requires an idle migration-role connection.

    schema override is for isolated test schemas. No down migrations or Go journal
    access. Missing pgvector is a prerequisite error, never auto-installed here.
    """
    if not isinstance(schema, str) or not schema.strip() or schema in ('public', 'pg_catalog', 'information_schema'):
        raise ValueError('Expected a dedicated ML schema')
    if connection.info.transaction_status != TransactionStatus.IDLE:
        raise ValueError('Migrations require an idle connection')
    files = sorted(path for path in MIGRATIONS.iterdir() if re.fullmatch(r'\d{6}_[a-z0-9_]+\.sql', path.name))
    scripts = {path.stem: path.read_bytes() for path in files}
    if not scripts:
        raise MigrationError('No bundled ML migrations found')
    key = int.from_bytes(hashlib.sha256(f'ml-migrations:{schema}'.encode()).digest()[:8], 'big', signed=True)
    journal = sql.Identifier(schema, 'schema_migrations')
    try:
        with connection.transaction():
            with connection.cursor(row_factory=tuple_row) as cursor:
                cursor.execute('SET TRANSACTION ISOLATION LEVEL READ COMMITTED')
                cursor.execute('SELECT pg_advisory_xact_lock(%s)', (key,))
                cursor.execute("SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='vector'")
                extension = cursor.fetchone()
                if extension is None:
                    raise MigrationError('Infrastructure must install pgvector before ML migrations')
                cursor.execute('SHOW server_encoding')
                if cursor.fetchone()[0] != 'UTF8':
                    raise MigrationError('ML storage requires UTF8 server encoding')
                cursor.execute(sql.SQL('CREATE SCHEMA IF NOT EXISTS {}').format(sql.Identifier(schema)))
                cursor.execute(sql.SQL('SET LOCAL search_path TO {}, {}, pg_catalog').format(
                    sql.Identifier(schema), sql.Identifier(extension[0])))
                cursor.execute(sql.SQL(
                    'CREATE TABLE IF NOT EXISTS {} ('
                    'version TEXT PRIMARY KEY, checksum TEXT NOT NULL, '
                    'applied_at TIMESTAMPTZ NOT NULL DEFAULT now())'
                ).format(journal))
                cursor.execute(sql.SQL('SELECT version, checksum FROM {} ORDER BY version').format(journal))
                applied = dict(cursor.fetchall())
                if set(applied) - set(scripts):
                    raise MigrationError('ML journal contains unknown migrations')
                if set(applied) != set(list(scripts)[:len(applied)]):
                    raise MigrationError('ML journal is not an ordered prefix of bundled migrations')
                for name, checksum in applied.items():
                    if hashlib.sha256(scripts[name]).hexdigest() != checksum:
                        raise MigrationError('Applied ML migration checksum differs from bundled SQL')
                completed = []
                for name, content in scripts.items():
                    if name in applied:
                        continue
                    cursor.execute(content.decode('utf-8'))
                    cursor.execute(sql.SQL('INSERT INTO {} (version, checksum) VALUES (%s, %s)').format(journal),
                                   (name, hashlib.sha256(content).hexdigest()))
                    completed.append(name)
        return tuple(completed)
    except psycopg.DatabaseError:
        raise MigrationError('PostgreSQL ML migration failed; transaction rolled back') from None


def grant_runtime_access(connection, role: str, *, schema: str = ML_SCHEMA):
    """Schema owner grants a pre-provisioned runtime role only ML data rights.

    Infrastructure creates the login roles/credentials separately. No Go grants
    are modified. Call again when provisioning a fresh runtime role.
    """
    if not isinstance(role, str) or not role.strip():
        raise ValueError('Expected a runtime role name')
    if not isinstance(schema, str) or not schema.strip() or schema == 'public':
        raise ValueError('Expected a dedicated ML schema')
    if connection.info.transaction_status != TransactionStatus.IDLE:
        raise ValueError('Permission setup requires an idle connection')
    runtime = sql.Identifier(role)
    versions = sql.Identifier(schema, 'document_versions')
    chunks = sql.Identifier(schema, 'document_chunks')
    journal = sql.Identifier(schema, 'schema_migrations')
    try:
        with connection.transaction():
            connection.execute(sql.SQL('REVOKE ALL ON SCHEMA {} FROM {}').format(sql.Identifier(schema), runtime))
            connection.execute(sql.SQL('GRANT USAGE ON SCHEMA {} TO {}').format(sql.Identifier(schema), runtime))
            for table in (versions, chunks, journal):
                connection.execute(sql.SQL('REVOKE ALL ON TABLE {} FROM {}').format(table, runtime))
            connection.execute(sql.SQL('GRANT SELECT, INSERT ON {}, {} TO {}').format(versions, chunks, runtime))
            connection.execute(sql.SQL(
                'GRANT UPDATE (embedding, embedding_model, embedding_version) ON {} TO {}'
            ).format(chunks, runtime))
            catalog = sql.Identifier(schema, 'document_version_catalog')
            operations = sql.Identifier(schema, 'article_processings')
            for name in ('document_version_catalog', 'article_processings', 'events',
                         'event_documents', 'event_facts', 'fact_evidence'):
                table = sql.Identifier(schema, name)
                connection.execute(sql.SQL('REVOKE ALL ON TABLE {} FROM {}').format(table, runtime))
                connection.execute(sql.SQL('GRANT SELECT, INSERT ON {} TO {}').format(table, runtime))
                if name in ('events', 'event_documents', 'event_facts', 'fact_evidence'):
                    connection.execute(sql.SQL('GRANT UPDATE ON {} TO {}').format(table, runtime))
            connection.execute(sql.SQL('GRANT UPDATE (ready, indexed_at) ON {} TO {}').format(catalog, runtime))
            connection.execute(sql.SQL(
                'GRANT UPDATE (state, document_version_id, result, error, updated_at) ON {} TO {}'
            ).format(operations, runtime))
    except psycopg.DatabaseError:
        raise MigrationError('ML runtime permission setup failed') from None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--runtime-role', help='Grant ML data privileges to this pre-created role')
    args = parser.parse_args()
    dsn = os.environ.get('ML_MIGRATION_DATABASE_URL')
    if not dsn:
        raise SystemExit('ML_MIGRATION_DATABASE_URL is required; runtime DSN is not used for migrations')
    try:
        with psycopg.connect(dsn, autocommit=True) as connection:
            completed = apply_migrations(connection)
            if args.runtime_role:
                grant_runtime_access(connection, args.runtime_role)
    except (psycopg.Error, MigrationError) as error:
        # Never print PostgreSQL diagnostics, DSN or connection credentials.
        message = str(error) if isinstance(error, MigrationError) else 'Cannot connect using ML migration configuration'
        raise SystemExit(message) from None
    print('ML migrations: ' + (', '.join(completed) if completed else 'up to date'))


if __name__ == '__main__':
    main()
