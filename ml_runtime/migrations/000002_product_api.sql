CREATE TABLE ml.analysis_operations (
    analysis_id uuid PRIMARY KEY,
    request_operation_id uuid NOT NULL UNIQUE,
    fingerprint bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
    request jsonb NOT NULL,
    state text NOT NULL DEFAULT 'QUEUED'
        CHECK (state IN ('QUEUED', 'RUNNING', 'SUCCEEDED', 'INSUFFICIENT_EVIDENCE', 'FAILED')),
    response jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    CHECK ((state IN ('SUCCEEDED', 'INSUFFICIENT_EVIDENCE', 'FAILED')) = (response IS NOT NULL))
);
CREATE INDEX analysis_operations_queue ON ml.analysis_operations (created_at) WHERE state = 'QUEUED';
CREATE TABLE ml.ranking_snapshots (
    id uuid PRIMARY KEY,
    fingerprint text NOT NULL,
    items jsonb NOT NULL,
    expires_at timestamptz NOT NULL DEFAULT now() + interval '15 minutes'
);
