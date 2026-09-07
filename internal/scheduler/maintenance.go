package scheduler

import (
	"context"
	"time"
)

// MaintenanceConfig bounds how long history is kept.
type MaintenanceConfig struct {
	Interval            time.Duration
	RawRetentionDays    int
	DailyRetentionDays  int
	HourlyRetentionDays int
	HistoryHours        int
}

// pruneEvery decouples deletion from aggregation. The rollup is cheap and
// wants to run often — the page's history strip reads only from the daily
// table, so until it runs a fresh deploy renders no history at all. Pruning
// is a bulk delete that only needs to keep up with a retention window
// measured in days.
const pruneEvery = time.Hour

// RunMaintenance rolls raw observations into daily buckets and prunes expired
// history until ctx is cancelled.
func (s *Scheduler) RunMaintenance(ctx context.Context, cfg MaintenanceConfig) {
	s.maintain(ctx, cfg)

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.maintain(ctx, cfg)
		}
	}
}

func (s *Scheduler) maintain(ctx context.Context, cfg MaintenanceConfig) {
	now := s.clock.Now()

	// Yesterday is recomputed alongside today on every tick. A restart or a
	// crash across midnight would otherwise leave that day permanently
	// half-counted, and the rollup is a full recompute so redoing it is free.
	days := []time.Time{now, now.AddDate(0, 0, -1)}
	if err := s.store.Rollup(ctx, days); err != nil {
		s.log.Error("rollup failed", "err", err)
	}
	if err := s.store.RollupHourly(ctx, cfg.HistoryHours, now); err != nil {
		s.log.Error("hourly rollup failed", "err", err)
	}
	if now.Sub(s.lastPrune) < pruneEvery {
		return
	}
	s.lastPrune = now
	if err := s.store.Prune(ctx, cfg.RawRetentionDays, cfg.DailyRetentionDays, now); err != nil {
		s.log.Error("prune failed", "err", err)
	}
	if err := s.store.PruneHourly(ctx, cfg.HourlyRetentionDays, now); err != nil {
		s.log.Error("hourly prune failed", "err", err)
	}
}
