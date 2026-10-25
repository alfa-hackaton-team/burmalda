ALTER TABLE processing_jobs
    ADD COLUMN external_id text,
    ADD COLUMN canonical_url text,
    ADD COLUMN event_ids jsonb,
    ADD CONSTRAINT processing_jobs_event_ids_array CHECK (
        event_ids IS NULL OR jsonb_typeof(event_ids) = 'array'),
    ADD CONSTRAINT processing_jobs_success_events CHECK (
        status <> 'SUCCEEDED' OR event_ids IS NOT NULL) NOT VALID;

UPDATE processing_jobs AS job
SET external_id = article.external_id,
    canonical_url = article.canonical_url
FROM article_versions AS version
JOIN articles AS article ON article.id = version.article_id
WHERE job.article_version_id = version.id;
