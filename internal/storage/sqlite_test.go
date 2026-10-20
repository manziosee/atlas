package storage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestServiceStorePersistsAndFilters(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "registry.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := store.Register(ctx, Service{
		ID: "orders-1", Name: "orders", NodeID: "node-a", Address: "10.0.0.1:8080",
		Region: "jnb", Version: "v1", Tags: []string{"blue", "public"}, Health: HealthHealthy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if service.Revision != 1 {
		t.Fatalf("initial revision = %d, want 1", service.Revision)
	}

	filtered, err := store.List(ctx, Filter{Name: "orders", Region: "jnb", Health: HealthHealthy})
	if err != nil || len(filtered) != 1 || filtered[0].ID != service.ID {
		t.Fatalf("filtered list = %+v, err = %v", filtered, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	persisted, err := store.Get(ctx, service.ID)
	if err != nil || persisted.Revision != 1 || len(persisted.Tags) != 2 {
		t.Fatalf("persisted service = %+v, err = %v", persisted, err)
	}
}

func TestRegistryDigestAndReplicationSnapshot(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	before, err := store.RegistryDigest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	service, err := store.Register(ctx, Service{ID: "api-1", Name: "api", NodeID: "node-1", Address: "127.0.0.1:8080", Region: "eu", Health: HealthHealthy})
	if err != nil {
		t.Fatal(err)
	}
	afterRegister, err := store.RegistryDigest(ctx)
	if err != nil || before == afterRegister {
		t.Fatalf("digest after registration = %x, err = %v; want digest to change", afterRegister, err)
	}
	if err := store.updateCheckedAt(ctx, service.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	afterCheck, err := store.RegistryDigest(ctx)
	if err != nil || afterCheck != afterRegister {
		t.Fatalf("local health-check timestamp changed digest: got %x, want %x, err %v", afterCheck, afterRegister, err)
	}
	if err := store.Delete(ctx, service.ID, service.NodeID); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := store.RegistryDigest(ctx)
	if err != nil || afterDelete == afterCheck {
		t.Fatalf("digest after delete = %x, err = %v; want tombstone to change digest", afterDelete, err)
	}
	snapshot, err := store.ReplicationSnapshot(ctx)
	if err != nil || len(snapshot) != 1 || !snapshot[0].Deleted {
		t.Fatalf("replication snapshot = %+v, err = %v; want deletion tombstone", snapshot, err)
	}
}

func TestServiceStoreRevisionHealthAndDeleteTombstone(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	service, err := store.Register(ctx, Service{ID: "cache-1", Name: "cache", NodeID: "node-a", Address: "10.0.0.2:6379", Region: "fra"})
	if err != nil {
		t.Fatal(err)
	}
	checked := time.Now().UTC()
	if err := store.SetHealth(ctx, service.ID, "node-a", HealthHealthy, checked); err != nil {
		t.Fatal(err)
	}
	service, err = store.Get(ctx, service.ID)
	if err != nil || service.Health != HealthHealthy || service.Revision != 2 {
		t.Fatalf("updated service = %+v, err = %v", service, err)
	}
	if err := store.Delete(ctx, service.ID, "node-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, service.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete err = %v, want ErrNotFound", err)
	}
	items, err := store.Snapshot(ctx, 10)
	if err != nil || len(items) != 1 || !items[0].Deleted || items[0].Revision != 3 {
		t.Fatalf("snapshot = %+v, err = %v", items, err)
	}
}

func TestServiceStoreMergeUsesRevision(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	if err := store.Merge(ctx, Service{ID: "search-1", Name: "search", NodeID: "node-b", Address: "10.0.0.3:9200", Region: "sin", Health: HealthHealthy, Revision: 4}); err != nil {
		t.Fatal(err)
	}
	if err := store.Merge(ctx, Service{ID: "search-1", Name: "stale", NodeID: "node-b", Address: "10.0.0.3:9200", Region: "sin", Health: HealthUnhealthy, Revision: 3}); err != nil {
		t.Fatal(err)
	}
	service, err := store.Get(ctx, "search-1")
	if err != nil || service.Name != "search" || service.Revision != 4 {
		t.Fatalf("service after stale merge = %+v, err = %v", service, err)
	}
	if err := store.Merge(ctx, Service{ID: "search-1", Name: "search", NodeID: "node-b", Address: "10.0.0.3:9200", Region: "sin", Health: HealthUnhealthy, Revision: 5, Deleted: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, "search-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after tombstone err = %v, want ErrNotFound", err)
	}
}

func TestServiceMergeConvergesAcrossReplicaOrderings(t *testing.T) {
	ctx := context.Background()
	at := time.Unix(1_700_000_000, 123).UTC()
	updates := []Service{
		{ID: "shared-1", Name: "alpha", NodeID: "owner-a", Address: "127.0.0.1:8080", Region: "eu", Health: HealthHealthy, Revision: 7, UpdatedAt: at},
		{ID: "shared-1", Name: "alpha", NodeID: "owner-z", Address: "127.0.0.1:8080", Region: "eu", Health: HealthHealthy, Revision: 7, UpdatedAt: at},
		{ID: "shared-1", Name: "zulu", NodeID: "owner-z", Address: "127.0.0.1:8080", Region: "eu", Health: HealthHealthy, Revision: 7, Deleted: true, UpdatedAt: at, CheckedAt: time.Now()},
	}
	orders := [][]int{{0, 1, 2}, {2, 1, 0}, {1, 0, 2}, {1, 2, 0}}
	for _, order := range orders {
		store, err := Open(ctx, ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		for _, index := range order {
			if err := store.Merge(ctx, updates[index]); err != nil {
				_ = store.Close()
				t.Fatal(err)
			}
		}
		// Applying the winner again must leave the replica unchanged.
		if err := store.Merge(ctx, updates[2]); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		got, err := store.Snapshot(ctx, 1)
		_ = store.Close()
		if err != nil || len(got) != 1 || got[0].Name != "zulu" || got[0].NodeID != "owner-z" || !got[0].Deleted || !got[0].CheckedAt.IsZero() {
			t.Fatalf("merge order %v produced %+v, err %v", order, got, err)
		}
	}
}

func TestOwnedHealthChecksPublishOnlyStateChanges(t *testing.T) {
	ctx := context.Background()
	healthy := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	service, err := store.Register(ctx, Service{
		ID: "web-1", Name: "web", NodeID: "node-a", Address: server.Listener.Addr().String(),
		Region: "jnb", HealthCheck: server.URL + "/healthz", Health: HealthUnknown,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CheckOwnedHealth(ctx, "node-b", time.Second); err != nil {
		t.Fatal(err)
	}
	unchanged, err := store.Get(ctx, service.ID)
	if err != nil || unchanged.Health != HealthUnknown {
		t.Fatalf("non-owner health check changed record: %+v, err %v", unchanged, err)
	}
	if err := store.CheckOwnedHealth(ctx, "node-a", time.Second); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Get(ctx, service.ID)
	if err != nil || updated.Health != HealthHealthy || updated.Revision != 2 {
		t.Fatalf("healthy check result = %+v, err = %v", updated, err)
	}
	healthy = false
	if err := store.CheckOwnedHealth(ctx, "node-a", time.Second); err != nil {
		t.Fatal(err)
	}
	updated, err = store.Get(ctx, service.ID)
	if err != nil || updated.Health != HealthUnhealthy || updated.Revision != 3 {
		t.Fatalf("unhealthy check result = %+v, err = %v", updated, err)
	}
}

func TestBackupRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, filepath.Join(dir, "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	want, err := store.Register(ctx, Service{
		ID: "billing-1", Name: "billing", NodeID: "node-a", Address: "10.0.0.9:8080",
		Region: "fra", Health: HealthHealthy,
	})
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "backup.db")
	if err := store.Backup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	if err := Restore(ctx, backup, backup+".restored"); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(ctx, backup+".restored")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	got, err := restored.Get(ctx, want.ID)
	if err != nil || got.Name != want.Name || got.Revision != want.Revision {
		t.Fatalf("restored service = %+v, err = %v", got, err)
	}
	if err := Restore(ctx, backup, backup+".restored"); err == nil {
		t.Fatal("restore overwrote an existing database")
	}
}

func TestSnapshotRotatesAcrossAllRecords(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("service-%d", i)
		if _, err := store.Register(ctx, Service{
			ID: id, Name: "service", NodeID: "node-a", Address: "10.0.0.1:8080", Region: "jnb",
		}); err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[string]bool)
	for range 3 {
		batch, err := store.Snapshot(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) != 2 {
			t.Fatalf("snapshot length = %d, want 2", len(batch))
		}
		for _, service := range batch {
			seen[service.ID] = true
		}
	}
	if len(seen) != 5 {
		t.Fatalf("rotating snapshots covered %d/5 records: %v", len(seen), seen)
	}
}
