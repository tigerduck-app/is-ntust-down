package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/SamWang8891/is-ntust-down/internal/probe"
	"github.com/SamWang8891/is-ntust-down/internal/web/i18n"
)

// --- wire types ---
//
// These are the public contract. They are deliberately separate from the
// internal view structs so a refactor of the page cannot silently change what
// third parties receive.

type apiStatus struct {
	GeneratedAt  string       `json:"generated_at"`
	OverallState string       `json:"overall_state"`
	HistoryDays  int          `json:"history_days"`
	Services     []apiService `json:"services"`
}

type apiService struct {
	Key           string     `json:"key"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	State         string     `json:"state"`
	Since         *string    `json:"since"`
	LastCheckedAt *string    `json:"last_checked_at"`
	UptimePct     *float64   `json:"uptime_pct"`
	Checks        []apiCheck `json:"checks"`
}

type apiCheck struct {
	Key           string  `json:"key"`
	Name          string  `json:"name"`
	State         string  `json:"state"`
	Since         *string `json:"since"`
	LastCheckedAt *string `json:"last_checked_at"`
	LatencyMS     *int    `json:"latency_ms"`
	ReasonCode    *string `json:"reason_code"`
}

type apiServiceMeta struct {
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Checks      []apiKV `json:"checks"`
}

type apiKV struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type apiHistory struct {
	Service    string      `json:"service"`
	Resolution string      `json:"resolution"`
	Buckets    int         `json:"bucket_count"`
	Items      []apiBucket `json:"buckets"`
}

// apiBucket serves both resolutions. Start is a local date at day resolution
// and a local RFC3339 hour at hour resolution; `resolution` says which, so a
// consumer never has to guess from the string's shape.
type apiBucket struct {
	Start     string   `json:"start"`
	State     string   `json:"state"`
	UptimePct *float64 `json:"uptime_pct"`
	Samples   int      `json:"samples"`
}

// --- handlers ---

type API struct {
	builder     *Builder
	cacheMaxAge time.Duration
	now         func() time.Time
}

func NewAPI(b *Builder, cacheMaxAge time.Duration) *API {
	return &API{builder: b, cacheMaxAge: cacheMaxAge, now: time.Now}
}

func (a *API) Status(w http.ResponseWriter, r *http.Request) {
	view, err := a.builder.Build(r.Context(), a.now(), negotiate(r))
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "Status data is temporarily unavailable.")
		return
	}

	payload := apiStatus{
		GeneratedAt:  view.GeneratedAt.Format(time.RFC3339),
		OverallState: string(view.Overall),
		HistoryDays:  view.HistoryDays,
	}
	for _, svc := range view.Services {
		as := apiService{
			Key:           svc.Key,
			Name:          svc.Name,
			Description:   svc.Desc,
			State:         string(svc.State),
			Since:         formatTime(svc.Since),
			LastCheckedAt: formatTime(svc.LastCheckedAt),
			UptimePct:     svc.UptimePct,
			Checks:        make([]apiCheck, 0, len(svc.Checks)),
		}
		for _, c := range svc.Checks {
			as.Checks = append(as.Checks, apiCheck{
				Key:           c.Key,
				Name:          c.Name,
				State:         string(c.State),
				Since:         formatTime(c.Since),
				LastCheckedAt: formatTime(c.LastCheckedAt),
				LatencyMS:     c.LatencyMS,
				ReasonCode:    publishableReason(c.ReasonCode),
			})
		}
		payload.Services = append(payload.Services, as)
	}
	a.write(w, r, payload)
}

func (a *API) Services(w http.ResponseWriter, r *http.Request) {
	cat := i18n.For(negotiate(r))
	out := make([]apiServiceMeta, 0)
	for _, svc := range a.builder.Services() {
		meta := apiServiceMeta{
			Key:         svc.Key,
			Name:        cat.T(svc.NameID),
			Description: cat.T(svc.DescID),
			Checks:      make([]apiKV, 0, len(svc.Checks)),
		}
		for _, c := range svc.Checks {
			meta.Checks = append(meta.Checks, apiKV{Key: c.Key, Name: cat.T(c.NameID)})
		}
		out = append(out, meta)
	}
	a.write(w, r, out)
}

func (a *API) History(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	known := false
	for _, svc := range a.builder.Services() {
		if svc.Key == key {
			known = true
			break
		}
	}
	if !known {
		writeJSONError(w, http.StatusNotFound, "unknown_service",
			fmt.Sprintf("No service with key %q.", key))
		return
	}

	resolution := r.URL.Query().Get("resolution")
	if resolution == "" {
		resolution = "day"
	}
	if resolution != "day" && resolution != "hour" {
		writeJSONError(w, http.StatusBadRequest, "invalid_resolution",
			`resolution must be "day" or "hour".`)
		return
	}

	limit := a.builder.historyDays
	param := "days"
	if resolution == "hour" {
		limit = a.builder.historyHours
		param = "hours"
	}

	count := limit
	if raw := r.URL.Query().Get(param); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > limit {
			writeJSONError(w, http.StatusBadRequest, "invalid_"+param,
				fmt.Sprintf("%s must be an integer between 1 and %d.", param, limit))
			return
		}
		count = n
	}

	view, err := a.builder.Build(r.Context(), a.now(), negotiate(r))
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "Status data is temporarily unavailable.")
		return
	}

	payload := apiHistory{Service: key, Resolution: resolution, Items: []apiBucket{}}
	for _, svc := range view.Services {
		if svc.Key != key {
			continue
		}
		if resolution == "hour" {
			// Trim from the left: the caller asked for the most recent N.
			window := svc.Hours
			if len(window) > count {
				window = window[len(window)-count:]
			}
			for _, h := range window {
				payload.Items = append(payload.Items, apiBucket{
					Start:     h.Start.Format(time.RFC3339),
					State:     string(h.State),
					UptimePct: h.UptimePct,
					Samples:   h.Samples,
				})
			}
			break
		}
		window := svc.Days
		if len(window) > count {
			window = window[len(window)-count:]
		}
		for _, d := range window {
			payload.Items = append(payload.Items, apiBucket{
				Start:     d.Date,
				State:     string(d.State),
				UptimePct: d.UptimePct,
				Samples:   d.Samples,
			})
		}
	}
	payload.Buckets = len(payload.Items)
	a.write(w, r, payload)
}

// write serialises, tags, and caches a response.
//
// The ETag plus a short max-age is what actually keeps this endpoint cheap:
// the data only changes on probe cadence, so most third-party traffic should
// never reach the database at all.
func (a *API) write(w http.ResponseWriter, r *http.Request, payload any) {
	body, err := json.Marshal(payload)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal", "Could not encode the response.")
		return
	}

	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(int(a.cacheMaxAge.Seconds())))
	w.Header().Set("ETag", etag)
	// Public, read-only data; browser-based consumers are the likely audience.
	w.Header().Set("Access-Control-Allow-Origin", "*")
	// Service and check names are translated, so a cache keyed on URL alone
	// would serve one visitor's language to another.
	w.Header().Set("Vary", "Accept-Language, Accept-Encoding")

	if match := r.Header.Get("If-None-Match"); match == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(body)
}

func writeJSONError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}

func formatTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.Format(time.RFC3339)
	return &s
}

// publishableReason is the last gate before a reason code becomes public.
// An unrecognised code is replaced rather than emitted, so a future probe
// that forgets to use a constant cannot leak upstream text through the API.
func publishableReason(r probe.ReasonCode) *string {
	if r == probe.ReasonNone {
		return nil
	}
	if !probe.IsKnownReason(r) {
		unknown := string(probe.ReasonUnexpectedBody)
		return &unknown
	}
	s := string(r)
	return &s
}
