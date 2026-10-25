CREATE TABLE ml.events (
    id uuid PRIMARY KEY,
    event_type text NOT NULL CHECK (length(btrim(event_type)) > 0),
    title text NOT NULL CHECK (length(btrim(title)) > 0),
    summary text NOT NULL CHECK (length(btrim(summary)) > 0),
    occurred_at timestamptz,
    geography jsonb NOT NULL DEFAULT '[]'::jsonb
        CHECK (jsonb_typeof(geography) = 'array'),
    status text NOT NULL DEFAULT 'candidate'
        CHECK (status IN ('candidate', 'confirmed', 'disputed', 'rejected')),
    confidence numeric CHECK (confidence IS NULL OR confidence BETWEEN 0 AND 1),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ml.event_extraction_runs (
    document_version_id uuid NOT NULL,
    event_pipeline_config_version text NOT NULL
        CHECK (length(btrim(event_pipeline_config_version)) > 0),
    result_fingerprint char(64) NOT NULL
        CHECK (result_fingerprint ~ '^[0-9a-f]{64}$'),
    result_payload jsonb NOT NULL CHECK (jsonb_typeof(result_payload) = 'object'),
    event_ids uuid[] NOT NULL DEFAULT ARRAY[]::uuid[],
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (document_version_id, event_pipeline_config_version)
);

CREATE TABLE ml.event_documents (
    event_id uuid NOT NULL REFERENCES ml.events(id),
    document_version_id uuid NOT NULL,
    event_pipeline_config_version text NOT NULL
        CHECK (length(btrim(event_pipeline_config_version)) > 0),
    candidate_index integer NOT NULL CHECK (candidate_index >= 0),
    candidate_fingerprint char(64) NOT NULL
        CHECK (candidate_fingerprint ~ '^[0-9a-f]{64}$'),
    evidence jsonb NOT NULL
        CHECK (jsonb_typeof(evidence) = 'array' AND jsonb_array_length(evidence) > 0),
    evidence_role text NOT NULL DEFAULT 'support'
        CHECK (evidence_role IN ('support', 'contradict', 'context')),
    is_primary boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, document_version_id, event_pipeline_config_version),
    UNIQUE (document_version_id, event_pipeline_config_version, candidate_index)
);

CREATE INDEX event_documents_version_idx
    ON ml.event_documents (document_version_id, event_pipeline_config_version);

CREATE INDEX events_status_created_idx
    ON ml.events (status, created_at DESC);
