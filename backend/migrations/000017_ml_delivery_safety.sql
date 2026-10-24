ALTER TABLE article_versions
    ADD COLUMN text_origin text NOT NULL DEFAULT 'unknown'
        CHECK (text_origin IN ('unknown','full_article','rss_summary','pdf_extracted')),
    ADD COLUMN extraction_method text NOT NULL DEFAULT 'unknown',
    ADD COLUMN extraction_issues jsonb NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(extraction_issues) = 'array');

CREATE TABLE article_version_source_snapshots (
    article_version_id uuid PRIMARY KEY REFERENCES article_versions (id) ON DELETE CASCADE,
    source_name text NOT NULL,
    source_type text NOT NULL,
    source_role text NOT NULL CHECK (source_role IN ('external','bank','expert')),
    source_trust_level text NOT NULL,
    external_id text,
    canonical_url text,
    source_config_version bigint NOT NULL CHECK (source_config_version > 0),
    capture_origin text NOT NULL DEFAULT 'CAPTURED_AT_INGEST'
        CHECK (capture_origin IN ('CAPTURED_AT_INGEST','CAPTURED_AT_ENQUEUE'))
);

ALTER TABLE processing_jobs ADD COLUMN source_snapshot_origin text NOT NULL DEFAULT 'CAPTURED_AT_INGEST'
    CHECK (source_snapshot_origin IN ('CAPTURED_AT_INGEST','CAPTURED_AT_ENQUEUE','UNVERIFIED_LEGACY'));

UPDATE processing_jobs SET source_snapshot_origin='UNVERIFIED_LEGACY',
    last_error=CASE WHEN status NOT IN ('SUCCEEDED','FAILED') THEN
        'Historical source configuration was reconstructed by migration 000009; manual verification required'
        ELSE last_error END,
    last_error_code=CASE WHEN status NOT IN ('SUCCEEDED','FAILED') THEN
        'SOURCE_SNAPSHOT_UNVERIFIED' ELSE last_error_code END,
    last_error_retryable=CASE WHEN status NOT IN ('SUCCEEDED','FAILED') THEN false ELSE last_error_retryable END
WHERE created_at <= (SELECT applied_at FROM schema_migrations WHERE version=9);
