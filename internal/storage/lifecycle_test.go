package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "registry.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestUpsertIsIdempotentAndKeepsHealthForMetadataEdits(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	var changes []Change
	store.OnChange(func(change Change) { changes = append(changes, change) })

	base := Service{ID: "web-1", Name: "web", NodeID: "a", Address: "10.0.0.1:80", Region: "eu"}
	created, outcome, err := store.Upsert(ctx, base)
	if err != nil || outcome != Created || created.Revision != 1 {
		t.Fatalf("create = %+v %v %v", created, outcome, err)
	}
	again, outcome, err := store.Upsert(ctx, base)
	if err != nil || outcome != Unchanged || again.Revision != 1 {
		t.Fatalf("retry = %+v %v %v, want unchanged revision 1", again, outcome, err)
	}
	if err := store.SetHealth(ctx, "web-1", "a", HealthHealthy, time.Now()); err != nil {
		t.Fatal(err)
	}
	tagged := base
	tagged.Tags = []string{"canary"}
	updated, outcome, err := store.Upsert(ctx, tagged)
	if err != nil || outcome != Updated || updated.Health != HealthHealthy || updated.Revision != 3 {
		t.Fatalf("metadata edit = %+v %v %v, want healthy revision 3", updated, outcome, err)
	}
	moved := tagged
	moved.Address = "10.0.0.2:80"
	updated, _, err = store.Upsert(ctx, moved)
	if err != nil || updated.Health != HealthUnknown {
		t.Fatalf("address change = %+v %v, want health reset", updated, err)
	}
	if _, _, err := store.Upsert(ctx, Service{ID: "web-1", Name: "web", NodeID: "b", Address: "10.0.0.1:80"}); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("foreign upsert err = %v, want ErrNotOwner", err)
	}
	if len(changes) != 4 || changes[0].Before != nil || changes[1].Before == nil {
		t.Fatalf("changes = %d (%+v), want 4 with Before set after the first", len(changes), changes)
	}
}

func TestUpsertReusesDeletedIDFromAnotherNode(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	if _, err := store.Register(ctx, Service{ID: "job-1", Name: "job", NodeID: "a", Address: "10.0.0.1:80"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Tombstone(ctx, "job-1"); err != nil {
		t.Fatal(err)
	}
	moved, outcome, err := store.Upsert(ctx, Service{ID: "job-1", Name: "job", NodeID: "b", Address: "10.0.0.9:80"})
	if err != nil || outcome != Created || moved.NodeID != "b" || moved.Revision != 3 {
		t.Fatalf("takeover = %+v %v %v", moved, outcome, err)
	}
}

func TestUpdateChecksOwnerAndRevision(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	service, err := store.Register(ctx, Service{ID: "db-1", Name: "db", NodeID: "a", Address: "10.0.0.1:5432", Version: "15"})
	if err != nil {
		t.Fatal(err)
	}
	setVersion := func(v string) func(*Service) error {
		return func(s *Service) error { s.Version = v; return nil }
	}
	if _, _, err := store.Update(ctx, service.ID, "b", 0, setVersion("16")); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("foreign update err = %v", err)
	}
	if _, _, err := store.Update(ctx, service.ID, "a", 99, setVersion("16")); !errors.Is(err, ErrRevisionMismatch) {
		t.Fatalf("stale update err = %v", err)
	}
	updated, outcome, err := store.Update(ctx, service.ID, "a", 1, setVersion("16"))
	if err != nil || outcome != Updated || updated.Version != "16" || updated.Revision != 2 {
		t.Fatalf("update = %+v %v %v", updated, outcome, err)
	}
	if _, outcome, _ := store.Update(ctx, service.ID, "a", 0, setVersion("16")); outcome != Unchanged {
		t.Fatalf("no-op update outcome = %v", outcome)
	}
	if _, _, err := store.Update(ctx, service.ID, "a", 0, func(s *Service) error { s.TTLSeconds = 5; s.HealthCheck = "http://10.0.0.1:5432/"; return nil }); err == nil {
		t.Fatal("update accepted both a TTL and a health check")
	}
}

func TestHeartbeatAndTTLExpiry(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	service, err := store.Register(ctx, Service{ID: "worker-1", Name: "worker", NodeID: "a", Address: "10.0.0.1:9000", TTLSeconds: 10})
	if err != nil || service.HeartbeatAt.IsZero() {
		t.Fatalf("register = %+v %v, want heartbeat clock started", service, err)
	}
	if _, err := store.Heartbeat(ctx, service.ID, "b", time.Now()); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("foreign heartbeat err = %v", err)
	}
	start := time.Now()
	beat, err := store.Heartbeat(ctx, service.ID, "a", start)
	if err != nil || beat.Health != HealthHealthy {
		t.Fatalf("heartbeat = %+v %v", beat, err)
	}
	if unhealthy, gone, err := store.ExpireTTL(ctx, "a", start.Add(5*time.Second)); err != nil || unhealthy+gone != 0 {
		t.Fatalf("early expiry = %d/%d %v", unhealthy, gone, err)
	}
	if unhealthy, _, err := store.ExpireTTL(ctx, "a", start.Add(11*time.Second)); err != nil || unhealthy != 1 {
		t.Fatalf("expiry after one TTL = %d %v", unhealthy, err)
	}
	if got, _ := store.Get(ctx, service.ID); got.Health != HealthUnhealthy {
		t.Fatalf("health after missed TTL = %s", got.Health)
	}
	if _, gone, err := store.ExpireTTL(ctx, "a", start.Add(31*time.Second)); err != nil || gone != 1 {
		t.Fatalf("expiry after three TTLs = %d %v", gone, err)
	}
	if _, err := store.Get(ctx, service.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after deregistration err = %v", err)
	}

	plain, err := store.Register(ctx, Service{ID: "plain", Name: "plain", NodeID: "a", Address: "10.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Heartbeat(ctx, plain.ID, "a", time.Now()); !errors.Is(err, ErrNoTTL) {
		t.Fatalf("heartbeat without TTL err = %v", err)
	}
}

func TestReplicatedTTLServiceGetsGraceOnOwner(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	err := store.Merge(ctx, Service{ID: "r-1", Name: "r", NodeID: "a", Address: "10.0.0.1:1", TTLSeconds: 1, Revision: 4, Health: HealthHealthy, UpdatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if u, g, err := store.ExpireTTL(ctx, "a", time.Now().Add(time.Hour)); err != nil || u+g != 0 {
		t.Fatalf("first expiry pass = %d/%d %v, want grace", u, g, err)
	}
}

func TestTombstoneOwnedByAndOwnerNodes(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	for i, owner := range []string{"a", "a", "b"} {
		if _, err := store.Register(ctx, Service{ID: fmt.Sprintf("s-%d", i), Name: "s", NodeID: owner, Address: "10.0.0.1:1"}); err != nil {
			t.Fatal(err)
		}
	}
	owners, err := store.OwnerNodes(ctx)
	if err != nil || len(owners) != 2 {
		t.Fatalf("owners = %v %v", owners, err)
	}
	if n, err := store.TombstoneOwnedBy(ctx, "a"); err != nil || n != 2 {
		t.Fatalf("tombstoned = %d %v", n, err)
	}
	owners, _ = store.OwnerNodes(ctx)
	if len(owners) != 1 || owners[0] != "b" {
		t.Fatalf("owners after eviction = %v", owners)
	}
}

func TestListPageFiltersAndCursor(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	for i := range 7 {
		tags := []string{"all"}
		if i%2 == 0 {
			tags = append(tags, "even")
		}
		if _, err := store.Register(ctx, Service{
			ID: fmt.Sprintf("svc-%d", i), Name: "api", NodeID: "a", Address: fmt.Sprintf("10.0.0.%d:80", i+1),
			Version: fmt.Sprintf("v%d", i%2), Tags: tags,
		}); err != nil {
			t.Fatal(err)
		}
	}
	var seen []string
	cursor := ""
	for {
		page, err := store.ListPage(ctx, Filter{Name: "api"}, cursor, 3)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range page.Services {
			seen = append(seen, s.ID)
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != 7 {
		t.Fatalf("paged %d services (%v), want 7", len(seen), seen)
	}
	even, err := store.ListPage(ctx, Filter{Tags: []string{"all", "even"}, Version: "v0"}, "", 100)
	if err != nil || len(even.Services) != 4 {
		t.Fatalf("tag+version filter = %d %v, want 4", len(even.Services), err)
	}
	if _, err := store.ListPage(ctx, Filter{}, "not-a-cursor!", 10); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("bad cursor err = %v", err)
	}
	if _, err := store.ListPage(ctx, Filter{}, "", MaxPageSize+1); err == nil {
		t.Fatal("oversized page accepted")
	}
}

func TestMigratesVersionOneDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, migrations[1]+`PRAGMA user_version = 1;
INSERT INTO services (id,name,node_id,address,region,revision,updated_at) VALUES ('old','old','a','10.0.0.1:1','eu',1,1);`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := store.Get(ctx, "old")
	if err != nil || service.TTLSeconds != 0 || !service.HeartbeatAt.IsZero() {
		t.Fatalf("migrated row = %+v %v", service, err)
	}
}
