-- ML1 owns article_processings, events and event_documents. ML2 only adds
-- byte-exact request recovery and event-result checkpoints to those tables.
ALTER TABLE ml.article_processings
    ADD COLUMN request_payload bytea,
    ADD COLUMN event_ids jsonb,
    ADD COLUMN quality_result jsonb,
    ADD COLUMN event_pipeline_config_version text;

UPDATE ml.article_processings
SET request_payload = convert_to(request_snapshot::text, 'UTF8')
WHERE request_payload IS NULL;

ALTER TABLE ml.article_processings
    ALTER COLUMN request_payload SET NOT NULL,
    ADD CONSTRAINT article_processings_event_ids_array_check
        CHECK (event_ids IS NULL OR jsonb_typeof(event_ids) = 'array'),
    ADD CONSTRAINT article_processings_quality_result_object_check
        CHECK (quality_result IS NULL OR jsonb_typeof(quality_result) = 'object'),
    ADD CONSTRAINT article_processings_event_config_nonempty_check
        CHECK (event_pipeline_config_version IS NULL OR length(btrim(event_pipeline_config_version)) > 0);
