package feeds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/observability"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/polling"
	"github.com/alfa-hackaton-team/burmalda/backend/internal/sources"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var ErrLostArticleLease = errors.New("article fetch lease was lost")

type ArticleJob struct {
	ID, Token, SourceID, ArticleURL string
	ExternalID, Title               string
	PublishedAt                     *time.Time
	Endpoint, Name, SourceType      string
	SourceRole, TrustLevel          string
	AdapterConfig                   []byte
	ConfigVersion                   int64
	Attempts                        int
}

type ArticleOutcome struct {
	Item       *material
	Error      string
	ErrorCode  string
	HTTPStatus *int
	RetryAfter time.Duration
	Retryable  bool
}

type ArticleJobStore struct {
	db       *pgxpool.Pool
	Settings func() config.Runtime
}

func NewArticleJobStore(db *pgxpool.Pool) *ArticleJobStore { return &ArticleJobStore{db: db} }

func (s *ArticleJobStore) settings() config.Runtime {
	if s.Settings != nil {
		return s.Settings()
	}
	return config.DefaultRuntime()
}

func (s *ArticleJobStore) Claim(ctx context.Context) (ArticleJob, bool, error) {
	settings := s.settings()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ArticleJob{}, false, fmt.Errorf("begin article claim: %w", err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `WITH exhausted AS (
		SELECT id FROM article_fetch_jobs
		WHERE attempts >= $1 AND
		 ((status IN ('QUEUED','RETRY') AND next_attempt_at<=clock_timestamp())
		  OR (status='RUNNING' AND lease_until<=clock_timestamp()))
		ORDER BY next_attempt_at, id FOR UPDATE SKIP LOCKED LIMIT 100
	) UPDATE article_fetch_jobs j SET status='DEAD', lease_token=NULL, lease_until=NULL,
		last_error=COALESCE(last_error, 'article retry attempts exhausted'),
		last_error_code=COALESCE(last_error_code, 'RETRY_EXHAUSTED'),
		completed_at=clock_timestamp(), updated_at=clock_timestamp()
		FROM exhausted WHERE j.id=exhausted.id`, settings.ArticleRetry.MaxAttempts)
	if err != nil {
		return ArticleJob{}, false, fmt.Errorf("close exhausted article jobs: %w", err)
	}
	var job ArticleJob
	err = tx.QueryRow(ctx, `SELECT j.id::text, j.source_id::text, j.article_url,
		j.external_id, j.title, j.published_at, j.attempts,
		s.endpoint, s.name, s.source_type, s.source_role, s.trust_level,
		s.adapter_config, s.config_version
		FROM article_fetch_jobs j JOIN sources s ON s.id=j.source_id
		WHERE s.enabled AND s.adapter_type IN ('rss','atom') AND j.attempts<$1
		  AND ((j.status IN ('QUEUED','RETRY') AND j.next_attempt_at<=clock_timestamp())
		    OR (j.status='RUNNING' AND j.lease_until<=clock_timestamp()))
		ORDER BY j.next_attempt_at, j.id FOR UPDATE OF j SKIP LOCKED LIMIT 1`,
		settings.ArticleRetry.MaxAttempts).Scan(
		&job.ID, &job.SourceID, &job.ArticleURL, &job.ExternalID, &job.Title,
		&job.PublishedAt, &job.Attempts, &job.Endpoint, &job.Name, &job.SourceType,
		&job.SourceRole, &job.TrustLevel, &job.AdapterConfig, &job.ConfigVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return ArticleJob{}, false, fmt.Errorf("commit exhausted article jobs: %w", err)
		}
		return ArticleJob{}, false, nil
	}
	if err != nil {
		return ArticleJob{}, false, fmt.Errorf("select due article: %w", err)
	}
	err = tx.QueryRow(ctx, `UPDATE article_fetch_jobs SET status='RUNNING',
		lease_token=gen_random_uuid(),
		lease_until=clock_timestamp()+($2::double precision*interval '1 second'),
		attempts=attempts+1, updated_at=clock_timestamp()
		WHERE id=$1::uuid RETURNING lease_token::text, attempts`,
		job.ID, settings.ArticleRetry.Lease.Std().Seconds()).Scan(&job.Token, &job.Attempts)
	if err != nil {
		return ArticleJob{}, false, fmt.Errorf("lease article: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ArticleJob{}, false, fmt.Errorf("commit article claim: %w", err)
	}
	return job, true, nil
}

func (s *ArticleJobStore) Renew(ctx context.Context, job ArticleJob, duration time.Duration) error {
	command, err := s.db.Exec(ctx, `UPDATE article_fetch_jobs SET
		lease_until=clock_timestamp()+($3::double precision*interval '1 second')
		WHERE id=$1::uuid AND lease_token=$2::uuid AND status='RUNNING'
		  AND lease_until>clock_timestamp()`, job.ID, job.Token, duration.Seconds())
	if err != nil {
		return fmt.Errorf("renew article lease: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrLostArticleLease
	}
	return nil
}

func (s *ArticleJobStore) Finish(ctx context.Context, job ArticleJob, outcome ArticleOutcome, pipelineVersion string) (string, error) {
	settings := s.settings()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin article finish: %w", err)
	}
	defer tx.Rollback(ctx)
	var currentVersion int64
	if err := tx.QueryRow(ctx, `SELECT config_version FROM sources WHERE id=$1::uuid FOR UPDATE`,
		job.SourceID).Scan(&currentVersion); err != nil {
		return "", fmt.Errorf("lock article source: %w", err)
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT lease_token=$2::uuid AND lease_until>clock_timestamp()
		FROM article_fetch_jobs WHERE id=$1::uuid AND status='RUNNING'`,
		job.ID, job.Token).Scan(&valid)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrLostArticleLease
		}
		return "", fmt.Errorf("lock article job: %w", err)
	}
	if !valid {
		return "", ErrLostArticleLease
	}
	if currentVersion != job.ConfigVersion {
		outcome = ArticleOutcome{Retryable: true, ErrorCode: "SOURCE_CONFIG_CHANGED", Error: "source configuration changed during article fetch"}
	}
	status := "SUCCEEDED"
	if outcome.Item != nil && currentVersion == job.ConfigVersion {
		lease := polling.Lease{SourceID: job.SourceID, Name: job.Name,
			SourceType: job.SourceType, SourceRole: job.SourceRole, TrustLevel: job.TrustLevel}
		if _, _, err := saveEntry(ctx, tx, lease, "", time.Now().UTC(),
			outcome.Item.ParserVersion, *outcome.Item, pipelineVersion); err != nil {
			return "", fmt.Errorf("save retried article: %w", err)
		}
	} else {
		status = "DEAD"
		if outcome.Retryable && job.Attempts < settings.ArticleRetry.MaxAttempts {
			status = "RETRY"
		}
		if outcome.Error == "" {
			outcome.Error = "article fetch failed"
		}
	}
	var completedAt *time.Time
	if status != "RETRY" {
		now := time.Now().UTC()
		completedAt = &now
	}
	delay := time.Duration(0)
	if status == "RETRY" {
		delay = polling.RetryDelay(job.Attempts, outcome.RetryAfter, settings)
	}
	command, err := tx.Exec(ctx, `UPDATE article_fetch_jobs SET status=$3,
		lease_token=NULL, lease_until=NULL,
		next_attempt_at=clock_timestamp()+($4::double precision*interval '1 second'),
		last_error=$5, last_error_code=$6, last_http_status=$7,
		completed_at=$8, updated_at=clock_timestamp()
		WHERE id=$1::uuid AND lease_token=$2::uuid AND status='RUNNING'
		  AND lease_until>clock_timestamp()`,
		job.ID, job.Token, status, delay.Seconds(), nullIfEmpty(outcome.Error),
		nullIfEmpty(outcome.ErrorCode), outcome.HTTPStatus, completedAt)
	if err != nil {
		return "", fmt.Errorf("finish article job: %w", err)
	}
	if command.RowsAffected() != 1 {
		return "", ErrLostArticleLease
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit article job: %w", err)
	}
	return status, nil
}

type ArticleJobProcessor struct {
	Fetcher         ArticleFetcher
	Limits          config.Runtime
	PipelineVersion string
	MaxTextBytes    int
}

func (p ArticleJobProcessor) Process(ctx context.Context, job ArticleJob) ArticleOutcome {
	response, err := p.Fetcher.FetchArticle(ctx, job.Endpoint, job.ArticleURL)
	var status *int
	if response.Status != 0 {
		status = &response.Status
	}
	if err != nil {
		return ArticleOutcome{Error: err.Error(), ErrorCode: "ARTICLE_FETCH_FAILED", HTTPStatus: status,
			RetryAfter: response.RetryAfter, Retryable: RetryableArticleFetch(response.Status, err)}
	}
	adapter, err := sources.ParseFeedAdapterConfig(job.AdapterConfig)
	if err != nil || !adapter.ArticleHTML {
		return ArticleOutcome{Error: "article HTML adapter is disabled", ErrorCode: "ADAPTER_CONFIG_INVALID"}
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(response.ContentType, ";", 2)[0]))
	var body, parserVersion string
	switch mediaType {
	case "text/html", "application/xhtml+xml":
		body, err = ExtractArticleHTMLWithOptions(response.Body, response.ContentType,
			adapter.ArticleContainerID, adapter.ArticleExcludeClasses)
		parserVersion = ArticleHTMLParserVersionForOptions(adapter.ArticleContainerID, adapter.ArticleExcludeClasses)
	case "application/pdf":
		body, err = ExtractArticlePDFWithLimits(response.Body, p.Limits.Feeds.MaxPDFPages, p.Limits.Feeds.MaxPDFTextBytes)
		parserVersion = ArticlePDFParserVersion
	default:
		err = fmt.Errorf("unsupported article media type")
	}
	if err != nil {
		return ArticleOutcome{Error: err.Error(), ErrorCode: "ARTICLE_EXTRACTION_FAILED", HTTPStatus: status}
	}
	if p.MaxTextBytes > 0 && len(body) > p.MaxTextBytes {
		return ArticleOutcome{Error: fmt.Sprintf("article text exceeds configured ML limit of %d bytes", p.MaxTextBytes),
			ErrorCode: "ARTICLE_TEXT_TOO_LARGE", HTTPStatus: status}
	}
	item := material{Entry: Entry{ExternalID: job.ExternalID, CanonicalURL: job.ArticleURL,
		Title: job.Title, Body: body, PublishedAt: job.PublishedAt},
		Raw: response.Body, ContentType: response.ContentType, ParserVersion: parserVersion}
	return ArticleOutcome{Item: &item}
}

type ArticleJobRunner struct {
	Store           *ArticleJobStore
	Processor       func(context.Context, ArticleJob) ArticleOutcome
	Settings        func() config.Runtime
	PipelineVersion string
}

func (r ArticleJobRunner) Run(ctx context.Context) error {
	if r.Store == nil || r.Processor == nil || r.Settings == nil {
		return fmt.Errorf("invalid article job runner configuration")
	}
	for ctx.Err() == nil {
		settings := r.Settings()
		job, ok, err := r.Store.Claim(ctx)
		if err != nil {
			return err
		}
		if !ok {
			timer := time.NewTimer(settings.ArticleRetry.Idle.Std())
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
			continue
		}
		if err := r.processOne(ctx, job, settings.ArticleRetry.Lease.Std(),
			settings.ArticleRetry.FinishTimeout.Std()); err != nil && ctx.Err() == nil {
			return err
		}
	}
	return nil
}

func (r ArticleJobRunner) processOne(ctx context.Context, job ArticleJob, leaseDuration, finishTimeout time.Duration) error {
	started := time.Now()
	ctx, span := otel.Tracer("ingestion/articles").Start(ctx, "article.fetch_job",
		trace.WithAttributes(attribute.String("source.id", job.SourceID), attribute.String("article_job.id", job.ID)))
	defer span.End()
	jobCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopped := make(chan struct{})
	renewErrors := make(chan error, 1)
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(leaseDuration / 3)
		defer ticker.Stop()
		for {
			select {
			case <-jobCtx.Done():
				return
			case <-ticker.C:
				if err := r.Store.Renew(jobCtx, job, leaseDuration); err != nil {
					renewErrors <- err
					cancel()
					return
				}
			}
		}
	}()
	outcome := r.Processor(jobCtx, job)
	cancel()
	<-stopped
	select {
	case err := <-renewErrors:
		if !errors.Is(err, context.Canceled) {
			return err
		}
	default:
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), finishTimeout)
	defer finishCancel()
	status, err := r.Store.Finish(finishCtx, job, outcome, r.PipelineVersion)
	if errors.Is(err, ErrLostArticleLease) {
		slog.Warn("discarded stale article result", "article_job_id", job.ID)
		return nil
	}
	if err != nil {
		return err
	}
	observability.Article(status, time.Since(started))
	slog.Info("article fetch job updated", "article_job_id", job.ID, "source_id", job.SourceID,
		"status", status, "attempts", job.Attempts, "error_code", outcome.ErrorCode,
		"http_status", outcome.HTTPStatus, "duration_ms", time.Since(started).Milliseconds(),
		"trace_id", span.SpanContext().TraceID().String())
	return nil
}
