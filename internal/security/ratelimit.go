package security

import (
	"context"
	"math"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimiter applies a token bucket per client IP. Idle buckets are evicted
// so memory stays bounded under address churn.
//
// The client IP is taken from the TCP peer address only. X-Forwarded-For is
// intentionally ignored: trusting it without a known proxy list lets any
// client pick its own identity and bypass the limit.
type RateLimiter struct {
	rps   rate.Limit
	burst int
	ttl   time.Duration
	max   int

	mu      sync.Mutex
	clients map[string]*client
}

type client struct {
	lim  *rate.Limiter
	seen time.Time
}

// NewRateLimiter creates a limiter allowing rps requests/second with the given
// burst per client. Call Run to start background eviction.
func NewRateLimiter(rps float64, burst int) *RateLimiter {
	return &RateLimiter{
		rps:     rate.Limit(rps),
		burst:   burst,
		ttl:     3 * time.Minute,
		max:     100_000,
		clients: make(map[string]*client),
	}
}

// Run evicts idle clients until ctx is cancelled.
func (l *RateLimiter) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			l.mu.Lock()
			for ip, c := range l.clients {
				if now.Sub(c.seen) > l.ttl {
					delete(l.clients, ip)
				}
			}
			l.mu.Unlock()
		}
	}
}

func (l *RateLimiter) limiter(ip string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, ok := l.clients[ip]
	if !ok {
		if len(l.clients) >= l.max {
			// Table full (likely an address-spraying attack): share one
			// tight bucket rather than grow without bound.
			ip = "overflow"
			if c, ok = l.clients[ip]; ok {
				c.seen = time.Now()
				return c.lim
			}
		}
		c = &client{lim: rate.NewLimiter(l.rps, l.burst)}
		l.clients[ip] = c
	}
	c.seen = time.Now()
	return c.lim
}

// Middleware responds 429 with Retry-After when a client exceeds its budget.
func (l *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}
		lim := l.limiter(ip)
		if !lim.Allow() {
			retry := math.Ceil(1 / float64(l.rps))
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Max(retry, 1))))
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
