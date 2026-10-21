package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
)

type fakeCluster struct{}

func (fakeCluster) LocalNode() membership.Member {
	return membership.Member{ID: "n1", Addr: "10.0.0.1:7946", State: membership.StateAlive}
}
func (f fakeCluster) Members() []membership.Member { return []membership.Member{f.LocalNode()} }

type fakeEventCluster struct {
	fakeCluster
	events chan membership.Event
}

func (f *fakeEventCluster) Subscribe(_ int) (<-chan membership.Event, func()) {
	return f.events, func() {}
}

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

func TestServiceRegistryAPI(t *testing.T) {
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := NewHandler(Deps{
		Cluster: fakeCluster{}, Registry: store,
		Auth:        security.NewTokenAuthenticator([]string{token}),
		RateLimiter: security.NewRateLimiter(1000, 1000), Log: log,
	})

	body := `{"name":"api-service","address":"127.0.0.1:8088","version":"v1","tags":["public"]}`
	unauthorized := httptest.NewRecorder()
	h.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodPost, "/v1/services", strings.NewReader(body)))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized registration = %d, want 401", unauthorized.Code)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/services", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("registration = %d: %s", rec.Code, rec.Body.String())
	}
	var created storage.Service
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.NodeID != "n1" || created.Name != "api-service" || created.Revision != 1 {
		t.Fatalf("created service = %+v", created)
	}

	list := do(h, "/v1/services?name=api-service&health=unknown", token)
	if list.Code != http.StatusOK {
		t.Fatalf("service lookup = %d: %s", list.Code, list.Body.String())
	}
	var listed struct {
		Count    int               `json:"count"`
		Services []storage.Service `json:"services"`
	}
	if err := json.NewDecoder(list.Body).Decode(&listed); err != nil || listed.Count != 1 || listed.Services[0].ID != created.ID {
		t.Fatalf("list response = %+v, err = %v", listed, err)
	}
	resolved := do(h, "/v1/lookup/api-service?region=jnb&key=customer-17", token)
	var selected storage.Service
	if resolved.Code != http.StatusOK || json.NewDecoder(resolved.Body).Decode(&selected) != nil || selected.ID != created.ID {
		t.Fatalf("routed lookup = %d %+v", resolved.Code, selected)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/v1/services/"+created.ID, nil)
	deleteReq.Header.Set("Authorization", "Bearer "+token)
	deleteRec := httptest.NewRecorder()
	h.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", deleteRec.Code, deleteRec.Body.String())
	}
	if got := do(h, "/v1/services/"+created.ID, token).Code; got != http.StatusNotFound {
		t.Fatalf("GET deleted service = %d, want 404", got)
	}
}

func TestServiceRegistrationValidatesInput(t *testing.T) {
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	h := NewHandler(Deps{
		Cluster: fakeCluster{}, Registry: store,
		Auth:        security.NewTokenAuthenticator([]string{token}),
		RateLimiter: security.NewRateLimiter(1000, 1000),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	for _, body := range []string{
		`{"name":"bad name","address":"127.0.0.1:8080"}`,
		`{"name":"api","address":"127.0.0.1:99999"}`,
		`{"name":"api","address":"127.0.0.1:8080","unexpected":true}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/services", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("POST body %s = %d, want 400", body, rec.Code)
		}
	}
}

func TestMembershipEventStream(t *testing.T) {
	events := make(chan membership.Event, 1)
	cluster := &fakeEventCluster{events: events}
	h := NewHandler(Deps{
		Cluster:     cluster,
		Auth:        security.NewTokenAuthenticator([]string{token}),
		RateLimiter: security.NewRateLimiter(1000, 1000),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream; charset=utf-8" {
		t.Fatalf("event stream response = %d, Content-Type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	events <- membership.Event{Type: membership.EventJoin, Member: membership.Member{ID: "streamed"}}
	scanner := bufio.NewScanner(resp.Body)
	found := false
	deadline := time.After(time.Second)
	for !found {
		line := make(chan string, 1)
		errCh := make(chan error, 1)
		go func() {
			if scanner.Scan() {
				line <- scanner.Text()
			} else {
				errCh <- scanner.Err()
			}
		}()
		select {
		case got := <-line:
			found = strings.Contains(got, `"id":"streamed"`)
		case err := <-errCh:
			cancel()
			t.Fatalf("event stream ended before event: %v", err)
		case <-deadline:
			cancel()
			t.Fatal("timed out waiting for event")
		}
	}
	cancel()
}

func TestDashboardServesEmbeddedAssetsWithoutExposingRegistryData(t *testing.T) {
	h := NewHandler(Deps{
		Cluster:     fakeCluster{},
		Auth:        security.NewTokenAuthenticator([]string{token}),
		RateLimiter: security.NewRateLimiter(1000, 1000),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/dashboard/", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Cluster overview") {
		t.Fatalf("dashboard page = %d %q", page.Code, page.Body.String())
	}
	if got := page.Header().Get("Content-Security-Policy"); !strings.Contains(got, "script-src 'self'") {
		t.Fatalf("dashboard CSP = %q", got)
	}
	script := httptest.NewRecorder()
	h.ServeHTTP(script, httptest.NewRequest(http.MethodGet, "/dashboard/app.js", nil))
	if script.Code != http.StatusOK || !strings.Contains(script.Body.String(), "Authorization") {
		t.Fatalf("dashboard script = %d", script.Code)
	}
	if got := do(h, "/v1/members", "").Code; got != http.StatusUnauthorized {
		t.Fatalf("dashboard must not make the API public: /v1/members = %d", got)
	}
}
