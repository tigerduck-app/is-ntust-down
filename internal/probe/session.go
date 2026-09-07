package probe

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// maxBodyBytes caps how much of a response we read. Probes only ever inspect
// forms and small JSON payloads, and an unbounded read turns one misbehaving
// upstream into an out-of-memory kill for the whole monitor.
const maxBodyBytes = 4 << 20

// maxRedirects bounds the SSO chain. The real flow is six hops; anything
// longer is a redirect loop, and following it forever would hold a probe
// goroutine open until its context expires.
const maxRedirects = 6

// Page is one fetched response. URL is the *final* URL after redirects, which
// is what tells us whether we ended up at the SSO wall or past it.
type Page struct {
	Body   string
	URL    *url.URL
	Status int
}

// Session is a single probe run's HTTP context.
//
// Each run gets a fresh cookie jar, discarded when the run ends. Probes must
// not share session state: a cookie left over from a previous successful login
// would let a broken SSO look healthy, which is precisely the failure this
// service exists to catch.
type Session struct {
	client *http.Client
	ua     string
}

func NewSession(timeout time.Duration, ua string) (*Session, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &Session{
		ua: ua,
		client: &http.Client{
			Jar:     jar,
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= maxRedirects {
					return errors.New("too many redirects")
				}
				return nil
			},
		},
	}, nil
}

const (
	acceptHTML = "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"
	acceptJSON = "application/json, text/plain, */*"
)

func (s *Session) Get(ctx context.Context, rawURL string) (*Page, error) {
	return s.do(ctx, http.MethodGet, rawURL, nil, acceptHTML, "", "")
}

// GetJSON sets an explicit JSON Accept header. querycourse performs strict
// content negotiation and answers an HTML-preferring Accept with a 500 and an
// XML error body, so this is load-bearing rather than cosmetic.
func (s *Session) GetJSON(ctx context.Context, rawURL string) (*Page, error) {
	return s.do(ctx, http.MethodGet, rawURL, nil, acceptJSON, "", "")
}

func (s *Session) PostForm(ctx context.Context, u *url.URL, form Form, referer, origin string) (*Page, error) {
	body := strings.NewReader(form.Values().Encode())
	return s.do(ctx, http.MethodPost, u.String(), body, acceptHTML, referer, origin)
}

func (s *Session) do(ctx context.Context, method, rawURL string, body io.Reader, accept, referer, origin string) (*Page, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	// The NTUST edge (Citrix NetScaler) serves an anti-bot JS challenge to
	// generic browser UAs, which breaks the redirect chain mid-flow. The
	// Moodle app UA is on its allow-list, so it is set on every hop rather
	// than only on Moodle hosts.
	req.Header.Set("User-Agent", s.ua)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "zh-TW,zh;q=0.9,en-US;q=0.8,en;q=0.7")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, err
	}
	return &Page{Body: string(raw), URL: resp.Request.URL, Status: resp.StatusCode}, nil
}

// ResolveBridges follows auto-submitting OIDC/SAML bridge forms until the
// chain settles, we reach the SSO wall, or we run out of hops.
func (s *Session) ResolveBridges(ctx context.Context, p *Page, ssoHost string, maxSteps int) (*Page, error) {
	current := p
	for range maxSteps {
		if IsSSOLoginPage(current.Body, current.URL, ssoHost) {
			return current, nil
		}
		bridge, ok := FindOIDCBridge(ParseForms(current.Body))
		if !ok {
			return current, nil
		}
		target, err := current.URL.Parse(bridge.Action)
		if err != nil {
			return current, err
		}
		next, err := s.PostForm(ctx, target, bridge, current.URL.String(), "")
		if err != nil {
			return current, err
		}
		current = next
	}
	return current, nil
}

// sensitiveParam matches the secrets that ride NTUST's auth chain in query
// strings and hidden inputs. The Android client carries the same list.
var sensitiveParam = regexp.MustCompile(`(?i)\b(wstoken|passport|token|privatetoken|code|state|password)=([^&\s"']+)`)

// Redact strips credential material from a string before it can reach a log.
// Probe failures are logged with the URL that failed, and the SSO chain puts
// tokens in exactly those URLs.
func Redact(s string) string {
	return sensitiveParam.ReplaceAllString(s, "$1=***")
}

// classifyTransport maps a transport failure onto the public vocabulary.
// The error text itself is discarded: it can embed the full request URL,
// tokens and all.
func classifyTransport(err error) ReasonCode {
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return ReasonTimeout
	}
	return ReasonUnreachable
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// classifyStatus maps an unexpected HTTP status onto the public vocabulary.
func classifyStatus(code int) ReasonCode {
	switch {
	case code >= 500:
		return ReasonHTTP5xx
	case code >= 400:
		return ReasonHTTP4xx
	default:
		return ReasonUnexpectedBody
	}
}
