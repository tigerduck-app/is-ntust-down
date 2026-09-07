package web

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatalf("bad CIDR %q: %v", s, err)
	}
	return n
}

func TestAllowExhaustsBurstThenRefills(t *testing.T) {
	rl := NewRateLimiter(60, 3, nil)
	now := time.Now()
	rl.now = func() time.Time { return now }

	for i := range 3 {
		if ok, _, _ := rl.Allow("client"); !ok {
			t.Fatalf("request %d denied inside the burst", i+1)
		}
	}
	ok, remaining, reset := rl.Allow("client")
	if ok {
		t.Fatal("burst was not enforced")
	}
	if remaining != 0 {
		t.Errorf("remaining = %d, want 0", remaining)
	}
	if reset <= 0 {
		t.Error("a denied request must say when to retry")
	}

	// 60/minute is one per second.
	now = now.Add(time.Second)
	if ok, _, _ := rl.Allow("client"); !ok {
		t.Fatal("bucket did not refill")
	}
}

func TestClientsAreLimitedIndependently(t *testing.T) {
	rl := NewRateLimiter(60, 1, nil)
	if ok, _, _ := rl.Allow("a"); !ok {
		t.Fatal("first client denied")
	}
	if ok, _, _ := rl.Allow("b"); !ok {
		t.Fatal("one client's usage limited another")
	}
}

func TestClientIPIgnoresForwardedHeaderFromUntrustedPeers(t *testing.T) {
	rl := NewRateLimiter(60, 10, nil) // trust nothing

	r := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")

	// Honouring this header from a direct client would let anyone mint a
	// fresh identity per request and walk straight past the limiter.
	if got := rl.ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("ClientIP = %q, want the peer address 203.0.113.9", got)
	}
}

func TestClientIPTrustsForwardedHeaderFromAProxy(t *testing.T) {
	rl := NewRateLimiter(60, 10, []*net.IPNet{mustCIDR(t, "10.0.0.0/8")})

	r := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	r.RemoteAddr = "10.0.0.5:443"
	r.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.9")

	// The rightmost address the trusted chain did not vouch for is the real
	// client; anything further left it supplied about itself.
	if got := rl.ClientIP(r); got != "198.51.100.7" {
		t.Fatalf("ClientIP = %q, want 198.51.100.7", got)
	}
}

func TestMiddlewareAdvertisesLimitsAnd429s(t *testing.T) {
	rl := NewRateLimiter(60, 1, nil)
	h := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if first.Code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", first.Code)
	}
	if first.Header().Get("RateLimit-Limit") != "60" {
		t.Errorf("RateLimit-Limit = %q", first.Header().Get("RateLimit-Limit"))
	}

	second := httptest.NewRecorder()
	h.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second request = %d, want 429", second.Code)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Error("429 must carry Retry-After")
	}
}
