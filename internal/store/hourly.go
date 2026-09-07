package store

import (
	"context"
	"time"

	"github.com/SamWang8891/is-ntust-down/internal/probe"
)

// HourBucket is one cell of the hourly strip.
type HourBucket struct {
	Hour      time.Time
	State     probe.State
	UptimePct *float64
	Up        int
	Degraded  int
	Down      int
	Unknown   int
	Retired   int
}

func (b HourBucket) Conclusive() int { return b.Up + b.Degraded + b.Down }

// RollupHourly recomputes the last `hours` local hours.
//
// Like the daily rollup this is a full recompute rather than an increment, so
// running it repeatedly is free and a crash mid-hour cannot leave a bucket
// permanently half-counted.
func (s *Store) RollupHourly(ctx context.Context, hours int, now time.Time) error {
	since := now.Add(-time.Duration(hours) * time.Hour).Truncate(time.Hour)

	_, err := s.pool.Exec(ctx,
		`INSERT INTO check_hourly
		     (check_key, hour, up_count, degraded_count, down_count, unknown_count, retired_count, uptime_pct)
		 SELECT check_key,
		        date_trunc('hour', observed_at AT TIME ZONE $2) AS hour,
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
		  WHERE observed_at >= $1
		  GROUP BY check_key, 2
		 ON CONFLICT (check_key, hour) DO UPDATE SET
		     up_count       = EXCLUDED.up_count,
		     degraded_count = EXCLUDED.degraded_count,
		     down_count     = EXCLUDED.down_count,
		     unknown_count  = EXCLUDED.unknown_count,
		     retired_count  = EXCLUDED.retired_count,
		     uptime_pct     = EXCLUDED.uptime_pct`,
		since, s.tz.String())
	return err
}

// HourlyHistory returns the last `hours` local hours for every check, oldest
// first, with empty hours present so every strip keeps a fixed width and the
// hours line up across services.
func (s *Store) HourlyHistory(ctx context.Context, hours int, now time.Time) (map[string][]HourBucket, error) {
	end := startOfHour(now.In(s.tz), s.tz)
	start := end.Add(-time.Duration(hours-1) * time.Hour)

	rows, err := s.pool.Query(ctx,
		`SELECT check_key, hour, up_count, degraded_count, down_count, unknown_count, retired_count, uptime_pct
		   FROM check_hourly
		  WHERE hour >= $1 AND hour <= $2`,
		naiveLocal(start), naiveLocal(end))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byKey := map[string]map[string]HourBucket{}
	for rows.Next() {
		var key string
		var hour time.Time
		var upC, degC, downC, unkC, retC int
		var pct *float64
		if err := rows.Scan(&key, &hour, &upC, &degC, &downC, &unkC, &retC, &pct); err != nil {
			return nil, err
		}
		// `hour` comes back as a naive timestamp; re-anchor it in the display
		// zone so downstream formatting and comparisons agree.
		local := time.Date(hour.Year(), hour.Month(), hour.Day(), hour.Hour(), 0, 0, 0, s.tz)
		b := HourBucket{
			Hour: local, UptimePct: pct,
			Up: upC, Degraded: degC, Down: downC, Unknown: unkC, Retired: retC,
			State: dayState(upC, degC, downC, unkC, retC),
		}
		if byKey[key] == nil {
			byKey[key] = map[string]HourBucket{}
		}
		byKey[key][local.Format("2006-01-02T15")] = b
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// The current hour comes straight from raw results rather than from the
	// rollup. The rollup only runs every ROLLUP_INTERVAL, so the newest box —
	// the one answering "is it broken right now", which is the entire point of
	// an hourly view — would otherwise sit grey for minutes after every probe
	// and stay grey for the whole interval after a restart.
	live, err := s.currentHourBuckets(ctx, end)
	if err != nil {
		return nil, err
	}
	for key, b := range live {
		if byKey[key] == nil {
			byKey[key] = map[string]HourBucket{}
		}
		byKey[key][end.Format("2006-01-02T15")] = b
	}

	out := map[string][]HourBucket{}
	for key, cells := range byKey {
		series := make([]HourBucket, 0, hours)
		for h := start; !h.After(end); h = h.Add(time.Hour) {
			if b, ok := cells[h.Format("2006-01-02T15")]; ok {
				series = append(series, b)
			} else {
				series = append(series, HourBucket{Hour: h, State: probe.StateUnknown})
			}
		}
		out[key] = series
	}
	return out, nil
}

// currentHourBuckets aggregates the hour in progress directly. It is one
// indexed range scan over at most an hour of rows per check, which is cheap
// enough to run on a page load and is what keeps the newest box honest.
func (s *Store) currentHourBuckets(ctx context.Context, hourStart time.Time) (map[string]HourBucket, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT check_key,
		        COUNT(*) FILTER (WHERE state = 'up'),
		        COUNT(*) FILTER (WHERE state = 'degraded'),
		        COUNT(*) FILTER (WHERE state = 'down'),
		        COUNT(*) FILTER (WHERE state = 'unknown'),
		        COUNT(*) FILTER (WHERE state = 'retired')
		   FROM check_results
		  WHERE observed_at >= $1
		  GROUP BY check_key`, hourStart)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]HourBucket{}
	for rows.Next() {
		var key string
		var up, deg, down, unk, ret int
		if err := rows.Scan(&key, &up, &deg, &down, &unk, &ret); err != nil {
			return nil, err
		}
		b := HourBucket{
			Hour: hourStart, Up: up, Degraded: deg, Down: down, Unknown: unk, Retired: ret,
			State: dayState(up, deg, down, unk, ret),
		}
		if c := b.Conclusive(); c > 0 {
			pct := 100 * float64(up) / float64(c)
			b.UptimePct = &pct
		}
		out[key] = b
	}
	return out, rows.Err()
}

// PruneHourly drops hourly buckets past their retention window.
func (s *Store) PruneHourly(ctx context.Context, days int, now time.Time) error {
	cutoff := now.In(s.tz).AddDate(0, 0, -days)
	_, err := s.pool.Exec(ctx, `DELETE FROM check_hourly WHERE hour < $1`, naiveLocal(cutoff))
	return err
}

func startOfHour(t time.Time, loc *time.Location) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, loc)
}

// naiveLocal strips the zone so a local wall-clock time compares correctly
// against the naive `hour` column.
func naiveLocal(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
}
