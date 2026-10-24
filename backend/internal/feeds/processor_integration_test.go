package feeds

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/migrate"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/mljobs"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/polling"
	"github.com/jackc/pgx/v5/pgxpool"
)

type scriptedFetcher struct {
	responses []Response
	seen      []polling.Lease
	articles  []Response
	seenLinks []string
}

func (f *scriptedFetcher) Fetch(_ context.Context, lease polling.Lease) (Response, error) {
	f.seen = append(f.seen, lease)
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response, nil
}

func (f *scriptedFetcher) FetchArticle(_ context.Context, _, articleURL string) (Response, error) {
	f.seenLinks = append(f.seenLinks, articleURL)
	response := f.articles[0]
	f.articles = f.articles[1:]
	return response, nil
}

func TestFeedVersionsAndConditionalPolls(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for the PostgreSQL integration test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := migrate.Run(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var sourceID string
	err = pool.QueryRow(ctx, `INSERT INTO sources
		(name, source_type, source_role, trust_level, adapter_type, endpoint,
		 polling_interval_seconds)
		VALUES ('feed-test', 'news', 'external', 'reviewed', 'rss',
		        'https://example.com/feed.xml', 60) RETURNING id::text`).Scan(&sourceID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM source_polls WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `UPDATE articles SET latest_version_id=NULL WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM processing_jobs WHERE article_version_id IN
			(SELECT id FROM article_versions WHERE source_id=$1::uuid)`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM article_versions WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM articles WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM raw_artifacts WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM sources WHERE id=$1::uuid`, sourceID)
	}()

	original, err := os.ReadFile("testdata/rss.xml")
	if err != nil {
		t.Fatal(err)
	}
	changed := []byte(strings.Replace(string(original), "First", "Changed", 1))
	fetcher := &scriptedFetcher{responses: []Response{
		{Status: 200, Body: original, ETag: `"v1"`},
		{Status: 304},
		{Status: 200, Body: original, ETag: `"v1"`},
		{Status: 200, Body: changed, ETag: `"v2"`},
		{Status: 200, Body: original, ETag: `"v3"`},
	}}
	processor := Processor{Fetcher: fetcher}
	store := polling.NewStore(pool)
	for i, expected := range []struct{ status, articles, versions int }{
		{200, 1, 1}, {304, 0, 0}, {200, 0, 0},
		{200, 0, 1}, {200, 0, 1},
	} {
		if i > 0 {
			_, err = pool.Exec(ctx, `UPDATE sources SET next_poll_at=clock_timestamp()-interval '1 second'
				WHERE id=$1::uuid`, sourceID)
			if err != nil {
				t.Fatal(err)
			}
		}
		lease, ok, err := store.Claim(ctx, time.Minute)
		if err != nil || !ok {
			t.Fatalf("claim %d: ok=%v err=%v", i, ok, err)
		}
		outcome, err := processor.Poll(ctx, lease)
		if err != nil {
			t.Fatalf("process %d: %v", i, err)
		}
		if err := store.Finish(ctx, lease, outcome); err != nil {
			t.Fatalf("finish %d: %v", i, err)
		}
		var status, articles, versions int
		err = pool.QueryRow(ctx, `SELECT http_status, new_article_count, new_version_count
			FROM source_polls WHERE id=$1::uuid`, lease.PollID).Scan(&status, &articles, &versions)
		if err != nil {
			t.Fatal(err)
		}
		if status != expected.status || articles != expected.articles || versions != expected.versions {
			t.Fatalf("poll %d: got (%d,%d,%d), want %+v", i, status, articles, versions, expected)
		}
	}
	if fetcher.seen[1].HTTPETag == nil || *fetcher.seen[1].HTTPETag != `"v1"` {
		t.Fatalf("conditional ETag not loaded: %+v", fetcher.seen[1].HTTPETag)
	}
	var articleCount, versionCount, artifactCount int
	err = pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM articles WHERE source_id=$1::uuid),
		(SELECT count(*) FROM article_versions WHERE source_id=$1::uuid),
		(SELECT count(*) FROM raw_artifacts WHERE source_id=$1::uuid)`, sourceID).Scan(
		&articleCount, &versionCount, &artifactCount)
	if err != nil {
		t.Fatal(err)
	}
	if articleCount != 1 || versionCount != 3 || artifactCount != 2 {
		t.Fatalf("articles=%d versions=%d artifacts=%d", articleCount, versionCount, artifactCount)
	}
	var latestVersion int
	if err := pool.QueryRow(ctx, `SELECT v.version_no FROM articles a
		JOIN article_versions v ON v.id=a.latest_version_id
		WHERE a.source_id=$1::uuid`, sourceID).Scan(&latestVersion); err != nil {
		t.Fatal(err)
	}
	if latestVersion != 3 {
		t.Fatalf("latest version=%d, want 3", latestVersion)
	}
}

func TestHTMLArticleVersionsAndOriginals(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := migrate.Run(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var sourceID string
	if err := pool.QueryRow(ctx, `INSERT INTO sources
		(name, source_type, source_role, trust_level, adapter_type, endpoint,
		 adapter_config, polling_interval_seconds)
		VALUES ('html-test', 'news', 'external', 'reviewed', 'rss',
		'https://example.com/feed.xml', '{"article_html":true}', 60)
		RETURNING id::text`).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM source_polls WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `UPDATE articles SET latest_version_id=NULL WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM processing_jobs WHERE article_version_id IN
			(SELECT id FROM article_versions WHERE source_id=$1::uuid)`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM article_versions WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM articles WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM raw_artifacts WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM sources WHERE id=$1::uuid`, sourceID)
	}()
	feed, err := os.ReadFile("testdata/rss.xml")
	if err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile("testdata/article.html")
	if err != nil {
		t.Fatal(err)
	}
	menu := []byte(strings.Replace(string(first), "Top menu", "Updated menu", 1))
	changed := []byte(strings.Replace(string(menu), "opened higher", "opened lower", 1))
	fetcher := &scriptedFetcher{responses: []Response{
		{Status: 200, Body: feed, ETag: `"unchanged"`},
		{Status: 200, Body: feed, ETag: `"unchanged"`},
		{Status: 200, Body: feed, ETag: `"unchanged"`},
	}, articles: []Response{
		{Status: 200, Body: first, ContentType: "text/html"},
		{Status: 200, Body: menu, ContentType: "text/html"},
		{Status: 200, Body: changed, ContentType: "text/html"},
	}}
	store := polling.NewStore(pool)
	for i, expectedVersions := range []int{1, 0, 1} {
		if i > 0 {
			if _, err := pool.Exec(ctx, `UPDATE sources SET next_poll_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, sourceID); err != nil {
				t.Fatal(err)
			}
		}
		lease, ok, err := store.Claim(ctx, time.Minute)
		if err != nil || !ok {
			t.Fatalf("claim %d: %v %v", i, ok, err)
		}
		outcome, err := (Processor{Fetcher: fetcher, PipelineVersion: "test-pipeline", MaxTextBytes: 1000}).Poll(ctx, lease)
		if err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
		if err := store.Finish(ctx, lease, outcome); err != nil {
			t.Fatalf("finish %d: %v", i, err)
		}
		var got int
		if err := pool.QueryRow(ctx, `SELECT new_version_count FROM source_polls WHERE id=$1::uuid`, lease.PollID).Scan(&got); err != nil || got != expectedVersions {
			t.Fatalf("poll %d versions=%d err=%v, want %d", i, got, err, expectedVersions)
		}
	}
	if fetcher.seen[1].HTTPETag != nil || len(fetcher.seenLinks) != 3 {
		t.Fatalf("article polling skipped unchanged feed: etag=%v links=%v", fetcher.seen[1].HTTPETag, fetcher.seenLinks)
	}
	var raw []byte
	var contentType, body string
	if err := pool.QueryRow(ctx, `SELECT r.content, r.content_type, v.body_text
		FROM articles a JOIN article_versions v ON v.id=a.latest_version_id
		JOIN raw_artifacts r ON r.id=v.raw_artifact_id WHERE a.source_id=$1::uuid`, sourceID).Scan(&raw, &contentType, &body); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, changed) || contentType != "text/html" || !strings.Contains(body, "opened lower") {
		t.Fatalf("wrong latest original or text: content_type=%q body=%q", contentType, body)
	}
	var jobs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM processing_jobs WHERE article_version_id IN
		(SELECT id FROM article_versions WHERE source_id=$1::uuid)`, sourceID).Scan(&jobs); err != nil || jobs != 2 {
		t.Fatalf("processing jobs=%d err=%v, want 2", jobs, err)
	}
	jobStore := mljobs.NewStore(pool)
	job, ok, err := jobStore.Claim(ctx)
	if err != nil || !ok || job.PipelineVersion != "test-pipeline" || job.RawArtifactID == "" {
		t.Fatalf("claim ML job: ok=%v job=%+v err=%v", ok, job, err)
	}
	if err := jobStore.Finish(ctx, job, mljobs.Result{
		ProcessingID: "processing-test", Succeeded: true,
		DocumentVersionID: "document-test", EventIDs: []string{},
	}); err != nil {
		t.Fatal(err)
	}
	var status, documentID string
	var eventIDs []byte
	if err := pool.QueryRow(ctx, `SELECT status, document_version_id, event_ids
		FROM processing_jobs WHERE id=$1::uuid`, job.ID).Scan(&status, &documentID, &eventIDs); err != nil {
		t.Fatal(err)
	}
	if status != "SUCCEEDED" || documentID != "document-test" || string(eventIDs) != "[]" {
		t.Fatalf("job status=%q document=%q events=%s", status, documentID, eventIDs)
	}
}
