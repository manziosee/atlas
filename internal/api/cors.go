package api

import (
	"net/http"
	"slices"
)

// corsMiddleware lets the browser origins in allowed call the API, so the
// Atlas Console can run on another host. Requests authenticate with bearer
// tokens, never cookies, so credentials are not allowed. "*" allows any
// origin. With no origins configured, it adds nothing.
func corsMiddleware(allowed []string, next http.Handler) http.Handler {
	if len(allowed) == 0 {
		return next
	}
	any := slices.Contains(allowed, "*")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" || (!any && !slices.Contains(allowed, origin)) {
			next.ServeHTTP(w, r)
			return
		}
		h := w.Header()
		h.Add("Vary", "Origin")
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Expose-Headers", "ETag, Location, Content-Disposition, X-Request-Id")
		// SecureHeaders defaults to same-origin; an allowed origin may read it.
		h.Set("Cross-Origin-Resource-Policy", "cross-origin")
		if r.Method != http.MethodOptions || r.Header.Get("Access-Control-Request-Method") == "" {
			next.ServeHTTP(w, r)
			return
		}
		// Preflight: answer before authentication, which it cannot carry.
		h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, If-Match, Idempotency-Key, Last-Event-ID")
		h.Set("Access-Control-Max-Age", "600")
		if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
			h.Set("Access-Control-Allow-Private-Network", "true")
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
