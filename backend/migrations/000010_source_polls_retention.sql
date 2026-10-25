CREATE INDEX source_polls_retention_idx ON source_polls (created_at, id)
    WHERE status IN ('SUCCEEDED', 'PARTIAL', 'FAILED');
