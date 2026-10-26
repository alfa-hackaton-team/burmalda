CREATE INDEX articles_recent_news_idx ON articles (first_seen_at DESC, id DESC)
WHERE latest_version_id IS NOT NULL;
