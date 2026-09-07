package web

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/SamWang8891/is-ntust-up/internal/config"
	"github.com/SamWang8891/is-ntust-up/internal/probe"
	"github.com/SamWang8891/is-ntust-up/internal/web/i18n"
)

//go:embed templates/*.html static/*
var assetFS embed.FS

// Options configures the HTTP surface.
type Options struct {
	Services     []config.Service
	HistoryDays  int
	HistoryHours int
	CacheMaxAge  time.Duration
	RateLimiter  *RateLimiter
	Log          *slog.Logger
	Catalog      i18n.Catalog
}

type Server struct {
	mux         *http.ServeMux
	tmpl        *template.Template
	builder     *Builder
	opts        Options
	themeScript template.JS
	csp         string
	now         func() time.Time
}

func NewServer(reader Reader, opts Options) (*Server, error) {
	// The theme and range bootstrap must run before first paint, so it is
	// inlined rather than fetched — a dark-mode visitor would otherwise get a
	// white flash on every navigation. Inlining it means the CSP has to allow
	// it by hash, which is computed here from the file actually embedded so
	// the two can never drift apart.
	scriptBytes, err := assetFS.ReadFile("static/boot.js")
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(scriptBytes)
	scriptHash := "sha256-" + base64.StdEncoding.EncodeToString(sum[:])

	s := &Server{
		builder:     NewBuilder(reader, opts.HistoryDays, opts.HistoryHours, opts.Services),
		opts:        opts,
		themeScript: template.JS(scriptBytes),
		now:         time.Now,
		csp: strings.Join([]string{
			"default-src 'self'",
			"script-src 'self' '" + scriptHash + "'",
			"style-src 'self'",
			"img-src 'self' data:",
			"base-uri 'none'",
			"frame-ancestors 'none'",
			"form-action 'none'",
		}, "; "),
	}

	s.tmpl, err = template.New("").Funcs(s.funcs()).ParseFS(assetFS, "templates/*.html")
	if err != nil {
		return nil, err
	}

	api := NewAPI(s.builder, opts.CacheMaxAge)
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", s.handlePage)
	mux.Handle("GET /static/", http.FileServerFS(assetFS))

	// The monitor's own liveness is never rate limited: an infra probe that
	// gets 429ed reports the monitor as down.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	limited := func(h http.HandlerFunc) http.Handler {
		if opts.RateLimiter == nil {
			return h
		}
		return opts.RateLimiter.Middleware(h)
	}
	mux.Handle("GET /api/v1/status", limited(api.Status))
	mux.Handle("GET /api/v1/services", limited(api.Services))
	mux.Handle("GET /api/v1/services/{key}/history", limited(api.History))

	s.mux = mux
	return s, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", s.csp)
	s.mux.ServeHTTP(w, r)
}

type pageData struct {
	*PageView
	ThemeScript template.JS
	Now         time.Time
}

// T and the label helpers hang off pageData rather than being template funcs
// because the language is chosen per request, and a func closed over one
// catalogue at construction time would serve every visitor the same language.
func (d pageData) T(id string) string { return d.Cat.T(id) }

func (d pageData) HistoryLabel(days int) string {
	return fmt.Sprintf(d.Cat.T("label.history"), days)
}

func (d pageData) HoursLabel(hours int) string {
	return fmt.Sprintf(d.Cat.T("label.history_hours"), hours)
}

func (d pageData) Clock(t *time.Time) string {
	if t == nil {
		return d.Cat.T("label.never_checked")
	}
	return t.Format("2006-01-02 15:04")
}

// OtherLang is the language the switch offers.
func (d pageData) OtherLang() string { return string(d.Lang.Other()) }

// negotiate resolves the display language for one request. An explicit ?lang=
// wins so a link can pin a language; otherwise the browser's Accept-Language
// decides, falling back to English.
func negotiate(r *http.Request) i18n.Lang {
	if q := r.URL.Query().Get("lang"); q != "" {
		if l, ok := i18n.Parse(q); ok {
			return l
		}
	}
	return i18n.Match(r.Header.Get("Accept-Language"))
}

func (s *Server) handlePage(w http.ResponseWriter, r *http.Request) {
	lang := negotiate(r)
	view, err := s.builder.Build(r.Context(), s.now(), lang)
	if err != nil {
		s.opts.Log.Error("page build failed", "err", err)
		http.Error(w, i18n.For(lang).T("label.unavailable"), http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Without this a shared cache would hand a Chinese page to an English
	// visitor, and vice versa, for as long as the entry lives.
	w.Header().Set("Vary", "Accept-Language")
	w.Header().Set("Cache-Control", "public, max-age="+fmt.Sprint(int(s.opts.CacheMaxAge.Seconds())))
	if err := s.tmpl.ExecuteTemplate(w, "index.html", pageData{
		PageView:    view,
		ThemeScript: s.themeScript,
		Now:         view.GeneratedAt,
	}); err != nil {
		s.opts.Log.Error("template render failed", "err", err)
	}
}

func (s *Server) funcs() template.FuncMap {
	return template.FuncMap{
		"glyph": func(st probe.State) string {
			// Colour is never the only signal: the page must stay readable in
			// greyscale, with colour vision deficiency, and in a screenshot.
			switch st {
			case probe.StateUp:
				return "✓"
			case probe.StateDegraded:
				return "!"
			case probe.StateDown:
				return "✕"
			case probe.StateRetired:
				return "—"
			default:
				return "?"
			}
		},
		"pct": func(v *float64) string {
			if v == nil {
				return "—"
			}
			return fmt.Sprintf("%.2f%%", *v)
		},
		"stamp": func(t time.Time) string { return t.Format("2006-01-02 15:04:05") },
	}
}
