// Package api exposes the node's HTTP control plane. api/v1/openapi.yaml is
// the full reference; every /v1 route requires a bearer token.
//
//	GET    /healthz, /readyz                 liveness and readiness (public)
//	GET    /v1/node, /v1/members             this node and its cluster view
//	POST   /v1/services                      register (idempotent with id or Idempotency-Key)
//	GET    /v1/services                      paged, filtered listing
//	GET    /v1/services/{id}                 one instance
//	PUT    /v1/services/{id}                 create or replace
//	PATCH  /v1/services/{id}                 partial update
//	DELETE /v1/services/{id}                 deregister
//	POST   /v1/services/{id}/heartbeat       TTL heartbeat
//	GET    /v1/lookup/{name}                 routed instance selection
//	GET    /v1/events                        membership and registry event stream
//	*      /v1/admin/...                     admin operations (admin scope)
package api

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/manziosee/atlas/internal/events"
	"github.com/manziosee/atlas/internal/httperr"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/metrics"
	"github.com/manziosee/atlas/internal/routing"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/version"
)

// maxBodyBytes caps request bodies accepted by the control API.
const maxBodyBytes = 1 << 20

//go:embed dashboard/*
var dashboardFiles embed.FS

// Cluster is the subset of membership.Memberlist the API needs.
type Cluster interface {
	LocalNode() membership.Member
	Members() []membership.Member
}

// Deps wires the handler to the rest of the node.
type Deps struct {
	Cluster     Cluster
	Registry    ServiceRegistry
	Selector    *routing.Selector
	Auth        *security.TokenAuthenticator
	RateLimiter *security.RateLimiter
	Log         *slog.Logger
	Ready       func() bool
	// Events feeds GET /v1/events and the gRPC Watch stream.
	Events *events.Broker
	// Replay returns a Fly-Replay header value that routes a request to the
	// node that owns a service, or "" when replay is unavailable.
	Replay func(ownerNodeID string) string
}

// NewHandler builds the full middleware chain and routes.
func NewHandler(d Deps) http.Handler {
	if d.Selector == nil {
		d.Selector = routing.NewSelector()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dashboard", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/", http.StatusPermanentRedirect)
	})
	assets, _ := fs.Sub(dashboardFiles, "dashboard")
	static := http.StripPrefix("/dashboard/", http.FileServer(http.FS(assets)))
	mux.Handle("GET /dashboard/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		static.ServeHTTP(w, r)
	}))
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
	v1.HandleFunc("POST /v1/services", d.registerService)
	v1.HandleFunc("GET /v1/services", d.listServices)
	v1.HandleFunc("GET /v1/lookup/{name}", d.lookupService)
	v1.HandleFunc("GET /v1/services/{id}", d.getService)
	v1.HandleFunc("PUT /v1/services/{id}", d.replaceService)
	v1.HandleFunc("PATCH /v1/services/{id}", d.patchService)
	v1.HandleFunc("DELETE /v1/services/{id}", d.deleteService)
	v1.HandleFunc("POST /v1/services/{id}/heartbeat", d.heartbeat)
	v1.HandleFunc("GET /v1/events", d.streamEvents)
	v1.HandleFunc("GET /v1/admin/credentials", d.listCredentials)
	v1.HandleFunc("POST /v1/admin/backup", d.backup)
	v1.HandleFunc("DELETE /v1/admin/nodes/{id}", d.evictNode)
	mux.Handle("/v1/", d.Auth.Middleware(jsonFallback(v1)))

	h := jsonFallback(mux)
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

// jsonFallback answers unmatched routes and wrong methods with the JSON error
// body instead of ServeMux's plain-text defaults.
func jsonFallback(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		probe := &statusProbe{header: http.Header{}}
		mux.ServeHTTP(probe, r)
		if allow := probe.header.Get("Allow"); allow != "" {
			w.Header().Set("Allow", allow)
		}
		switch probe.code {
		case http.StatusMethodNotAllowed:
			httperr.Write(w, http.StatusMethodNotAllowed, httperr.CodeMethodNotAllowed, "method not allowed for this route")
		case http.StatusNotFound, 0:
			httperr.Write(w, http.StatusNotFound, httperr.CodeRouteNotFound, "no route matches "+r.URL.Path)
		default:
			// A redirect (for example a missing trailing slash) is not an error.
			for key, values := range probe.header {
				w.Header()[key] = values
			}
			w.WriteHeader(probe.code)
		}
	})
}

// statusProbe records what ServeMux would answer for an unmatched request.
type statusProbe struct {
	header http.Header
	code   int
}

func (p *statusProbe) Header() http.Header         { return p.header }
func (p *statusProbe) Write(b []byte) (int, error) { return len(b), nil }
func (p *statusProbe) WriteHeader(code int)        { p.code = code }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

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
			"status", rec.code, "duration_ms", elapsed.Milliseconds(), "remote", r.RemoteAddr,
			"token_id", security.AuthenticatedTokenID(r.Context()))
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
				httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "internal error")
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
