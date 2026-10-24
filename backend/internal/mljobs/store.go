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
	ID, Token, VersionID, ArticleID, SourceID string
	DedupeKey, PipelineVersion                string
	SourceName, SourceType, SourceRole        string
	TrustLevel, Title, Body, ContentHash      string
	RawArtifactID                             string
	ExternalID, CanonicalURL                  *string
	Language, Author                          *string
	PublishedAt                               *time.Time
	FetchedAt                                 time.Time
	Metadata                                  json.RawMessage
	ProcessingID                              *string
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
		WHERE ((status IN ('QUEUED','RETRY') AND next_attempt_at <= clock_timestamp())
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
		source_name, source_type, source_role, source_trust_level,
		external_id, canonical_url, ml_processing_id, attempts`, id, settings.ML.Lease.Std().Seconds()).Scan(
		&job.ID, &job.Token, &job.VersionID, &job.DedupeKey, &job.PipelineVersion,
		&job.SourceName, &job.SourceType, &job.SourceRole, &job.TrustLevel,
		&job.ExternalID, &job.CanonicalURL, &job.ProcessingID, &job.Attempts)
	if err != nil {
		return Job{}, false, fmt.Errorf("lease ML job: %w", err)
	}
	err = tx.QueryRow(ctx, `SELECT v.article_id::text, v.source_id::text, v.title,
		v.body_text, v.content_hash, v.raw_artifact_id::text, v.language,
		v.author, v.published_at, v.fetched_at, v.metadata
		FROM article_versions v WHERE v.id=$1::uuid`, job.VersionID).Scan(
		&job.ArticleID, &job.SourceID, &job.Title, &job.Body,
		&job.ContentHash, &job.RawArtifactID, &job.Language, &job.Author,
		&job.PublishedAt, &job.FetchedAt, &job.Metadata)
	if err != nil {
		return Job{}, false, fmt.Errorf("load ML job material: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Job{}, false, fmt.Errorf("commit ML claim: %w", err)
	}
	return job, true, nil
}

func (s *Store) Finish(ctx context.Context, job Job, result Result) error {
	settings := s.settings()
	status := "RETRY"
	if result.Succeeded {
		status = "SUCCEEDED"
	} else if result.Permanent || (job.Attempts >= settings.ML.MaxAttempts && !result.Pending) {
		status = "FAILED"
	}
	var events []byte
	if result.Succeeded {
		if result.DocumentVersionID == "" || result.EventIDs == nil {
			return fmt.Errorf("successful ML result is incomplete")
		}
		var err error
		events, err = json.Marshal(result.EventIDs)
		if err != nil {
			return err
		}
	}
	if result.ProcessingID != "" && job.ProcessingID != nil && result.ProcessingID != *job.ProcessingID {
		return fmt.Errorf("ML processing ID changed for an existing job")
	}
	delay := result.RetryAfter
	if delay <= 0 {
		delay = min(time.Duration(job.Attempts)*settings.ML.RetryStep.Std(), settings.ML.RetryMax.Std())
	}
	if delay > settings.ML.RetryAfterMax.Std() {
		delay = settings.ML.RetryAfterMax.Std()
	}
	command, err := s.db.Exec(ctx, `UPDATE processing_jobs SET
		status=$3, ml_processing_id=COALESCE(ml_processing_id, NULLIF($4::text,'')),
		document_version_id=CASE WHEN $3='SUCCEEDED' THEN $5 ELSE document_version_id END,
		event_ids=CASE WHEN $3='SUCCEEDED' THEN $6::jsonb ELSE event_ids END,
		last_error=$7, lease_token=NULL, lease_until=NULL,
		attempts=CASE WHEN $9::boolean THEN 0 ELSE attempts END,
		next_attempt_at=clock_timestamp()+($8::double precision*interval '1 second'),
		completed_at=CASE WHEN $3 IN ('SUCCEEDED','FAILED') THEN clock_timestamp() ELSE NULL END,
		updated_at=clock_timestamp()
		WHERE id=$1::uuid AND lease_token=$2::uuid AND lease_until>clock_timestamp()
		AND status='RUNNING'`, job.ID, job.Token, status, result.ProcessingID,
		result.DocumentVersionID, events, nullIfEmpty(result.Error), delay.Seconds(), result.Pending)
	if err != nil {
		return fmt.Errorf("finish ML job: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrLostLease
	}
	return nil
}

func nullIfEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
