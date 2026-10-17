package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/security"
)

type fakeCluster struct{}

func (fakeCluster) LocalNode() membership.Member {
	return membership.Member{ID: "n1", Addr: "10.0.0.1:7946", State: membership.StateAlive}
}
func (f fakeCluster) Members() []membership.Member { return []membership.Member{f.LocalNode()} }

var token = strings.Repeat("x", 40)

func newTestHandler() http.Handler {
	return NewHandler(Deps{
		Cluster:     fakeCluster{},
		Auth:        security.NewTokenAuthenticator([]string{token}),
		RateLimiter: security.NewRateLimiter(1000, 1000),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Ready:       func() bool { return true },
	})
}

func do(h http.Handler, path, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPublicEndpoints(t *testing.T) {
	h := newTestHandler()
	for _, p := range []string{"/healthz", "/readyz"} {
		if rec := do(h, p, ""); rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", p, rec.Code)
		}
	}
}

func TestProtectedEndpointsRequireToken(t *testing.T) {
	h := newTestHandler()
	for _, p := range []string{"/v1/node", "/v1/members"} {
		if rec := do(h, p, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without token: status %d", p, rec.Code)
		}
		if rec := do(h, p, "wrong"); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s with bad token: status %d", p, rec.Code)
		}
	}

	rec := do(h, "/v1/members", token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil || body.Count != 1 {
		t.Fatalf("body = %+v, err = %v", body, err)
	}
	if rec.Header().Get("X-Request-Id") == "" {
		t.Error("missing X-Request-Id")
	}
}

func TestMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/healthz", nil)
	rec := httptest.NewRecorder()
	newTestHandler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d", rec.Code)
	}
}
