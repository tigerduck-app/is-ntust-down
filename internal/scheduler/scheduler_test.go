package scheduler

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/SamWang8891/is-ntust-up/internal/probe"
	"github.com/SamWang8891/is-ntust-up/internal/store"
)

// --- doubles ---

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

type fakeRepo struct {
	// The sweep records concurrently, so the double needs the same safety the
	// real pgx pool provides.
	mu sync.Mutex

	// recordInto, when set, makes recorded results visible to CurrentStates,
	// so a test can observe one phase of the sweep feeding the next.
	recordInto map[string]store.Current
	breakers   map[string]store.BreakerRecord
	current    map[string]store.Current
	recorded   []store.Current
	attempts   int
	loadErr    error
	stateErr   error
	attemptErr error
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		breakers: map[string]store.BreakerRecord{},
		current:  map[string]store.Current{},
	}
}

func (f *fakeRepo) RecordResult(_ context.Context, key string, r probe.Result, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := store.Current{CheckKey: key, State: r.State, ReasonCode: r.ReasonCode, LastCheckedAt: now}
	f.recorded = append(f.recorded, c)
	if f.recordInto != nil {
		f.recordInto[key] = c
	}
	return nil
}

func (f *fakeRepo) CurrentStates(context.Context) (map[string]store.Current, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]store.Current, len(f.current))
	for k, v := range f.current {
		out[k] = v
	}
	return out, f.stateErr
}

func (f *fakeRepo) LoadBreaker(_ context.Context, key string) (store.BreakerRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadErr != nil {
		return store.BreakerRecord{}, f.loadErr
	}
	rec, ok := f.breakers[key]
	if !ok {
		return store.BreakerRecord{CheckKey: key}, nil
	}
	return rec, nil
}

func (f *fakeRepo) SaveBreaker(_ context.Context, rec store.BreakerRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.breakers[rec.CheckKey] = rec
	return nil
}

func (f *fakeRepo) CountAttemptsSince(context.Context, string, time.Time) (int, error) {
	return f.attempts, f.attemptErr
}

func (f *fakeRepo) Rollup(context.Context, []time.Time) error          { return nil }
func (f *fakeRepo) Prune(context.Context, int, int, time.Time) error   { return nil }
func (f *fakeRepo) RollupHourly(context.Context, int, time.Time) error { return nil }
func (f *fakeRepo) PruneHourly(context.Context, int, time.Time) error  { return nil }
func (f *fakeRepo) lastRecorded() store.Current {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.recorded[len(f.recorded)-1]
}

func (f *fakeRepo) recordedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.recorded)
}

// scriptedProbe returns queued results and counts how often it actually ran —
// which is the property the lockout guard is judged on.
type scriptedProbe struct {
	mu      sync.Mutex
	key     string
	results []probe.Result
	runs    int
}

func (p *scriptedProbe) Key() string { return p.key }

func (p *scriptedProbe) Run(context.Context) probe.Result {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.runs++
	if len(p.results) == 0 {
		return probe.Result{State: probe.StateUp}
	}
	r := p.results[0]
	if len(p.results) > 1 {
		p.results = p.results[1:]
	}
	return r
}

func (p *scriptedProbe) runCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.runs
}

func newTestScheduler(repo Repository, clock Clock, guard GuardConfig) *Scheduler {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(repo, nil, guard, log).WithClock(clock)
}

func defaultGuard() GuardConfig {
	return GuardConfig{
		FailureThreshold:  2,
		BackoffInitial:    time.Hour,
		BackoffMax:        12 * time.Hour,
		MaxAttemptsPerDay: 24,
	}
}

func guardedJob(p probe.Probe, gate string) Job {
	return Job{Probe: p, Interval: time.Hour, Guarded: true, GateCheckKey: gate}
}

// --- the gate ---

func TestCredentialedProbeWaitsForAHealthyLoginPage(t *testing.T) {
	repo := newFakeRepo()
	repo.current["moodle.sso_page"] = store.Current{CheckKey: "moodle.sso_page", State: probe.StateDown}

	p := &scriptedProbe{key: "moodle.sso_login"}
	s := newTestScheduler(repo, &fakeClock{now: time.Now()}, defaultGuard())

	got := s.RunOnce(context.Background(), guardedJob(p, "moodle.sso_page"))

	// This is the property that keeps the account alive during an NTUST
	// outage: exactly when SSO is broken, we must not spend login attempts
	// discovering it.
	if p.runCount() != 0 {
		t.Fatalf("probe ran %d times while the login page was down; it must not spend an attempt", p.runCount())
	}
	if got.State != probe.StateUnknown || got.ReasonCode != probe.ReasonGateClosed {
		t.Fatalf("got %q/%q, want unknown/gate_closed", got.State, got.ReasonCode)
	}
}

func TestGateOpenAllowsTheProbe(t *testing.T) {
	repo := newFakeRepo()
	repo.current["moodle.sso_page"] = store.Current{CheckKey: "moodle.sso_page", State: probe.StateUp}

	p := &scriptedProbe{key: "moodle.sso_login", results: []probe.Result{{State: probe.StateUp}}}
	s := newTestScheduler(repo, &fakeClock{now: time.Now()}, defaultGuard())

	if got := s.RunOnce(context.Background(), guardedJob(p, "moodle.sso_page")); got.State != probe.StateUp {
		t.Fatalf("state = %q, want up", got.State)
	}
	if p.runCount() != 1 {
		t.Fatalf("probe ran %d times, want 1", p.runCount())
	}
}

func TestAMissingGateCheckBlocks(t *testing.T) {
	repo := newFakeRepo() // no current state recorded yet
	p := &scriptedProbe{key: "moodle.sso_login"}
	s := newTestScheduler(repo, &fakeClock{now: time.Now()}, defaultGuard())

	s.RunOnce(context.Background(), guardedJob(p, "moodle.sso_page"))
	if p.runCount() != 0 {
		t.Fatal("probe ran before its gate had ever reported; unknown is not permission")
	}
}

// --- the breaker ---

func TestRejectedCredentialsTripTheBreakerImmediately(t *testing.T) {
	repo := newFakeRepo()
	repo.current["moodle.sso_page"] = store.Current{State: probe.StateUp}
	clock := &fakeClock{now: time.Now()}

	p := &scriptedProbe{key: "moodle.sso_login", results: []probe.Result{
		{State: probe.StateDown, ReasonCode: probe.ReasonSSOLoginRejected},
	}}
	s := newTestScheduler(repo, clock, defaultGuard())
	job := guardedJob(p, "moodle.sso_page")

	s.RunOnce(context.Background(), job)

	// The failure threshold is 2, but a wrong password can never succeed on
	// retry — spending the threshold on it is how accounts get banned.
	rec := repo.breakers["moodle.sso_login"]
	if rec.OpenUntil == nil {
		t.Fatal("breaker did not open on a rejected credential")
	}
	if !rec.OpenUntil.After(clock.now) {
		t.Fatal("breaker opened in the past")
	}

	s.RunOnce(context.Background(), job)
	if p.runCount() != 1 {
		t.Fatalf("probe ran %d times; the second attempt should have been blocked", p.runCount())
	}
}

func TestTransientFailuresRespectTheThreshold(t *testing.T) {
	repo := newFakeRepo()
	repo.current["moodle.sso_page"] = store.Current{State: probe.StateUp}

	p := &scriptedProbe{key: "moodle.sso_login", results: []probe.Result{
		{State: probe.StateDown, ReasonCode: probe.ReasonTimeout},
	}}
	s := newTestScheduler(repo, &fakeClock{now: time.Now()}, defaultGuard())
	job := guardedJob(p, "moodle.sso_page")

	s.RunOnce(context.Background(), job)
	if repo.breakers["moodle.sso_login"].OpenUntil != nil {
		t.Fatal("a single timeout should not suspend probing")
	}

	s.RunOnce(context.Background(), job)
	if repo.breakers["moodle.sso_login"].OpenUntil == nil {
		t.Fatal("breaker should open once the threshold is reached")
	}
}

func TestSuccessResetsTheBreaker(t *testing.T) {
	repo := newFakeRepo()
	repo.current["moodle.sso_page"] = store.Current{State: probe.StateUp}
	repo.breakers["moodle.sso_login"] = store.BreakerRecord{
		CheckKey: "moodle.sso_login", ConsecutiveFails: 1, BackoffSeconds: 3600,
	}

	p := &scriptedProbe{key: "moodle.sso_login", results: []probe.Result{{State: probe.StateUp}}}
	s := newTestScheduler(repo, &fakeClock{now: time.Now()}, defaultGuard())
	s.RunOnce(context.Background(), guardedJob(p, "moodle.sso_page"))

	rec := repo.breakers["moodle.sso_login"]
	if rec.ConsecutiveFails != 0 || rec.BackoffSeconds != 0 || rec.OpenUntil != nil {
		t.Fatalf("breaker not reset after success: %+v", rec)
	}
}

func TestBackoffDoublesAndIsCapped(t *testing.T) {
	repo := newFakeRepo()
	repo.current["moodle.sso_page"] = store.Current{State: probe.StateUp}
	clock := &fakeClock{now: time.Now()}
	guard := GuardConfig{
		FailureThreshold: 1, BackoffInitial: time.Hour, BackoffMax: 4 * time.Hour, MaxAttemptsPerDay: 100,
	}
	s := newTestScheduler(repo, clock, guard)

	p := &scriptedProbe{key: "moodle.sso_login", results: []probe.Result{
		{State: probe.StateDown, ReasonCode: probe.ReasonTimeout},
	}}
	job := guardedJob(p, "moodle.sso_page")

	want := []int{3600, 7200, 14400, 14400}
	for i, expect := range want {
		s.RunOnce(context.Background(), job)
		if got := repo.breakers["moodle.sso_login"].BackoffSeconds; got != expect {
			t.Fatalf("attempt %d: backoff = %ds, want %ds", i+1, got, expect)
		}
		// Wait out the suspension so the next attempt is permitted.
		clock.advance(time.Duration(expect)*time.Second + time.Minute)
	}
}

func TestDailyCeilingBlocksFurtherAttempts(t *testing.T) {
	repo := newFakeRepo()
	repo.current["moodle.sso_page"] = store.Current{State: probe.StateUp}
	repo.attempts = 24

	p := &scriptedProbe{key: "moodle.sso_login"}
	s := newTestScheduler(repo, &fakeClock{now: time.Now()}, defaultGuard())

	got := s.RunOnce(context.Background(), guardedJob(p, "moodle.sso_page"))
	if p.runCount() != 0 {
		t.Fatal("probe ran past the daily ceiling")
	}
	if got.State != probe.StateUnknown {
		t.Fatalf("state = %q, want unknown", got.State)
	}
}

func TestAStorageFailureFailsClosed(t *testing.T) {
	repo := newFakeRepo()
	repo.loadErr = context.DeadlineExceeded

	p := &scriptedProbe{key: "moodle.sso_login"}
	s := newTestScheduler(repo, &fakeClock{now: time.Now()}, defaultGuard())
	s.RunOnce(context.Background(), guardedJob(p, "moodle.sso_page"))

	// Without the guard's state we cannot know how many attempts are already
	// spent, and guessing wrong costs the account rather than a data point.
	if p.runCount() != 0 {
		t.Fatal("probe ran while the breaker state was unreadable")
	}
}

// --- unguarded jobs and recording ---

func TestUnguardedProbesAlwaysRun(t *testing.T) {
	repo := newFakeRepo()
	p := &scriptedProbe{key: "moodle.site", results: []probe.Result{{State: probe.StateUp}}}
	s := newTestScheduler(repo, &fakeClock{now: time.Now()}, defaultGuard())

	s.RunOnce(context.Background(), Job{Probe: p, Interval: time.Minute})
	if p.runCount() != 1 {
		t.Fatalf("unguarded probe ran %d times, want 1", p.runCount())
	}
	if repo.lastRecorded().CheckKey != "moodle.site" {
		t.Fatal("result not recorded")
	}
}

func TestSkippedRunsAreRecordedAsUnknown(t *testing.T) {
	repo := newFakeRepo()
	repo.current["moodle.sso_page"] = store.Current{State: probe.StateDown}
	p := &scriptedProbe{key: "moodle.sso_login"}
	s := newTestScheduler(repo, &fakeClock{now: time.Now()}, defaultGuard())

	s.RunOnce(context.Background(), guardedJob(p, "moodle.sso_page"))

	// A paused check must never render as an outage: the page would be
	// reporting an incident it has not observed.
	rec := repo.lastRecorded()
	if rec.State != probe.StateUnknown {
		t.Fatalf("skipped run recorded as %q, want unknown", rec.State)
	}
}

func TestAnUnknownResultLeavesTheBreakerAlone(t *testing.T) {
	repo := newFakeRepo()
	repo.current["moodle.sso_page"] = store.Current{State: probe.StateUp}
	repo.breakers["moodle.sso_login"] = store.BreakerRecord{CheckKey: "moodle.sso_login", ConsecutiveFails: 1}

	p := &scriptedProbe{key: "moodle.sso_login", results: []probe.Result{
		{State: probe.StateUnknown, ReasonCode: probe.ReasonNotConfigured},
	}}
	s := newTestScheduler(repo, &fakeClock{now: time.Now()}, defaultGuard())
	s.RunOnce(context.Background(), guardedJob(p, "moodle.sso_page"))

	if got := repo.breakers["moodle.sso_login"].ConsecutiveFails; got != 1 {
		t.Fatalf("consecutive fails = %d, want 1: nothing was attempted", got)
	}
}

// --- initial sweep ---

func TestInitialSweepRunsEveryCheckImmediately(t *testing.T) {
	repo := newFakeRepo()
	repo.current["moodle.sso_page"] = store.Current{State: probe.StateUp}

	site := &scriptedProbe{key: "moodle.site", results: []probe.Result{{State: probe.StateUp}}}
	page := &scriptedProbe{key: "moodle.sso_page", results: []probe.Result{{State: probe.StateUp}}}
	login := &scriptedProbe{key: "moodle.sso_login", results: []probe.Result{{State: probe.StateUp}}}

	s := newTestScheduler(repo, &fakeClock{now: time.Now()}, defaultGuard())
	// Hourly and daily cadences: without the sweep none of these would have
	// produced a data point for a very long time.
	s.jobs = []Job{
		{Probe: site, Interval: time.Hour},
		{Probe: page, Interval: time.Hour},
		guardedJob(login, "moodle.sso_page"),
	}

	s.RunInitialSweep(context.Background())

	for _, p := range []*scriptedProbe{site, page, login} {
		if p.runCount() != 1 {
			t.Errorf("%s ran %d times during the sweep, want 1", p.key, p.runCount())
		}
	}
	if got := repo.recordedCount(); got != 3 {
		t.Errorf("recorded %d results, want 3", got)
	}
}

func TestInitialSweepRunsGatedProbesAfterTheirGate(t *testing.T) {
	repo := newFakeRepo() // gate has never reported

	page := &scriptedProbe{key: "moodle.sso_page", results: []probe.Result{{State: probe.StateUp}}}
	login := &scriptedProbe{key: "moodle.sso_login", results: []probe.Result{{State: probe.StateUp}}}

	s := newTestScheduler(repo, &fakeClock{now: time.Now()}, defaultGuard())
	s.jobs = []Job{
		{Probe: page, Interval: time.Hour},
		guardedJob(login, "moodle.sso_page"),
	}

	// RecordResult must feed CurrentStates for the gate to see this sweep's
	// own work, which is the whole reason the phases are ordered.
	repo.recordInto = repo.current

	s.RunInitialSweep(context.Background())

	if login.runCount() != 1 {
		t.Fatalf("credentialed probe ran %d times; the gate should have been satisfied by the sweep's own structural check", login.runCount())
	}
}

func TestInitialSweepSkipsALoginItAlreadyHasAFreshAnswerFor(t *testing.T) {
	now := time.Now()
	repo := newFakeRepo()
	repo.current["moodle.sso_page"] = store.Current{State: probe.StateUp}
	repo.current["moodle.sso_login"] = store.Current{
		State: probe.StateUp, LastCheckedAt: now.Add(-5 * time.Minute),
	}

	login := &scriptedProbe{key: "moodle.sso_login", results: []probe.Result{{State: probe.StateUp}}}
	s := newTestScheduler(repo, &fakeClock{now: now}, defaultGuard())
	s.jobs = []Job{guardedJob(login, "moodle.sso_page")}

	s.RunInitialSweep(context.Background())

	// A crash-looping container must not present credentials to NTUST on
	// every restart when it already has a recent answer.
	if login.runCount() != 0 {
		t.Fatalf("credentialed probe ran %d times despite a 5-minute-old result on an hourly check", login.runCount())
	}
}

func TestInitialSweepReRunsAStaleLogin(t *testing.T) {
	now := time.Now()
	repo := newFakeRepo()
	repo.current["moodle.sso_page"] = store.Current{State: probe.StateUp}
	repo.current["moodle.sso_login"] = store.Current{
		State: probe.StateUp, LastCheckedAt: now.Add(-3 * time.Hour),
	}

	login := &scriptedProbe{key: "moodle.sso_login", results: []probe.Result{{State: probe.StateUp}}}
	s := newTestScheduler(repo, &fakeClock{now: now}, defaultGuard())
	s.jobs = []Job{guardedJob(login, "moodle.sso_page")}

	s.RunInitialSweep(context.Background())

	if login.runCount() != 1 {
		t.Fatalf("credentialed probe ran %d times; a 3-hour-old result on an hourly check is stale", login.runCount())
	}
}
