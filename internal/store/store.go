// Package store owns persistence. It knows about probe results and nothing
// about how they were obtained.
package store

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SamWang8891/is-ntust-up/internal/probe"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store is the Postgres-backed history.
type Store struct {
	pool *pgxpool.Pool
	tz   *time.Location
}

// Open connects and verifies the connection. tzName buckets days for the
// rollup: a student reading 「9月7日」 means the Taipei day, and bucketing in
// UTC would split it across two bars on the strip.
func Open(ctx context.Context, dsn, tzName string) (*Store, error) {
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return nil, fmt.Errorf("display timezone %q: %w", tzName, err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Store{pool: pool, tz: loc}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Location() *time.Location { return s.tz }

// Migrate applies the embedded schema. The statements are idempotent, so this
// runs on every boot without bookkeeping.
func (s *Store) Migrate(ctx context.Context) error {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		sqlBytes, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, string(sqlBytes)); err != nil {
			return fmt.Errorf("migration %s: %w", name, err)
		}
	}
	return nil
}

// Current is a check's live state.
type Current struct {
	CheckKey      string
	State         probe.State
	Since         time.Time
	LastCheckedAt time.Time
	LatencyMS     *int
	ReasonCode    probe.ReasonCode
}

// RecordResult appends an observation and refreshes the live state.
func (s *Store) RecordResult(ctx context.Context, key string, r probe.Result, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var latency *int
	if r.LatencyMS > 0 {
		latency = &r.LatencyMS
	}
	var status *int
	if r.HTTPStatus > 0 {
		status = &r.HTTPStatus
	}
	var reason *string
	if r.ReasonCode != probe.ReasonNone {
		v := string(r.ReasonCode)
		reason = &v
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO check_results (check_key, observed_at, state, latency_ms, http_status, reason_code)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		key, now, string(r.State), latency, status, reason,
	); err != nil {
		return err
	}

	// `since` advances only when the state actually changes, so the page can
	// say "up since Monday" rather than "up since 40 seconds ago".
	if _, err := tx.Exec(ctx,
		`INSERT INTO check_current (check_key, state, since, last_checked_at, latency_ms, reason_code)
		 VALUES ($1, $2, $3, $3, $4, $5)
		 ON CONFLICT (check_key) DO UPDATE SET
		     state           = EXCLUDED.state,
		     since           = CASE WHEN check_current.state = EXCLUDED.state
		                            THEN check_current.since ELSE EXCLUDED.since END,
		     last_checked_at = EXCLUDED.last_checked_at,
		     latency_ms      = EXCLUDED.latency_ms,
		     reason_code     = EXCLUDED.reason_code`,
		key, string(r.State), now, latency, reason,
	); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// CurrentStates returns every check's live state, keyed by check key.
func (s *Store) CurrentStates(ctx context.Context) (map[string]Current, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT check_key, state, since, last_checked_at, latency_ms, reason_code FROM check_current`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]Current{}
	for rows.Next() {
		var c Current
		var state string
		var reason *string
		if err := rows.Scan(&c.CheckKey, &state, &c.Since, &c.LastCheckedAt, &c.LatencyMS, &reason); err != nil {
			return nil, err
		}
		c.State = probe.State(state)
		if reason != nil {
			c.ReasonCode = probe.ReasonCode(*reason)
		}
		out[c.CheckKey] = c
	}
	return out, rows.Err()
}

// DayBucket is one cell of the uptime strip. UptimePct is nil when the day
// holds no conclusive samples, which renders as a blank bar rather than a
// misleading 0%.
type DayBucket struct {
	Day       time.Time
	State     probe.State
	UptimePct *float64
	Up        int
	Degraded  int
	Down      int
	Unknown   int
	Retired   int
}

// Conclusive counts the samples that say something about availability.
// Unknown and retired samples are excluded from both sides of an uptime
// ratio: one is missing data and the other is a deliberate end state, and
// counting either would let the page invent an outage or hide one.
func (b DayBucket) Conclusive() int { return b.Up + b.Degraded + b.Down }

// DailyHistory returns the last `days` local days for every check, oldest
// first, with days that have no data present but empty so the strip keeps a
// fixed width and dates line up across services.
func (s *Store) DailyHistory(ctx context.Context, days int, now time.Time) (map[string][]DayBucket, error) {
	local := now.In(s.tz)
	end := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, s.tz)
	start := end.AddDate(0, 0, -(days - 1))

	rows, err := s.pool.Query(ctx,
		`SELECT check_key, day, up_count, degraded_count, down_count, unknown_count, retired_count, uptime_pct
		   FROM check_daily
		  WHERE day >= $1 AND day <= $2`,
		start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type cell struct {
		bucket DayBucket
	}
	byKey := map[string]map[string]cell{}
	for rows.Next() {
		var key string
		var day time.Time
		var upC, degC, downC, unkC, retC int
		var pct *float64
		if err := rows.Scan(&key, &day, &upC, &degC, &downC, &unkC, &retC, &pct); err != nil {
			return nil, err
		}
		b := DayBucket{
			Day:       day,
			UptimePct: pct,
			Up:        upC,
			Degraded:  degC,
			Down:      downC,
			Unknown:   unkC,
			Retired:   retC,
			State:     dayState(upC, degC, downC, unkC, retC),
		}
		if byKey[key] == nil {
			byKey[key] = map[string]cell{}
		}
		byKey[key][day.Format("2006-01-02")] = cell{bucket: b}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := map[string][]DayBucket{}
	for key, cells := range byKey {
		series := make([]DayBucket, 0, days)
		for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
			if c, ok := cells[d.Format("2006-01-02")]; ok {
				series = append(series, c.bucket)
			} else {
				series = append(series, DayBucket{Day: d, State: probe.StateUnknown})
			}
		}
		out[key] = series
	}
	return out, nil
}

// dayState summarises a day for the strip.
func dayState(up, degraded, down, unknown, retired int) probe.State {
	conclusive := up + degraded + down
	switch {
	case conclusive == 0 && retired > 0:
		return probe.StateRetired
	case conclusive == 0:
		return probe.StateUnknown
	case down == conclusive:
		return probe.StateDown
	case down > 0 || degraded > 0:
		return probe.StateDegraded
	default:
		return probe.StateUp
	}
}

// Rollup recomputes daily buckets for the given local days.
//
// It is a full recompute rather than an increment, which makes it idempotent:
// running it twice, or after a crash mid-day, produces the same rows. That is
// what lets it safely recompute yesterday as well as today on every tick, so a
// restart across midnight cannot leave a permanent hole in the strip.
func (s *Store) Rollup(ctx context.Context, days []time.Time) error {
	if len(days) == 0 {
		return nil
	}
	dates := make([]time.Time, len(days))
	for i, d := range days {
		local := d.In(s.tz)
		dates[i] = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO check_daily
		     (check_key, day, up_count, degraded_count, down_count, unknown_count, retired_count, uptime_pct)
		 SELECT check_key,
		        (observed_at AT TIME ZONE $2)::date AS day,
		        COUNT(*) FILTER (WHERE state = 'up'),
		        COUNT(*) FILTER (WHERE state = 'degraded'),
		        COUNT(*) FILTER (WHERE state = 'down'),
		        COUNT(*) FILTER (WHERE state = 'unknown'),
		        COUNT(*) FILTER (WHERE state = 'retired'),
		        CASE WHEN COUNT(*) FILTER (WHERE state IN ('up','degraded','down')) = 0
		             THEN NULL
		             ELSE ROUND(
		                 100.0 * COUNT(*) FILTER (WHERE state = 'up')
		                       / COUNT(*) FILTER (WHERE state IN ('up','degraded','down')), 2)
		        END
		   FROM check_results
		  WHERE (observed_at AT TIME ZONE $2)::date = ANY($1::date[])
		  GROUP BY check_key, 2
		 ON CONFLICT (check_key, day) DO UPDATE SET
		     up_count       = EXCLUDED.up_count,
		     degraded_count = EXCLUDED.degraded_count,
		     down_count     = EXCLUDED.down_count,
		     unknown_count  = EXCLUDED.unknown_count,
		     retired_count  = EXCLUDED.retired_count,
		     uptime_pct     = EXCLUDED.uptime_pct`,
		dates, s.tz.String())
	return err
}

// Prune drops history past its retention window.
func (s *Store) Prune(ctx context.Context, rawDays, dailyDays int, now time.Time) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM check_results WHERE observed_at < $1`,
		now.AddDate(0, 0, -rawDays)); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx,
		`DELETE FROM check_daily WHERE day < $1`,
		now.In(s.tz).AddDate(0, 0, -dailyDays))
	return err
}
