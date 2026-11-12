package storage

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"testing"
)

func TestValidateMetadata(t *testing.T) {
	tooMany := map[string]string{}
	for i := range MaxMetadataEntries + 1 {
		tooMany[fmt.Sprintf("k%d", i)] = "v"
	}
	for name, tc := range map[string]struct {
		metadata map[string]string
		ok       bool
	}{
		"nil":             {nil, true},
		"typical":         {map[string]string{"team": "payments", "git.commit": "a1b2c3", "app.kubernetes.io/name": "api", "empty": ""}, true},
		"too many":        {tooMany, false},
		"empty key":       {map[string]string{"": "v"}, false},
		"leading dot":     {map[string]string{".team": "v"}, false},
		"quote in key":    {map[string]string{`te"am`: "v"}, false},
		"space in key":    {map[string]string{"te am": "v"}, false},
		"long key":        {map[string]string{strings.Repeat("k", MaxMetadataKeyLength+1): "v"}, false},
		"long value":      {map[string]string{"k": strings.Repeat("v", MaxMetadataValueBytes+1)}, false},
		"invalid utf8":    {map[string]string{"k": "\xff"}, false},
		"over total size": {map[string]string{"a": strings.Repeat("v", 200), "b": strings.Repeat("v", 200), "c": strings.Repeat("v", 200)}, false},
	} {
		if err := ValidateMetadata(tc.metadata); (err == nil) != tc.ok {
			t.Errorf("%s: ValidateMetadata error = %v, want ok=%v", name, err, tc.ok)
		}
	}
}

func TestMetadataPersistsFiltersAndUpdates(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	payments := map[string]string{"team": "payments", "protocol": "grpc"}
	if _, err := store.Register(ctx, Service{ID: "a", Name: "api", NodeID: "n1", Address: "10.0.0.1:80", Health: HealthHealthy, Metadata: payments}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Register(ctx, Service{ID: "b", Name: "api", NodeID: "n1", Address: "10.0.0.2:80", Health: HealthHealthy, Metadata: map[string]string{"team": "search"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Register(ctx, Service{ID: "c", Name: "api", NodeID: "n1", Address: "10.0.0.3:80", Health: HealthHealthy}); err != nil {
		t.Fatal(err)
	}

	got, err := store.Get(ctx, "a")
	if err != nil || !maps.Equal(got.Metadata, payments) {
		t.Fatalf("stored metadata = %v, err = %v", got.Metadata, err)
	}
	if plain, err := store.Get(ctx, "c"); err != nil || plain.Metadata != nil {
		t.Fatalf("service without metadata = %v, err = %v; want nil", plain.Metadata, err)
	}

	for _, tc := range []struct {
		filter map[string]string
		want   []string
	}{
		{map[string]string{"team": "payments"}, []string{"a"}},
		{map[string]string{"team": "payments", "protocol": "grpc"}, []string{"a"}},
		{map[string]string{"team": "payments", "protocol": "http"}, nil},
		{map[string]string{"team": "search"}, []string{"b"}},
		{map[string]string{"missing": ""}, nil},
	} {
		listed, err := store.List(ctx, Filter{Name: "api", Metadata: tc.filter})
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, s := range listed {
			ids = append(ids, s.ID)
		}
		if strings.Join(ids, ",") != strings.Join(tc.want, ",") {
			t.Errorf("filter %v = %v, want %v", tc.filter, ids, tc.want)
		}
	}

	// Updating a copy must not alias the stored map, and equal metadata is
	// not a change.
	unchanged, outcome, err := store.Update(ctx, "a", "n1", 0, func(s *Service) error {
		s.Metadata = map[string]string{"protocol": "grpc", "team": "payments"}
		return nil
	})
	if err != nil || outcome != Unchanged || unchanged.Revision != 1 {
		t.Fatalf("equal metadata update = %v rev %d, err = %v; want unchanged", outcome, unchanged.Revision, err)
	}
	changed, outcome, err := store.Update(ctx, "a", "n1", 0, func(s *Service) error {
		s.Metadata["team"] = "billing"
		return nil
	})
	if err != nil || outcome != Updated || changed.Metadata["team"] != "billing" || changed.Revision != 2 {
		t.Fatalf("metadata update = %v %+v, err = %v", outcome, changed, err)
	}
	cleared, _, err := store.Update(ctx, "a", "n1", 0, func(s *Service) error {
		s.Metadata = nil
		return nil
	})
	if err != nil || cleared.Metadata != nil {
		t.Fatalf("cleared metadata = %v, err = %v", cleared.Metadata, err)
	}
	if _, err := store.Register(ctx, Service{ID: "d", Name: "api", NodeID: "n1", Address: "10.0.0.4:80", Health: HealthHealthy, Metadata: map[string]string{"bad key": "v"}}); err == nil {
		t.Fatal("invalid metadata key was stored")
	}
}

func TestRegistryDigestCoversMetadata(t *testing.T) {
	ctx := context.Background()
	first, second := openTestStore(t), openTestStore(t)
	base := Service{ID: "a", Name: "api", NodeID: "n1", Address: "10.0.0.1:80", Health: HealthHealthy, Revision: 1}
	withTeam := base
	withTeam.Metadata = map[string]string{"team": "payments"}
	if err := first.Merge(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := second.Merge(ctx, withTeam); err != nil {
		t.Fatal(err)
	}
	a, err := first.RegistryDigest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.RegistryDigest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("registries that differ only in metadata have the same digest")
	}
}
