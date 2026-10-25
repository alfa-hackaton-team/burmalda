-- ML-owned state only. No FK/grants into Go schemas.
-- Pending metadata may precede a quality-approved document snapshot.
CREATE TABLE document_version_catalog (
    document_version_id UUID PRIMARY KEY,
    document_id UUID NOT NULL,
    version_no INTEGER NOT NULL CHECK (version_no > 0),
    ready BOOLEAN NOT NULL DEFAULT false,
    indexed_at TIMESTAMPTZ,
    UNIQUE (document_id, version_no),
    CHECK (ready = (indexed_at IS NOT NULL))
);

CREATE TABLE article_processings (
    processing_id UUID PRIMARY KEY,
    article_version_id UUID NOT NULL,
    pipeline_version TEXT NOT NULL CHECK (length(btrim(pipeline_version)) > 0),
    idempotency_key TEXT NOT NULL UNIQUE CHECK (length(btrim(idempotency_key)) > 0),
    request_fingerprint BYTEA NOT NULL CHECK (octet_length(request_fingerprint) = 32),
    request_snapshot JSONB NOT NULL CHECK (jsonb_typeof(request_snapshot) = 'object'),
    trace_id TEXT NOT NULL CHECK (length(btrim(trace_id)) > 0),
    state TEXT NOT NULL DEFAULT 'QUEUED' CHECK (state IN ('QUEUED','RUNNING','SUCCEEDED','FAILED')),
    document_version_id UUID REFERENCES document_versions(id) ON DELETE RESTRICT,
    result JSONB CHECK (jsonb_typeof(result) = 'object'),
    error JSONB CHECK (jsonb_typeof(error) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (article_version_id, pipeline_version),
    CHECK (document_version_id IS NULL OR document_version_id = article_version_id),
    CHECK ((state = 'FAILED') = (error IS NOT NULL)),
    CHECK ((state = 'SUCCEEDED') = (result IS NOT NULL)),
    CHECK (state <> 'SUCCEEDED' OR document_version_id IS NOT NULL)
);
CREATE INDEX article_processings_pending_idx ON article_processings(state, created_at)
    WHERE state IN ('QUEUED','RUNNING');

CREATE TABLE events (
    id UUID PRIMARY KEY,
    event_type TEXT NOT NULL CHECK (length(btrim(event_type)) > 0),
    title TEXT NOT NULL CHECK (length(btrim(title)) > 0),
    summary TEXT NOT NULL,
    occurred_at TIMESTAMPTZ,
    geography JSONB NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(geography) = 'array'),
    status TEXT NOT NULL DEFAULT 'candidate' CHECK (status IN ('candidate','confirmed','disputed','rejected')),
    confidence NUMERIC CHECK (confidence BETWEEN 0 AND 1),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE event_documents (
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE RESTRICT,
    document_version_id UUID NOT NULL REFERENCES document_versions(id) ON DELETE RESTRICT,
    evidence_role TEXT NOT NULL DEFAULT 'support' CHECK (evidence_role IN ('support','contradict','context')),
    is_primary BOOLEAN NOT NULL DEFAULT false,
    evidence_quotes JSONB NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(evidence_quotes) = 'array'),
    candidate_fingerprint TEXT CHECK (length(btrim(candidate_fingerprint)) > 0),
    pipeline_version TEXT NOT NULL CHECK (length(btrim(pipeline_version)) > 0),
    event_pipeline_config_version TEXT NOT NULL CHECK (length(btrim(event_pipeline_config_version)) > 0),
    PRIMARY KEY (event_id, document_version_id, event_pipeline_config_version),
    UNIQUE (document_version_id, event_pipeline_config_version, candidate_fingerprint)
);
CREATE INDEX event_documents_version_idx ON event_documents(document_version_id);

CREATE TABLE event_facts (
    id UUID PRIMARY KEY,
    event_id UUID NOT NULL REFERENCES events(id) ON DELETE RESTRICT,
    subject TEXT NOT NULL,
    predicate TEXT NOT NULL,
    object_value TEXT NOT NULL,
    verification_status TEXT NOT NULL DEFAULT 'candidate'
        CHECK (verification_status IN ('candidate','confirmed','disputed','rejected')),
    confidence NUMERIC CHECK (confidence BETWEEN 0 AND 1)
);
CREATE INDEX event_facts_event_idx ON event_facts(event_id);

CREATE TABLE fact_evidence (
    fact_id UUID NOT NULL REFERENCES event_facts(id) ON DELETE RESTRICT,
    chunk_id UUID NOT NULL REFERENCES document_chunks(id) ON DELETE RESTRICT,
    quote_text TEXT NOT NULL CHECK (length(btrim(quote_text)) > 0),
    evidence_role TEXT NOT NULL DEFAULT 'support' CHECK (evidence_role IN ('support','contradict','context')),
    PRIMARY KEY (fact_id, chunk_id)
);
CREATE INDEX fact_evidence_chunk_idx ON fact_evidence(chunk_id);
