package storage

import (
	"context"
	"database/sql"
	"fmt"
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

func TestAnalyticsCapsVisitorsPerDay(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	for i := 0; i <= AnalyticsMaxUniqueVisitorsPerDay; i++ {
		hash := fmt.Sprintf("%064x", i+1)
		if err := store.RecordPageView(ctx, now, "/", hash); err != nil {
			t.Fatalf("RecordPageView(%d): %v", i, err)
		}
	}
	report, err := store.Analytics(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Daily[0].UniqueVisitors; got != AnalyticsMaxUniqueVisitorsPerDay {
		t.Fatalf("unique visitors = %d, want cap %d", got, AnalyticsMaxUniqueVisitorsPerDay)
	}
	if !report.Daily[0].UniqueVisitorsCapped {
		t.Fatal("visitor cap was reached but not reported")
	}
	if got := report.Daily[0].PageViews; got != AnalyticsMaxUniqueVisitorsPerDay+1 {
		t.Fatalf("page views = %d, want every request counted", got)
	}
}

func TestAnalyticsCapsDistinctPagesPerDay(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	now := time.Now().UTC()
	for i := 0; i <= AnalyticsMaxPagesPerDay; i++ {
		hash := fmt.Sprintf("%064x", i+1)
		if err := store.RecordPageView(ctx, now, fmt.Sprintf("/route-%d", i), hash); err != nil {
			t.Fatalf("RecordPageView(%d): %v", i, err)
		}
	}
	var pageCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM analytics_page_views WHERE day = ?`, now.Format("2006-01-02")).Scan(&pageCount); err != nil {
		t.Fatal(err)
	}
	if pageCount != AnalyticsMaxPagesPerDay {
		t.Fatalf("stored route count = %d, want cap %d including /other", pageCount, AnalyticsMaxPagesPerDay)
	}
	var otherViews int64
	if err := store.db.QueryRowContext(ctx, `SELECT views FROM analytics_page_views WHERE day = ? AND page = '/other'`, now.Format("2006-01-02")).Scan(&otherViews); err != nil {
		t.Fatal(err)
	}
	if otherViews != 2 {
		t.Fatalf("/other views = %d, want 2", otherViews)
	}
}

func TestVisitorSketchesMergeAndEstimateDistinctHashes(t *testing.T) {
	first := make([]byte, visitorSketchSize)
	second := make([]byte, visitorSketchSize)
	if err := addVisitorHash(first, fmt.Sprintf("%016x%048x", uint64(0)<<54|1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := addVisitorHash(second, fmt.Sprintf("%016x%048x", uint64(1)<<54|1, 0)); err != nil {
		t.Fatal(err)
	}
	merged, err := MergeVisitorSketches(first, second)
	if err != nil {
		t.Fatal(err)
	}
	got, err := EstimateUniqueVisitors(merged)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("estimated visitors = %d, want 2", got)
	}
	merged, err = MergeVisitorSketches(first, first)
	if err != nil {
		t.Fatal(err)
	}
	got, err = EstimateUniqueVisitors(merged)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("duplicate visitor estimate = %d, want 1", got)
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
	if report.Daily[len(report.Daily)-1].UniqueVisitors != 0 {
		t.Fatalf("expired visitor rows remain after reopening: %+v", report.Daily)
	}
}

func TestAnalyticsMigrationCapsExistingVisitorRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "registry.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().Format("2006-01-02")
	for _, statement := range []string{
		`CREATE TABLE analytics_page_views(day TEXT NOT NULL, page TEXT NOT NULL, views INTEGER NOT NULL, PRIMARY KEY(day, page))`,
		`CREATE TABLE analytics_visitors(day TEXT NOT NULL, visitor_hash TEXT NOT NULL, PRIMARY KEY(day, visitor_hash))`,
		`PRAGMA user_version = 5`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO analytics_visitors(day, visitor_hash) VALUES (?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		t.Fatal(err)
	}
	for i := 0; i <= AnalyticsMaxUniqueVisitorsPerDay; i++ {
		if _, err := stmt.ExecContext(ctx, day, fmt.Sprintf("%064x", i+1)); err != nil {
			_ = stmt.Close()
			_ = tx.Rollback()
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM analytics_visitors WHERE day = ?`, day).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != AnalyticsMaxUniqueVisitorsPerDay {
		t.Fatalf("migrated visitor count = %d, want cap %d", count, AnalyticsMaxUniqueVisitorsPerDay)
	}
	var capped int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM analytics_visitor_caps WHERE day = ?`, day).Scan(&capped); err != nil {
		t.Fatal(err)
	}
	if capped != 1 {
		t.Fatalf("migrated cap marker count = %d, want 1", capped)
	}
}
