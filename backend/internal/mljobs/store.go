package mljobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrLostLease = errors.New("ML job lease was lost")

type Job struct {
	VersionNo                                 *int
	ID, Token, VersionID, ArticleID, SourceID string
	DedupeKey, PipelineVersion                string
	SourceName, SourceType, SourceRole        string
	SourceConfigVersion                       int64
	TrustLevel, Title, Body, ContentHash      string
	RawArtifactID                             string
	ExternalID, CanonicalURL                  *string
	Language, Author                          *string
	PublishedAt                               *time.Time
	FetchedAt                                 time.Time
	Metadata                                  json.RawMessage
	DeliveryExtraction                        json.RawMessage
	ProcessingID                              *string
	ProcessingStartedAt                       *time.Time
	Attempts                                  int
}

type Store struct {
	db       *pgxpool.Pool
	Settings func() config.Runtime
}

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

func (s *Store) settings() config.Runtime {
	if s.Settings != nil {
		return s.Settings()
	}
	return config.DefaultRuntime()
}

func (s *Store) Claim(ctx context.Context) (Job, bool, error) {
	settings := s.settings()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM processing_jobs
		WHERE source_snapshot_origin <> 'UNVERIFIED_LEGACY'
		AND ((status IN ('QUEUED','RETRY') AND next_attempt_at <= clock_timestamp())
		    OR (status='RUNNING' AND lease_until <= clock_timestamp()))
		ORDER BY next_attempt_at, id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, fmt.Errorf("claim ML job: %w", err)
	}
	var job Job
	err = tx.QueryRow(ctx, `UPDATE processing_jobs SET status='RUNNING',
		lease_token=gen_random_uuid(), lease_until=clock_timestamp()+($2::double precision*interval '1 second'),
		attempts=attempts+1, updated_at=clock_timestamp()
		WHERE id=$1::uuid RETURNING id::text, lease_token::text,
		article_version_id::text, dedupe_key, pipeline_version,
		source_name, source_type, source_role, source_trust_level, source_config_version,
			external_id, canonical_url, ml_processing_id, processing_started_at, attempts`, id, settings.ML.Lease.Std().Seconds()).Scan(
		&job.ID, &job.Token, &job.VersionID, &job.DedupeKey, &job.PipelineVersion,
		&job.SourceName, &job.SourceType, &job.SourceRole, &job.TrustLevel, &job.SourceConfigVersion,
		&job.ExternalID, &job.CanonicalURL, &job.ProcessingID, &job.ProcessingStartedAt, &job.Attempts)
	if err != nil {
		return Job{}, false, fmt.Errorf("lease ML job: %w", err)
	}
	err = tx.QueryRow(ctx, `SELECT v.article_id::text, v.source_id::text, v.title,
		v.body_text, v.content_hash, v.raw_artifact_id::text, v.language,
		v.author, v.published_at, v.fetched_at, v.metadata, j.delivery_extraction, j.delivery_version_no
		FROM article_versions v JOIN processing_jobs j ON j.id=$2::uuid WHERE v.id=$1::uuid`, job.VersionID, job.ID).Scan(
		&job.ArticleID, &job.SourceID, &job.Title, &job.Body,
		&job.ContentHash, &job.RawArtifactID, &job.Language, &job.Author,
		&job.PublishedAt, &job.FetchedAt, &job.Metadata, &job.DeliveryExtraction, &job.VersionNo)
	if err != nil {
		return Job{}, false, fmt.Errorf("load ML job material: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Job{}, false, fmt.Errorf("commit ML claim: %w", err)
	}
	return job, true, nil
}

func (s *Store) Finish(ctx context.Context, job Job, result Result) (string, error) {
	settings := s.settings()
	status, result := finishDecision(job, result, settings, time.Now())
	var events []byte
	if result.Succeeded {
		if result.DocumentVersionID == "" || result.EventIDs == nil {
			return "", fmt.Errorf("successful ML result is incomplete")
		}
		var err error
		events, err = json.Marshal(result.EventIDs)
		if err != nil {
			return "", err
		}
	}
	if result.ProcessingID != "" && job.ProcessingID != nil && result.ProcessingID != *job.ProcessingID {
		return "", fmt.Errorf("ML processing ID changed for an existing job")
	}
	delay := result.RetryAfter
	if delay <= 0 {
		delay = min(time.Duration(job.Attempts)*settings.ML.RetryStep.Std(), settings.ML.RetryMax.Std())
	}
	if delay > settings.ML.RetryAfterMax.Std() {
		delay = settings.ML.RetryAfterMax.Std()
	}
	var errorRetryable *bool
	if result.Error != "" {
		errorRetryable = &result.ErrorRetryable
	}
	command, err := s.db.Exec(ctx, `UPDATE processing_jobs SET
		status=$3, ml_processing_id=COALESCE(ml_processing_id, NULLIF($4::text,'')),
		processing_started_at=CASE WHEN $4::text<>'' THEN COALESCE(processing_started_at, clock_timestamp())
			ELSE processing_started_at END,
		document_version_id=CASE WHEN $3='SUCCEEDED' THEN $5 ELSE document_version_id END,
		event_ids=CASE WHEN $3='SUCCEEDED' THEN $6::jsonb ELSE event_ids END,
		last_error=$7, last_error_code=$10, last_error_retryable=$11,
		lease_token=NULL, lease_until=NULL,
		attempts=CASE WHEN $9::boolean THEN 0 ELSE attempts END,
		next_attempt_at=clock_timestamp()+($8::double precision*interval '1 second'),
		completed_at=CASE WHEN $3 IN ('SUCCEEDED','FAILED') THEN clock_timestamp() ELSE NULL END,
		updated_at=clock_timestamp()
		WHERE id=$1::uuid AND lease_token=$2::uuid AND lease_until>clock_timestamp()
		AND status='RUNNING'`, job.ID, job.Token, status, result.ProcessingID,
		result.DocumentVersionID, events, nullIfEmpty(result.Error), delay.Seconds(),
		result.Pending && status != "FAILED", nullIfEmpty(result.ErrorCode), errorRetryable)
	if err != nil {
		return "", fmt.Errorf("finish ML job: %w", err)
	}
	if command.RowsAffected() != 1 {
		return "", ErrLostLease
	}
	return status, nil
}

func finishDecision(job Job, result Result, settings config.Runtime, now time.Time) (string, Result) {
	status := "RETRY"
	if result.Succeeded {
		status = "SUCCEEDED"
	} else if result.Permanent || (job.Attempts >= settings.ML.MaxAttempts && !result.Pending) {
		status = "FAILED"
	}
	if result.Pending && result.ProcessingID != "" && job.ProcessingStartedAt != nil &&
		now.Sub(*job.ProcessingStartedAt) >= settings.ML.MaxProcessingDuration.Std() {
		status = "FAILED"
		result.Error = "ML processing exceeded maximum duration"
		result.ErrorCode = "PROCESSING_TIMEOUT"
		result.ErrorRetryable = false
	}
	return status, result
}

func nullIfEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
