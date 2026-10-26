-- Independent transport idempotency for synchronous ML2 follow-ups.
CREATE TABLE ml.follow_up_operations (
    operation_id uuid PRIMARY KEY,
    idempotency_key text NOT NULL UNIQUE,
    fingerprint bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
    request_snapshot jsonb NOT NULL CHECK (jsonb_typeof(request_snapshot) = 'object'),
    state text NOT NULL CHECK (state IN ('RUNNING', 'SUCCEEDED', 'FAILED', 'INSUFFICIENT_EVIDENCE')),
    response jsonb CHECK (response IS NULL OR jsonb_typeof(response) = 'object'),
    error jsonb CHECK (error IS NULL OR jsonb_typeof(error) = 'object'),
    created_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    CHECK ((state = 'RUNNING') = (response IS NULL)),
    CHECK ((state = 'FAILED') = (error IS NOT NULL)),
    CHECK ((state = 'RUNNING') = (completed_at IS NULL))
);
