-- Executed by ml.migrate with the ML schema first in search_path.
-- pgvector must already be installed by infrastructure; no Go tables/FKs.
CREATE TABLE document_versions (
    id UUID PRIMARY KEY,
    document_id UUID NOT NULL,
    snapshot JSONB NOT NULL CHECK (jsonb_typeof(snapshot) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (snapshot->>'document_version_id' IS NOT NULL
           AND (snapshot->>'document_version_id')::uuid = id),
    CHECK (snapshot->>'document_id' IS NOT NULL
           AND (snapshot->>'document_id')::uuid = document_id),
    CHECK (NOT (snapshot ? 'trace_id') AND NOT (snapshot ? 'pipeline_version'))
);
CREATE INDEX document_versions_document_id_idx ON document_versions(document_id);

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
    -- Neutral language configuration; no FTS index or search is introduced.
    search_vector TSVECTOR GENERATED ALWAYS AS (to_tsvector('simple'::regconfig, chunk_text)) STORED,
    embedding VECTOR(1024),
    embedding_model TEXT,
    embedding_version TEXT,
    UNIQUE (document_version_id, chunking_profile_id, chunk_no),
    CHECK (char_end - char_start = char_length(chunk_text)),
    CHECK (
        (embedding IS NULL AND embedding_model IS NULL AND embedding_version IS NULL)
        OR
        (embedding IS NOT NULL AND embedding_model IS NOT NULL AND embedding_version IS NOT NULL
         AND length(btrim(embedding_model)) > 0 AND length(btrim(embedding_version)) > 0)
    )
);
