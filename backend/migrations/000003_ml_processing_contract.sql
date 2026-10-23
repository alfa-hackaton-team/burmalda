ALTER TABLE article_versions
    ADD COLUMN language text,
    ADD COLUMN author text,
    ADD COLUMN metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD CONSTRAINT article_versions_metadata_object CHECK (jsonb_typeof(metadata) = 'object'),
    ADD CONSTRAINT article_versions_body_nonblank CHECK (length(btrim(body_text)) > 0);

ALTER TABLE processing_jobs
    ADD COLUMN source_name text,
    ADD COLUMN source_type text,
    ADD COLUMN source_role text,
    ADD COLUMN source_trust_level text;

UPDATE processing_jobs AS job
SET source_name = source.name,
    source_type = source.source_type,
    source_role = source.source_role,
    source_trust_level = source.trust_level
FROM article_versions AS version
JOIN sources AS source ON source.id = version.source_id
WHERE job.article_version_id = version.id;

ALTER TABLE processing_jobs
    ALTER COLUMN source_name SET NOT NULL,
    ALTER COLUMN source_type SET NOT NULL,
    ALTER COLUMN source_role SET NOT NULL,
    ALTER COLUMN source_trust_level SET NOT NULL,
    ADD CONSTRAINT processing_jobs_source_fields_nonblank
        CHECK (length(btrim(source_name)) > 0
            AND length(btrim(source_type)) > 0
            AND source_role IN ('external', 'bank', 'expert')
            AND length(btrim(source_trust_level)) > 0),
    ADD CONSTRAINT processing_jobs_success_document
        CHECK (status <> 'SUCCEEDED' OR nullif(btrim(document_version_id), '') IS NOT NULL);
