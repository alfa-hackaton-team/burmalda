ALTER TABLE source_polls DROP CONSTRAINT source_polls_status_check;
ALTER TABLE source_polls ADD CONSTRAINT source_polls_status_check
    CHECK (status IN ('QUEUED', 'RUNNING', 'SUCCEEDED', 'PARTIAL', 'FAILED'));

ALTER TABLE source_polls DROP CONSTRAINT source_polls_times;
ALTER TABLE source_polls ADD CONSTRAINT source_polls_times CHECK (
    (status = 'QUEUED' AND started_at IS NULL AND finished_at IS NULL)
    OR (status = 'RUNNING' AND started_at IS NOT NULL AND finished_at IS NULL)
    OR (status IN ('SUCCEEDED', 'PARTIAL', 'FAILED') AND started_at IS NOT NULL
        AND finished_at IS NOT NULL AND finished_at >= started_at)
);

ALTER TABLE source_polls ADD COLUMN failed_entry_count integer NOT NULL DEFAULT 0
    CHECK (failed_entry_count >= 0);

CREATE TABLE source_poll_entry_failures (
    poll_id uuid NOT NULL REFERENCES source_polls (id) ON DELETE CASCADE,
    entry_no integer NOT NULL CHECK (entry_no > 0),
    article_url text NOT NULL,
    status text NOT NULL DEFAULT 'FAILED' CHECK (status = 'FAILED'),
    error_code text NOT NULL,
    error_message text NOT NULL,
    PRIMARY KEY (poll_id, entry_no)
);
