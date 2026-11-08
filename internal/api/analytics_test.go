package api

import (
	"context"
	"encoding/json"
	"fmt"
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

const testTelemetryKey = "telemetry-key-telemetry-key-telemetry-key"

func analyticsHandler(t *testing.T, store *storage.Store, key string, auth *security.TokenAuthenticator) http.Handler {
	t.Helper()
	if auth == nil {
		auth = security.NewTokenAuthenticator(nil)
	}
	return NewHandler(Deps{
		Cluster: fakeCluster{}, Registry: store, Auth: auth, AnalyticsEnabled: true, TelemetryKey: key,
		RateLimiter: security.NewRateLimiter(1000, 1000),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func sendEvent(h http.Handler, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, telemetryPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set(telemetryKeyHeader, key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func readAnalytics(t *testing.T, h http.Handler, token string, days int) (adminAnalytics, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/v1/admin/analytics?days=%d", days), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin analytics = %d: %s", rec.Code, rec.Body.String())
	}
	var report adminAnalytics
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	return report, rec.Body.String()
}

func openStore(t *testing.T, name string) *storage.Store {
	t.Helper()
	store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

const (
	visitorA = "123e4567-e89b-12d3-a456-426614174000"
	visitorB = "123e4567-e89b-12d3-a456-426614174001"
)

func TestTelemetryAndAdminAnalyticsScopes(t *testing.T) {
	store := openStore(t, "registry.db")
	adminToken := strings.Repeat("a", 40)
	readToken := strings.Repeat("r", 40)
	auth, err := security.NewConfiguredTokenAuthenticator(nil, []security.Credential{
		{Token: adminToken, Scopes: []security.Scope{security.ScopeAdmin}, ExpiresAt: time.Now().Add(time.Hour)},
		{Token: readToken, Scopes: []security.Scope{security.ScopeRead}, ExpiresAt: time.Now().Add(time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := analyticsHandler(t, store, testTelemetryKey, auth)

	for _, body := range []string{
		`{"visitor_id":"` + visitorA + `","path":"/","country":"rw"}`,
		`{"type":"pageview","visitor_id":"` + visitorA + `","path":"/docs/overview?secret=value","country":"RW"}`,
		`{"type":"pageview","visitor_id":"` + visitorB + `","path":"/about","country":"FR"}`,
		`{"type":"connect","visitor_id":"` + visitorB + `","kind":"node","version":"v1.4.0"}`,
		`{"type":"connect","visitor_id":"` + visitorA + `","kind":"demo"}`,
	} {
		if rec := sendEvent(h, testTelemetryKey, body); rec.Code != http.StatusNoContent {
			t.Fatalf("event %s = %d: %s", body, rec.Code, rec.Body.String())
		}
	}

	for name, tok := range map[string]string{"unauthenticated": "", "read scope": readToken} {
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/analytics", nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Fatalf("%s read admin analytics", name)
		}
	}

	report, raw := readAnalytics(t, h, adminToken, 1)
	if strings.Contains(raw, "sketches") {
		t.Fatal("internal sketches were returned to the client")
	}
	day := report.Daily[0]
	if day.PageViews != 3 || day.UniqueVisitors != 2 || day.Systems != 1 || day.NodeConnections != 1 || day.DemoConnections != 1 {
		t.Fatalf("day = %+v", day)
	}
	if fmt.Sprint(report.Pages) != "[{/ 1} {/about 1} {/docs 1}]" {
		t.Fatalf("pages = %+v; want normalized paths without queries", report.Pages)
	}
	if fmt.Sprint(report.Countries) != "[{RW 2 1} {FR 1 1}]" || fmt.Sprint(report.Versions) != "[{v1.4.0 1}]" {
		t.Fatalf("countries = %+v, versions = %+v", report.Countries, report.Versions)
	}
	if report.APIRequestsSinceStart == 0 {
		t.Fatal("API request counters missing")
	}
}

func TestTelemetryKeyAndValidation(t *testing.T) {
	keyed := analyticsHandler(t, openStore(t, "a.db"), testTelemetryKey, nil)
	for _, c := range []struct {
		key, body string
		want      int
	}{
		{"", `{"visitor_id":"` + visitorA + `","path":"/"}`, http.StatusUnauthorized},
		{"wrong-key-wrong-key-wrong-key-wrong-key", `{"visitor_id":"` + visitorA + `","path":"/"}`, http.StatusUnauthorized},
		{testTelemetryKey, `{"visitor_id":"not-a-uuid","path":"/"}`, http.StatusBadRequest},
		{testTelemetryKey, `{"type":"click","visitor_id":"` + visitorA + `"}`, http.StatusBadRequest},
		{testTelemetryKey, `{"type":"connect","visitor_id":"` + visitorA + `","kind":"cluster"}`, http.StatusBadRequest},
		{testTelemetryKey, `{"visitor_id":"` + visitorA + `","path":"/","country":"Rwanda"}`, http.StatusBadRequest},
		{testTelemetryKey, `{"visitor_id":"` + visitorA + `","path":"/","country":"RW"}`, http.StatusNoContent},
	} {
		if rec := sendEvent(keyed, c.key, c.body); rec.Code != c.want {
			t.Errorf("key %q body %s = %d, want %d: %s", c.key, c.body, rec.Code, c.want, rec.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodPost, telemetryPath, strings.NewReader(`{"visitor_id":"`+visitorA+`","path":"/"}`))
	req.Header.Set(telemetryKeyHeader, testTelemetryKey)
	rec := httptest.NewRecorder()
	keyed.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("missing content type = %d, want 415", rec.Code)
	}

	// Without a configured key, events are accepted but countries are not
	// trusted, since anyone could have sent them.
	store := openStore(t, "b.db")
	open := analyticsHandler(t, store, "", nil)
	if rec := sendEvent(open, "", `{"visitor_id":"`+visitorA+`","path":"/","country":"RW"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("unkeyed event = %d", rec.Code)
	}
	data, err := store.Analytics(context.Background(), 1)
	if err != nil || len(data.Countries) != 0 || data.Daily[0].PageViews != 1 {
		t.Fatalf("unkeyed analytics = %+v, %v; want the view without a country", data, err)
	}
}

func TestAdminAnalyticsSingleNodeKeepsExactCounts(t *testing.T) {
	store := openStore(t, "exact.db")
	const visitors = 1500 // large enough that a sketch estimate would differ
	now := time.Now().UTC()
	for i := 0; i < visitors; i++ {
		e := storage.TelemetryEvent{At: now, Kind: storage.EventPageView, Page: "/", VisitorHash: fmt.Sprintf("%064x", (i+1)*7919)}
		if err := store.RecordTelemetry(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	token := strings.Repeat("e", 40)
	report, _ := readAnalytics(t, analyticsHandler(t, store, "", security.NewTokenAuthenticator([]string{token})), token, 1)
	if report.NodesIncluded != 1 || report.Daily[0].UniqueVisitors != visitors {
		t.Fatalf("single node unique visitors = %d (nodes %d), want exactly %d", report.Daily[0].UniqueVisitors, report.NodesIncluded, visitors)
	}
}

func TestAdminAnalyticsDeduplicatesAcrossNodes(t *testing.T) {
	store1, store2 := openStore(t, "node-1.db"), openStore(t, "node-2.db")
	node1 := membership.Member{ID: "n1", Addr: "10.0.0.1:7946", State: membership.StateAlive}
	node2 := membership.Member{ID: "n2", Addr: "10.0.0.2:7946", State: membership.StateAlive}
	down := membership.Member{ID: "n3", Addr: "10.0.0.3:7946", State: membership.StateDead}
	token := strings.Repeat("c", 40)
	deps := func(self membership.Member, members []membership.Member, store *storage.Store) Deps {
		return Deps{
			Cluster: analyticsCluster{self: self, members: members}, AnalyticsEnabled: true, TelemetryKey: testTelemetryKey,
			Registry: store, Auth: security.NewTokenAuthenticator([]string{token}),
			RateLimiter: security.NewRateLimiter(1000, 1000), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
			Forwarder: &http.Client{Timeout: 4 * time.Second},
		}
	}
	server2 := httptest.NewServer(NewHandler(deps(node2, []membership.Member{node1, node2}, store2)))
	defer server2.Close()
	node2.APIAddr = server2.URL
	h1 := NewHandler(deps(node1, []membership.Member{node1, node2, down}, store1))

	// The same browser reaches both nodes; a second browser only node 2.
	for _, target := range []http.Handler{h1, server2.Config.Handler} {
		for _, body := range []string{
			`{"visitor_id":"` + visitorA + `","path":"/","country":"RW"}`,
			`{"type":"connect","visitor_id":"` + visitorA + `","kind":"node","version":"v2"}`,
		} {
			if rec := sendEvent(target, testTelemetryKey, body); rec.Code != http.StatusNoContent {
				t.Fatalf("event = %d: %s", rec.Code, rec.Body.String())
			}
		}
	}
	if rec := sendEvent(server2.Config.Handler, testTelemetryKey, `{"visitor_id":"`+visitorB+`","path":"/","country":"RW"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("event = %d", rec.Code)
	}

	report, _ := readAnalytics(t, h1, token, 1)
	if report.NodesIncluded != 2 || len(report.NodeErrors) != 1 || report.NodeErrors[0].NodeID != "n3" {
		t.Fatalf("nodes included %d, errors %+v; want 2 and the dead node reported", report.NodesIncluded, report.NodeErrors)
	}
	day := report.Daily[0]
	if day.PageViews != 3 || day.UniqueVisitors != 2 || day.Systems != 1 || day.NodeConnections != 2 {
		t.Fatalf("day = %+v; want 3 views, 2 browsers, 1 system and 2 connections", day)
	}
	if fmt.Sprint(report.Countries) != "[{RW 3 2}]" || fmt.Sprint(report.Versions) != "[{v2 2}]" {
		t.Fatalf("countries = %+v, versions = %+v", report.Countries, report.Versions)
	}
}

func TestTelemetryDisabledByDefault(t *testing.T) {
	h := NewHandler(Deps{
		Cluster: fakeCluster{}, Registry: openStore(t, "off.db"),
		Auth:        security.NewTokenAuthenticator(nil),
		RateLimiter: security.NewRateLimiter(1000, 1000),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if rec := sendEvent(h, "", `{"visitor_id":"`+visitorA+`","path":"/"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("disabled telemetry status = %d, want 404", rec.Code)
	}
}

func TestAnalyticsPageNormalization(t *testing.T) {
	for in, want := range map[string]string{
		"": "/", "/": "/", "/docs/kubernetes/": "/docs", "/services/?id=abc#x": "/services", "/connect#token=secret": "/connect",
		"/a%20b": "/", "/" + strings.Repeat("x", 80): "/",
	} {
		if got := analyticsPage(in); got != want {
			t.Errorf("analyticsPage(%q) = %q, want %q", in, got, want)
		}
	}
}

type analyticsCluster struct {
	self    membership.Member
	members []membership.Member
}

func (c analyticsCluster) LocalNode() membership.Member { return c.self }
func (c analyticsCluster) Members() []membership.Member { return c.members }
