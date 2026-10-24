CREATE UNIQUE INDEX articles_source_external_id_unique
    ON articles (source_id, external_id) WHERE external_id IS NOT NULL;

CREATE UNIQUE INDEX articles_source_canonical_url_unique
    ON articles (source_id, canonical_url) WHERE canonical_url IS NOT NULL;

ALTER TABLE processing_jobs
    ADD COLUMN processing_started_at timestamptz,
    ADD COLUMN last_error_code text,
    ADD COLUMN last_error_retryable boolean;
