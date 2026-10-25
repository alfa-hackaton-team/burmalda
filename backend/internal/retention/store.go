package retention

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store { return &Store{db: db} }

func Cutoff(now time.Time, days int) time.Time {
	return now.UTC().AddDate(0, 0, -days)
}

func (s *Store) Count(ctx context.Context, cutoff time.Time) (int64, error) {
	var count int64
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM source_polls
		WHERE status IN ('SUCCEEDED', 'PARTIAL', 'FAILED') AND created_at < $1`, cutoff).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count expired source polls: %w", err)
	}
	return count, nil
}

func (s *Store) DeleteBatch(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	var count int64
	err := s.db.QueryRow(ctx, `WITH candidates AS (
		SELECT id FROM source_polls
		WHERE status IN ('SUCCEEDED', 'PARTIAL', 'FAILED') AND created_at < $1
		ORDER BY created_at, id LIMIT $2 FOR UPDATE SKIP LOCKED
	), deleted AS (
		DELETE FROM source_polls AS p USING candidates AS c
		WHERE p.id = c.id RETURNING p.id
	)
	SELECT count(*) FROM deleted`, cutoff, batchSize).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("delete expired source polls: %w", err)
	}
	return count, nil
}
