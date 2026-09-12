package probe

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// MoodleConfig carries everything the three Moodle probes need. Credentials
// are empty when SSO login probing is disabled.
type MoodleConfig struct {
	BaseURL   string
	SSOHost   string
	UserAgent string
	Timeout   time.Duration
	Username  string
	Password  string
}

const moodleWSPath = "/webservice/rest/server.php"

type moodleSiteInfo struct {
	Username  string `json:"username"`
	SiteName  string `json:"sitename"`
	Exception string `json:"exception"`
	ErrorCode string `json:"errorcode"`
}

func moodleWSURL(baseURL, token string) string {
	q := url.Values{
		"wstoken":            {token},
		"wsfunction":         {"core_webservice_get_site_info"},
		"moodlewsrestformat": {"json"},
	}
	return strings.TrimRight(baseURL, "/") + moodleWSPath + "?" + q.Encode()
}

// --- moodle.site ---

// MoodleSiteProbe answers "is Moodle itself serving requests".
type MoodleSiteProbe struct{ Cfg MoodleConfig }

func (p *MoodleSiteProbe) Key() string { return "moodle.site" }

// Run calls the webservice endpoint with a deliberately empty token and
// treats a well-formed "invalidtoken" rejection as healthy.
//
// A plain homepage GET would not do: the NTUST edge can serve a cached or
// static front page while PHP and Moodle behind it are down, so the front
// page being 200 proves almost nothing. Getting a structured Moodle exception
// back proves Moodle actually bootstrapped and reached its auth layer.
//
// An empty wstoken is a token validation, not a login attempt, so this cannot
// contribute to the account lockout that guards the credentialed probe.
func (p *MoodleSiteProbe) Run(ctx context.Context) Result {
	s, err := NewSession(p.Cfg.Timeout, p.Cfg.UserAgent)
	if err != nil {
		return down(0, 0, ReasonUnreachable)
	}
	started := time.Now()
	page, err := s.Get(ctx, moodleWSURL(p.Cfg.BaseURL, ""))
	latency := int(time.Since(started).Milliseconds())
	if err != nil {
		return down(latency, 0, classifyTransport(err))
	}
	if page.Status != 200 {
		return down(latency, page.Status, classifyStatus(page.Status))
	}

	var info moodleSiteInfo
	if err := json.Unmarshal([]byte(page.Body), &info); err != nil {
		// An HTML body here is the NetScaler anti-bot challenge or an error
		// page — Moodle is not answering, whatever the status code says.
		return down(latency, page.Status, ReasonUnexpectedBody)
	}
	if info.Exception == "" && info.SiteName == "" {
		return down(latency, page.Status, ReasonUnexpectedBody)
	}
	return up(latency, page.Status)
}

// --- moodle.sso (structural) ---

// MoodleSSOStructuralProbe walks the OIDC launch chain up to the credential
// prompt without ever submitting one.
type MoodleSSOStructuralProbe struct{ Cfg MoodleConfig }

func (p *MoodleSSOStructuralProbe) Key() string { return "moodle.sso_page" }

func (p *MoodleSSOStructuralProbe) Run(ctx context.Context) Result {
	s, err := NewSession(p.Cfg.Timeout, p.Cfg.UserAgent)
	if err != nil {
		return down(0, 0, ReasonUnreachable)
	}
	launch, err := moodleLaunchURL(p.Cfg.BaseURL)
	if err != nil {
		return down(0, 0, ReasonUnreachable)
	}

	started := time.Now()
	page, err := s.Get(ctx, launch)
	latency := int(time.Since(started).Milliseconds())
	if err != nil {
		return down(latency, 0, classifyTransport(err))
	}
	page, err = s.ResolveBridges(ctx, page, p.Cfg.SSOHost, maxRedirects)
	if err != nil {
		return down(int(time.Since(started).Milliseconds()), page.Status, classifyTransport(err))
	}
	latency = int(time.Since(started).Milliseconds())

	form, ok := FindLoginForm(ParseForms(page.Body))
	if !ok || !strings.Contains(page.URL.Host, p.Cfg.SSOHost) {
		return down(latency, page.Status, ReasonSSOFormMissing)
	}
	// An anti-forgery token is what makes the form submittable. A rendered
	// form without one means the IdP is serving a shell it cannot process,
	// which fails a real login while looking fine to a naive page check.
	if form.Get("__RequestVerificationToken") == "" {
		return down(latency, page.Status, ReasonSSOFormMissing)
	}
	return up(latency, page.Status)
}

// --- moodle.sso (credentialed) ---

// MoodleSSOLoginProbe performs a real end-to-end SSO login and verifies the
// resulting webservice token authenticates as the expected student.
//
// This flow is ported from the TigerDuck Android client. It must never fall
// back to POSTing /login/token.php: NTUST Moodle authenticates via OIDC only,
// and that endpoint counts every attempt as a failed login, banning the
// account within roughly ten attempts.
type MoodleSSOLoginProbe struct{ Cfg MoodleConfig }

func (p *MoodleSSOLoginProbe) Key() string { return "moodle.sso_login" }

func (p *MoodleSSOLoginProbe) Run(ctx context.Context) Result {
	if p.Cfg.Username == "" || p.Cfg.Password == "" {
		return Result{State: StateUnknown, ReasonCode: ReasonNotConfigured}
	}
	s, err := NewSession(p.Cfg.Timeout, p.Cfg.UserAgent)
	if err != nil {
		return down(0, 0, ReasonUnreachable)
	}
	launch, err := moodleLaunchURL(p.Cfg.BaseURL)
	if err != nil {
		return down(0, 0, ReasonUnreachable)
	}

	started := time.Now()
	page, err := s.Get(ctx, launch)
	if err != nil {
		return down(int(time.Since(started).Milliseconds()), 0, classifyTransport(err))
	}

	token, res := p.resolveToken(ctx, s, page, started)
	if res != nil {
		return *res
	}

	// The token existing is not the same as the token working. Verifying it
	// against the claimed student id is what makes this an SSO check rather
	// than a redirect check.
	verify, err := s.Get(ctx, moodleWSURL(p.Cfg.BaseURL, token))
	latency := int(time.Since(started).Milliseconds())
	if err != nil {
		return down(latency, 0, classifyTransport(err))
	}
	var info moodleSiteInfo
	if err := json.Unmarshal([]byte(verify.Body), &info); err != nil {
		return down(latency, verify.Status, ReasonUnexpectedBody)
	}
	if info.Exception != "" {
		return down(latency, verify.Status, ReasonTokenInvalid)
	}
	if !strings.EqualFold(info.Username, p.Cfg.Username) {
		return down(latency, verify.Status, ReasonUsernameMismatch)
	}
	return up(latency, verify.Status)
}

// resolveToken walks the OIDC chain, submitting credentials once, until the
// mobile launch confirmation page yields a token. It returns either a token or
// a terminal Result.
func (p *MoodleSSOLoginProbe) resolveToken(ctx context.Context, s *Session, page *Page, started time.Time) (string, *Result) {
	submitted := false

	for range maxRedirects {
		latency := int(time.Since(started).Milliseconds())

		if b64 := extractMobileToken(page.Body); b64 != "" {
			token, err := decodeWSToken(b64)
			if err != nil {
				r := down(latency, page.Status, ReasonTokenInvalid)
				return "", &r
			}
			return token, nil
		}

		if bridge, ok := FindOIDCBridge(ParseForms(page.Body)); ok {
			target, err := page.URL.Parse(bridge.Action)
			if err != nil {
				r := down(latency, page.Status, ReasonSSOBridgeMissing)
				return "", &r
			}
			next, err := s.PostForm(ctx, target, bridge, page.URL.String(), "")
			if err != nil {
				r := down(latency, page.Status, classifyTransport(err))
				return "", &r
			}
			page = next
			continue
		}

		if strings.Contains(page.URL.Host, p.Cfg.SSOHost) {
			// Still on the IdP after submitting means it never handed us back
			// to Moodle. NTUST re-renders the credential form both on a wrong
			// password and when its own redirect fails, and the monitor's
			// password does not change, so this is reported as the SSO
			// failing. It stays terminal either way: a second submission is
			// what walks the account toward a lockout.
			if submitted {
				r := down(latency, page.Status, ReasonSSORedirectFailed)
				return "", &r
			}
			form, ok := FindLoginForm(ParseForms(page.Body))
			if !ok || form.Get("__RequestVerificationToken") == "" {
				r := down(latency, page.Status, ReasonSSOFormMissing)
				return "", &r
			}
			next, err := p.submitLogin(ctx, s, page, form)
			if err != nil {
				r := down(latency, page.Status, classifyTransport(err))
				return "", &r
			}
			submitted = true
			page = next
			continue
		}

		r := down(latency, page.Status, ReasonSSOBridgeMissing)
		return "", &r
	}

	r := down(int(time.Since(started).Milliseconds()), page.Status, ReasonSSOBridgeMissing)
	return "", &r
}

func (p *MoodleSSOLoginProbe) submitLogin(ctx context.Context, s *Session, page *Page, form Form) (*Page, error) {
	target, err := page.URL.Parse(form.Action)
	if err != nil {
		return nil, err
	}
	form.Set("Username", strings.ToUpper(strings.TrimSpace(p.Cfg.Username)))
	form.Set("Password", p.Cfg.Password)
	// The IdP rejects the POST outright when its captcha fields are absent,
	// even though none is active for this flow.
	for _, k := range []string{"captcha", "cf-turnstile-response", "h-captcha-response", "g-recaptcha-response"} {
		if !form.Has(k) {
			form.Set(k, "")
		}
	}
	return s.PostForm(ctx, target, form, page.URL.String(), "https://"+page.URL.Host)
}

func moodleLaunchURL(baseURL string) (string, error) {
	passport, err := randomPassport()
	if err != nil {
		return "", err
	}
	q := url.Values{
		"service":   {"moodle_mobile_app"},
		"passport":  {passport},
		"urlscheme": {"moodlemobile"},
	}
	return strings.TrimRight(baseURL, "/") + "/admin/tool/mobile/launch.php?" + q.Encode(), nil
}

// randomPassport returns 128 bits of entropy. Moodle HMACs its token response
// against this value, so a weak passport would let anyone registering the
// moodlemobile:// scheme forge one.
func randomPassport() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

var mobileTokenRe = regexp.MustCompile(`moodlemobile://token=([A-Za-z0-9+/=_\-]+)`)

func extractMobileToken(body string) string {
	m := mobileTokenRe.FindStringSubmatch(body)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// decodeWSToken unwraps the launch payload, which is
// signature:::wstoken:::privatetoken. NTUST sometimes omits the third part,
// so both shapes are accepted.
func decodeWSToken(b64 string) (string, error) {
	var raw []byte
	var err error
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		raw, err = enc.DecodeString(b64)
		if err == nil {
			break
		}
	}
	if err != nil {
		return "", fmt.Errorf("token payload is not base64")
	}
	parts := strings.SplitN(string(raw), ":::", 3)
	if len(parts) < 2 || parts[1] == "" {
		return "", fmt.Errorf("unexpected token payload shape")
	}
	return parts[1], nil
}
