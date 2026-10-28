package httpapi

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// RateLimiter is a small in-process token bucket. It is intentionally simple:
// one process, one bucket per key. A multi-instance deployment must move the
// limiter to shared storage or rely on the reverse proxy; that is documented
// rather than hidden.
type RateLimiter struct {
	mu              sync.Mutex
	capacity        float64
	refillPerSecond float64
	window          time.Duration
	buckets         map[string]*tokenBucket
	now             func() time.Time
	lastSweep       time.Time
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// NewRateLimiter allows max requests per window per key (with a burst of max).
// Invalid parameters fall back to a conservative default rather than disabling
// rate limiting.
func NewRateLimiter(max int, window time.Duration) *RateLimiter {
	if max < 1 {
		max = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	return &RateLimiter{
		capacity:        float64(max),
		refillPerSecond: float64(max) / window.Seconds(),
		window:          window,
		buckets:         make(map[string]*tokenBucket),
		now:             time.Now,
	}
}

// Allow consumes one token for key and reports whether the request may
// proceed.
func (l *RateLimiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweepLocked(now)

	bucket, ok := l.buckets[key]
	if !ok {
		bucket = &tokenBucket{tokens: l.capacity, last: now}
		l.buckets[key] = bucket
	}
	elapsed := now.Sub(bucket.last).Seconds()
	if elapsed > 0 {
		bucket.tokens += elapsed * l.refillPerSecond
		if bucket.tokens > l.capacity {
			bucket.tokens = l.capacity
		}
		bucket.last = now
	}
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

// sweepLocked bounds memory growth from one-off client addresses. It runs only
// while the mutex is held and only when the bucket map is large or a window
// has elapsed.
func (l *RateLimiter) sweepLocked(now time.Time) {
	if len(l.buckets) < 1024 && now.Sub(l.lastSweep) < l.window {
		return
	}
	cutoff := now.Add(-l.window)
	for key, bucket := range l.buckets {
		if bucket.last.Before(cutoff) {
			delete(l.buckets, key)
		}
	}
	l.lastSweep = now
}

// RateLimit rejects requests over the limiter's budget with the standard error
// envelope. keyFn should include the route as well as the client identity so
// one route's attempts do not exhaust another's budget.
func RateLimit(limiter *RateLimiter, keyFn func(*http.Request) string) Middleware {
	if keyFn == nil {
		keyFn = clientIPKey
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limiter != nil && !limiter.Allow(keyFn(r)) {
				w.Header().Set("Retry-After", "60")
				writeError(w, r, errRateLimited)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIPKey(r *http.Request) string {
	return clientIP(r)
}

// clientIP uses the transport peer address. X-Forwarded-For is deliberately not
// trusted: with a reverse proxy, rate limiting should either be configured at
// the proxy or this function extended behind an explicit trusted-proxy setting.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	if r.RemoteAddr == "" {
		return "unknown"
	}
	return r.RemoteAddr
}

// authRateLimitKey isolates each auth path's bucket per client.
func authRateLimitKey(r *http.Request) string {
	return r.URL.Path + "|" + clientIP(r)
}
