ALTER TABLE processing_jobs ADD COLUMN source_config_version bigint;

UPDATE processing_jobs AS job
SET source_config_version = source.config_version
FROM article_versions AS version
JOIN sources AS source ON source.id = version.source_id
WHERE job.article_version_id = version.id;

ALTER TABLE processing_jobs
    ALTER COLUMN source_config_version SET NOT NULL,
    ADD CONSTRAINT processing_jobs_source_config_version_positive
        CHECK (source_config_version > 0);
