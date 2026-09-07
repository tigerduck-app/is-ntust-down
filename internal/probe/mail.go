package probe

import (
	"bufio"
	"context"
	"net"
	"strings"
	"time"
)

// MailConfig carries what the webmail and mail-transport probes need.
//
// NTUST webmail is Mail2000 (Openfind) on the university's own server and does
// not use ssoam2 at all — it has its own USERID/PASSWD login. That login is
// additionally protected by a CAPTCHA and a client-side challenge-response, so
// there is no credentialed check here to match the Moodle and course selection
// ones. What can be verified without credentials is that the login CGI is
// alive and serving a usable form.
type MailConfig struct {
	BaseURL       string
	LoginPath     string
	UserAgent     string
	Timeout       time.Duration
	SMTPHosts     []string
	SMTPCheckPort string
}

// --- mail.site ---

type MailSiteProbe struct{ Cfg MailConfig }

func (p *MailSiteProbe) Key() string { return "mail.site" }

func (p *MailSiteProbe) Run(ctx context.Context) Result {
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

// --- mail.login_page ---

// MailLoginPageProbe checks that the login CGI renders a usable form.
//
// The landing page is a static file served by Apache, so it stays 200 even
// when the Mail2000 backend behind it is dead. Asserting the form's own fields
// are present is what separates "the web server is up" from "you could log in
// if you tried" — the same reasoning as calling Moodle's webservice endpoint
// rather than fetching its front page.
type MailLoginPageProbe struct{ Cfg MailConfig }

func (p *MailLoginPageProbe) Key() string { return "mail.login_page" }

func (p *MailLoginPageProbe) Run(ctx context.Context) Result {
	s, err := NewSession(p.Cfg.Timeout, p.Cfg.UserAgent)
	if err != nil {
		return down(0, 0, ReasonUnreachable)
	}
	started := time.Now()
	page, err := s.Get(ctx, strings.TrimRight(p.Cfg.BaseURL, "/")+p.Cfg.LoginPath)
	latency := int(time.Since(started).Milliseconds())
	if err != nil {
		return down(latency, 0, classifyTransport(err))
	}
	if page.Status >= 400 {
		return down(latency, page.Status, classifyStatus(page.Status))
	}

	// Mail2000 spreads several forms across the page (normal, ajax, FIDO).
	// Any one carrying the credential fields plus the session token means the
	// CGI ran and produced a form that could actually be submitted.
	for _, f := range ParseForms(page.Body) {
		if f.Has("USERID") && f.Has("PASSWD") && f.Has("CLIENT_TOKEN") {
			return up(latency, page.Status)
		}
	}
	return down(latency, page.Status, ReasonLoginFormMissing)
}

// --- mail.smtp ---

// MailSMTPProbe checks that the MX hosts accept connections and greet.
//
// This is the only mail check that says anything about messages actually being
// delivered rather than the web UI being reachable. It is off by default
// because many hosting providers block outbound port 25, which would report
// NTUST as down for reasons that have nothing to do with NTUST.
type MailSMTPProbe struct{ Cfg MailConfig }

func (p *MailSMTPProbe) Key() string { return "mail.smtp" }

func (p *MailSMTPProbe) Run(ctx context.Context) Result {
	if len(p.Cfg.SMTPHosts) == 0 {
		return Result{State: StateUnknown, ReasonCode: ReasonNotConfigured}
	}

	started := time.Now()
	var greeted, failed int
	for _, host := range p.Cfg.SMTPHosts {
		if smtpGreets(ctx, host, p.Cfg.Timeout) {
			greeted++
		} else {
			failed++
		}
	}
	latency := int(time.Since(started).Milliseconds())

	switch {
	case greeted == 0:
		return down(latency, 0, ReasonUnreachable)
	case failed > 0:
		// Mail still flows while one MX answers, but a silent loss of
		// redundancy is worth surfacing rather than rounding up to healthy.
		return Result{State: StateDegraded, LatencyMS: latency, ReasonCode: ReasonMXPartial}
	default:
		return up(latency, 0)
	}
}

// smtpGreets reports whether a host answers with a 220 service banner.
func smtpGreets(ctx context.Context, hostPort string, timeout time.Duration) bool {
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return false
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(timeout))
	banner, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return false
	}
	// Say goodbye properly rather than dropping the socket; an abandoned
	// connection looks like a scanner to the far side.
	_, _ = conn.Write([]byte("QUIT\r\n"))
	return strings.HasPrefix(strings.TrimSpace(banner), "220")
}
