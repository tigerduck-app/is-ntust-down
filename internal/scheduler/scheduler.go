// Package scheduler decides when each probe runs. It is the only component
// that knows an NTUST account can be locked out, and therefore the only one
// allowed to decline to run a check.
package scheduler

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/SamWang8891/is-ntust-up/internal/probe"
	"github.com/SamWang8891/is-ntust-up/internal/store"
)

// Clock is injectable so backoff and the daily ceiling can be tested in
// milliseconds instead of hours.
type Clock interface {
	Now() time.Time
}

// Repository is the slice of storage the scheduler needs. Narrowing it to an
// interface keeps the lockout guard — the logic most expensive to get wrong —
// testable without a database.
type Repository interface {
	RecordResult(ctx context.Context, key string, r probe.Result, now time.Time) error
	CurrentStates(ctx context.Context) (map[string]store.Current, error)
	LoadBreaker(ctx context.Context, key string) (store.BreakerRecord, error)
	SaveBreaker(ctx context.Context, rec store.BreakerRecord) error
	CountAttemptsSince(ctx context.Context, key string, since time.Time) (int, error)
	Rollup(ctx context.Context, days []time.Time) error
	RollupHourly(ctx context.Context, hours int, now time.Time) error
	Prune(ctx context.Context, rawDays, dailyDays int, now time.Time) error
	PruneHourly(ctx context.Context, days int, now time.Time) error
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Job pairs a probe with its cadence and its guard.
type Job struct {
	Probe    probe.Probe
	Interval time.Duration

	// Guarded marks a probe that spends real credentials against NTUST.
	Guarded bool

	// GateCheckKey names a check that must currently be up before a guarded
	// probe may run. Attempting a login while the login page is known broken
	// spends the lockout budget to learn something already known.
	GateCheckKey string
}

// maxStartJitter bounds the boot-time stagger.
const maxStartJitter = 30 * time.Second

// GuardConfig bounds credentialed probing.
type GuardConfig struct {
	FailureThreshold  int
	BackoffInitial    time.Duration
	BackoffMax        time.Duration
	MaxAttemptsPerDay int
}

type Scheduler struct {
	store Repository
	jobs  []Job
	guard GuardConfig
	clock Clock
	log   *slog.Logger

	lastPrune time.Time
}

func New(st Repository, jobs []Job, guard GuardConfig, log *slog.Logger) *Scheduler {
	return &Scheduler{store: st, jobs: jobs, guard: guard, clock: systemClock{}, log: log}
}

// WithClock replaces the clock. Tests only.
func (s *Scheduler) WithClock(c Clock) *Scheduler { s.clock = c; return s }

// Run sweeps every check once immediately, then blocks until ctx is
// cancelled, running each job on its own cadence.
func (s *Scheduler) Run(ctx context.Context) {
	s.RunInitialSweep(ctx)

	var wg sync.WaitGroup
	for _, job := range s.jobs {
		wg.Add(1)
		go func(j Job) {
			defer wg.Done()
			s.runJob(ctx, j)
		}(job)
	}
	wg.Wait()
}

// RunInitialSweep gives every check a result before the page is first served.
//
// It runs in two phases. Unguarded probes go first and concurrently, so a
// restart produces a fully populated page in about one probe timeout rather
// than after a full interval. The guarded credentialed probes follow, because
// their gate reads the very state this sweep has just written — firing them
// alongside the structural checks would have them consult whatever was in the
// database from the previous run.
func (s *Scheduler) RunInitialSweep(ctx context.Context) {
	var wg sync.WaitGroup
	for _, job := range s.jobs {
		if job.Guarded {
			continue
		}
		wg.Add(1)
		go func(j Job) {
			defer wg.Done()
			s.RunOnce(ctx, j)
		}(job)
	}
	wg.Wait()

	for _, job := range s.jobs {
		if !job.Guarded || ctx.Err() != nil {
			continue
		}
		// Without this a crash-looping container would present credentials to
		// NTUST on every restart. The daily ceiling bounds the damage, but
		// re-spending a login we already have a fresh answer for is pure risk
		// for no information.
		if s.hasFreshResult(ctx, job) {
			s.log.Info("skipping boot login check, recent result already stored",
				"check", job.Probe.Key())
			continue
		}
		s.RunOnce(ctx, job)
	}
}

// hasFreshResult reports whether a check already holds a conclusive result
// newer than its own interval.
func (s *Scheduler) hasFreshResult(ctx context.Context, job Job) bool {
	current, err := s.store.CurrentStates(ctx)
	if err != nil {
		return false
	}
	c, ok := current[job.Probe.Key()]
	if !ok || c.State == probe.StateUnknown {
		return false
	}
	return s.clock.Now().Sub(c.LastCheckedAt) < job.Interval
}

func (s *Scheduler) runJob(ctx context.Context, job Job) {
	// The initial sweep has already produced a result for this check, so the
	// stagger here only spreads the ongoing cadence — it no longer delays the
	// first data point.
	spread := min(job.Interval/4, maxStartJitter)
	jitter := time.Duration(rand.Int64N(int64(spread) + 1))
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}

	ticker := time.NewTicker(job.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.RunOnce(ctx, job)
		}
	}
}

// RunOnce executes a single cycle: consult the guard, run the probe if
// allowed, update the guard, and record the outcome.
func (s *Scheduler) RunOnce(ctx context.Context, job Job) probe.Result {
	key := job.Probe.Key()

	if job.Guarded {
		if reason, blocked := s.guardBlocks(ctx, job); blocked {
			// Declining to check is missing data, not an outage. Reporting
			// `down` here would let the safety valve manufacture the very
			// incident it exists to avoid causing.
			res := probe.Result{State: probe.StateUnknown, ReasonCode: reason}
			s.record(ctx, key, res)
			return res
		}
	}

	res := job.Probe.Run(ctx)

	if job.Guarded {
		s.updateBreaker(ctx, key, res)
	}
	s.record(ctx, key, res)
	return res
}

func (s *Scheduler) guardBlocks(ctx context.Context, job Job) (probe.ReasonCode, bool) {
	now := s.clock.Now()
	key := job.Probe.Key()

	rec, err := s.store.LoadBreaker(ctx, key)
	if err != nil {
		s.log.Error("breaker load failed", "check", key, "err", err)
		// Fail closed. If we cannot read the guard we cannot know how many
		// attempts we have already spent, and guessing wrong costs the
		// account.
		return probe.ReasonBreakerOpen, true
	}
	if rec.OpenUntil != nil && now.Before(*rec.OpenUntil) {
		return probe.ReasonBreakerOpen, true
	}

	if s.guard.MaxAttemptsPerDay > 0 {
		n, err := s.store.CountAttemptsSince(ctx, key, now.Add(-24*time.Hour))
		if err != nil {
			s.log.Error("attempt count failed", "check", key, "err", err)
			return probe.ReasonBreakerOpen, true
		}
		if n >= s.guard.MaxAttemptsPerDay {
			return probe.ReasonBreakerOpen, true
		}
	}

	if job.GateCheckKey != "" {
		current, err := s.store.CurrentStates(ctx)
		if err != nil {
			s.log.Error("gate lookup failed", "check", key, "err", err)
			return probe.ReasonGateClosed, true
		}
		gate, ok := current[job.GateCheckKey]
		if !ok || gate.State != probe.StateUp {
			return probe.ReasonGateClosed, true
		}
	}

	return probe.ReasonNone, false
}

func (s *Scheduler) updateBreaker(ctx context.Context, key string, res probe.Result) {
	now := s.clock.Now()
	rec, err := s.store.LoadBreaker(ctx, key)
	if err != nil {
		s.log.Error("breaker load failed", "check", key, "err", err)
		return
	}

	switch res.State {
	case probe.StateUp:
		rec.ConsecutiveFails = 0
		rec.OpenUntil = nil
		rec.BackoffSeconds = 0

	case probe.StateDown:
		rec.ConsecutiveFails++
		// A rejected credential will never succeed on retry, and retrying is
		// precisely what walks an account toward a lockout. Treat it as
		// terminal immediately rather than spending the threshold on it.
		hardAuthFailure := res.ReasonCode == probe.ReasonSSOLoginRejected ||
			res.ReasonCode == probe.ReasonUsernameMismatch ||
			res.ReasonCode == probe.ReasonTokenInvalid

		if hardAuthFailure || rec.ConsecutiveFails >= s.guard.FailureThreshold {
			rec.BackoffSeconds = nextBackoff(rec.BackoffSeconds, s.guard)
			until := now.Add(time.Duration(rec.BackoffSeconds) * time.Second)
			rec.OpenUntil = &until
			s.log.Warn("credentialed probe suspended",
				"check", key,
				"reason", string(res.ReasonCode),
				"consecutive_fails", rec.ConsecutiveFails,
				"resume_at", until.Format(time.RFC3339))
		}

	default:
		// Unknown: nothing was attempted, so nothing changes.
		return
	}

	if err := s.store.SaveBreaker(ctx, rec); err != nil {
		s.log.Error("breaker save failed", "check", key, "err", err)
	}
}

func nextBackoff(currentSeconds int, guard GuardConfig) int {
	initial := int(guard.BackoffInitial.Seconds())
	max := int(guard.BackoffMax.Seconds())
	if currentSeconds <= 0 {
		return min(initial, max)
	}
	return min(currentSeconds*2, max)
}

func (s *Scheduler) record(ctx context.Context, key string, res probe.Result) {
	if err := s.store.RecordResult(ctx, key, res, s.clock.Now()); err != nil {
		s.log.Error("record failed", "check", key, "err", err)
		return
	}
	if res.State != probe.StateUp {
		s.log.Info("check not healthy", "check", key, "state", string(res.State), "reason", string(res.ReasonCode))
	}
}
