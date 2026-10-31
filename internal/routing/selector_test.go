package routing

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/manziosee/atlas/internal/storage"
)

func TestSelectPrefersHealthyAndLocalRegion(t *testing.T) {
	selector := NewSelector()
	services := []storage.Service{
		{ID: "a", Region: "fra", Health: storage.HealthHealthy},
		{ID: "b", Region: "jnb", Health: storage.HealthHealthy},
		{ID: "c", Region: "jnb", Health: storage.HealthUnhealthy},
	}
	for range 20 {
		got, err := selector.Select(services, "jnb", "customer-17")
		if err != nil || got.ID != "b" {
			t.Fatalf("Select() = %+v, %v; want healthy local instance b", got, err)
		}
	}
}

func TestSelectUsesUnknownOnlyAsHealthyFallback(t *testing.T) {
	selector := NewSelector()
	services := []storage.Service{
		{ID: "unknown", Health: storage.HealthUnknown},
		{ID: "bad", Health: storage.HealthUnhealthy},
	}
	got, err := selector.Select(services, "", "")
	if err != nil || got.ID != "unknown" {
		t.Fatalf("Select() = %+v, %v; want unknown fallback", got, err)
	}
	if _, err := selector.Select(services[1:], "", "key"); !errors.Is(err, ErrNoInstance) {
		t.Fatalf("Select() err = %v, want ErrNoInstance", err)
	}
}

func TestConsistentHashKeepsMostKeysWhenAddingInstance(t *testing.T) {
	selector := NewSelector()
	base := []storage.Service{
		{ID: "a", Health: storage.HealthHealthy},
		{ID: "b", Health: storage.HealthHealthy},
	}
	withNew := append(append([]storage.Service(nil), base...), storage.Service{ID: "c", Health: storage.HealthHealthy})
	stable := 0
	for i := range 1000 {
		key := "customer-" + strconv.Itoa(i)
		before, err := selector.Select(base, "", key)
		if err != nil {
			t.Fatal(err)
		}
		after, err := selector.Select(withNew, "", key)
		if err != nil {
			t.Fatal(err)
		}
		if before.ID == after.ID {
			stable++
		}
	}
	if stable < 500 {
		t.Fatalf("adding one node remapped too many keys: %d/1000 stayed stable", stable)
	}
}

func TestSelectPrefersLowestObservedLatencyAfterRegion(t *testing.T) {
	selector := NewSelector()
	services := []storage.Service{
		{ID: "fra-1", NodeID: "fra-node", Region: "fra", Health: storage.HealthHealthy},
		{ID: "jnb-1", NodeID: "jnb-node-a", Region: "jnb", Health: storage.HealthHealthy},
		{ID: "jnb-2", NodeID: "jnb-node-b", Region: "jnb", Health: storage.HealthHealthy},
	}
	latency := map[string]time.Duration{"fra-node": 2 * time.Millisecond, "jnb-node-a": 20 * time.Millisecond, "jnb-node-b": 8 * time.Millisecond}
	got, err := selector.SelectWithLatency(services, "jnb", "account-4", func(node string) time.Duration { return latency[node] })
	if err != nil || got.ID != "jnb-2" {
		t.Fatalf("SelectWithLatency() = %+v, %v; want nearest local instance jnb-2", got, err)
	}
}

func TestSelectSkipsDrainingInstances(t *testing.T) {
	services := []storage.Service{
		{ID: "a", Name: "api", Health: storage.HealthHealthy, Draining: true},
		{ID: "b", Name: "api", Health: storage.HealthUnknown},
	}
	selector := NewSelector()
	for range 10 {
		got, err := selector.Select(services, "", "")
		if err != nil || got.ID != "b" {
			t.Fatalf("Select() = %+v %v, want the non-draining instance", got, err)
		}
	}
	services[1].Draining = true
	if _, err := selector.Select(services, "", ""); err == nil {
		t.Fatal("Select() returned an instance although every instance is draining")
	}
}

func TestStalePolicies(t *testing.T) {
	services := []storage.Service{
		{ID: "live", NodeID: "n1", Health: storage.HealthUnknown},
		{ID: "stale", NodeID: "gone", Health: storage.HealthHealthy},
	}
	alive := func(id string) bool { return id == "n1" }
	pick := func(stale StalePolicy, list []storage.Service) (string, error) {
		got, err := NewSelector().Choose(list, Options{OwnerAlive: alive, Policy: Policy{Stale: stale}})
		return got.ID, err
	}
	if id, _ := pick(StaleInclude, services); id != "stale" {
		t.Fatalf("include picked %q, want the healthy stale instance", id)
	}
	if id, _ := pick(StalePreferLive, services); id != "live" {
		t.Fatalf("prefer-live picked %q, want the live-owner instance", id)
	}
	if id, _ := pick("", services); id != "live" {
		t.Fatalf("default policy picked %q, want prefer-live behavior", id)
	}
	if id, _ := pick(StalePreferLive, services[1:]); id != "stale" {
		t.Fatalf("prefer-live with only stale instances picked %q", id)
	}
	if _, err := pick(StaleExclude, services[1:]); err == nil {
		t.Fatal("exclude returned a stale instance")
	}
}

func TestLatencyToleranceAndOff(t *testing.T) {
	services := []storage.Service{
		{ID: "a", NodeID: "near", Health: storage.HealthHealthy},
		{ID: "b", NodeID: "close", Health: storage.HealthHealthy},
		{ID: "c", NodeID: "far", Health: storage.HealthHealthy},
	}
	rtt := map[string]time.Duration{"near": 10 * time.Millisecond, "close": 14 * time.Millisecond, "far": 90 * time.Millisecond}
	latency := func(id string) time.Duration { return rtt[id] }
	seen := func(policy Policy) map[string]bool {
		selector := NewSelector()
		out := map[string]bool{}
		for range 12 {
			got, err := selector.Choose(services, Options{Latency: latency, Policy: policy})
			if err != nil {
				t.Fatal(err)
			}
			out[got.ID] = true
		}
		return out
	}
	if got := seen(Policy{Latency: LatencyNearest}); len(got) != 1 || !got["a"] {
		t.Fatalf("strict nearest = %v, want only a", got)
	}
	if got := seen(Policy{Latency: LatencyNearest, Tolerance: 5 * time.Millisecond}); len(got) != 2 || got["c"] {
		t.Fatalf("5ms tolerance = %v, want a and b", got)
	}
	if got := seen(Policy{Latency: LatencyOff}); len(got) != 3 {
		t.Fatalf("latency off = %v, want all three", got)
	}
}

func TestParsePolicy(t *testing.T) {
	policy, err := ParsePolicy("", "", 0)
	if err != nil || policy.Stale != StalePreferLive || policy.Latency != LatencyNearest {
		t.Fatalf("defaults = %+v %v", policy, err)
	}
	for _, bad := range [][2]string{{"sometimes", ""}, {"", "fastest"}} {
		if _, err := ParsePolicy(bad[0], bad[1], 0); err == nil {
			t.Errorf("ParsePolicy(%q, %q) accepted", bad[0], bad[1])
		}
	}
	if _, err := ParsePolicy("", "", 2*time.Minute); err == nil {
		t.Error("tolerance over 1m accepted")
	}
}
