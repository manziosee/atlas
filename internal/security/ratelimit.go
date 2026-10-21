package security

import (
	"context"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimiter applies a token bucket per client IP. Idle buckets are evicted
// so memory stays bounded under address churn.
//
// Forwarded client addresses are honored only when the TCP peer is inside an
// explicitly configured trusted proxy subnet.
type RateLimiter struct {
	rps            rate.Limit
	burst          int
	ttl            time.Duration
	max            int
	trustedProxies []*net.IPNet

	mu      sync.Mutex
	clients map[string]*client
}

type client struct {
	lim  *rate.Limiter
	seen time.Time
}

// NewRateLimiter creates a limiter allowing rps requests/second with the given
// burst per client. Call Run to start background eviction.
func NewRateLimiter(rps float64, burst int, trustedProxies ...*net.IPNet) *RateLimiter {
	return &RateLimiter{
		rps:            rate.Limit(rps),
		burst:          burst,
		ttl:            3 * time.Minute,
		max:            100_000,
		trustedProxies: append([]*net.IPNet(nil), trustedProxies...),
		clients:        make(map[string]*client),
	}
}

// Update applies validated rate and proxy settings to existing and future
// client buckets. Callers must validate positive limits first.
func (l *RateLimiter) Update(rps float64, burst int, trustedProxies ...*net.IPNet) {
	l.mu.Lock()
	l.rps, l.burst = rate.Limit(rps), burst
	l.trustedProxies = append([]*net.IPNet(nil), trustedProxies...)
	for _, client := range l.clients {
		client.lim.SetLimit(rate.Limit(rps))
		client.lim.SetBurst(burst)
	}
	l.mu.Unlock()
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
		if !l.AllowClient(r.RemoteAddr, r.Header.Get("X-Forwarded-For")) {
			retry := math.Ceil(1 / float64(l.currentLimit()))
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Max(retry, 1))))
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *RateLimiter) currentLimit() rate.Limit {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rps
}

// AllowClient applies the token bucket to a transport peer, optionally using
// a forwarded chain when the transport peer is a trusted proxy.
func (l *RateLimiter) AllowClient(remoteAddr, forwarded string) bool {
	return l.limiter(l.clientIP(remoteAddr, forwarded)).Allow()
}

func (l *RateLimiter) clientIP(remoteAddr, forwarded string) string {
	peer, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		peer = remoteAddr
	}
	peerIP := net.ParseIP(peer)
	if peerIP == nil || !l.isTrusted(peerIP) {
		return peer
	}

	if forwarded == "" {
		return peerIP.String()
	}
	chain := strings.Split(forwarded, ",")
	ips := make([]net.IP, 0, len(chain)+1)
	for _, item := range chain {
		ip := net.ParseIP(strings.TrimSpace(item))
		if ip == nil {
			return peerIP.String()
		}
		ips = append(ips, ip)
	}
	ips = append(ips, peerIP)
	for i := len(ips) - 1; i >= 0; i-- {
		if !l.isTrusted(ips[i]) {
			return ips[i].String()
		}
	}
	return ips[0].String()
}

func (l *RateLimiter) isTrusted(ip net.IP) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, subnet := range l.trustedProxies {
		if subnet.Contains(ip) {
			return true
		}
	}
	return false
}
