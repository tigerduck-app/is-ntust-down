package scheduler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

type countingRepo struct {
	*fakeRepo
	rollups       int
	hourlyRollups int
	prunes        int
	hourlyPrunes  int
}

func (c *countingRepo) Rollup(context.Context, []time.Time) error {
	c.rollups++
	return nil
}

func (c *countingRepo) RollupHourly(context.Context, int, time.Time) error {
	c.hourlyRollups++
	return nil
}

func (c *countingRepo) Prune(context.Context, int, int, time.Time) error {
	c.prunes++
	return nil
}

func (c *countingRepo) PruneHourly(context.Context, int, time.Time) error {
	c.hourlyPrunes++
	return nil
}

func TestRollupRunsOftenAndPruneRunsHourly(t *testing.T) {
	repo := &countingRepo{fakeRepo: newFakeRepo()}
	clock := &fakeClock{now: time.Now()}
	s := New(repo, nil, defaultGuard(), slog.New(slog.NewTextHandler(io.Discard, nil))).WithClock(clock)

	cfg := MaintenanceConfig{
		Interval: 5 * time.Minute, RawRetentionDays: 14,
		DailyRetentionDays: 400, HourlyRetentionDays: 30, HistoryHours: 24,
	}

	// Twelve five-minute ticks is one hour.
	for range 12 {
		s.maintain(context.Background(), cfg)
		clock.advance(5 * time.Minute)
	}

	// The page's history strip reads only from the daily table, so the rollup
	// must keep up with the probes rather than with the retention window.
	if repo.rollups != 12 {
		t.Errorf("rollups = %d, want 12", repo.rollups)
	}
	// Pruning is a bulk delete guarding a window measured in days; running it
	// every tick would be pure load.
	if repo.prunes != 1 || repo.hourlyPrunes != 1 {
		t.Errorf("prunes = %d/%d, want 1 each in the first hour", repo.prunes, repo.hourlyPrunes)
	}
	// The hourly strip is the page's default view, so its rollup has to keep
	// pace with the probes exactly as the daily one does.
	if repo.hourlyRollups != 12 {
		t.Errorf("hourly rollups = %d, want 12", repo.hourlyRollups)
	}

	s.maintain(context.Background(), cfg)
	if repo.prunes != 2 {
		t.Errorf("prunes = %d, want 2 once an hour has elapsed", repo.prunes)
	}
}

func TestRollupAlwaysRecomputesYesterday(t *testing.T) {
	repo := newFakeRepo()
	clock := &fakeClock{now: time.Date(2026, 9, 8, 0, 3, 0, 0, time.UTC)}
	s := New(&recordingRepo{fakeRepo: repo}, nil, defaultGuard(),
		slog.New(slog.NewTextHandler(io.Discard, nil))).WithClock(clock)

	s.maintain(context.Background(), MaintenanceConfig{Interval: time.Minute})

	// A restart three minutes after midnight must not leave yesterday
	// permanently half-counted on the strip.
	got := s.store.(*recordingRepo).days
	if len(got) != 2 {
		t.Fatalf("rolled up %d days, want 2", len(got))
	}
	if got[1].Day() != 7 {
		t.Errorf("second day = %v, want the 7th (yesterday)", got[1])
	}
}

type recordingRepo struct {
	*fakeRepo
	days []time.Time
}

func (r *recordingRepo) Rollup(_ context.Context, days []time.Time) error {
	r.days = days
	return nil
}

func (r *recordingRepo) RollupHourly(context.Context, int, time.Time) error { return nil }
func (r *recordingRepo) Prune(context.Context, int, int, time.Time) error   { return nil }
func (r *recordingRepo) PruneHourly(context.Context, int, time.Time) error  { return nil }
