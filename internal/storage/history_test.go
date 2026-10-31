package storage

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestHistoryRecordsEveryChangeNewestFirst(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	service, err := store.Register(ctx, Service{ID: "h-1", Name: "billing", NodeID: "n", Address: "10.0.0.1:80"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetHealth(ctx, service.ID, "n", HealthHealthy, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Update(ctx, service.ID, "n", 0, func(s *Service) error { s.Version = "2"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, service.ID, "n"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Register(ctx, Service{ID: "other", Name: "other", NodeID: "n", Address: "10.0.0.2:80"}); err != nil {
		t.Fatal(err)
	}
	// An unchanged re-registration and a checked-at update are not changes.
	if _, outcome, err := store.Upsert(ctx, Service{ID: "other", Name: "other", NodeID: "n", Address: "10.0.0.2:80"}); err != nil || outcome != Unchanged {
		t.Fatalf("identical re-registration = %v %v", outcome, err)
	}
	if err := store.SetHealth(ctx, "other", "n", HealthUnknown, time.Now()); err != nil {
		t.Fatal(err)
	}

	entries, next, err := store.History(ctx, HistoryFilter{ServiceID: service.ID, Limit: 10})
	if err != nil || next != 0 {
		t.Fatalf("History = %v next=%d", err, next)
	}
	want := []string{ChangeDeregistered, ChangeUpdated, ChangeHealth, ChangeRegistered}
	if len(entries) != len(want) {
		t.Fatalf("entries = %+v, want %v", entries, want)
	}
	for i, entry := range entries {
		if entry.Change != want[i] || entry.Revision != uint64(len(want)-i) || entry.Service.ID != service.ID {
			t.Errorf("entry %d = %s rev %d, want %s rev %d", i, entry.Change, entry.Revision, want[i], len(want)-i)
		}
	}
	if entries[1].Service.Version != "2" || !entries[0].Service.Deleted {
		t.Fatalf("snapshots = %+v / %+v", entries[1].Service, entries[0].Service)
	}

	byName, _, err := store.History(ctx, HistoryFilter{Name: "other", Limit: 10})
	if err != nil || len(byName) != 1 || byName[0].Change != ChangeRegistered {
		t.Fatalf("name filter = %+v %v", byName, err)
	}
	if _, _, err := store.History(ctx, HistoryFilter{Limit: 0}); err == nil {
		t.Fatal("zero page size accepted")
	}
}

func TestHistoryPagingCapAndPrune(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	for i := range historyPerService + 20 {
		if _, err := store.Register(ctx, Service{ID: "busy", Name: "busy", NodeID: "n", Address: fmt.Sprintf("10.0.0.1:%d", 1000+i)}); err != nil {
			t.Fatal(err)
		}
	}
	var all []HistoryEntry
	before := int64(0)
	for {
		page, next, err := store.History(ctx, HistoryFilter{ServiceID: "busy", Before: before, Limit: 30})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page...)
		if next == 0 {
			break
		}
		before = next
	}
	if len(all) != historyPerService || all[0].Revision != historyPerService+20 {
		t.Fatalf("kept %d entries starting at revision %d, want the newest %d", len(all), all[0].Revision, historyPerService)
	}

	removed, err := store.PruneHistory(ctx, time.Now().Add(time.Hour))
	if err != nil || removed != historyPerService {
		t.Fatalf("PruneHistory removed %d, err %v", removed, err)
	}

	store.SetHistoryEnabled(false)
	if _, err := store.Register(ctx, Service{ID: "quiet", Name: "quiet", NodeID: "n", Address: "10.0.0.9:80"}); err != nil {
		t.Fatal(err)
	}
	if entries, _, _ := store.History(ctx, HistoryFilter{ServiceID: "quiet", Limit: 5}); len(entries) != 0 {
		t.Fatalf("history recorded while disabled: %+v", entries)
	}
}

func TestHistoryIncludesReplicatedChanges(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	if err := store.Merge(ctx, Service{ID: "r", Name: "remote", NodeID: "m", Address: "10.0.0.3:80", Health: HealthUnknown, Revision: 3, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	entries, _, err := store.History(ctx, HistoryFilter{ServiceID: "r", Limit: 5})
	if err != nil || len(entries) != 1 || entries[0].Revision != 3 || entries[0].Service.NodeID != "m" {
		t.Fatalf("replicated history = %+v %v", entries, err)
	}
}

func TestFailedHistoryWriteStoresNothing(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	notified := 0
	store.OnChange(func(Change) { notified++ })
	if _, err := store.db.ExecContext(ctx, `DROP TABLE service_history`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Register(ctx, Service{ID: "atomic", Name: "atomic", NodeID: "n", Address: "10.0.0.1:80"}); err == nil {
		t.Fatal("registration succeeded although its history could not be written")
	}
	if _, err := store.Get(ctx, "atomic"); err == nil {
		t.Fatal("the service was stored without its history entry")
	}
	if notified != 0 {
		t.Fatalf("listeners ran %d times for a write that was rolled back", notified)
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	store.SetHistoryEnabled(false)
	if _, err := store.Register(cancelled, Service{ID: "cancelled", Name: "c", NodeID: "n", Address: "10.0.0.2:80"}); err == nil {
		t.Fatal("registration succeeded with a cancelled context")
	}
	if _, err := store.Get(ctx, "cancelled"); err == nil || notified != 0 {
		t.Fatalf("a cancelled write was stored or notified (notified=%d)", notified)
	}
}
