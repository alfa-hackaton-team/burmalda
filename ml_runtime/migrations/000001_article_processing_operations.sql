CREATE TABLE ml.article_processing_operations (
    processing_id uuid PRIMARY KEY,
    idempotency_key text NOT NULL UNIQUE,
    request_fingerprint char(64) NOT NULL
        CHECK (request_fingerprint ~ '^[0-9a-f]{64}$'),
    article_version_id uuid NOT NULL,
    pipeline_version text NOT NULL CHECK (length(btrim(pipeline_version)) > 0),
    trace_id text NOT NULL CHECK (length(btrim(trace_id)) > 0),
    -- ASCII-escaped JSON bytes preserve NUL/replacement/surrogate text so R02
    -- can return the specified quality FAIL instead of losing it at storage.
    request_payload bytea NOT NULL,
    state text NOT NULL DEFAULT 'QUEUED'
        CHECK (state IN ('QUEUED', 'RUNNING', 'SUCCEEDED', 'FAILED')),
    attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    document_version_id uuid,
    event_ids jsonb,
    quality_result jsonb,
    error jsonb,
    CHECK (event_ids IS NULL OR jsonb_typeof(event_ids) = 'array'),
    CHECK (quality_result IS NULL OR jsonb_typeof(quality_result) = 'object'),
    CHECK (error IS NULL OR jsonb_typeof(error) = 'object'),
    CHECK (
        (state = 'SUCCEEDED' AND document_version_id IS NOT NULL AND event_ids IS NOT NULL AND error IS NULL)
        OR (state = 'FAILED' AND error IS NOT NULL)
        OR (state IN ('QUEUED', 'RUNNING') AND error IS NULL)
    ),
    CHECK (state <> 'SUCCEEDED' OR document_version_id = article_version_id)
);

CREATE INDEX article_processing_operations_state_created_idx
    ON ml.article_processing_operations (state, created_at)
    WHERE state IN ('QUEUED', 'RUNNING');

CREATE INDEX article_processing_operations_article_pipeline_idx
    ON ml.article_processing_operations (article_version_id, pipeline_version);
