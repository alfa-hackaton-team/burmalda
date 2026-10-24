ALTER TABLE processing_jobs ADD COLUMN delivery_extraction jsonb
    CHECK (delivery_extraction IS NULL OR jsonb_typeof(delivery_extraction) = 'object');

UPDATE processing_jobs j SET delivery_extraction = jsonb_build_object(
    'text_origin', v.text_origin, 'extraction_issues', v.extraction_issues)
FROM article_versions v
WHERE j.article_version_id = v.id AND j.status = 'QUEUED' AND j.attempts = 0
    AND j.ml_processing_id IS NULL AND j.processing_started_at IS NULL;

CREATE FUNCTION snapshot_ml_extraction() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    SELECT jsonb_build_object('text_origin', text_origin, 'extraction_issues', extraction_issues)
    INTO NEW.delivery_extraction FROM article_versions WHERE id = NEW.article_version_id;
    RETURN NEW;
END;
$$;

CREATE TRIGGER processing_jobs_extraction_snapshot
    BEFORE INSERT ON processing_jobs FOR EACH ROW EXECUTE FUNCTION snapshot_ml_extraction();
