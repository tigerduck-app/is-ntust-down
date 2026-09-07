package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// BreakerRecord is the persisted state of one credentialed check's guard.
type BreakerRecord struct {
	CheckKey         string
	ConsecutiveFails int
	OpenUntil        *time.Time
	BackoffSeconds   int
}

// LoadBreaker returns the stored guard state, or a zero record when the check
// has never tripped.
func (s *Store) LoadBreaker(ctx context.Context, key string) (BreakerRecord, error) {
	rec := BreakerRecord{CheckKey: key}
	err := s.pool.QueryRow(ctx,
		`SELECT consecutive_fails, open_until, backoff_seconds
		   FROM breaker_state WHERE check_key = $1`, key,
	).Scan(&rec.ConsecutiveFails, &rec.OpenUntil, &rec.BackoffSeconds)
	if errors.Is(err, pgx.ErrNoRows) {
		return rec, nil
	}
	return rec, err
}

func (s *Store) SaveBreaker(ctx context.Context, rec BreakerRecord) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO breaker_state (check_key, consecutive_fails, open_until, backoff_seconds)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (check_key) DO UPDATE SET
		     consecutive_fails = EXCLUDED.consecutive_fails,
		     open_until        = EXCLUDED.open_until,
		     backoff_seconds   = EXCLUDED.backoff_seconds`,
		rec.CheckKey, rec.ConsecutiveFails, rec.OpenUntil, rec.BackoffSeconds)
	return err
}

// CountAttemptsSince counts credentialed attempts actually made in a window.
//
// Skipped runs are recorded as `unknown` and excluded: the daily ceiling
// exists to bound how many times we present credentials to NTUST, and a run
// the guard declined never touched them.
func (s *Store) CountAttemptsSince(ctx context.Context, key string, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM check_results
		  WHERE check_key = $1 AND observed_at >= $2 AND state <> 'unknown'`,
		key, since).Scan(&n)
	return n, err
}
