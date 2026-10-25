CREATE TABLE ml.events (
    event_id uuid PRIMARY KEY,
    event_type text NOT NULL CHECK (length(btrim(event_type)) > 0),
    title text NOT NULL CHECK (length(btrim(title)) > 0),
    summary text NOT NULL CHECK (length(btrim(summary)) > 0),
    occurred_at timestamptz,
    geography jsonb NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(geography) = 'array'),
    status text NOT NULL DEFAULT 'candidate'
        CHECK (status IN ('candidate', 'confirmed', 'disputed', 'rejected')),
    confidence numeric CHECK (confidence IS NULL OR confidence BETWEEN 0 AND 1),
    dedupe_key char(64) NOT NULL UNIQUE
        CHECK (dedupe_key ~ '^[0-9a-f]{64}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ml.event_documents (
    event_id uuid NOT NULL REFERENCES ml.events(event_id),
    document_version_id uuid NOT NULL,
    pipeline_version text NOT NULL CHECK (length(btrim(pipeline_version)) > 0),
    candidate_index integer NOT NULL CHECK (candidate_index >= 0),
    candidate_fingerprint char(64) NOT NULL
        CHECK (candidate_fingerprint ~ '^[0-9a-f]{64}$'),
    evidence_quotes jsonb NOT NULL
        CHECK (jsonb_typeof(evidence_quotes) = 'array' AND jsonb_array_length(evidence_quotes) > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, document_version_id, pipeline_version),
    UNIQUE (document_version_id, pipeline_version, candidate_index)
);

CREATE INDEX event_documents_version_idx
    ON ml.event_documents (document_version_id, pipeline_version);

CREATE INDEX events_status_created_idx
    ON ml.events (status, created_at DESC);
