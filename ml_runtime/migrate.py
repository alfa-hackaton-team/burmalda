"""Apply independently versioned ML PostgreSQL migrations."""

from __future__ import annotations

import hashlib
import os
import re
from pathlib import Path
from typing import Any, Callable


_MIGRATION_NAME = re.compile(r"^(\d{6})_[a-z0-9_]+\.sql$")
_ADVISORY_LOCK_ID = 0x4D4C32425552  # independent from ML1's migration runner
_JOURNAL = "ml.ml2_schema_migrations"


def apply_migrations(
    dsn: str | None = None,
    *,
    migrations_dir: Path | None = None,
    connection_factory: Callable[[], Any] | None = None,
) -> list[str]:
    """Apply unapplied SQL migrations in one locked transaction.

    Apply ML1 migrations first. Use the dedicated migration database role, not
    ML_DATABASE_URL. ML2 uses its own journal because ML1 owns ml.schema_migrations.
    """
    database_url = dsn if dsn is not None else _migration_database_url()
    if not database_url and connection_factory is None:
        raise ValueError("ML_MIGRATION_DATABASE_URL must be configured")
    directory = migrations_dir or Path(__file__).with_name("migrations")
    files = sorted(directory.glob("*.sql"))
    migrations: list[tuple[int, str, str, str]] = []
    for path in files:
        match = _MIGRATION_NAME.fullmatch(path.name)
        if match is None:
            raise ValueError(f"Invalid ML migration filename: {path.name}")
        raw = path.read_bytes()
        migrations.append((int(match.group(1)), path.name, raw.decode("utf-8"), hashlib.sha256(raw).hexdigest()))
    if len({version for version, *_ in migrations}) != len(migrations):
        raise ValueError("ML migration versions must be unique")

    connection = connection_factory() if connection_factory is not None else _connect(database_url)
    applied_now: list[str] = []
    try:
        with connection:
            with connection.cursor() as cursor:
                cursor.execute("CREATE SCHEMA IF NOT EXISTS ml")
                cursor.execute(
                    f"""CREATE TABLE IF NOT EXISTS {_JOURNAL} (
                           version integer PRIMARY KEY,
                           name text NOT NULL UNIQUE,
                           checksum char(64) NOT NULL,
                           applied_at timestamptz NOT NULL DEFAULT now()
                       )"""
                )
                cursor.execute("SELECT pg_advisory_xact_lock(%s)", (_ADVISORY_LOCK_ID,))
                cursor.execute(f"SELECT version, name, checksum FROM {_JOURNAL}")
                existing = {row[0]: (row[1], row[2].strip()) for row in cursor.fetchall()}
                for version, name, sql, checksum in migrations:
                    prior = existing.get(version)
                    if prior is not None:
                        if prior != (name, checksum):
                            raise RuntimeError(f"Applied ML migration {version:06d} differs from its recorded checksum")
                        continue
                    for statement in _split_sql(sql):
                        cursor.execute(statement)
                    cursor.execute(
                        f"INSERT INTO {_JOURNAL} (version, name, checksum) VALUES (%s, %s, %s)",
                        (version, name, checksum),
                    )
                    applied_now.append(name)
    finally:
        connection.close()
    return applied_now


def _split_sql(script: str) -> list[str]:
    # Current migrations contain only DDL statements, not procedural blocks.
    return [statement.strip() for statement in script.split(";") if statement.strip()]


def _connect(dsn: str):
    try:
        import psycopg
    except ImportError as exc:  # pragma: no cover - depends on deployment extras
        raise RuntimeError("Install the psycopg runtime dependency to run ML migrations") from exc
    return psycopg.connect(dsn)


def grant_runtime_access(connection_factory: Callable[[], Any], role: str) -> None:
    """Add ML2 checkpoint-column rights to a role already provisioned by ML1."""
    if not isinstance(role, str) or not role.strip():
        raise ValueError("ML runtime database role must be non-empty")
    try:
        from psycopg import sql
    except ImportError as exc:  # pragma: no cover - depends on deployment extras
        raise RuntimeError("Install psycopg to grant ML runtime privileges") from exc
    connection = connection_factory()
    try:
        with connection:
            with connection.cursor() as cursor:
                cursor.execute(
                    sql.SQL("GRANT UPDATE (event_ids, event_pipeline_config_version, quality_result) "
                            "ON ml.article_processings TO {}")
                    .format(sql.Identifier(role))
                )
                cursor.execute(sql.SQL(
                    "GRANT SELECT, INSERT, UPDATE ON ml.follow_up_operations TO {}"
                ).format(sql.Identifier(role)))
                cursor.execute(sql.SQL('GRANT SELECT, INSERT, UPDATE ON ml.analysis_operations TO {}')
                               .format(sql.Identifier(role)))
                cursor.execute(sql.SQL('GRANT SELECT, INSERT, DELETE ON ml.ranking_snapshots TO {}')
                               .format(sql.Identifier(role)))
    finally:
        connection.close()


def main() -> int:
    import argparse

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runtime-role", help="Grant ML2 checkpoint updates to an existing runtime role")
    parser.add_argument('--reader-role', help='Grant a backend role read-only event and evidence access')
    args = parser.parse_args()
    applied = apply_migrations()
    if args.runtime_role:
        database_url = _migration_database_url()
        grant_runtime_access(lambda: _connect(database_url), args.runtime_role)
    if args.reader_role:
        from psycopg import sql
        with _connect(_migration_database_url()) as connection:
            connection.execute(sql.SQL('GRANT USAGE ON SCHEMA ml TO {}').format(sql.Identifier(args.reader_role)))
            connection.execute(sql.SQL('GRANT SELECT ON ml.events, ml.event_documents, '
                'ml.document_versions, ml.document_chunks, ml.document_version_catalog TO {}')
                .format(sql.Identifier(args.reader_role)))
    if applied:
        print("Applied ML migrations: " + ", ".join(applied))
    else:
        print("ML schema is up to date")
    return 0


def _migration_database_url() -> str:
    """Use ML1's established singular variable; retain the earlier ML2 alias."""
    return os.environ.get("ML_MIGRATION_DATABASE_URL", os.environ.get("ML_MIGRATIONS_DATABASE_URL", ""))


if __name__ == "__main__":
    raise SystemExit(main())
