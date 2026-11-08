package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// hashOf returns a realistic visitor hash: production hashes are SHA-256.
func hashOf(i int) string {
	sum := sha256.Sum256([]byte(fmt.Sprint(i)))
	return hex.EncodeToString(sum[:])
}

func pageView(at time.Time, page, hash, country string) TelemetryEvent {
	return TelemetryEvent{At: at, Kind: EventPageView, Page: page, VisitorHash: hash, Country: country}
}

func openMemory(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

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
	for _, e := range []TelemetryEvent{
		pageView(now, "/", hashOf(1), "RW"),
		pageView(now, "/docs/:slug", hashOf(1), "RW"),
		pageView(now, "/about", hashOf(2), "FR"),
		pageView(yesterday, "/", hashOf(3), ""),
	} {
		if err := store.RecordTelemetry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	report, err := store.Analytics(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	today := report.Daily[1]
	if len(report.Daily) != 2 || today.Date != now.Format("2006-01-02") || today.PageViews != 3 || today.UniqueVisitors != 2 {
		t.Fatalf("today = %+v, want 3 views and 2 unique visitors", today)
	}
	if report.Daily[0].PageViews != 1 || report.Daily[0].UniqueVisitors != 1 {
		t.Fatalf("yesterday = %+v, want 1 view and visitor", report.Daily[0])
	}
	if len(report.Pages) != 3 || report.Pages[0].Page != "/" || report.Pages[0].PageViews != 2 {
		t.Fatalf("pages = %+v, want '/' with 2 views and 3 routes", report.Pages)
	}
	want := []AnalyticsCountry{{Country: "RW", PageViews: 2, Visitors: 1}, {Country: "FR", PageViews: 1, Visitors: 1}}
	if fmt.Sprint(report.Countries) != fmt.Sprint(want) {
		t.Fatalf("countries = %+v, want %+v", report.Countries, want)
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

func TestAnalyticsCountsSystemsAndVersions(t *testing.T) {
	ctx := context.Background()
	store := openMemory(t)
	now := time.Now().UTC()
	events := []TelemetryEvent{
		pageView(now, "/", hashOf(1), "KE"),
		{At: now, Kind: EventConnect, Connect: ConnectNode, Version: "v1.2.0", VisitorHash: hashOf(1)},
		{At: now, Kind: EventConnect, Connect: ConnectNode, Version: "v1.2.0", VisitorHash: hashOf(1)},
		{At: now, Kind: EventConnect, Connect: ConnectNode, Version: "edge-abc", VisitorHash: hashOf(2), Country: "US"},
		{At: now, Kind: EventConnect, Connect: ConnectDemo, VisitorHash: hashOf(3)},
		// A later country never replaces the first one seen that day.
		pageView(now, "/", hashOf(1), "TZ"),
	}
	for _, e := range events {
		if err := store.RecordTelemetry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	report, err := store.Analytics(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	day := report.Daily[0]
	if day.Systems != 2 || day.NodeConnections != 3 || day.DemoConnections != 1 || day.UniqueVisitors != 3 {
		t.Fatalf("day = %+v, want 2 systems, 3 node and 1 demo connections, 3 visitors", day)
	}
	if fmt.Sprint(report.Versions) != fmt.Sprint([]AnalyticsVersion{{"v1.2.0", 2}, {"edge-abc", 1}}) {
		t.Fatalf("versions = %+v", report.Versions)
	}
	visitors := map[string]int64{}
	for _, c := range report.Countries {
		visitors[c.Country] = c.Visitors
	}
	if visitors["KE"] != 1 || visitors["US"] != 1 || visitors["TZ"] != 0 {
		t.Fatalf("country visitors = %v, want the first country per browser and day", visitors)
	}
	if fmt.Sprint(report.SystemCountries) != "[{KE 1} {US 1}]" {
		t.Fatalf("system countries = %+v, want one system each in KE and US", report.SystemCountries)
	}
	if n, _ := EstimateUniqueVisitors(report.Sketches.Systems[day.Date]); n != 2 {
		t.Fatalf("systems sketch estimate = %d, want 2", n)
	}
}

func TestAnalyticsRejectsInvalidInput(t *testing.T) {
	store := openMemory(t)
	now := time.Now()
	for _, e := range []TelemetryEvent{
		pageView(now, "", hashOf(1), ""),
		pageView(now, "about", hashOf(1), ""),
		pageView(now, "/", "not-a-hash", ""),
		pageView(now, "/", hashOf(1), "rwanda"),
		{At: now, Kind: EventConnect, Connect: "cluster", VisitorHash: hashOf(1)},
		{At: now, Kind: EventConnect, Connect: ConnectNode, Version: "v1; drop table", VisitorHash: hashOf(1)},
		{At: now, Kind: "click", VisitorHash: hashOf(1)},
	} {
		if err := store.RecordTelemetry(context.Background(), e); err == nil {
			t.Errorf("RecordTelemetry(%+v) succeeded; want validation error", e)
		}
	}
	if _, err := store.Analytics(context.Background(), 0); err == nil {
		t.Error("Analytics(0) succeeded; want validation error")
	}
}

func TestAnalyticsCapsVisitorsPerDay(t *testing.T) {
	ctx := context.Background()
	store := openMemory(t)
	now := time.Now().UTC()
	for i := 0; i <= AnalyticsMaxUniqueVisitorsPerDay; i++ {
		if err := store.RecordTelemetry(ctx, pageView(now, "/", hashOf(i+1), "")); err != nil {
			t.Fatalf("RecordTelemetry(%d): %v", i, err)
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

func TestAnalyticsCapsDistinctPagesAndVersionsPerDay(t *testing.T) {
	ctx := context.Background()
	store := openMemory(t)
	now := time.Now().UTC()
	day := now.Format("2006-01-02")
	for i := 0; i <= AnalyticsMaxPagesPerDay; i++ {
		if err := store.RecordTelemetry(ctx, pageView(now, fmt.Sprintf("/route-%d", i), hashOf(i+1), "")); err != nil {
			t.Fatalf("page %d: %v", i, err)
		}
	}
	for i := 0; i <= AnalyticsMaxVersionsPerDay; i++ {
		e := TelemetryEvent{At: now, Kind: EventConnect, Connect: ConnectNode, Version: fmt.Sprintf("v0.%d", i), VisitorHash: hashOf(i + 1)}
		if err := store.RecordTelemetry(ctx, e); err != nil {
			t.Fatalf("version %d: %v", i, err)
		}
	}
	var pages, versions, otherViews, otherConnections int64
	for query, dst := range map[string]*int64{
		`SELECT COUNT(*) FROM analytics_page_views WHERE day = ?`:                           &pages,
		`SELECT COUNT(DISTINCT version) FROM analytics_connections WHERE day = ?`:           &versions,
		`SELECT views FROM analytics_page_views WHERE day = ? AND page = '/other'`:          &otherViews,
		`SELECT connections FROM analytics_connections WHERE day = ? AND version = 'other'`: &otherConnections,
	} {
		if err := store.db.QueryRowContext(ctx, query, day).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	if pages != AnalyticsMaxPagesPerDay || otherViews != 2 {
		t.Fatalf("pages = %d (/other %d), want %d including /other with 2 views", pages, otherViews, AnalyticsMaxPagesPerDay)
	}
	if versions != AnalyticsMaxVersionsPerDay || otherConnections != 2 {
		t.Fatalf("versions = %d (other %d), want %d including other with 2", versions, otherConnections, AnalyticsMaxVersionsPerDay)
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
	if got, _ := EstimateUniqueVisitors(merged); got != 2 {
		t.Fatalf("estimated visitors = %d, want 2", got)
	}
	dst := map[string][]byte{"d": first}
	if err := MergeSketchMaps(dst, map[string][]byte{"d": first, "e": second}); err != nil {
		t.Fatal(err)
	}
	if got, _ := EstimateUniqueVisitors(dst["d"]); got != 1 {
		t.Fatalf("duplicate visitor estimate = %d, want 1", got)
	}
	if err := MergeSketchMaps(dst, map[string][]byte{"x": {1, 2}}); err == nil {
		t.Fatal("a malformed sketch was merged")
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
	for _, e := range []TelemetryEvent{
		pageView(old, "/", hashOf(1), "RW"),
		{At: old, Kind: EventConnect, Connect: ConnectNode, Version: "v1", VisitorHash: hashOf(1)},
	} {
		if err := store.RecordTelemetry(ctx, e); err != nil {
			t.Fatal(err)
		}
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
	if len(report.Pages) != 0 || len(report.Countries) != 0 || len(report.Versions) != 0 {
		t.Fatalf("expired data remains after reopening: %+v", report)
	}
}
