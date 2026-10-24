package polling

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alfa-hackaton-team/burmalda/backend/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrLostLease = errors.New("source lease was lost")

type Lease struct {
	SourceID      string
	PollID        string
	Token         string
	Name          string
	SourceType    string
	SourceRole    string
	TrustLevel    string
	AdapterType   string
	AdapterConfig []byte
	Endpoint      string
	ConfigVersion int64
	HTTPETag      *string
	LastModified  *string
	Cursor        *string
}

type Outcome struct {
	Success         bool
	Partial         bool
	HTTPStatus      *int
	DiscoveredCount int
	NewArticleCount int
	NewVersionCount int
	CursorAfter     *string
	Error           string
	RetryAfter      time.Duration
	EntryFailures   []EntryFailure
	Persist         func(context.Context, pgx.Tx) (newArticles, newVersions int, err error)
}

type EntryFailure struct {
	EntryNo      int
	ArticleURL   string
	ExternalID   string
	Title        string
	PublishedAt  *time.Time
	ErrorCode    string
	ErrorMessage string
	HTTPStatus   *int
	RetryAfter   time.Duration
	Retryable    bool
	Queue        bool
}

type Store struct {
	db       *pgxpool.Pool
	Settings func() config.Runtime
}

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

func (s *Store) Claim(ctx context.Context, leaseDuration time.Duration) (Lease, bool, error) {
	if leaseDuration <= 0 {
		return Lease{}, false, fmt.Errorf("lease duration must be positive")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Lease{}, false, fmt.Errorf("begin source claim: %w", err)
	}
	defer tx.Rollback(ctx)

	var lease Lease
	err = tx.QueryRow(ctx, `SELECT id::text, name, source_type, source_role, trust_level,
		adapter_type, adapter_config, endpoint, config_version,
		http_etag, http_last_modified, source_cursor
		FROM sources
		WHERE enabled AND next_poll_at <= clock_timestamp()
		  AND (lease_until IS NULL OR lease_until <= clock_timestamp())
		ORDER BY next_poll_at, id
		FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(
		&lease.SourceID, &lease.Name, &lease.SourceType, &lease.SourceRole,
		&lease.TrustLevel, &lease.AdapterType, &lease.AdapterConfig, &lease.Endpoint, &lease.ConfigVersion,
		&lease.HTTPETag, &lease.LastModified, &lease.Cursor)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, fmt.Errorf("select due source: %w", err)
	}

	_, err = tx.Exec(ctx, `UPDATE source_polls
		SET status='FAILED', finished_at=clock_timestamp(),
		    error_message='source lease expired or was replaced'
		WHERE source_id=$1::uuid AND status='RUNNING'`, lease.SourceID)
	if err != nil {
		return Lease{}, false, fmt.Errorf("close abandoned poll: %w", err)
	}
	err = tx.QueryRow(ctx, `UPDATE sources
		SET lease_token=gen_random_uuid(),
		    lease_until=clock_timestamp() + ($2::double precision * interval '1 second')
		WHERE id=$1::uuid RETURNING lease_token::text`,
		lease.SourceID, leaseDuration.Seconds()).Scan(&lease.Token)
	if err != nil {
		return Lease{}, false, fmt.Errorf("lease source: %w", err)
	}
	err = tx.QueryRow(ctx, `INSERT INTO source_polls
		(source_id, status, started_at, cursor_before)
		VALUES ($1::uuid, 'RUNNING', clock_timestamp(), $2)
		RETURNING id::text`, lease.SourceID, lease.Cursor).Scan(&lease.PollID)
	if err != nil {
		return Lease{}, false, fmt.Errorf("create source poll: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Lease{}, false, fmt.Errorf("commit source claim: %w", err)
	}
	return lease, true, nil
}

func (s *Store) SetTraceID(ctx context.Context, lease Lease, traceID string) error {
	command, err := s.db.Exec(ctx, `UPDATE source_polls AS p SET trace_id=$3
		FROM sources AS s WHERE p.id=$1::uuid AND p.source_id=s.id
		AND s.lease_token=$2::uuid AND p.status='RUNNING'`, lease.PollID, lease.Token, traceID)
	if err != nil {
		return fmt.Errorf("save poll trace ID: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrLostLease
	}
	return nil
}

func (s *Store) Renew(ctx context.Context, lease Lease, leaseDuration time.Duration) error {
	if leaseDuration <= 0 {
		return fmt.Errorf("lease duration must be positive")
	}
	command, err := s.db.Exec(ctx, `UPDATE sources
		SET lease_until=clock_timestamp() + ($3::double precision * interval '1 second')
		WHERE id=$1::uuid AND lease_token=$2::uuid
		  AND lease_until > clock_timestamp()`,
		lease.SourceID, lease.Token, leaseDuration.Seconds())
	if err != nil {
		return fmt.Errorf("renew source lease: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrLostLease
	}
	return nil
}

func (s *Store) Finish(ctx context.Context, lease Lease, result Outcome) error {
	return s.finish(ctx, lease, &result)
}

func (s *Store) FinishResult(ctx context.Context, lease Lease, result Outcome) (Outcome, error) {
	err := s.finish(ctx, lease, &result)
	return result, err
}

func (s *Store) finish(ctx context.Context, lease Lease, result *Outcome) error {
	settings := config.DefaultRuntime()
	if s.Settings != nil {
		settings = s.Settings()
	}
	if result.DiscoveredCount < 0 || result.NewArticleCount < 0 || result.NewVersionCount < 0 {
		return fmt.Errorf("poll counts must not be negative")
	}
	if result.HTTPStatus != nil && (*result.HTTPStatus < 100 || *result.HTTPStatus > 599) {
		return fmt.Errorf("invalid HTTP status")
	}
	if result.RetryAfter < 0 {
		return fmt.Errorf("retry delay must not be negative")
	}
	if result.Partial && (!result.Success || len(result.EntryFailures) == 0) {
		return fmt.Errorf("partial poll requires saved entries and entry failures")
	}
	if result.Partial && result.Error == "" {
		return fmt.Errorf("partial poll requires an error summary")
	}
	if !result.Success && result.Error == "" {
		result.Error = "source poll failed"
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin poll finish: %w", err)
	}
	defer tx.Rollback(ctx)
	var currentToken *string
	var valid bool
	err = tx.QueryRow(ctx, `SELECT lease_token::text,
		COALESCE(lease_until > clock_timestamp(), false)
		FROM sources WHERE id=$1::uuid FOR UPDATE`,
		lease.SourceID).Scan(&currentToken, &valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLostLease
	}
	if err != nil {
		return fmt.Errorf("lock source to finish poll: %w", err)
	}
	if currentToken == nil || *currentToken != lease.Token || !valid {
		return ErrLostLease
	}
	if result.Persist != nil {
		newArticles, newVersions, err := result.Persist(ctx, tx)
		if err != nil {
			return fmt.Errorf("persist poll results: %w", err)
		}
		result.NewArticleCount = newArticles
		result.NewVersionCount = newVersions
		if newArticles < 0 || newVersions < 0 {
			return fmt.Errorf("persist returned negative counts")
		}
	}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(lease_until > clock_timestamp(), false)
		FROM sources WHERE id=$1::uuid`, lease.SourceID).Scan(&valid); err != nil {
		return fmt.Errorf("recheck source lease: %w", err)
	}
	if !valid {
		return ErrLostLease
	}
	for _, failure := range result.EntryFailures {
		if failure.EntryNo < 1 || failure.ErrorCode == "" || failure.ErrorMessage == "" {
			return fmt.Errorf("invalid source entry failure")
		}
		if _, err := tx.Exec(ctx, `INSERT INTO source_poll_entry_failures
			(poll_id, entry_no, article_url, error_code, error_message)
			VALUES ($1::uuid, $2, $3, $4, $5)`, lease.PollID, failure.EntryNo,
			failure.ArticleURL, failure.ErrorCode, failure.ErrorMessage); err != nil {
			return fmt.Errorf("save source entry failure: %w", err)
		}
		if failure.Queue {
			if failure.ArticleURL == "" {
				return fmt.Errorf("queued article failure needs a URL")
			}
			jobStatus := "DEAD"
			delay := time.Duration(0)
			if failure.Retryable && settings.ArticleRetry.MaxAttempts > 1 {
				jobStatus = "RETRY"
				delay = RetryDelay(1, failure.RetryAfter, settings)
			}
			_, err := tx.Exec(ctx, `INSERT INTO article_fetch_jobs
				(source_id, article_url, external_id, title, published_at, status,
				 attempts, next_attempt_at, last_error, last_error_code, last_http_status, completed_at)
				VALUES ($1::uuid, $2, $3, $4, $5, $6, 1,
				 clock_timestamp()+($7::double precision*interval '1 second'), $8, $9, $10,
				 CASE WHEN $6='DEAD' THEN clock_timestamp() ELSE NULL END)
				ON CONFLICT (source_id, article_url) DO UPDATE SET
				 external_id=EXCLUDED.external_id, title=EXCLUDED.title,
				 published_at=EXCLUDED.published_at, status=EXCLUDED.status,
				 attempts=1, next_attempt_at=EXCLUDED.next_attempt_at,
				 lease_token=NULL, lease_until=NULL, last_error=EXCLUDED.last_error,
				 last_error_code=EXCLUDED.last_error_code, last_http_status=EXCLUDED.last_http_status,
				 completed_at=EXCLUDED.completed_at, updated_at=clock_timestamp()
				WHERE article_fetch_jobs.status='SUCCEEDED'`,
				lease.SourceID, failure.ArticleURL, failure.ExternalID, failure.Title,
				failure.PublishedAt, jobStatus, delay.Seconds(), failure.ErrorMessage,
				failure.ErrorCode, failure.HTTPStatus)
			if err != nil {
				return fmt.Errorf("queue failed article: %w", err)
			}
		}
	}
	status := "FAILED"
	var errorMessage *string
	if result.Success {
		status = "SUCCEEDED"
		if result.Partial {
			status = "PARTIAL"
			errorMessage = &result.Error
		}
	} else {
		errorMessage = &result.Error
	}
	command, err := tx.Exec(ctx, `UPDATE source_polls SET
		status=$3, finished_at=clock_timestamp(), http_status=$4,
		discovered_count=$5, new_article_count=$6, new_version_count=$7,
		cursor_after=$8, error_message=$9, failed_entry_count=$10
		WHERE id=$1::uuid AND source_id=$2::uuid AND status='RUNNING'`,
		lease.PollID, lease.SourceID, status, result.HTTPStatus,
		result.DiscoveredCount, result.NewArticleCount, result.NewVersionCount,
		result.CursorAfter, errorMessage, len(result.EntryFailures))
	if err != nil {
		return fmt.Errorf("finish poll record: %w", err)
	}
	if command.RowsAffected() != 1 {
		return ErrLostLease
	}
	_, err = tx.Exec(ctx, `UPDATE sources SET
		lease_token=NULL, lease_until=NULL,
		next_poll_at=clock_timestamp() +
			(GREATEST(polling_interval_seconds::double precision, $5::double precision) * interval '1 second'),
		last_sync_at=clock_timestamp(),
		last_success_at=CASE WHEN $2::boolean AND NOT $6::boolean THEN clock_timestamp() ELSE last_success_at END,
		health=CASE WHEN $2::boolean AND NOT $6::boolean THEN 'HEALTHY' ELSE 'DEGRADED' END,
		last_error=$3,
		source_cursor=CASE WHEN $2::boolean AND NOT $6::boolean AND $4::text IS NOT NULL
			THEN $4::text ELSE source_cursor END
		WHERE id=$1::uuid`, lease.SourceID, result.Success, errorMessage,
		result.CursorAfter, result.RetryAfter.Seconds(), result.Partial)
	if err != nil {
		return fmt.Errorf("finish source poll: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit poll result: %w", err)
	}
	return nil
}

func (s *Store) ReapExpired(ctx context.Context, limit int) (int64, error) {
	if limit < 1 {
		return 0, fmt.Errorf("limit must be positive")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin expired lease cleanup: %w", err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT s.id::text FROM sources AS s
		WHERE s.lease_until <= clock_timestamp()
		   OR (s.lease_token IS NULL AND EXISTS (
			SELECT 1 FROM source_polls AS p
			WHERE p.source_id=s.id AND p.status='RUNNING'))
		ORDER BY s.id FOR UPDATE OF s SKIP LOCKED LIMIT $1`, limit)
	if err != nil {
		return 0, fmt.Errorf("select expired leases: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan expired source: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("read expired sources: %w", err)
	}
	rows.Close()
	var closed int64
	for _, id := range ids {
		command, err := tx.Exec(ctx, `UPDATE source_polls SET
			status='FAILED', finished_at=clock_timestamp(),
			error_message='source lease expired or was replaced'
			WHERE source_id=$1::uuid AND status='RUNNING'`, id)
		if err != nil {
			return 0, fmt.Errorf("close expired poll: %w", err)
		}
		closed += command.RowsAffected()
		_, err = tx.Exec(ctx, `UPDATE sources SET lease_token=NULL, lease_until=NULL
			WHERE id=$1::uuid AND lease_until <= clock_timestamp()`, id)
		if err != nil {
			return 0, fmt.Errorf("clear expired lease: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit expired lease cleanup: %w", err)
	}
	return closed, nil
}
