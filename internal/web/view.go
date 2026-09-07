package web

import (
	"context"
	"fmt"
	"time"

	"github.com/SamWang8891/is-ntust-up/internal/config"
	"github.com/SamWang8891/is-ntust-up/internal/probe"
	"github.com/SamWang8891/is-ntust-up/internal/store"
	"github.com/SamWang8891/is-ntust-up/internal/web/i18n"
)

// Reader is the slice of storage the web layer needs. It is read-only by
// construction: nothing served to the public can write history.
type Reader interface {
	CurrentStates(ctx context.Context) (map[string]store.Current, error)
	DailyHistory(ctx context.Context, days int, now time.Time) (map[string][]store.DayBucket, error)
	HourlyHistory(ctx context.Context, hours int, now time.Time) (map[string][]store.HourBucket, error)
	Location() *time.Location
}

type CheckView struct {
	Key           string
	Name          string
	State         probe.State
	StateLabel    string
	ReasonCode    probe.ReasonCode
	Reason        string
	LatencyMS     *int
	Since         *time.Time
	LastCheckedAt *time.Time
}

type DayView struct {
	Start     time.Time
	Date      string
	Label     string
	State     probe.State
	UptimePct *float64
	Samples   int
	HasData   bool
	Tooltip   string
}

// HourView is one cell of the recent-detail strip.
type HourView struct {
	Start     time.Time
	Label     string
	State     probe.State
	UptimePct *float64
	Samples   int
	HasData   bool
	Tooltip   string
}

type ServiceView struct {
	Key           string
	Name          string
	Desc          string
	State         probe.State
	StateLabel    string
	Since         *time.Time
	LastCheckedAt *time.Time
	UptimePct     *float64
	HourUptimePct *float64
	Checks        []CheckView
	Days          []DayView
	Hours         []HourView
}

type PageView struct {
	Lang         i18n.Lang
	GeneratedAt  time.Time
	Overall      probe.State
	OverallText  string
	Services     []ServiceView
	HistoryDays  int
	HistoryHours int
	Cat          i18n.Catalog
}

// Builder turns stored state plus the service registry into a renderable view.
type Builder struct {
	reader       Reader
	historyDays  int
	historyHours int
	services     []config.Service
}

func NewBuilder(r Reader, historyDays, historyHours int, services []config.Service) *Builder {
	return &Builder{
		reader:      r,
		historyDays: historyDays, historyHours: historyHours,
		services: services,
	}
}

// Services exposes the registry this builder renders, so the API describes
// exactly the services the page shows.
func (b *Builder) Services() []config.Service { return b.services }

func (b *Builder) Build(ctx context.Context, now time.Time, lang i18n.Lang) (*PageView, error) {
	cat := i18n.For(lang)
	current, err := b.reader.CurrentStates(ctx)
	if err != nil {
		return nil, err
	}
	history, err := b.reader.DailyHistory(ctx, b.historyDays, now)
	if err != nil {
		return nil, err
	}
	hourly, err := b.reader.HourlyHistory(ctx, b.historyHours, now)
	if err != nil {
		return nil, err
	}
	loc := b.reader.Location()

	var services []ServiceView
	var serviceStates []probe.State

	for _, svc := range b.services {
		sv := ServiceView{
			Key:  svc.Key,
			Name: cat.T(svc.NameID),
			Desc: cat.T(svc.DescID),
		}

		var checkStates []probe.State
		for _, chk := range svc.Checks {
			cv := CheckView{Key: chk.Key, Name: cat.T(chk.NameID), State: probe.StateUnknown}
			if cur, ok := current[chk.Key]; ok {
				cv.State = cur.State
				cv.ReasonCode = cur.ReasonCode
				cv.LatencyMS = cur.LatencyMS
				since := cur.Since.In(loc)
				checked := cur.LastCheckedAt.In(loc)
				cv.Since = &since
				cv.LastCheckedAt = &checked
				if sv.LastCheckedAt == nil || checked.After(*sv.LastCheckedAt) {
					sv.LastCheckedAt = &checked
				}
			}
			cv.StateLabel = cat.T("state." + string(cv.State))
			if cv.ReasonCode != probe.ReasonNone {
				cv.Reason = cat.T("reason." + string(cv.ReasonCode))
			}
			checkStates = append(checkStates, cv.State)
			sv.Checks = append(sv.Checks, cv)
		}

		sv.State = probe.DeriveServiceState(checkStates)
		sv.StateLabel = cat.T("state." + string(sv.State))
		sv.Since = earliestSince(sv.Checks, sv.State)
		sv.Days, sv.UptimePct = b.buildStrip(svc, history, loc, now, lang, cat)
		sv.Hours, sv.HourUptimePct = b.buildHourStrip(svc, hourly, loc, now, lang, cat)

		serviceStates = append(serviceStates, sv.State)
		services = append(services, sv)
	}

	overall := probe.DeriveServiceState(serviceStates)
	return &PageView{
		Lang:         lang,
		GeneratedAt:  now.In(loc),
		Overall:      overall,
		OverallText:  cat.T(bannerID(overall)),
		Services:     services,
		HistoryDays:  b.historyDays,
		HistoryHours: b.historyHours,
		Cat:          cat,
	}, nil
}

// buildStrip collapses a service's checks into one bar per day, and computes
// the window's uptime from raw sample counts rather than by averaging the
// per-day percentages — a day with four samples should not weigh as much as a
// day with a thousand.
func (b *Builder) buildStrip(svc config.Service, history map[string][]store.DayBucket, loc *time.Location, now time.Time, lang i18n.Lang, cat i18n.Catalog) ([]DayView, *float64) {
	end := startOfDay(now.In(loc), loc)
	start := end.AddDate(0, 0, -(b.historyDays - 1))

	days := make([]DayView, 0, b.historyDays)
	var totalUp, totalConclusive int

	for i := 0; i < b.historyDays; i++ {
		day := start.AddDate(0, 0, i)
		var dayStates []probe.State
		var up, conclusive, samples int

		for _, chk := range svc.Checks {
			series, ok := history[chk.Key]
			if !ok || i >= len(series) {
				continue
			}
			bucket := series[i]
			dayStates = append(dayStates, bucket.State)
			up += bucket.Up
			conclusive += bucket.Conclusive()
			samples += bucket.Up + bucket.Degraded + bucket.Down + bucket.Unknown + bucket.Retired
		}

		dv := DayView{
			Start:   day,
			Date:    day.Format("2006-01-02"),
			Label:   formatDay(lang, day),
			State:   probe.DeriveServiceState(dayStates),
			Samples: samples,
			HasData: samples > 0,
		}
		if conclusive > 0 {
			pct := 100 * float64(up) / float64(conclusive)
			dv.UptimePct = &pct
		}
		dv.Tooltip = dayTooltip(cat, lang, dv)

		totalUp += up
		totalConclusive += conclusive
		days = append(days, dv)
	}

	var windowPct *float64
	if totalConclusive > 0 {
		pct := 100 * float64(totalUp) / float64(totalConclusive)
		windowPct = &pct
	}
	return days, windowPct
}

// buildHourStrip is the day strip's sibling at hour resolution. It answers a
// different question — "is it broken right now, and when did that start
// today" — which a day bucket cannot: one bad hour in twenty-four still
// renders as a mostly-green day.
func (b *Builder) buildHourStrip(svc config.Service, hourly map[string][]store.HourBucket, loc *time.Location, now time.Time, lang i18n.Lang, cat i18n.Catalog) ([]HourView, *float64) {
	end := startOfHour(now.In(loc), loc)
	start := end.Add(-time.Duration(b.historyHours-1) * time.Hour)

	hours := make([]HourView, 0, b.historyHours)
	var totalUp, totalConclusive int

	for i := 0; i < b.historyHours; i++ {
		at := start.Add(time.Duration(i) * time.Hour)
		var states []probe.State
		var up, conclusive, samples int

		for _, chk := range svc.Checks {
			series, ok := hourly[chk.Key]
			if !ok || i >= len(series) {
				continue
			}
			bucket := series[i]
			states = append(states, bucket.State)
			up += bucket.Up
			conclusive += bucket.Conclusive()
			samples += bucket.Up + bucket.Degraded + bucket.Down + bucket.Unknown + bucket.Retired
		}

		hv := HourView{
			Start:   at,
			Label:   fmt.Sprintf("%02d", at.Hour()),
			State:   probe.DeriveServiceState(states),
			Samples: samples,
			HasData: samples > 0,
		}
		if conclusive > 0 {
			pct := 100 * float64(up) / float64(conclusive)
			hv.UptimePct = &pct
		}
		hv.Tooltip = hourTooltip(cat, lang, at, hv)

		totalUp += up
		totalConclusive += conclusive
		hours = append(hours, hv)
	}

	var windowPct *float64
	if totalConclusive > 0 {
		pct := 100 * float64(totalUp) / float64(totalConclusive)
		windowPct = &pct
	}
	return hours, windowPct
}

func hourTooltip(cat i18n.Catalog, lang i18n.Lang, at time.Time, h HourView) string {
	span := fmt.Sprintf("%s %02d:00–%02d:00", formatDay(lang, at), at.Hour(), (at.Hour()+1)%24)
	if !h.HasData {
		return fmt.Sprintf("%s — %s", span, cat.T("label.no_data"))
	}
	if h.UptimePct == nil {
		return fmt.Sprintf("%s — %s", span, cat.T("state."+string(h.State)))
	}
	return fmt.Sprintf("%s — %s %.2f%%%s", span, cat.T("label.uptime"), *h.UptimePct, samples(lang, h.Samples))
}

// formatDay writes a date the way each locale expects. A shared format would
// read as broken in one of them.
func formatDay(lang i18n.Lang, t time.Time) string {
	if lang == i18n.LangZhTW {
		return fmt.Sprintf("%d月%d日", int(t.Month()), t.Day())
	}
	return t.Format("Jan 2")
}

// samples uses full-width parentheses in Chinese, where half-width ones sit
// badly against the surrounding characters.
func samples(lang i18n.Lang, n int) string {
	if lang == i18n.LangZhTW {
		return fmt.Sprintf("（%d）", n)
	}
	return fmt.Sprintf(" (%d)", n)
}

func startOfHour(t time.Time, loc *time.Location) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, loc)
}

func dayTooltip(cat i18n.Catalog, lang i18n.Lang, d DayView) string {
	if !d.HasData {
		return fmt.Sprintf("%s — %s", d.Label, cat.T("label.no_data"))
	}
	if d.UptimePct == nil {
		return fmt.Sprintf("%s — %s", d.Label, cat.T("state."+string(d.State)))
	}
	return fmt.Sprintf("%s — %s %.2f%%%s", d.Label, cat.T("label.uptime"), *d.UptimePct, samples(lang, d.Samples))
}

// earliestSince reports how long the service has held its current state,
// taken from the check that entered that state first.
func earliestSince(checks []CheckView, serviceState probe.State) *time.Time {
	var out *time.Time
	for _, c := range checks {
		if c.Since == nil || c.State != serviceState {
			continue
		}
		if out == nil || c.Since.Before(*out) {
			t := *c.Since
			out = &t
		}
	}
	return out
}

func bannerID(s probe.State) string {
	switch s {
	case probe.StateUp, probe.StateRetired:
		return "banner.ok"
	case probe.StateDegraded:
		return "banner.degraded"
	case probe.StateDown:
		return "banner.down"
	default:
		return "banner.unknown"
	}
}

func startOfDay(t time.Time, loc *time.Location) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}
