ALTER TABLE processing_jobs ADD COLUMN delivery_version_no integer
    CHECK (delivery_version_no > 0);

UPDATE processing_jobs j SET delivery_version_no = v.version_no
FROM article_versions v
WHERE j.article_version_id = v.id AND j.status = 'QUEUED' AND j.attempts = 0
    AND j.ml_processing_id IS NULL AND j.processing_started_at IS NULL;

CREATE FUNCTION snapshot_ml_version_number() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    SELECT version_no INTO NEW.delivery_version_no
    FROM article_versions WHERE id = NEW.article_version_id;
    RETURN NEW;
END;
$$;

CREATE TRIGGER processing_jobs_version_number_snapshot
    BEFORE INSERT ON processing_jobs FOR EACH ROW EXECUTE FUNCTION snapshot_ml_version_number();
