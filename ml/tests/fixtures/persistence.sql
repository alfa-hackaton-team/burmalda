-- Isolated integration-test fixture, NOT a production migration.
-- Agreed document_chunks fields/checks from docs/rfc/database_scheme.md.
-- Local parent is deliberately minimal: its storage contract is not defined yet.
CREATE TABLE document_versions (
    id UUID PRIMARY KEY
);

CREATE TABLE document_chunks (
    id UUID PRIMARY KEY,
    document_version_id UUID NOT NULL REFERENCES document_versions(id) ON DELETE RESTRICT,
    chunking_profile_id TEXT NOT NULL CHECK (length(btrim(chunking_profile_id)) > 0),
    chunk_no INTEGER NOT NULL CHECK (chunk_no >= 0),
    chunk_text TEXT NOT NULL CHECK (length(chunk_text) > 0),
    char_start INTEGER NOT NULL CHECK (char_start >= 0),
    char_end INTEGER NOT NULL CHECK (char_end > char_start),
    token_count INTEGER NOT NULL CHECK (token_count > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 'simple' is a test-only choice; production FTS configuration is open.
    search_vector TSVECTOR GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, chunk_text)) STORED,
    -- Plain array placeholder: no pgvector extension is required or exercised.
    -- Production type remains VECTOR(N), with N to be agreed before migration.
    embedding DOUBLE PRECISION[],
    embedding_model TEXT,
    embedding_version TEXT,
    UNIQUE (document_version_id, chunking_profile_id, chunk_no),
    CHECK (char_end - char_start = char_length(chunk_text)),
    CHECK (
        (embedding IS NULL AND embedding_model IS NULL AND embedding_version IS NULL)
        OR
        (embedding IS NOT NULL AND embedding_model IS NOT NULL AND embedding_version IS NOT NULL)
    )
);
