CREATE INDEX processing_jobs_created_at_idx ON processing_jobs(created_at);
CREATE INDEX processing_jobs_completed_at_idx ON processing_jobs(completed_at) WHERE completed_at IS NOT NULL;
CREATE INDEX article_fetch_jobs_created_at_idx ON article_fetch_jobs(created_at);
CREATE INDEX article_fetch_jobs_completed_at_idx ON article_fetch_jobs(completed_at) WHERE completed_at IS NOT NULL;
