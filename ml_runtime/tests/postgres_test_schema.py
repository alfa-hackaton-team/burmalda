"""Minimal disposable ML1 schema for ML2 PostgreSQL integration tests."""

from __future__ import annotations

from uuid import UUID

import psycopg

from ml_runtime.migrate import apply_migrations


def prepare_ml_test_schema(dsn: str) -> None:
    """Create the ML1-owned base tables used by ML2, then apply ML2 extensions.

    The definitions intentionally match PR #24's operation and event table
    columns. Tests do not need pgvector because the indexer is faked here.
    Use a disposable database; this helper creates objects in schema ``ml``.
    """
    with psycopg.connect(dsn) as connection:
        with connection.cursor() as cursor:
            cursor.execute("CREATE SCHEMA IF NOT EXISTS ml")
            cursor.execute(
                """CREATE TABLE IF NOT EXISTS ml.document_versions (
                       id uuid PRIMARY KEY,
                       document_id uuid NOT NULL,
                       snapshot jsonb NOT NULL,
                       created_at timestamptz NOT NULL DEFAULT now(),
                       CHECK (jsonb_typeof(snapshot) = 'object'),
                       CHECK (NOT (snapshot ? 'trace_id') AND NOT (snapshot ? 'pipeline_version'))
                   )"""
            )
            cursor.execute(
                """CREATE TABLE IF NOT EXISTS ml.article_processings (
                       processing_id uuid PRIMARY KEY,
                       article_version_id uuid NOT NULL,
                       pipeline_version text NOT NULL,
                       idempotency_key text NOT NULL UNIQUE,
                       request_fingerprint bytea NOT NULL CHECK (octet_length(request_fingerprint) = 32),
                       request_snapshot jsonb NOT NULL CHECK (jsonb_typeof(request_snapshot) = 'object'),
                       trace_id text NOT NULL,
                       state text NOT NULL DEFAULT 'QUEUED'
                           CHECK (state IN ('QUEUED', 'RUNNING', 'SUCCEEDED', 'FAILED')),
                       document_version_id uuid REFERENCES ml.document_versions(id),
                       result jsonb CHECK (result IS NULL OR jsonb_typeof(result) = 'object'),
                       error jsonb CHECK (error IS NULL OR jsonb_typeof(error) = 'object'),
                       created_at timestamptz NOT NULL DEFAULT now(),
                       updated_at timestamptz NOT NULL DEFAULT now(),
                       UNIQUE (article_version_id, pipeline_version),
                       CHECK ((state = 'FAILED') = (error IS NOT NULL)),
                       CHECK ((state = 'SUCCEEDED') = (result IS NOT NULL)),
                       CHECK (state <> 'SUCCEEDED' OR document_version_id IS NOT NULL)
                   )"""
            )
            cursor.execute(
                """CREATE TABLE IF NOT EXISTS ml.events (
                       id uuid PRIMARY KEY,
                       event_type text NOT NULL CHECK (length(btrim(event_type)) > 0),
                       title text NOT NULL CHECK (length(btrim(title)) > 0),
                       summary text NOT NULL,
                       occurred_at timestamptz,
                       geography jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(geography) = 'array'),
                       status text NOT NULL DEFAULT 'candidate'
                           CHECK (status IN ('candidate', 'confirmed', 'disputed', 'rejected')),
                       confidence numeric CHECK (confidence BETWEEN 0 AND 1),
                       created_at timestamptz NOT NULL DEFAULT now()
                   )"""
            )
            cursor.execute(
                """CREATE TABLE IF NOT EXISTS ml.event_documents (
                       event_id uuid NOT NULL REFERENCES ml.events(id) ON DELETE RESTRICT,
                       document_version_id uuid NOT NULL REFERENCES ml.document_versions(id) ON DELETE RESTRICT,
                       evidence_role text NOT NULL DEFAULT 'support'
                           CHECK (evidence_role IN ('support', 'contradict', 'context')),
                       is_primary boolean NOT NULL DEFAULT false,
                       evidence_quotes jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(evidence_quotes) = 'array'),
                       candidate_fingerprint text,
                       pipeline_version text NOT NULL,
                       event_pipeline_config_version text NOT NULL,
                       PRIMARY KEY (event_id, document_version_id, event_pipeline_config_version),
                       UNIQUE (document_version_id, event_pipeline_config_version, candidate_fingerprint)
                   )"""
            )
    apply_migrations(dsn)


def seed_document_version(dsn: str, document_version_id: str, document_id: str) -> None:
    with psycopg.connect(dsn) as connection:
        with connection.cursor() as cursor:
            cursor.execute(
                """INSERT INTO ml.document_versions (id, document_id, snapshot)
                   VALUES (%s, %s, '{}'::jsonb) ON CONFLICT (id) DO NOTHING""",
                (UUID(document_version_id), UUID(document_id)),
            )


def cleanup_test_versions(dsn: str, document_version_ids: tuple[str, ...]) -> None:
    with psycopg.connect(dsn) as connection:
        with connection.cursor() as cursor:
            cursor.execute(
                "DELETE FROM ml.event_documents WHERE document_version_id = ANY(%s::uuid[])",
                ([UUID(value) for value in document_version_ids],),
            )
            cursor.execute(
                """DELETE FROM ml.events e WHERE NOT EXISTS (
                       SELECT 1 FROM ml.event_documents d WHERE d.event_id = e.id
                   )"""
            )
            cursor.execute(
                "DELETE FROM ml.article_processings WHERE article_version_id = ANY(%s::uuid[])",
                ([UUID(value) for value in document_version_ids],),
            )
            cursor.execute(
                "DELETE FROM ml.document_versions WHERE id = ANY(%s::uuid[])",
                ([UUID(value) for value in document_version_ids],),
            )
