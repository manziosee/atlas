// Package api exposes the node's HTTP control plane.
//
//	GET /healthz     liveness (unauthenticated)
//	GET /readyz      readiness (unauthenticated)
//	GET /v1/node     this node's record          (bearer token)
//	GET /v1/members  this node's cluster view    (bearer token)
package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/metrics"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/version"
)

// maxBodyBytes caps request bodies; the API is read-only today.
const maxBodyBytes = 1 << 20

// Cluster is the subset of membership.Memberlist the API needs.
type Cluster interface {
	LocalNode() membership.Member
	Members() []membership.Member
}

// Deps wires the handler to the rest of the node.
type Deps struct {
	Cluster     Cluster
	Auth        *security.TokenAuthenticator
	RateLimiter *security.RateLimiter
	Log         *slog.Logger
	Ready       func() bool
}

// NewHandler builds the full middleware chain and routes.
func NewHandler(d Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if d.Ready != nil && !d.Ready() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	v1 := http.NewServeMux()
	v1.HandleFunc("GET /v1/node", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"node":    d.Cluster.LocalNode(),
			"version": version.Version,
			"commit":  version.Commit,
		})
	})
	v1.HandleFunc("GET /v1/members", func(w http.ResponseWriter, _ *http.Request) {
		members := d.Cluster.Members()
		writeJSON(w, http.StatusOK, map[string]any{"count": len(members), "members": members})
	})
	mux.Handle("/v1/", d.Auth.Middleware(v1))

	var h http.Handler = mux
	h = security.MaxBody(maxBodyBytes)(h)
	h = d.RateLimiter.Middleware(h)
	h = security.SecureHeaders(h)
	h = observe(d.Log, h)
	h = recoverer(d.Log, h)
	return h
}

// NewServer returns an http.Server with conservative timeouts that defeat
// slowloris-style resource exhaustion.
func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.code = code
	r.ResponseWriter.WriteHeader(code)
}

// observe adds a request ID, structured access logs and metrics.
func observe(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := newRequestID()
		w.Header().Set("X-Request-Id", id)
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}

		next.ServeHTTP(rec, r)

		// r.Pattern is set by ServeMux after routing; it keeps label
		// cardinality bounded (raw paths would let clients mint series).
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		elapsed := time.Since(start)
		metrics.HTTPRequests.WithLabelValues(route, r.Method, strconv.Itoa(rec.code)).Inc()
		metrics.HTTPDuration.WithLabelValues(route).Observe(elapsed.Seconds())
		log.Info("http request",
			"request_id", id, "method", r.Method, "path", r.URL.Path,
			"status", rec.code, "duration_ms", elapsed.Milliseconds(), "remote", r.RemoteAddr)
	})
}

func recoverer(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler { //nolint:errorlint // sentinel compared by identity per net/http docs
					panic(v)
				}
				log.Error("panic in handler", "panic", v, "path", r.URL.Path)
				http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func newRequestID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
