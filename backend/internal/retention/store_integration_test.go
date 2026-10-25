package retention

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/migrate"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDeleteBatchOnlyExpiredCompletedPolls(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := migrate.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	var sourceID string
	err = db.QueryRow(ctx, `INSERT INTO sources (name, source_type, source_role, trust_level,
		adapter_type, endpoint, polling_interval_seconds)
		VALUES ('retention-test', 'news', 'external', 'reviewed', 'rss',
		'https://example.com/feed.xml', 60) RETURNING id::text`).Scan(&sourceID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM source_polls WHERE source_id=$1::uuid`, sourceID)
		_, _ = db.Exec(context.Background(), `DELETE FROM sources WHERE id=$1::uuid`, sourceID)
	})
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -31)
	insert := func(status string, created time.Time) string {
		t.Helper()
		var id string
		err := db.QueryRow(ctx, `INSERT INTO source_polls
			(source_id, status, created_at, started_at, finished_at)
			VALUES ($1::uuid, $2, $3::timestamptz, $3::timestamptz,
			CASE WHEN $2 = 'RUNNING' THEN NULL ELSE $3::timestamptz END) RETURNING id::text`,
			sourceID, status, created).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := insert("FAILED", old)
	second := insert("PARTIAL", old)
	insert("RUNNING", old)
	insert("SUCCEEDED", now)
	for _, id := range []string{first, second} {
		_, err := db.Exec(ctx, `INSERT INTO source_poll_entry_failures
			(poll_id, entry_no, article_url, error_code, error_message)
			VALUES ($1::uuid, 1, 'https://example.com/article', 'HTTP_ERROR', 'test')`, id)
		if err != nil {
			t.Fatal(err)
		}
	}
	store := NewStore(db)
	cutoff := Cutoff(now, 30)
	if count, err := store.Count(ctx, cutoff); err != nil || count != 2 {
		t.Fatalf("candidates=%d err=%v; want 2", count, err)
	}
	if count, err := store.DeleteBatch(ctx, cutoff, 1); err != nil || count != 1 {
		t.Fatalf("first batch=%d err=%v; want 1", count, err)
	}
	if count, err := store.DeleteBatch(ctx, cutoff, 1); err != nil || count != 1 {
		t.Fatalf("second batch=%d err=%v; want 1", count, err)
	}
	if count, err := store.DeleteBatch(ctx, cutoff, 1); err != nil || count != 0 {
		t.Fatalf("third batch=%d err=%v; want 0", count, err)
	}
	var polls, failures int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM source_polls WHERE source_id=$1::uuid`, sourceID).Scan(&polls); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM source_poll_entry_failures
		WHERE poll_id IN ($1::uuid, $2::uuid)`, first, second).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if polls != 2 || failures != 0 {
		t.Fatalf("remaining polls=%d failures=%d; want 2 and 0", polls, failures)
	}
}
