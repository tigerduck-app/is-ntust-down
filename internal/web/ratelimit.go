package web

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RateLimiter is a per-client token bucket held in memory.
//
// A single binary needs no shared store for this. The real defence against
// traffic is the cache headers on the responses; this limiter exists to stop
// one badly written consumer from hammering the database.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket

	perMinute float64
	burst     float64
	trusted   []*net.IPNet
	now       func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

// bucketSweepThreshold bounds memory. Without eviction, every distinct client
// IP is remembered forever, which turns a public endpoint into a slow leak.
const bucketSweepThreshold = 10000

func NewRateLimiter(perMinute, burst int, trusted []*net.IPNet) *RateLimiter {
	if burst <= 0 {
		burst = perMinute
	}
	return &RateLimiter{
		buckets:   map[string]*bucket{},
		perMinute: float64(perMinute),
		burst:     float64(burst),
		trusted:   trusted,
		now:       time.Now,
	}
}

// Allow consumes a token, reporting whether the request may proceed, how many
// tokens remain, and how long until the bucket refills by one.
func (rl *RateLimiter) Allow(key string) (bool, int, time.Duration) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	b, ok := rl.buckets[key]
	if !ok {
		if len(rl.buckets) >= bucketSweepThreshold {
			rl.sweepLocked(now)
		}
		b = &bucket{tokens: rl.burst, last: now}
		rl.buckets[key] = b
	}

	refill := now.Sub(b.last).Minutes() * rl.perMinute
	b.tokens = math.Min(rl.burst, b.tokens+refill)
	b.last = now

	perSecond := rl.perMinute / 60
	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) / perSecond * float64(time.Second))
		return false, 0, wait
	}
	b.tokens--
	refillOne := time.Duration(float64(time.Second) / perSecond)
	return true, int(b.tokens), refillOne
}

func (rl *RateLimiter) sweepLocked(now time.Time) {
	for k, b := range rl.buckets {
		if b.tokens >= rl.burst && now.Sub(b.last) > 10*time.Minute {
			delete(rl.buckets, k)
		}
	}
}

// ClientIP identifies the caller.
//
// X-Forwarded-For is honoured only when the immediate peer is a configured
// trusted proxy. Behind a proxy without this check every request looks like it
// came from the proxy and one client starves everyone; exposed directly with
// the header trusted unconditionally, any client sets its own identity and the
// limiter becomes decorative.
func (rl *RateLimiter) ClientIP(r *http.Request) string {
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	if !rl.isTrusted(peer) {
		return peer
	}

	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return peer
	}
	// Walk right to left and take the first address the trusted chain did not
	// vouch for. Anything further left was supplied by the client itself.
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(parts[i])
		if candidate == "" {
			continue
		}
		if !rl.isTrusted(candidate) {
			return candidate
		}
	}
	return peer
}

func (rl *RateLimiter) isTrusted(ipStr string) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, n := range rl.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Middleware applies the limiter and advertises its state on every response.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	limit := strconv.Itoa(int(rl.perMinute))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed, remaining, reset := rl.Allow(rl.ClientIP(r))
		resetSeconds := int(math.Ceil(reset.Seconds()))

		w.Header().Set("RateLimit-Limit", limit)
		w.Header().Set("RateLimit-Remaining", strconv.Itoa(remaining))
		w.Header().Set("RateLimit-Reset", strconv.Itoa(resetSeconds))

		if !allowed {
			w.Header().Set("Retry-After", strconv.Itoa(max(resetSeconds, 1)))
			writeJSONError(w, http.StatusTooManyRequests, "rate_limited",
				"Too many requests. See the RateLimit-Reset header.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
