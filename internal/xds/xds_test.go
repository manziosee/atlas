package xds

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/manziosee/atlas/internal/storage"
)

type fakeRegistry []storage.Service

func (f fakeRegistry) List(context.Context, storage.Filter) ([]storage.Service, error) {
	return f, nil
}

var registry = fakeRegistry{
	{ID: "p1", Name: "payments", NodeID: "n1", Address: "10.0.0.1:8443", Region: "eu", Health: storage.HealthHealthy, TrafficWeight: 95, Version: "1.4", Metadata: map[string]string{"team": "pay", "atlas.id": "spoof"}},
	{ID: "p2", Name: "payments", NodeID: "n1", Address: "10.0.0.2:8443", Region: "eu", Health: storage.HealthHealthy, TrafficWeight: 5, Version: "1.5"},
	{ID: "p3", Name: "payments", NodeID: "n2", Address: "10.1.0.3:8443", Region: "us", Health: storage.HealthHealthy, TrafficWeight: 1},
	{ID: "p4", Name: "payments", NodeID: "n1", Address: "10.0.0.4:8443", Region: "eu", Health: storage.HealthUnhealthy, TrafficWeight: 1},
	{ID: "p5", Name: "payments", NodeID: "n1", Address: "10.0.0.5:8443", Region: "eu", Health: storage.HealthHealthy, Draining: true, TrafficWeight: 1},
	{ID: "p6", Name: "payments", NodeID: "n1", Address: "[fd00::6]:8443", Region: "eu", Health: storage.HealthUnknown, TrafficWeight: 1},
	{ID: "s1", Name: "search", NodeID: "n1", Address: "search.internal:9200", Region: "eu", Health: storage.HealthHealthy, TrafficWeight: 1},
}

func newServer(t *testing.T) http.Handler {
	t.Helper()
	return Handler(registry, Config{
		XDSCluster:   "atlas",
		RefreshDelay: 1500 * time.Millisecond,
		OwnerAlive:   func(node string) bool { return node != "n2" },
		Resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
			if host == "search.internal" {
				return []netip.Addr{netip.MustParseAddr("10.2.0.2"), netip.MustParseAddr("10.2.0.1"), netip.MustParseAddr("10.2.0.1")}, nil
			}
			return nil, errors.New("no such host")
		},
	})
}

func discover(t *testing.T, h http.Handler, path, body string) DiscoveryResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d %s", path, rec.Code, rec.Body.String())
	}
	var response DiscoveryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
}

func TestClusterDiscovery(t *testing.T) {
	h := newServer(t)
	response := discover(t, h, "/v3/discovery:clusters", `{"node":{"id":"envoy-1"},"type_url":"`+ClusterType+`"}`)
	if response.TypeURL != ClusterType || response.VersionInfo == "" || len(response.Resources) != 2 {
		t.Fatalf("CDS response = %+v", response)
	}
	cluster := response.Resources[0]
	if cluster["@type"] != ClusterType || cluster["name"] != "payments" || cluster["type"] != "EDS" {
		t.Fatalf("cluster = %v", cluster)
	}
	source := cluster["eds_cluster_config"].(map[string]any)["eds_config"].(map[string]any)["api_config_source"].(map[string]any)
	if source["api_type"] != "REST" || source["refresh_delay"] != "1.5s" || source["cluster_names"].([]any)[0] != "atlas" {
		t.Fatalf("eds config = %v", source)
	}
	again := discover(t, h, "/v3/discovery:clusters", `{}`)
	if again.VersionInfo != response.VersionInfo {
		t.Fatal("version changed without a registry change")
	}
	only := discover(t, h, "/v3/discovery:clusters", `{"resourceNames":["search"]}`)
	if len(only.Resources) != 1 || only.Resources[0]["name"] != "search" {
		t.Fatalf("camelCase resource filter = %+v", only.Resources)
	}
}

func TestEndpointDiscovery(t *testing.T) {
	h := newServer(t)
	response := discover(t, h, "/v3/discovery:endpoints", `{"resource_names":["payments","search","gone"]}`)
	if response.TypeURL != AssignmentType || len(response.Resources) != 3 {
		t.Fatalf("EDS response = %+v", response)
	}
	byName := map[string]map[string]any{}
	for _, resource := range response.Resources {
		byName[resource["cluster_name"].(string)] = resource
	}
	if endpoints := byName["gone"]["endpoints"].([]any); len(endpoints) != 0 {
		t.Fatalf("unknown cluster got endpoints %v; want an empty assignment", endpoints)
	}

	type endpoint struct {
		region, address, health, hostname string
		weight                            float64
		lb                                map[string]any
	}
	flatten := func(resource map[string]any) map[string]endpoint {
		out := map[string]endpoint{}
		for _, l := range resource["endpoints"].([]any) {
			locality := l.(map[string]any)
			region := locality["locality"].(map[string]any)["region"].(string)
			for _, e := range locality["lb_endpoints"].([]any) {
				lb := e.(map[string]any)
				ep := lb["endpoint"].(map[string]any)
				address := ep["address"].(map[string]any)["socket_address"].(map[string]any)
				hostname, _ := ep["hostname"].(string)
				out[address["address"].(string)] = endpoint{
					region: region, address: address["address"].(string), health: lb["health_status"].(string), hostname: hostname,
					weight: lb["load_balancing_weight"].(float64),
					lb:     lb["metadata"].(map[string]any)["filter_metadata"].(map[string]any)["envoy.lb"].(map[string]any),
				}
				if port := address["port_value"].(float64); port != 8443 && port != 9200 {
					t.Fatalf("port = %v", port)
				}
			}
		}
		return out
	}

	payments := flatten(byName["payments"])
	for address, want := range map[string]struct {
		region, health string
		weight         float64
	}{
		"10.0.0.1": {"eu", "HEALTHY", 95},
		"10.0.0.2": {"eu", "HEALTHY", 5},
		"10.1.0.3": {"us", "DEGRADED", 1},
		"10.0.0.4": {"eu", "UNHEALTHY", 1},
		"10.0.0.5": {"eu", "DRAINING", 1},
		"fd00::6":  {"eu", "UNKNOWN", 1},
	} {
		got, ok := payments[address]
		if !ok || got.region != want.region || got.health != want.health || got.weight != want.weight {
			t.Errorf("endpoint %s = %+v, want %+v", address, got, want)
		}
	}
	if lb := payments["10.0.0.1"].lb; lb["version"] != "1.4" || lb["team"] != "pay" || lb["atlas.id"] != "p1" {
		t.Errorf("subset metadata = %v; atlas.id must not be overridable", lb)
	}

	search := flatten(byName["search"])
	if len(search) != 2 || search["10.2.0.1"].hostname != "search.internal" || search["10.2.0.2"].hostname != "search.internal" {
		t.Fatalf("resolved hostname endpoints = %+v", search)
	}
}

func TestRejectsMalformedRequests(t *testing.T) {
	h := newServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v3/discovery:endpoints", strings.NewReader(`{"resource_names":`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed request = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v3/discovery:endpoints", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", rec.Code)
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{time.Second: "1s", 1500 * time.Millisecond: "1.5s", 250 * time.Millisecond: "0.25s"} {
		if got := duration(d); got != want {
			t.Errorf("duration(%v) = %q, want %q", d, got, want)
		}
	}
}
