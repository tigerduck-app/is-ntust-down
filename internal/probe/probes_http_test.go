package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const testUA = "MoodleMobile/test"

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// --- fake NTUST estate ---

type fakeOpts struct {
	rejectLogin bool
	wstoken     string
	username    string
}

// fakeNTUST stands up two servers, because Moodle and the IdP live on
// different hosts and the probes' host checks are load-bearing. Collapsing
// them into one server would let a broken host check pass.
type fakeNTUST struct {
	moodle *httptest.Server
	sso    *httptest.Server

	mu          sync.Mutex
	gotUsername string
	gotPassword string
	gotToken    string
}

func newFakeNTUST(t *testing.T, opts fakeOpts) *fakeNTUST {
	t.Helper()
	if opts.wstoken == "" {
		opts.wstoken = "GOODWSTOKEN"
	}
	if opts.username == "" {
		opts.username = "B11234567"
	}

	f := &fakeNTUST{}
	moodleMux := http.NewServeMux()
	ssoMux := http.NewServeMux()
	f.moodle = httptest.NewServer(moodleMux)
	f.sso = httptest.NewServer(ssoMux)
	t.Cleanup(f.moodle.Close)
	t.Cleanup(f.sso.Close)

	moodleMux.HandleFunc("/admin/tool/mobile/launch.php", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, f.sso.URL+"/account/login", http.StatusFound)
	})

	ssoMux.HandleFunc("/account/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			_, _ = w.Write([]byte(ssoLoginPage))
			return
		}
		_ = r.ParseForm()
		f.mu.Lock()
		f.gotUsername = r.PostForm.Get("Username")
		f.gotPassword = r.PostForm.Get("Password")
		f.gotToken = r.PostForm.Get("__RequestVerificationToken")
		f.mu.Unlock()

		if opts.rejectLogin {
			// The IdP re-renders the wall on bad credentials.
			_, _ = w.Write([]byte(ssoLoginPage))
			return
		}
		_, _ = w.Write([]byte(oidcBridge(f.moodle.URL + "/auth/oidc/")))
	})

	moodleMux.HandleFunc("/auth/oidc/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(mobileLaunchPage(opts.wstoken)))
	})

	moodleMux.HandleFunc(moodleWSPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("wstoken") != opts.wstoken {
			_, _ = w.Write([]byte(moodleInvalidTokenJSON))
			return
		}
		_, _ = w.Write([]byte(`{"username":"` + opts.username + `","sitename":"NTUST Moodle"}`))
	})

	return f
}

func (f *fakeNTUST) moodleCfg(user, pass string) MoodleConfig {
	return MoodleConfig{
		BaseURL:   f.moodle.URL,
		SSOHost:   f.sso.Listener.Addr().String(),
		UserAgent: testUA,
		Timeout:   5 * time.Second,
		Username:  user,
		Password:  pass,
	}
}

// --- moodle.site ---

func TestMoodleSiteProbe(t *testing.T) {
	t.Run("a structured invalidtoken rejection proves Moodle booted", func(t *testing.T) {
		f := newFakeNTUST(t, fakeOpts{})
		p := &MoodleSiteProbe{Cfg: f.moodleCfg("", "")}
		got := p.Run(testCtx(t))
		if got.State != StateUp {
			t.Fatalf("state = %q (%s), want up", got.State, got.ReasonCode)
		}
	})

	t.Run("a 200 HTML challenge page is not Moodle", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(netscalerChallenge))
		}))
		defer srv.Close()
		p := &MoodleSiteProbe{Cfg: MoodleConfig{BaseURL: srv.URL, UserAgent: testUA, Timeout: 5 * time.Second}}
		got := p.Run(testCtx(t))
		if got.State != StateDown || got.ReasonCode != ReasonUnexpectedBody {
			t.Fatalf("got %q/%q, want down/unexpected_body", got.State, got.ReasonCode)
		}
	})

	t.Run("server error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}))
		defer srv.Close()
		p := &MoodleSiteProbe{Cfg: MoodleConfig{BaseURL: srv.URL, UserAgent: testUA, Timeout: 5 * time.Second}}
		if got := p.Run(testCtx(t)); got.ReasonCode != ReasonHTTP5xx {
			t.Fatalf("reason = %q, want http_5xx", got.ReasonCode)
		}
	})
}

// --- moodle.sso structural ---

func TestMoodleSSOStructuralProbe(t *testing.T) {
	t.Run("reaching a submittable credential wall is healthy", func(t *testing.T) {
		f := newFakeNTUST(t, fakeOpts{})
		p := &MoodleSSOStructuralProbe{Cfg: f.moodleCfg("", "")}
		if got := p.Run(testCtx(t)); got.State != StateUp {
			t.Fatalf("state = %q (%s), want up", got.State, got.ReasonCode)
		}
	})

	t.Run("a wall with no anti-forgery token cannot be logged into", func(t *testing.T) {
		moodleMux := http.NewServeMux()
		ssoMux := http.NewServeMux()
		moodle := httptest.NewServer(moodleMux)
		sso := httptest.NewServer(ssoMux)
		defer moodle.Close()
		defer sso.Close()
		moodleMux.HandleFunc("/admin/tool/mobile/launch.php", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, sso.URL+"/account/login", http.StatusFound)
		})
		ssoMux.HandleFunc("/account/login", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(ssoLoginPageNoToken))
		})

		p := &MoodleSSOStructuralProbe{Cfg: MoodleConfig{
			BaseURL: moodle.URL, SSOHost: sso.Listener.Addr().String(),
			UserAgent: testUA, Timeout: 5 * time.Second,
		}}
		if got := p.Run(testCtx(t)); got.ReasonCode != ReasonSSOFormMissing {
			t.Fatalf("reason = %q, want sso_form_missing", got.ReasonCode)
		}
	})

	t.Run("structural probe never submits credentials", func(t *testing.T) {
		f := newFakeNTUST(t, fakeOpts{})
		p := &MoodleSSOStructuralProbe{Cfg: f.moodleCfg("B11234567", "hunter2")}
		p.Run(testCtx(t))
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.gotPassword != "" {
			t.Fatalf("structural probe posted a password (%q); it must never spend a login attempt", f.gotPassword)
		}
	})
}

// --- moodle.sso credentialed ---

func TestMoodleSSOLoginProbe(t *testing.T) {
	t.Run("full OIDC flow yields a token that authenticates", func(t *testing.T) {
		f := newFakeNTUST(t, fakeOpts{username: "B11234567"})
		p := &MoodleSSOLoginProbe{Cfg: f.moodleCfg("b11234567", "hunter2")}
		got := p.Run(testCtx(t))
		if got.State != StateUp {
			t.Fatalf("state = %q (%s), want up", got.State, got.ReasonCode)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.gotUsername != "B11234567" {
			t.Errorf("username sent as %q, want upper-cased B11234567", f.gotUsername)
		}
		if f.gotToken != "CFDJ8ABCDEF-token" {
			t.Errorf("anti-forgery token not echoed back: %q", f.gotToken)
		}
	})

	t.Run("rejected credentials are terminal, not retried", func(t *testing.T) {
		f := newFakeNTUST(t, fakeOpts{rejectLogin: true})
		p := &MoodleSSOLoginProbe{Cfg: f.moodleCfg("B11234567", "wrong")}
		got := p.Run(testCtx(t))
		if got.ReasonCode != ReasonSSOLoginRejected {
			t.Fatalf("reason = %q, want sso_login_rejected", got.ReasonCode)
		}
	})

	t.Run("a token that does not match the student fails", func(t *testing.T) {
		f := newFakeNTUST(t, fakeOpts{username: "B99999999"})
		p := &MoodleSSOLoginProbe{Cfg: f.moodleCfg("B11234567", "hunter2")}
		if got := p.Run(testCtx(t)); got.ReasonCode != ReasonUsernameMismatch {
			t.Fatalf("reason = %q, want username_mismatch", got.ReasonCode)
		}
	})

	t.Run("without credentials it reports unknown, never down", func(t *testing.T) {
		f := newFakeNTUST(t, fakeOpts{})
		p := &MoodleSSOLoginProbe{Cfg: f.moodleCfg("", "")}
		got := p.Run(testCtx(t))
		if got.State != StateUnknown || got.ReasonCode != ReasonNotConfigured {
			t.Fatalf("got %q/%q, want unknown/not_configured", got.State, got.ReasonCode)
		}
	})
}

// --- querycourse ---

func TestQueryCourseProbe(t *testing.T) {
	t.Run("json semester list", func(t *testing.T) {
		var gotAccept string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAccept = r.Header.Get("Accept")
			_, _ = w.Write([]byte(`[{"Semester":"1151","LoginEnable":true},{"Semester":"1142"}]`))
		}))
		defer srv.Close()
		p := &QueryCourseProbe{URL: srv.URL, UserAgent: testUA, Timeout: 5 * time.Second}
		if got := p.Run(testCtx(t)); got.State != StateUp {
			t.Fatalf("state = %q (%s), want up", got.State, got.ReasonCode)
		}
		// The upstream 500s on an HTML-preferring Accept, so this header is
		// part of the contract rather than a detail.
		if gotAccept != acceptJSON {
			t.Errorf("Accept = %q, want %q", gotAccept, acceptJSON)
		}
	})

	t.Run("an empty list is a 200 with nothing behind it", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`[]`))
		}))
		defer srv.Close()
		p := &QueryCourseProbe{URL: srv.URL, UserAgent: testUA, Timeout: 5 * time.Second}
		if got := p.Run(testCtx(t)); got.ReasonCode != ReasonUnexpectedBody {
			t.Fatalf("reason = %q, want unexpected_body", got.ReasonCode)
		}
	})

	t.Run("xml error body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`<Error><Message>bad accept</Message></Error>`))
		}))
		defer srv.Close()
		p := &QueryCourseProbe{URL: srv.URL, UserAgent: testUA, Timeout: 5 * time.Second}
		if got := p.Run(testCtx(t)); got.ReasonCode != ReasonHTTP5xx {
			t.Fatalf("reason = %q, want http_5xx", got.ReasonCode)
		}
	})
}

// --- tigerduck ---

func tigerduckServer(t *testing.T, health, version string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(health))
	})
	mux.HandleFunc("/version", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(version))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestTigerDuckV3Probe(t *testing.T) {
	t.Run("healthy and serving the expected version", func(t *testing.T) {
		srv := tigerduckServer(t, `{"status":"ok","env":"production"}`, `{"version":"3.1.0","api_base_path":"/v3"}`)
		p := &TigerDuckV3Probe{
			Cfg:              TigerDuckConfig{BaseURL: srv.URL, UserAgent: testUA, Timeout: 5 * time.Second},
			ProbePath:        "/health",
			ExpectedBasePath: "/v3",
		}
		if got := p.Run(testCtx(t)); got.State != StateUp {
			t.Fatalf("state = %q (%s), want up", got.State, got.ReasonCode)
		}
	})

	t.Run("healthy but serving the wrong API version is an outage for clients", func(t *testing.T) {
		srv := tigerduckServer(t, `{"status":"ok"}`, `{"version":"2.9.0","api_base_path":"/v2"}`)
		p := &TigerDuckV3Probe{
			Cfg:              TigerDuckConfig{BaseURL: srv.URL, UserAgent: testUA, Timeout: 5 * time.Second},
			ProbePath:        "/health",
			ExpectedBasePath: "/v3",
		}
		got := p.Run(testCtx(t))
		if got.State != StateDown {
			t.Fatalf("state = %q, want down: /health passing does not mean the right version is deployed", got.State)
		}
	})
}

// --- courseselection ---

func TestCourseSelectionSSOLoginProbe(t *testing.T) {
	newEstate := func(t *testing.T, reject bool) CourseSelectionConfig {
		t.Helper()
		csMux := http.NewServeMux()
		ssoMux := http.NewServeMux()
		cs := httptest.NewServer(csMux)
		sso := httptest.NewServer(ssoMux)
		t.Cleanup(cs.Close)
		t.Cleanup(sso.Close)

		csMux.HandleFunc("/ChooseList/D01/D01", func(w http.ResponseWriter, r *http.Request) {
			if _, err := r.Cookie("authed"); err == nil {
				_, _ = w.Write([]byte(`<html><body>選課清單</body></html>`))
				return
			}
			http.Redirect(w, r, sso.URL+"/account/login", http.StatusFound)
		})
		csMux.HandleFunc("/signin-oidc", func(w http.ResponseWriter, r *http.Request) {
			http.SetCookie(w, &http.Cookie{Name: "authed", Value: "1", Path: "/"})
			http.Redirect(w, r, "/ChooseList/D01/D01", http.StatusFound)
		})
		ssoMux.HandleFunc("/account/login", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && !reject {
				_, _ = w.Write([]byte(oidcBridge(cs.URL + "/signin-oidc")))
				return
			}
			_, _ = w.Write([]byte(ssoLoginPage))
		})

		return CourseSelectionConfig{
			BaseURL:   cs.URL,
			ProbePath: "/ChooseList/D01/D01",
			SSOHost:   sso.Listener.Addr().String(),
			UserAgent: testUA,
			Timeout:   5 * time.Second,
			Username:  "B11234567",
			Password:  "hunter2",
		}
	}

	t.Run("login lands past the wall on the service", func(t *testing.T) {
		p := &CourseSelectionSSOLoginProbe{Cfg: newEstate(t, false)}
		if got := p.Run(testCtx(t)); got.State != StateUp {
			t.Fatalf("state = %q (%s), want up", got.State, got.ReasonCode)
		}
	})

	t.Run("still at the wall after posting means rejection", func(t *testing.T) {
		p := &CourseSelectionSSOLoginProbe{Cfg: newEstate(t, true)}
		if got := p.Run(testCtx(t)); got.ReasonCode != ReasonSSOLoginRejected {
			t.Fatalf("reason = %q, want sso_login_rejected", got.ReasonCode)
		}
	})
}
