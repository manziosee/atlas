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
