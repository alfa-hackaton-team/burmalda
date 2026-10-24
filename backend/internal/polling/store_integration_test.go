package polling

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/migrate"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTwoWorkersClaimOneSource(t *testing.T) {
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
		VALUES ('polling-test', 'news', 'external', 'reviewed', 'rss',
		        'https://example.com/feed.xml', 60) RETURNING id::text`).Scan(&sourceID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM source_polls WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM sources WHERE id=$1::uuid`, sourceID)
	}()

	storeA, storeB := NewStore(pool), NewStore(pool)
	start := make(chan struct{})
	type claimResult struct {
		lease Lease
		ok    bool
		err   error
	}
	results := make(chan claimResult, 2)
	var workers sync.WaitGroup
	for _, store := range []*Store{storeA, storeB} {
		workers.Add(1)
		go func(store *Store) {
			defer workers.Done()
			<-start
			lease, ok, err := store.Claim(ctx, time.Minute)
			results <- claimResult{lease, ok, err}
		}(store)
	}
	close(start)
	workers.Wait()
	close(results)
	var won Lease
	var claims int
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.ok {
			won = result.lease
			claims++
		}
	}
	if claims != 1 || won.SourceID != sourceID {
		t.Fatalf("claims=%d, winner=%+v; want one claim for %s", claims, won, sourceID)
	}
	var running int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM source_polls
		WHERE source_id=$1::uuid AND status='RUNNING'`, sourceID).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if running != 1 {
		t.Fatalf("running polls=%d; want 1", running)
	}
	if err := storeA.Renew(ctx, won, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := storeA.Finish(ctx, won, Outcome{
		Success: true, DiscoveredCount: 2,
		Persist: func(ctx context.Context, tx pgx.Tx) (int, int, error) {
			_, err := tx.Exec(ctx, `UPDATE sources SET http_etag='"v1"' WHERE id=$1::uuid`, sourceID)
			return 1, 1, err
		},
	}); err != nil {
		t.Fatal(err)
	}
	var pollStatus string
	var nextDue bool
	var etag *string
	if err := pool.QueryRow(ctx, `SELECT p.status, s.next_poll_at > clock_timestamp(), s.http_etag
		FROM source_polls p JOIN sources s ON s.id=p.source_id
		WHERE p.id=$1::uuid`, won.PollID).Scan(&pollStatus, &nextDue, &etag); err != nil {
		t.Fatal(err)
	}
	if pollStatus != "SUCCEEDED" || !nextDue || etag == nil || *etag != `"v1"` {
		t.Fatalf("poll status=%s next due in future=%v etag=%v", pollStatus, nextDue, etag)
	}

	if _, err := pool.Exec(ctx, `UPDATE sources SET next_poll_at=clock_timestamp()-interval '1 second'
		WHERE id=$1::uuid`, sourceID); err != nil {
		t.Fatal(err)
	}
	stale, ok, err := storeB.Claim(ctx, time.Minute)
	if err != nil || !ok {
		t.Fatalf("second claim: ok=%v err=%v", ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sources SET lease_until=clock_timestamp()-interval '1 second'
		WHERE id=$1::uuid`, sourceID); err != nil {
		t.Fatal(err)
	}
	closed, err := storeA.ReapExpired(ctx, 10)
	if err != nil || closed != 1 {
		t.Fatalf("reap: closed=%d err=%v", closed, err)
	}
	called := false
	if err := storeB.Finish(ctx, stale, Outcome{
		Success: true,
		Persist: func(context.Context, pgx.Tx) (int, int, error) { called = true; return 0, 0, nil },
	}); !errors.Is(err, ErrLostLease) {
		t.Fatalf("stale worker finish=%v; want ErrLostLease", err)
	}
	if called {
		t.Fatal("stale worker persisted results")
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM source_polls WHERE id=$1::uuid`,
		stale.PollID).Scan(&pollStatus); err != nil {
		t.Fatal(err)
	}
	if pollStatus != "FAILED" {
		t.Fatalf("expired poll status=%s; want FAILED", pollStatus)
	}
}
