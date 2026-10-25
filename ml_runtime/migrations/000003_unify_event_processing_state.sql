ALTER TABLE ml.article_processing_operations
    ADD COLUMN event_pipeline_config_version text;

CREATE UNIQUE INDEX article_processing_operations_article_pipeline_unique_idx
    ON ml.article_processing_operations (article_version_id, pipeline_version);

DROP TABLE ml.event_extraction_runs;
