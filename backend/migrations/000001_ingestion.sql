CREATE TABLE sources (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL CHECK (length(btrim(name)) > 0),
    source_type text NOT NULL CHECK (length(btrim(source_type)) > 0),
    source_role text NOT NULL CHECK (source_role IN ('external', 'bank', 'expert')),
    trust_level text NOT NULL CHECK (length(btrim(trust_level)) > 0),
    adapter_type text NOT NULL CHECK (length(btrim(adapter_type)) > 0),
    endpoint text NOT NULL CHECK (length(btrim(endpoint)) > 0),
    credential_ref text,
    adapter_config jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(adapter_config) = 'object'),
    enabled boolean NOT NULL DEFAULT true,
    polling_interval_seconds integer NOT NULL CHECK (polling_interval_seconds > 0),
    next_poll_at timestamptz NOT NULL DEFAULT now(),
    config_version bigint NOT NULL DEFAULT 1 CHECK (config_version > 0),
    health text NOT NULL DEFAULT 'UNKNOWN'
        CHECK (health IN ('UNKNOWN', 'HEALTHY', 'DEGRADED', 'UNAVAILABLE')),
    last_sync_at timestamptz,
    last_success_at timestamptz,
    last_error text,
    http_etag text,
    http_last_modified text,
    source_cursor text,
    lease_until timestamptz,
    lease_token uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT sources_lease_pair CHECK ((lease_until IS NULL) = (lease_token IS NULL))
);

CREATE INDEX sources_due_idx ON sources (next_poll_at, id) WHERE enabled;

CREATE TABLE source_polls (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_id uuid NOT NULL REFERENCES sources (id) ON DELETE RESTRICT,
    status text NOT NULL DEFAULT 'QUEUED'
        CHECK (status IN ('QUEUED', 'RUNNING', 'SUCCEEDED', 'FAILED')),
    trace_id text,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    finished_at timestamptz,
    http_status integer CHECK (http_status BETWEEN 100 AND 599),
    discovered_count integer NOT NULL DEFAULT 0 CHECK (discovered_count >= 0),
    new_article_count integer NOT NULL DEFAULT 0 CHECK (new_article_count >= 0),
    new_version_count integer NOT NULL DEFAULT 0 CHECK (new_version_count >= 0),
    error_message text,
    cursor_before text,
    cursor_after text,
    CONSTRAINT source_polls_times CHECK (
        (status = 'QUEUED' AND started_at IS NULL AND finished_at IS NULL)
        OR (status = 'RUNNING' AND started_at IS NOT NULL AND finished_at IS NULL)
        OR (status IN ('SUCCEEDED', 'FAILED') AND started_at IS NOT NULL AND finished_at IS NOT NULL
            AND finished_at >= started_at)
    )
);

CREATE INDEX source_polls_source_created_idx ON source_polls (source_id, created_at DESC);

-- Repeated parses can refer to the same original without copying its bytes.
-- Exactly one storage location is present. S3 or another object store can be used later.
CREATE TABLE raw_artifacts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_id uuid NOT NULL REFERENCES sources (id) ON DELETE RESTRICT,
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    content_type text NOT NULL CHECK (length(btrim(content_type)) > 0),
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    content bytea,
    object_uri text,
    first_fetched_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT raw_artifacts_storage CHECK (
        (content IS NOT NULL) <> (object_uri IS NOT NULL)
        AND (object_uri IS NULL OR length(btrim(object_uri)) > 0)
        AND (content IS NULL OR size_bytes = octet_length(content))
    ),
    CONSTRAINT raw_artifacts_source_sha_unique UNIQUE (source_id, sha256),
    CONSTRAINT raw_artifacts_source_id_unique UNIQUE (source_id, id)
);

CREATE TABLE articles (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_id uuid NOT NULL REFERENCES sources (id) ON DELETE RESTRICT,
    identity_kind text NOT NULL
        CHECK (identity_kind IN ('external_id', 'canonical_url', 'fingerprint')),
    identity_key text NOT NULL CHECK (length(btrim(identity_key)) > 0),
    external_id text,
    canonical_url text,
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    latest_version_id uuid,
    CONSTRAINT articles_identity_fields CHECK (
        (external_id IS NULL OR length(btrim(external_id)) > 0)
        AND (canonical_url IS NULL OR length(btrim(canonical_url)) > 0)
        AND (identity_kind <> 'external_id' OR identity_key = external_id)
        AND (identity_kind <> 'canonical_url' OR identity_key = canonical_url)
    ),
    CONSTRAINT articles_source_identity_unique UNIQUE (source_id, identity_kind, identity_key),
    CONSTRAINT articles_source_id_unique UNIQUE (source_id, id)
);

CREATE INDEX articles_source_seen_idx ON articles (source_id, first_seen_at DESC);

CREATE TABLE article_versions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_id uuid NOT NULL,
    article_id uuid NOT NULL,
    version_no integer NOT NULL CHECK (version_no > 0),
    content_hash text NOT NULL CHECK (content_hash ~ '^[0-9a-f]{64}$'),
    parser_version text NOT NULL CHECK (length(btrim(parser_version)) > 0),
    title text NOT NULL,
    body_text text NOT NULL,
    published_at timestamptz,
    fetched_at timestamptz NOT NULL,
    raw_artifact_id uuid NOT NULL,
    CONSTRAINT article_versions_article_fk FOREIGN KEY (source_id, article_id)
        REFERENCES articles (source_id, id) ON DELETE RESTRICT,
    CONSTRAINT article_versions_raw_fk FOREIGN KEY (source_id, raw_artifact_id)
        REFERENCES raw_artifacts (source_id, id) ON DELETE RESTRICT,
    CONSTRAINT article_versions_number_unique UNIQUE (article_id, version_no),
    CONSTRAINT article_versions_content_unique UNIQUE (article_id, parser_version, content_hash),
    CONSTRAINT article_versions_article_id_unique UNIQUE (article_id, id)
);

ALTER TABLE articles ADD CONSTRAINT articles_latest_version_fk
    FOREIGN KEY (id, latest_version_id) REFERENCES article_versions (article_id, id)
    ON DELETE RESTRICT;

CREATE TABLE processing_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind text NOT NULL DEFAULT 'ARTICLE_PROCESSING'
        CHECK (kind = 'ARTICLE_PROCESSING'),
    article_version_id uuid NOT NULL REFERENCES article_versions (id) ON DELETE RESTRICT,
    dedupe_key text NOT NULL CHECK (length(btrim(dedupe_key)) > 0),
    pipeline_version text NOT NULL CHECK (length(btrim(pipeline_version)) > 0),
    ml_processing_id text UNIQUE,
    document_version_id text,
    status text NOT NULL DEFAULT 'QUEUED'
        CHECK (status IN ('QUEUED', 'RUNNING', 'RETRY', 'SUCCEEDED', 'FAILED')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    lease_token uuid,
    last_error text,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    CONSTRAINT processing_jobs_lease_pair CHECK ((lease_until IS NULL) = (lease_token IS NULL)),
    CONSTRAINT processing_jobs_dedupe_unique UNIQUE (dedupe_key),
    CONSTRAINT processing_jobs_version_pipeline_unique UNIQUE (article_version_id, pipeline_version),
    CONSTRAINT processing_jobs_completion CHECK (
        (status IN ('SUCCEEDED', 'FAILED') AND completed_at IS NOT NULL)
        OR (status NOT IN ('SUCCEEDED', 'FAILED') AND completed_at IS NULL)
    )
);

CREATE INDEX processing_jobs_due_idx ON processing_jobs (next_attempt_at, id)
    WHERE status IN ('QUEUED', 'RETRY');
