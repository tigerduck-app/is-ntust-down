package probe

import (
	"context"
	"net/url"
	"strings"
	"time"
)

// CourseSelectionConfig carries what the 選課系統 probes need.
type CourseSelectionConfig struct {
	BaseURL   string
	ProbePath string
	SSOHost   string
	UserAgent string
	Timeout   time.Duration
	Username  string
	Password  string
}

func (c CourseSelectionConfig) probeURL() string {
	return strings.TrimRight(c.BaseURL, "/") + c.ProbePath
}

func (c CourseSelectionConfig) host() string {
	u, err := url.Parse(c.BaseURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// --- courseselection.site ---

type CourseSelectionSiteProbe struct{ Cfg CourseSelectionConfig }

func (p *CourseSelectionSiteProbe) Key() string { return "courseselection.site" }

// Run checks reachability only. A redirect to the SSO wall is a healthy
// outcome here — that is what an unauthenticated visitor is supposed to get,
// and treating it as failure would report an outage every time the site works
// exactly as designed.
func (p *CourseSelectionSiteProbe) Run(ctx context.Context) Result {
	s, err := NewSession(p.Cfg.Timeout, p.Cfg.UserAgent)
	if err != nil {
		return down(0, 0, ReasonUnreachable)
	}
	started := time.Now()
	page, err := s.Get(ctx, p.Cfg.BaseURL)
	latency := int(time.Since(started).Milliseconds())
	if err != nil {
		return down(latency, 0, classifyTransport(err))
	}
	if page.Status >= 400 {
		return down(latency, page.Status, classifyStatus(page.Status))
	}
	return up(latency, page.Status)
}

// --- courseselection.sso (structural) ---

type CourseSelectionSSOStructuralProbe struct{ Cfg CourseSelectionConfig }

func (p *CourseSelectionSSOStructuralProbe) Key() string { return "courseselection.sso_page" }

func (p *CourseSelectionSSOStructuralProbe) Run(ctx context.Context) Result {
	s, err := NewSession(p.Cfg.Timeout, p.Cfg.UserAgent)
	if err != nil {
		return down(0, 0, ReasonUnreachable)
	}
	started := time.Now()
	page, err := s.Get(ctx, p.Cfg.probeURL())
	if err != nil {
		return down(int(time.Since(started).Milliseconds()), 0, classifyTransport(err))
	}
	page, err = s.ResolveBridges(ctx, page, p.Cfg.SSOHost, maxRedirects)
	latency := int(time.Since(started).Milliseconds())
	if err != nil {
		return down(latency, page.Status, classifyTransport(err))
	}

	form, ok := FindLoginForm(ParseForms(page.Body))
	if !ok || !strings.Contains(page.URL.Host, p.Cfg.SSOHost) {
		return down(latency, page.Status, ReasonSSOFormMissing)
	}
	if form.Get("__RequestVerificationToken") == "" {
		return down(latency, page.Status, ReasonSSOFormMissing)
	}
	return up(latency, page.Status)
}

// --- courseselection.sso (credentialed) ---

// CourseSelectionSSOLoginProbe performs a real SSO login against the course
// selection system, ported from the generic service-login flow in the
// TigerDuck Android client.
type CourseSelectionSSOLoginProbe struct{ Cfg CourseSelectionConfig }

func (p *CourseSelectionSSOLoginProbe) Key() string { return "courseselection.sso_login" }

func (p *CourseSelectionSSOLoginProbe) Run(ctx context.Context) Result {
	if p.Cfg.Username == "" || p.Cfg.Password == "" {
		return Result{State: StateUnknown, ReasonCode: ReasonNotConfigured}
	}
	s, err := NewSession(p.Cfg.Timeout, p.Cfg.UserAgent)
	if err != nil {
		return down(0, 0, ReasonUnreachable)
	}

	started := time.Now()
	page, err := s.Get(ctx, p.Cfg.probeURL())
	if err != nil {
		return down(int(time.Since(started).Milliseconds()), 0, classifyTransport(err))
	}
	page, err = s.ResolveBridges(ctx, page, p.Cfg.SSOHost, maxRedirects)
	if err != nil {
		return down(int(time.Since(started).Milliseconds()), page.Status, classifyTransport(err))
	}

	// Already through without credentials would mean the service is not
	// actually gated — that is not an SSO success, so it is not reported as
	// one.
	if !IsSSOLoginPage(page.Body, page.URL, p.Cfg.SSOHost) {
		return down(int(time.Since(started).Milliseconds()), page.Status, ReasonSSOFormMissing)
	}

	form, _ := FindLoginForm(ParseForms(page.Body))
	target, err := page.URL.Parse(form.Action)
	if err != nil {
		return down(int(time.Since(started).Milliseconds()), page.Status, ReasonSSOFormMissing)
	}
	form.Set("Username", strings.ToUpper(strings.TrimSpace(p.Cfg.Username)))
	form.Set("Password", p.Cfg.Password)
	for _, k := range []string{"captcha", "cf-turnstile-response", "h-captcha-response", "g-recaptcha-response"} {
		if !form.Has(k) {
			form.Set(k, "")
		}
	}

	page, err = s.PostForm(ctx, target, form, page.URL.String(), "https://"+page.URL.Host)
	if err != nil {
		return down(int(time.Since(started).Milliseconds()), 0, classifyTransport(err))
	}
	page, err = s.ResolveBridges(ctx, page, p.Cfg.SSOHost, maxRedirects)
	latency := int(time.Since(started).Milliseconds())
	if err != nil {
		return down(latency, page.Status, classifyTransport(err))
	}

	// A login only succeeded if it ended on the course-selection site. The
	// IdP answering 200 proves nothing: when NTUST's redirect breaks, students
	// are left on an SSO page, sometimes the credential form again, while the
	// site itself is fine to visit directly. Nothing is resubmitted here, so
	// this stays a single attempt.
	if !strings.EqualFold(page.URL.Host, p.Cfg.host()) {
		return down(latency, page.Status, ReasonSSORedirectFailed)
	}
	if page.Status >= 400 {
		return down(latency, page.Status, classifyStatus(page.Status))
	}
	return up(latency, page.Status)
}
