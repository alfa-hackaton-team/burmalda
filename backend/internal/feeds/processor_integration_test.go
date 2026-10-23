package feeds

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/migrate"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/polling"
	"github.com/jackc/pgx/v5/pgxpool"
)

type scriptedFetcher struct {
	responses []Response
	seen      []polling.Lease
}

func (f *scriptedFetcher) Fetch(_ context.Context, lease polling.Lease) (Response, error) {
	f.seen = append(f.seen, lease)
	response := f.responses[0]
	f.responses = f.responses[1:]
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
