"""Apply independently versioned ML PostgreSQL migrations."""

from __future__ import annotations

import hashlib
import os
import re
from pathlib import Path
from typing import Any, Callable


_MIGRATION_NAME = re.compile(r"^(\d{6})_[a-z0-9_]+\.sql$")
_ADVISORY_LOCK_ID = 0x4D4C4255524D  # stable lock for this ML migration runner


def apply_migrations(
    dsn: str | None = None,
    *,
    migrations_dir: Path | None = None,
    connection_factory: Callable[[], Any] | None = None,
) -> list[str]:
    """Apply unapplied SQL migrations in one locked transaction.

    Use the dedicated migration database role, not the runtime ML_DATABASE_URL
    role. Each SQL file is checksummed in ml.schema_migrations.
    """
    database_url = dsn if dsn is not None else os.environ.get("ML_MIGRATIONS_DATABASE_URL", "")
    if not database_url and connection_factory is None:
        raise ValueError("ML_MIGRATIONS_DATABASE_URL must be configured")
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
                    """CREATE TABLE IF NOT EXISTS ml.schema_migrations (
                           version integer PRIMARY KEY,
                           name text NOT NULL UNIQUE,
                           checksum char(64) NOT NULL,
                           applied_at timestamptz NOT NULL DEFAULT now()
                       )"""
                )
                cursor.execute("SELECT pg_advisory_xact_lock(%s)", (_ADVISORY_LOCK_ID,))
                cursor.execute("SELECT version, name, checksum FROM ml.schema_migrations")
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
                        "INSERT INTO ml.schema_migrations (version, name, checksum) VALUES (%s, %s, %s)",
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


def main() -> int:
    applied = apply_migrations()
    if applied:
        print("Applied ML migrations: " + ", ".join(applied))
    else:
        print("ML schema is up to date")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
