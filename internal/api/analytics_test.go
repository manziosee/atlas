package api

import (
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

func TestPageViewTelemetryAndAdminAnalyticsScopes(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	adminToken := strings.Repeat("a", 40)
	readToken := strings.Repeat("r", 40)
	auth, err := security.NewConfiguredTokenAuthenticator(nil, []security.Credential{
		{Token: adminToken, Scopes: []security.Scope{security.ScopeAdmin}, ExpiresAt: time.Now().Add(time.Hour)},
		{Token: readToken, Scopes: []security.Scope{security.ScopeRead}, ExpiresAt: time.Now().Add(time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Deps{
		Cluster: fakeCluster{}, Registry: store, Auth: auth,
		RateLimiter: security.NewRateLimiter(1000, 1000),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	post := func(visitorID, path string) *httptest.ResponseRecorder {
		body := `{"visitor_id":"` + visitorID + `","path":"` + path + `"}`
		req := httptest.NewRequest(http.MethodPost, "/v1/telemetry/pageview", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := post("123e4567-e89b-12d3-a456-426614174000", "/"); rec.Code != http.StatusNoContent {
		t.Fatalf("first page view = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := post("123e4567-e89b-12d3-a456-426614174000", "/docs/overview?secret=value"); rec.Code != http.StatusNoContent {
		t.Fatalf("repeat page view = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := post("123e4567-e89b-12d3-a456-426614174001", "/about"); rec.Code != http.StatusNoContent {
		t.Fatalf("second visitor page view = %d: %s", rec.Code, rec.Body.String())
	}

	for _, test := range []struct {
		name  string
		token string
		want  int
	}{
		{name: "unauthenticated", want: http.StatusUnauthorized},
		{name: "read scope", token: readToken, want: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/admin/analytics?days=30", nil)
			if test.token != "" {
				req.Header.Set("Authorization", "Bearer "+test.token)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, test.want, rec.Body.String())
			}
		})
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/analytics?days=1", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin analytics = %d: %s", rec.Code, rec.Body.String())
	}
	var report adminAnalytics
	if err := json.NewDecoder(rec.Body).Decode(&report); err != nil {
		t.Fatal(err)
	}
	if len(report.Daily) != 1 || report.Daily[0].PageViews != 3 || report.Daily[0].UniqueVisitors != 2 {
		t.Fatalf("daily analytics = %+v, want 3 views and 2 visitors", report.Daily)
	}
	if len(report.Pages) != 3 || report.Pages[0].Page != "/" || report.Pages[1].Page != "/about" ||
		report.Pages[2].Page != "/docs/:slug" || report.APIRequestsSinceStart == 0 {
		t.Fatalf("report pages/API requests = %+v / %d", report.Pages, report.APIRequestsSinceStart)
	}
}

func TestPageViewTelemetryRejectsInvalidVisitorID(t *testing.T) {
	store, err := storage.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := NewHandler(Deps{
		Cluster:     fakeCluster{},
		Registry:    store,
		Auth:        security.NewTokenAuthenticator(nil),
		RateLimiter: security.NewRateLimiter(1000, 1000),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})

	formReq := httptest.NewRequest(http.MethodPost, "/v1/telemetry/pageview", strings.NewReader(`{"visitor_id":"123e4567-e89b-12d3-a456-426614174000","path":"/"}`))
	formRec := httptest.NewRecorder()
	handler.ServeHTTP(formRec, formReq)
	if formRec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("missing content type status = %d, want 415", formRec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/telemetry/pageview", strings.NewReader(`{"visitor_id":"not-a-uuid","path":"/"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid visitor ID status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminAnalyticsAggregatesReachableClusterNodes(t *testing.T) {
	ctx := context.Background()
	store1, err := storage.Open(ctx, filepath.Join(t.TempDir(), "node-1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store1.Close() })
	store2, err := storage.Open(ctx, filepath.Join(t.TempDir(), "node-2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store2.Close() })

	node1 := membership.Member{ID: "n1", Addr: "10.0.0.1:7946", State: membership.StateAlive}
	node2 := membership.Member{ID: "n2", Addr: "10.0.0.2:7946", APIAddr: "http://n2", State: membership.StateAlive}
	token := strings.Repeat("c", 40)
	server2 := httptest.NewServer(NewHandler(Deps{
		Cluster:  analyticsCluster{self: node2, members: []membership.Member{node1, node2}},
		Registry: store2, Auth: security.NewTokenAuthenticator([]string{token}),
		RateLimiter: security.NewRateLimiter(1000, 1000),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	defer server2.Close()
	node2.APIAddr = server2.URL

	handler := NewHandler(Deps{
		Cluster:  analyticsCluster{self: node1, members: []membership.Member{node1, node2}},
		Registry: store1, Auth: security.NewTokenAuthenticator([]string{token}),
		RateLimiter: security.NewRateLimiter(1000, 1000),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Forwarder:   &http.Client{Timeout: 4 * time.Second},
	})
	for _, target := range []http.Handler{handler, server2.Config.Handler} {
		req := httptest.NewRequest(http.MethodPost, "/v1/telemetry/pageview", strings.NewReader(`{"visitor_id":"123e4567-e89b-12d3-a456-426614174000","path":"/"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		target.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("record page view = %d: %s", rec.Code, rec.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/analytics?days=1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cluster analytics = %d: %s", rec.Code, rec.Body.String())
	}
	var report adminAnalytics
	if err := json.NewDecoder(rec.Body).Decode(&report); err != nil {
		t.Fatal(err)
	}
	if report.NodesIncluded != 2 || len(report.NodeErrors) != 0 {
		t.Fatalf("nodes included/errors = %d / %+v", report.NodesIncluded, report.NodeErrors)
	}
	if len(report.Daily) != 1 || report.Daily[0].PageViews != 2 || report.Daily[0].UniqueVisitors != 2 {
		t.Fatalf("daily analytics = %+v, want 2 views and node-local unique counts from both nodes", report.Daily)
	}
}

type analyticsCluster struct {
	self    membership.Member
	members []membership.Member
}

func (c analyticsCluster) LocalNode() membership.Member { return c.self }
func (c analyticsCluster) Members() []membership.Member { return c.members }
