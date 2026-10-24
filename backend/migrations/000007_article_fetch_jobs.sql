CREATE TABLE article_fetch_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    source_id uuid NOT NULL REFERENCES sources (id) ON DELETE RESTRICT,
    article_url text NOT NULL CHECK (length(btrim(article_url)) > 0),
    external_id text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    published_at timestamptz,
    status text NOT NULL CHECK (status IN ('QUEUED', 'RUNNING', 'RETRY', 'SUCCEEDED', 'DEAD')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    lease_token uuid,
    lease_until timestamptz,
    last_error text,
    last_error_code text,
    last_http_status integer CHECK (last_http_status BETWEEN 100 AND 599),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    CONSTRAINT article_fetch_jobs_source_url_unique UNIQUE (source_id, article_url),
    CONSTRAINT article_fetch_jobs_lease_pair CHECK ((lease_token IS NULL) = (lease_until IS NULL)),
    CONSTRAINT article_fetch_jobs_running CHECK ((status = 'RUNNING') = (lease_token IS NOT NULL)),
    CONSTRAINT article_fetch_jobs_completion CHECK (
        (status IN ('SUCCEEDED', 'DEAD')) = (completed_at IS NOT NULL)
    )
);

CREATE INDEX article_fetch_jobs_due_idx ON article_fetch_jobs (next_attempt_at, id)
    WHERE status IN ('QUEUED', 'RETRY', 'RUNNING');
