package probe

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// mail2000LoginPage mirrors the shape of the real Mail2000 login: several
// forms on one page, with the credential fields on only one of them.
const mail2000LoginPage = `<!DOCTYPE html><html><head><title>國立臺灣科技大學(臺科大) WebMail</title></head><body>
<form name="ajax_login" id="ajax_form" hidden action="/cgi-bin/login" method="post">
  <input type="hidden" name="CLIENT_TOKEN" value="tok-abc" />
  <input type="hidden" name="MAKE_CHALLENGE" value="1" />
</form>
<form name="login" id="normal_form" action="/cgi-bin/login" method="post">
  <input type="hidden" name="CLIENT_TOKEN" value="tok-abc" />
  <input type="hidden" name="CHALLENGE" value="" />
  <input type="text" name="USERID" value="" />
  <input type="password" name="PASSWD" value="" />
  <input type="text" name="CaptAns" value="" />
</form>
<form name="fido_auth" action="/cgi-bin/login" method="post">
  <input type="hidden" name="fido_credential_id" value="" />
</form></body></html>`

// mail2000CGIError is what Apache serves when the backend CGI has fallen over:
// still a page, still HTTP 200, no usable form.
const mail2000CGIError = `<!DOCTYPE html><html><body><h1>Service Temporarily Unavailable</h1></body></html>`

func mailCfg(base string) MailConfig {
	return MailConfig{
		BaseURL:   base,
		LoginPath: "/cgi-bin/login?index=1",
		UserAgent: testUA,
		Timeout:   5 * time.Second,
	}
}

func TestMailSiteProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><meta http-equiv="refresh" content="0;URL='/cgi-bin/login?index=1'"></html>`))
	}))
	defer srv.Close()

	p := &MailSiteProbe{Cfg: mailCfg(srv.URL)}
	if got := p.Run(testCtx(t)); got.State != StateUp {
		t.Fatalf("state = %q (%s), want up", got.State, got.ReasonCode)
	}
}

func TestMailLoginPageProbe(t *testing.T) {
	t.Run("a usable login form is healthy", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(mail2000LoginPage))
		}))
		defer srv.Close()
		p := &MailLoginPageProbe{Cfg: mailCfg(srv.URL)}
		if got := p.Run(testCtx(t)); got.State != StateUp {
			t.Fatalf("state = %q (%s), want up", got.State, got.ReasonCode)
		}
	})

	t.Run("apache alive but the CGI is dead", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 200 with no form: exactly the case a homepage check would pass.
			_, _ = w.Write([]byte(mail2000CGIError))
		}))
		defer srv.Close()
		p := &MailLoginPageProbe{Cfg: mailCfg(srv.URL)}
		got := p.Run(testCtx(t))
		if got.State != StateDown || got.ReasonCode != ReasonLoginFormMissing {
			t.Fatalf("got %q/%q, want down/login_form_missing", got.State, got.ReasonCode)
		}
	})

	t.Run("never submits credentials", func(t *testing.T) {
		var methods []string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			methods = append(methods, r.Method)
			_, _ = w.Write([]byte(mail2000LoginPage))
		}))
		defer srv.Close()
		p := &MailLoginPageProbe{Cfg: mailCfg(srv.URL)}
		p.Run(testCtx(t))
		for _, m := range methods {
			// The real login is CAPTCHA-protected; this probe must stay a
			// read-only check of the form's presence.
			if m != http.MethodGet {
				t.Fatalf("probe issued a %s to the login endpoint", m)
			}
		}
	})
}

// fakeSMTP answers with a banner, or accepts and stays silent when greet is
// false — a hung listener, not a closed port.
func fakeSMTP(t *testing.T, greet bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				if greet {
					_, _ = fmt.Fprint(c, "220 mg1.ntust.edu.tw ESMTP ready\r\n")
				}
				buf := make([]byte, 64)
				_, _ = c.Read(buf)
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func TestMailSMTPProbe(t *testing.T) {
	t.Run("both MX hosts greet", func(t *testing.T) {
		cfg := mailCfg("http://unused")
		cfg.SMTPHosts = []string{fakeSMTP(t, true), fakeSMTP(t, true)}
		p := &MailSMTPProbe{Cfg: cfg}
		if got := p.Run(testCtx(t)); got.State != StateUp {
			t.Fatalf("state = %q (%s), want up", got.State, got.ReasonCode)
		}
	})

	t.Run("one MX down is degraded, not down", func(t *testing.T) {
		cfg := mailCfg("http://unused")
		cfg.Timeout = 500 * time.Millisecond
		cfg.SMTPHosts = []string{fakeSMTP(t, true), "127.0.0.1:1"}
		p := &MailSMTPProbe{Cfg: cfg}
		got := p.Run(testCtx(t))
		// Mail still flows on the surviving MX, so this is a loss of
		// redundancy rather than an outage.
		if got.State != StateDegraded || got.ReasonCode != ReasonMXPartial {
			t.Fatalf("got %q/%q, want degraded/mx_partial", got.State, got.ReasonCode)
		}
	})

	t.Run("no MX answers", func(t *testing.T) {
		cfg := mailCfg("http://unused")
		cfg.Timeout = 500 * time.Millisecond
		cfg.SMTPHosts = []string{"127.0.0.1:1", "127.0.0.1:2"}
		p := &MailSMTPProbe{Cfg: cfg}
		if got := p.Run(testCtx(t)); got.State != StateDown {
			t.Fatalf("state = %q, want down", got.State)
		}
	})

	t.Run("unconfigured reports unknown, not down", func(t *testing.T) {
		p := &MailSMTPProbe{Cfg: mailCfg("http://unused")}
		got := p.Run(testCtx(t))
		if got.State != StateUnknown || got.ReasonCode != ReasonNotConfigured {
			t.Fatalf("got %q/%q, want unknown/not_configured", got.State, got.ReasonCode)
		}
	})
}
