package feeds

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/migrate"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/polling"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestArticleFailureRetriesIndependentlyAndBecomesDead(t *testing.T) {
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
	err = pool.QueryRow(ctx, `INSERT INTO sources
		(name, source_type, source_role, trust_level, adapter_type, endpoint,
		 adapter_config, polling_interval_seconds)
		VALUES ('article-retry-test', 'news', 'external', 'reviewed', 'rss',
		'https://example.com/feed.xml', '{"article_html":true}', 60)
		RETURNING id::text`).Scan(&sourceID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM article_fetch_jobs WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM source_polls WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `UPDATE articles SET latest_version_id=NULL WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM article_versions WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM articles WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM raw_artifacts WHERE source_id=$1::uuid`, sourceID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM sources WHERE id=$1::uuid`, sourceID)
	}()
	feed := []byte(`<rss><channel>
		<item><guid>good</guid><link>https://example.com/good</link><title>Good</title></item>
		<item><guid>retry</guid><link>https://example.com/retry</link><title>Retry</title></item>
		</channel></rss>`)
	fetcher := &scriptedFetcher{responses: []Response{{Status: 200, Body: feed}},
		articles: []Response{{Status: 200, Body: []byte("<article>Good text</article>"), ContentType: "text/html"},
			{Status: 503, RetryAfter: 2 * time.Minute}}}
	pollStore := polling.NewStore(pool)
	lease, ok, err := pollStore.Claim(ctx, time.Minute)
	if err != nil || !ok || lease.SourceID != sourceID {
		t.Fatalf("claim source: ok=%v lease=%+v err=%v", ok, lease, err)
	}
	outcome, err := (Processor{Fetcher: fetcher}).Poll(ctx, lease)
	if err != nil || !outcome.Partial {
		t.Fatalf("poll outcome=%+v err=%v", outcome, err)
	}
	if err := pollStore.Finish(ctx, lease, outcome); err != nil {
		t.Fatal(err)
	}
	var jobID, status string
	var attempts int
	var retryDelaySeconds float64
	err = pool.QueryRow(ctx, `SELECT id::text, status, attempts,
		EXTRACT(EPOCH FROM next_attempt_at-created_at)::double precision FROM article_fetch_jobs
		WHERE source_id=$1::uuid AND article_url='https://example.com/retry'`, sourceID).Scan(
		&jobID, &status, &attempts, &retryDelaySeconds)
	if err != nil || status != "RETRY" || attempts != 1 || retryDelaySeconds < 120 {
		t.Fatalf("queued job: id=%s status=%s attempts=%d delay=%v err=%v", jobID, status, attempts, retryDelaySeconds, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE article_fetch_jobs SET next_attempt_at=clock_timestamp()-interval '1 second'
		WHERE id=$1::uuid`, jobID); err != nil {
		t.Fatal(err)
	}
	jobStore := NewArticleJobStore(pool)
	type claimResult struct {
		job ArticleJob
		ok  bool
		err error
	}
	start := make(chan struct{})
	claims := make(chan claimResult, 2)
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			claimed, claimedOK, claimErr := jobStore.Claim(ctx)
			claims <- claimResult{claimed, claimedOK, claimErr}
		}()
	}
	close(start)
	workers.Wait()
	close(claims)
	var job ArticleJob
	var winners int
	for claim := range claims {
		if claim.err != nil {
			t.Fatal(claim.err)
		}
		if claim.ok {
			job = claim.job
			winners++
		}
	}
	if winners != 1 || job.ID != jobID || job.Attempts != 2 {
		t.Fatalf("concurrent claims=%d job=%+v", winners, job)
	}
	if _, ok, err := jobStore.Claim(ctx); err != nil || ok {
		t.Fatalf("duplicate claim: ok=%v err=%v", ok, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE article_fetch_jobs SET lease_until=clock_timestamp()-interval '1 second'
		WHERE id=$1::uuid`, jobID); err != nil {
		t.Fatal(err)
	}
	recovered, ok, err := jobStore.Claim(ctx)
	if err != nil || !ok || recovered.ID != jobID || recovered.Attempts != 3 {
		t.Fatalf("recover expired lease: ok=%v job=%+v err=%v", ok, recovered, err)
	}
	if _, err := jobStore.Finish(ctx, job, ArticleOutcome{Error: "stale"}, ""); err != ErrLostArticleLease {
		t.Fatalf("stale owner finished job: %v", err)
	}
	retryFetcher := &scriptedFetcher{articles: []Response{{Status: 200,
		Body: []byte("<article>Recovered text</article>"), ContentType: "text/html"}}}
	result := (ArticleJobProcessor{Fetcher: retryFetcher, Limits: config.DefaultRuntime()}).Process(ctx, recovered)
	status, err = jobStore.Finish(ctx, recovered, result, "")
	if err != nil || status != "SUCCEEDED" {
		t.Fatalf("finish retry: status=%s err=%v", status, err)
	}
	var versions int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM article_versions WHERE source_id=$1::uuid`,
		sourceID).Scan(&versions); err != nil || versions != 2 {
		t.Fatalf("article versions=%d err=%v", versions, err)
	}
	var deadID string
	err = pool.QueryRow(ctx, `INSERT INTO article_fetch_jobs
		(source_id, article_url, external_id, title, status, attempts, next_attempt_at)
		VALUES ($1::uuid, 'https://example.com/dead', 'dead', 'Dead', 'RETRY', 4,
		 clock_timestamp()-interval '1 second') RETURNING id::text`, sourceID).Scan(&deadID)
	if err != nil {
		t.Fatal(err)
	}
	deadJob, ok, err := jobStore.Claim(ctx)
	if err != nil || !ok || deadJob.ID != deadID || deadJob.Attempts != 5 {
		t.Fatalf("claim final attempt: ok=%v job=%+v err=%v", ok, deadJob, err)
	}
	failingFetcher := &scriptedFetcher{articles: []Response{{Status: 503, RetryAfter: time.Minute}}}
	result = (ArticleJobProcessor{Fetcher: failingFetcher, Limits: config.DefaultRuntime()}).Process(ctx, deadJob)
	status, err = jobStore.Finish(ctx, deadJob, result, "")
	if err != nil || status != "DEAD" {
		t.Fatalf("final attempt: status=%s err=%v", status, err)
	}
	if _, ok, err := jobStore.Claim(ctx); err != nil || ok {
		t.Fatalf("dead job was claimed: ok=%v err=%v", ok, err)
	}
	var permanentID string
	err = pool.QueryRow(ctx, `INSERT INTO article_fetch_jobs
		(source_id, article_url, external_id, title, status, attempts, next_attempt_at)
		VALUES ($1::uuid, 'https://example.com/missing', 'missing', 'Missing', 'QUEUED', 0,
		 clock_timestamp()-interval '1 second') RETURNING id::text`, sourceID).Scan(&permanentID)
	if err != nil {
		t.Fatal(err)
	}
	permanentJob, ok, err := jobStore.Claim(ctx)
	if err != nil || !ok || permanentJob.ID != permanentID || permanentJob.Attempts != 1 {
		t.Fatalf("claim permanent failure: ok=%v job=%+v err=%v", ok, permanentJob, err)
	}
	missingFetcher := &scriptedFetcher{articles: []Response{{Status: 404}}}
	result = (ArticleJobProcessor{Fetcher: missingFetcher, Limits: config.DefaultRuntime()}).Process(ctx, permanentJob)
	status, err = jobStore.Finish(ctx, permanentJob, result, "")
	if err != nil || status != "DEAD" {
		t.Fatalf("permanent failure: status=%s err=%v", status, err)
	}
	var crashedID string
	err = pool.QueryRow(ctx, `INSERT INTO article_fetch_jobs
		(source_id, article_url, status, attempts, lease_token, lease_until)
		VALUES ($1::uuid, 'https://example.com/crashed', 'RUNNING', 5,
		 gen_random_uuid(), clock_timestamp()-interval '1 second') RETURNING id::text`,
		sourceID).Scan(&crashedID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := jobStore.Claim(ctx); err != nil || ok {
		t.Fatalf("exhausted crashed job was claimed: ok=%v err=%v", ok, err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM article_fetch_jobs WHERE id=$1::uuid`,
		crashedID).Scan(&status); err != nil || status != "DEAD" {
		t.Fatalf("exhausted crashed job: status=%s err=%v", status, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sources SET next_poll_at=clock_timestamp()-interval '1 second'
		WHERE id=$1::uuid`, sourceID); err != nil {
		t.Fatal(err)
	}
	allFailedFeed := []byte(`<rss><channel><item><guid>all-failed</guid>
		<link>https://example.com/all-failed</link><title>All failed</title>
		</item></channel></rss>`)
	allFailedFetcher := &scriptedFetcher{responses: []Response{{Status: 200, Body: allFailedFeed}},
		articles: []Response{{Status: 503}}}
	lease, ok, err = pollStore.Claim(ctx, time.Minute)
	if err != nil || !ok || lease.SourceID != sourceID {
		t.Fatalf("claim failed source: ok=%v lease=%+v err=%v", ok, lease, err)
	}
	outcome, err = (Processor{Fetcher: allFailedFetcher}).Poll(ctx, lease)
	if err != nil || outcome.Success || len(outcome.EntryFailures) != 1 {
		t.Fatalf("all-failed outcome=%+v err=%v", outcome, err)
	}
	if err := pollStore.Finish(ctx, lease, outcome); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM article_fetch_jobs
		WHERE source_id=$1::uuid AND article_url='https://example.com/all-failed'`,
		sourceID).Scan(&status); err != nil || status != "RETRY" {
		t.Fatalf("all-failed article job: status=%s err=%v", status, err)
	}
}
