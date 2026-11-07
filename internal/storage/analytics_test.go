package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestAnalyticsRecordsDailyViewsAndVisitors(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "registry.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Now().UTC()
	yesterday := now.AddDate(0, 0, -1)
	for _, event := range []struct {
		at   time.Time
		page string
		hash string
	}{
		{now, "/", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{now, "/docs/:slug", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{now, "/about", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{yesterday, "/", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
	} {
		if err := store.RecordPageView(ctx, event.at, event.page, event.hash); err != nil {
			t.Fatal(err)
		}
	}

	report, err := store.Analytics(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Daily) != 2 {
		t.Fatalf("daily length = %d, want 2", len(report.Daily))
	}
	today := report.Daily[1]
	if today.Date != now.Format("2006-01-02") || today.PageViews != 3 || today.UniqueVisitors != 2 {
		t.Fatalf("today = %+v, want 3 views and 2 unique visitors", today)
	}
	if report.Daily[0].PageViews != 1 || report.Daily[0].UniqueVisitors != 1 {
		t.Fatalf("yesterday = %+v, want 1 view and visitor", report.Daily[0])
	}
	if len(report.Pages) != 3 || report.Pages[0].Page != "/" || report.Pages[0].PageViews != 2 {
		t.Fatalf("pages = %+v, want '/' with 2 views and 3 routes", report.Pages)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := store.Analytics(ctx, 2)
	if err != nil || persisted.Daily[1].PageViews != 3 {
		t.Fatalf("persisted analytics = %+v, err = %v", persisted, err)
	}
}

func TestAnalyticsRejectsInvalidInput(t *testing.T) {
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	for _, event := range []struct {
		page string
		hash string
	}{
		{"", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"about", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"/", "not-a-hash"},
	} {
		if err := store.RecordPageView(context.Background(), time.Now(), event.page, event.hash); err == nil {
			t.Errorf("RecordPageView(%q, %q) succeeded; want validation error", event.page, event.hash)
		}
	}
	if _, err := store.Analytics(context.Background(), 0); err == nil {
		t.Error("Analytics(0) succeeded; want validation error")
	}
}

func TestAnalyticsPrunesExpiredRowsWhenStoreOpens(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "registry.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -AnalyticsRetentionDays)
	if err := store.RecordPageView(ctx, old, "/", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	report, err := store.Analytics(ctx, AnalyticsRetentionDays)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Pages) != 0 {
		t.Fatalf("expired page views remain after reopening: %+v", report.Pages)
	}
}
