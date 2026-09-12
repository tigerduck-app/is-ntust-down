package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SamWang8891/is-ntust-down/internal/config"
	"github.com/SamWang8891/is-ntust-down/internal/probe"
	"github.com/SamWang8891/is-ntust-down/internal/store"
	"github.com/SamWang8891/is-ntust-down/internal/web/i18n"
)

var taipei = mustLoadTaipei()

func mustLoadTaipei() *time.Location {
	loc, err := time.LoadLocation("Asia/Taipei")
	if err != nil {
		panic(err)
	}
	return loc
}

type fakeReader struct {
	current map[string]store.Current
	history map[string][]store.DayBucket
	hourly  map[string][]store.HourBucket
}

func (f *fakeReader) CurrentStates(context.Context) (map[string]store.Current, error) {
	return f.current, nil
}

func (f *fakeReader) DailyHistory(context.Context, int, time.Time) (map[string][]store.DayBucket, error) {
	return f.history, nil
}

func (f *fakeReader) HourlyHistory(context.Context, int, time.Time) (map[string][]store.HourBucket, error) {
	return f.hourly, nil
}

func (f *fakeReader) Location() *time.Location { return taipei }

func healthySeries(days int, end time.Time, up, down int) []store.DayBucket {
	out := make([]store.DayBucket, 0, days)
	start := end.AddDate(0, 0, -(days - 1))
	for i := 0; i < days; i++ {
		day := start.AddDate(0, 0, i)
		state := probe.StateUp
		if down > 0 {
			state = probe.StateDegraded
		}
		out = append(out, store.DayBucket{Day: day, State: state, Up: up, Down: down})
	}
	return out
}

// zhRequest asks for Traditional Chinese, which the page tests assert on.
func zhRequest(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	r.Header.Set("Accept-Language", "zh-TW,zh;q=0.9,en;q=0.8")
	return r
}

func newTestServer(t *testing.T, r Reader) *Server {
	t.Helper()
	srv, err := NewServer(r, Options{
		Services:     config.Registry(config.RegistryOptions{}),
		HistoryDays:  90,
		HistoryHours: 24,
		CacheMaxAge:  30 * time.Second,
		RateLimiter:  NewRateLimiter(1000, 1000, nil),
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv
}

func fixtureReader(now time.Time) *fakeReader {
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, taipei)
	return &fakeReader{
		current: map[string]store.Current{
			"moodle.site": {
				CheckKey: "moodle.site", State: probe.StateUp,
				Since: now.Add(-48 * time.Hour), LastCheckedAt: now,
			},
			"moodle.sso_page": {
				CheckKey: "moodle.sso_page", State: probe.StateUp,
				Since: now.Add(-48 * time.Hour), LastCheckedAt: now,
			},
			"moodle.sso_login": {
				CheckKey: "moodle.sso_login", State: probe.StateDown,
				ReasonCode: probe.ReasonSSOLoginRejected,
				Since:      now.Add(-2 * time.Hour), LastCheckedAt: now,
			},
		},
		history: map[string][]store.DayBucket{
			"moodle.site":     healthySeries(90, end, 1440, 0),
			"moodle.sso_page": healthySeries(90, end, 288, 0),
		},
		hourly: map[string][]store.HourBucket{
			"moodle.site":     hourlySeries(24, now, 60, 0),
			"moodle.sso_page": hourlySeries(24, now, 12, 0),
		},
	}
}

func hourlySeries(hours int, now time.Time, up, down int) []store.HourBucket {
	end := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, taipei)
	out := make([]store.HourBucket, 0, hours)
	for i := hours - 1; i >= 0; i-- {
		state := probe.StateUp
		if down > 0 {
			state = probe.StateDegraded
		}
		out = append(out, store.HourBucket{
			Hour: end.Add(-time.Duration(i) * time.Hour), State: state, Up: up, Down: down,
		})
	}
	return out
}

func TestStatusAPIShape(t *testing.T) {
	now := time.Now().In(taipei)
	srv := newTestServer(t, fixtureReader(now))

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("CORS header = %q, want *", got)
	}
	if !strings.Contains(rec.Header().Get("Cache-Control"), "max-age=30") {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("no ETag")
	}

	var payload apiStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Services) != 5 {
		t.Fatalf("got %d services, want 5", len(payload.Services))
	}

	var moodle *apiService
	for i := range payload.Services {
		if payload.Services[i].Key == "moodle" {
			moodle = &payload.Services[i]
		}
	}
	if moodle == nil {
		t.Fatal("moodle missing from the payload")
	}
	// Site and login page healthy, credentialed login failing: the honest
	// answer is partial, not a flat outage.
	if moodle.State != string(probe.StateDegraded) {
		t.Errorf("moodle state = %q, want degraded", moodle.State)
	}
	if len(moodle.Checks) != 3 {
		t.Errorf("moodle has %d checks, want 3", len(moodle.Checks))
	}
}

func TestETagYields304(t *testing.T) {
	srv := newTestServer(t, fixtureReader(time.Now().In(taipei)))

	first := httptest.NewRecorder()
	srv.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/v1/services", nil))
	etag := first.Header().Get("ETag")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	req.Header.Set("If-None-Match", etag)
	second := httptest.NewRecorder()
	srv.ServeHTTP(second, req)

	if second.Code != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", second.Code)
	}
	if second.Body.Len() != 0 {
		t.Error("304 must not carry a body")
	}
}

func TestHistoryEndpoint(t *testing.T) {
	srv := newTestServer(t, fixtureReader(time.Now().In(taipei)))

	t.Run("trims to the requested window", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/services/moodle/history?days=7", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var payload apiHistory
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload.Resolution != "day" || len(payload.Items) != 7 {
			t.Fatalf("resolution=%q buckets=%d, want day and 7", payload.Resolution, len(payload.Items))
		}
	})

	t.Run("hour resolution", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/api/v1/services/moodle/history?resolution=hour&hours=6", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var payload apiHistory
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload.Resolution != "hour" || len(payload.Items) != 6 {
			t.Fatalf("resolution=%q buckets=%d, want hour and 6", payload.Resolution, len(payload.Items))
		}
		// Hour buckets carry a full local timestamp; a bare date could not
		// distinguish 05:00 from 17:00.
		if _, err := time.Parse(time.RFC3339, payload.Items[0].Start); err != nil {
			t.Errorf("hour bucket start %q is not RFC3339: %v", payload.Items[0].Start, err)
		}
	})

	t.Run("rejects an unknown resolution", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/api/v1/services/moodle/history?resolution=minute", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("rejects an out-of-range hour window", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/api/v1/services/moodle/history?resolution=hour&hours=999", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})

	t.Run("unknown service", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/services/nope/history", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})

	t.Run("rejects an out-of-range window", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/services/moodle/history?days=9999", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
	})
}

func TestOnlyKnownReasonCodesAreEverPublished(t *testing.T) {
	now := time.Now().In(taipei)
	r := fixtureReader(now)
	// Simulate a probe that skipped the constants and stuffed raw upstream
	// text into the reason. The API must not pass it through: SSO error pages
	// carry tokens, and a public API would make such a leak permanent.
	r.current["moodle.site"] = store.Current{
		CheckKey: "moodle.site", State: probe.StateDown,
		ReasonCode: probe.ReasonCode("dial tcp: wstoken=SECRET123 refused"),
		Since:      now, LastCheckedAt: now,
	}
	srv := newTestServer(t, r)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))

	body := rec.Body.String()
	if strings.Contains(body, "SECRET123") {
		t.Fatal("an unrecognised reason code leaked upstream text into the public API")
	}

	var payload apiStatus
	_ = json.Unmarshal(rec.Body.Bytes(), &payload)
	for _, s := range payload.Services {
		for _, c := range s.Checks {
			if c.ReasonCode == nil {
				continue
			}
			if !probe.IsKnownReason(probe.ReasonCode(*c.ReasonCode)) {
				t.Fatalf("published unknown reason code %q", *c.ReasonCode)
			}
		}
	}
}

func TestHealthzIsNotRateLimited(t *testing.T) {
	srv, err := NewServer(fixtureReader(time.Now().In(taipei)), Options{
		Services:     config.Registry(config.RegistryOptions{}),
		HistoryDays:  90,
		HistoryHours: 24,
		CacheMaxAge:  30 * time.Second,
		RateLimiter:  NewRateLimiter(60, 1, nil), // burst of one
		Log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		// An infra probe that gets 429ed reports the monitor itself as down.
		if rec.Code != http.StatusOK {
			t.Fatalf("healthz request %d = %d, want 200", i+1, rec.Code)
		}
	}
}

func TestPageRendersInTaiwanMandarin(t *testing.T) {
	srv := newTestServer(t, fixtureReader(time.Now().In(taipei)))

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, zhRequest(http.MethodGet, "/"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`lang="zh-Hant-TW"`,
		"NTUST 服務狀態",
		"Moodle 數位學習平台",
		"SSO 登入驗證",
		"SSO 登入遭拒",
		`id="theme-toggle"`,
		"is-ntust-down:theme", // the inline pre-paint bootstrap
		"is-ntust-down:range",
		`data-range-panel="hour"`,
		`data-range-panel="day"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

func TestPageSetsACSPThatAllowsItsOwnInlineScript(t *testing.T) {
	srv := newTestServer(t, fixtureReader(time.Now().In(taipei)))

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, zhRequest(http.MethodGet, "/"))

	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self' 'sha256-") {
		t.Fatalf("CSP does not hash-allow the inline theme bootstrap: %q", csp)
	}
	if strings.Contains(csp, "unsafe-inline") {
		t.Error("CSP fell back to unsafe-inline")
	}
}

func TestStripCoversTheFullWindowEvenWithoutData(t *testing.T) {
	now := time.Now().In(taipei)
	// querycourse has no history at all in the fixture.
	b := NewBuilder(fixtureReader(now), 90, 24, config.Registry(config.RegistryOptions{}))
	view, err := b.Build(context.Background(), now, i18n.LangZhTW)
	if err != nil {
		t.Fatal(err)
	}
	for _, svc := range view.Services {
		// A fixed-width strip is what keeps dates lined up across services;
		// a short one would silently misalign every row beneath it.
		if len(svc.Days) != 90 {
			t.Errorf("%s day strip has %d cells, want 90", svc.Key, len(svc.Days))
		}
		if len(svc.Hours) != 24 {
			t.Errorf("%s hour strip has %d cells, want 24", svc.Key, len(svc.Hours))
		}
	}
}

func TestMailAppearsAndSMTPIsOptIn(t *testing.T) {
	without := config.Registry(config.RegistryOptions{})
	with := config.Registry(config.RegistryOptions{IncludeMailSMTP: true})

	count := func(svcs []config.Service) int {
		for _, s := range svcs {
			if s.Key == "mail" {
				return len(s.Checks)
			}
		}
		return -1
	}
	if got := count(without); got != 2 {
		t.Errorf("mail has %d checks by default, want 2 (site + login page)", got)
	}
	// Outbound port 25 is blocked by many hosts, so the MX check must not
	// appear — and silently report down — unless it was asked for.
	if got := count(with); got != 3 {
		t.Errorf("mail has %d checks with SMTP enabled, want 3", got)
	}
}

// --- language negotiation ---

func TestPageLanguageFollowsTheBrowser(t *testing.T) {
	srv := newTestServer(t, fixtureReader(time.Now().In(taipei)))

	cases := []struct {
		name      string
		accept    string
		wantLang  string
		wantCopy  string
		avoidCopy string
	}{
		{"Chinese browser", "zh-TW,zh;q=0.9", `lang="zh-Hant-TW"`, "NTUST 服務狀態", "NTUST Service Status"},
		{"English browser", "en-US,en;q=0.9", `lang="en"`, "NTUST Service Status", "NTUST 服務狀態"},
		// Anything we do not ship falls back to English rather than showing a
		// Japanese reader Traditional Chinese.
		{"Japanese browser", "ja-JP,ja;q=0.9", `lang="en"`, "NTUST Service Status", "NTUST 服務狀態"},
		{"no header at all", "", `lang="en"`, "NTUST Service Status", "NTUST 服務狀態"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.accept != "" {
				req.Header.Set("Accept-Language", tc.accept)
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			body := rec.Body.String()
			if !strings.Contains(body, tc.wantLang) {
				t.Errorf("missing %s", tc.wantLang)
			}
			if !strings.Contains(body, tc.wantCopy) {
				t.Errorf("missing copy %q", tc.wantCopy)
			}
			if strings.Contains(body, tc.avoidCopy) {
				t.Errorf("leaked the other language: %q", tc.avoidCopy)
			}
		})
	}
}

func TestExplicitLangParamWinsOverTheHeader(t *testing.T) {
	srv := newTestServer(t, fixtureReader(time.Now().In(taipei)))

	req := httptest.NewRequest(http.MethodGet, "/?lang=zh-TW", nil)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	// The switch has to beat the browser, or a Taiwanese student on an
	// English-locale machine could never get Chinese.
	if !strings.Contains(rec.Body.String(), "NTUST 服務狀態") {
		t.Fatal("?lang=zh-TW did not override Accept-Language")
	}
	if !strings.Contains(rec.Body.String(), `href="?lang=en"`) {
		t.Error("language switch does not offer the other language")
	}
}

func TestTranslatedResponsesVaryOnAcceptLanguage(t *testing.T) {
	srv := newTestServer(t, fixtureReader(time.Now().In(taipei)))

	for _, path := range []string{"/", "/api/v1/status", "/api/v1/services"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		// Without this a shared cache serves one visitor's language to the
		// next for as long as the entry lives.
		if !strings.Contains(rec.Header().Get("Vary"), "Accept-Language") {
			t.Errorf("%s Vary = %q, must include Accept-Language", path, rec.Header().Get("Vary"))
		}
	}
}

func TestAPIServiceNamesAreTranslated(t *testing.T) {
	srv := newTestServer(t, fixtureReader(time.Now().In(taipei)))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/services", nil)
	req.Header.Set("Accept-Language", "en-US")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	var out []apiServiceMeta
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, svc := range out {
		if svc.Key == "courseselection" && svc.Name != "Course Selection" {
			t.Errorf("English name = %q, want Course Selection", svc.Name)
		}
	}
}

func TestFooterLinksToTheRepository(t *testing.T) {
	srv := newTestServer(t, fixtureReader(time.Now().In(taipei)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	body := rec.Body.String()
	for _, want := range []string{
		`href="https://github.com/tigerduck-app/is-ntust-down"`,
		"tigerduck-app/is-ntust-down",
		// Inline SVG rather than a hosted image: the CSP forbids external
		// images, and the mark should not cost a request.
		`class="repo__mark"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("footer is missing %q", want)
		}
	}
}

func TestEveryPageSaysItIsForReferenceOnly(t *testing.T) {
	srv := newTestServer(t, fixtureReader(time.Now().In(taipei)))

	for _, tc := range []struct{ accept, want string }{
		{"zh-TW,zh;q=0.9", "本網站僅供參考"},
		{"en-US,en;q=0.9", "This site is for reference only"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Accept-Language", tc.accept)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		body := rec.Body.String()
		at := strings.Index(body, tc.want)
		if at < 0 {
			t.Errorf("%s page is missing %q", tc.accept, tc.want)
			continue
		}
		// It belongs above everything else, not buried in the footer.
		if masthead := strings.Index(body, `class="masthead"`); at > masthead {
			t.Errorf("%s disclaimer comes after the masthead", tc.accept)
		}
	}
}

func TestAPauseAfterAFailedSignInKeepsTheServiceDegraded(t *testing.T) {
	now := time.Now().In(taipei)
	r := fixtureReader(now)
	r.current["moodle.sso_login"] = store.Current{
		CheckKey: "moodle.sso_login", State: probe.StateUnknown,
		ReasonCode: probe.ReasonPausedAfterFailure,
		Since:      now.Add(-time.Hour), LastCheckedAt: now,
	}

	// Pausing protects the account; it must not also make the outage vanish
	// from the page for the length of the backoff.
	if got := moodleState(t, newTestServer(t, r)); got != string(probe.StateDegraded) {
		t.Fatalf("moodle state = %q, want degraded", got)
	}
}

func TestANeutralPauseDoesNotInventAnOutage(t *testing.T) {
	now := time.Now().In(taipei)
	r := fixtureReader(now)
	r.current["moodle.sso_login"] = store.Current{
		CheckKey: "moodle.sso_login", State: probe.StateUnknown,
		ReasonCode: probe.ReasonBreakerOpen,
		Since:      now.Add(-time.Hour), LastCheckedAt: now,
	}

	if got := moodleState(t, newTestServer(t, r)); got != string(probe.StateUp) {
		t.Fatalf("moodle state = %q, want up", got)
	}
}

func moodleState(t *testing.T, srv *Server) string {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	var payload apiStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, s := range payload.Services {
		if s.Key == "moodle" {
			return s.State
		}
	}
	t.Fatal("moodle missing from the payload")
	return ""
}

func TestTigerDuckV2IsGone(t *testing.T) {
	for _, svc := range config.Registry(config.RegistryOptions{IncludeMailSMTP: true}) {
		if svc.Key == "tigerduck-v2" {
			t.Fatal("tigerduck-v2 is still registered")
		}
	}
}
