package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/manziosee/atlas/internal/httperr"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/metrics"
	"github.com/manziosee/atlas/internal/storage"
)

// telemetryPath receives anonymous usage events from the hosted console.
const telemetryPath = "/v1/telemetry/event"

// telemetryKeyHeader carries the shared key between the site's edge function
// and the nodes (ATLAS_TELEMETRY_KEY).
const telemetryKeyHeader = "X-Atlas-Telemetry-Key"

const defaultAnalyticsDays = 30

type telemetryRecorder interface {
	RecordTelemetry(context.Context, storage.TelemetryEvent) error
}

type analyticsReader interface {
	Analytics(context.Context, int) (storage.Analytics, error)
}

type apiRouteCount struct {
	Route  string `json:"route"`
	Count  uint64 `json:"count"`
	Errors uint64 `json:"errors"`
}

type adminAnalytics struct {
	Days                  int                        `json:"days"`
	NodeID                string                     `json:"node_id"`
	NodesIncluded         int                        `json:"nodes_included"`
	NodeErrors            []analyticsNodeError       `json:"node_errors"`
	Daily                 []storage.AnalyticsDay     `json:"daily"`
	Pages                 []storage.AnalyticsPage    `json:"pages"`
	Countries             []storage.AnalyticsCountry `json:"countries"`
	Versions              []storage.AnalyticsVersion `json:"versions"`
	APIRequestsSinceStart uint64                     `json:"api_requests_since_start"`
	APIErrorsSinceStart   uint64                     `json:"api_errors_since_start"`
	APIRequestsByRoute    []apiRouteCount            `json:"api_requests_by_route"`
	// Sketches travel between nodes only; they are dropped before the
	// report reaches a client.
	Sketches *storage.AnalyticsSketches `json:"sketches,omitempty"`
}

type analyticsNodeError struct {
	NodeID string `json:"node_id"`
	Error  string `json:"error"`
}

type telemetryEvent struct {
	Type      string `json:"type"`
	VisitorID string `json:"visitor_id"`
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Version   string `json:"version"`
	Country   string `json:"country"`
}

func (d Deps) recordTelemetry(w http.ResponseWriter, r *http.Request) {
	if !d.AnalyticsEnabled {
		httperr.Write(w, http.StatusNotFound, httperr.CodeRouteNotFound, "telemetry is disabled")
		return
	}
	recorder, ok := d.Registry.(telemetryRecorder)
	if !ok {
		httperr.Write(w, http.StatusNotImplemented, httperr.CodeUnavailable, "telemetry is not supported by this registry")
		return
	}
	trusted := false
	if d.TelemetryKey != "" {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(telemetryKeyHeader)), []byte(d.TelemetryKey)) != 1 {
			httperr.Write(w, http.StatusUnauthorized, httperr.CodeUnauthorized, "a valid telemetry key is required")
			return
		}
		trusted = true
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		httperr.Write(w, http.StatusUnsupportedMediaType, httperr.CodeValidation, "content type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var in telemetryEvent
	if !decodeBody(w, r, &in) {
		return
	}
	if !isBrowserID(in.VisitorID) {
		httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "visitor_id must be a UUID")
		return
	}
	at := time.Now().UTC()
	digest := sha256.Sum256([]byte(at.Format("2006-01-02") + ":" + strings.ToLower(in.VisitorID)))
	event := storage.TelemetryEvent{At: at, VisitorHash: hex.EncodeToString(digest[:])}
	if trusted {
		// Only the site's own edge function knows the key and sets the country.
		event.Country = strings.ToUpper(in.Country)
	}
	switch in.Type {
	case "", storage.EventPageView:
		event.Kind, event.Page = storage.EventPageView, analyticsPage(in.Path)
	case storage.EventConnect:
		event.Kind, event.Connect, event.Version = storage.EventConnect, in.Kind, in.Version
	default:
		httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "type must be pageview or connect")
		return
	}
	if err := storage.ValidateTelemetry(event); err != nil {
		httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, strings.TrimPrefix(err.Error(), "storage: "))
		return
	}
	if err := recorder.RecordTelemetry(r.Context(), event); err != nil {
		d.Log.Error("telemetry event could not be recorded", "err", err)
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not record the event")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) adminAnalytics(w http.ResponseWriter, r *http.Request) {
	reader, ok := d.Registry.(analyticsReader)
	if !ok {
		httperr.Write(w, http.StatusNotImplemented, httperr.CodeUnavailable, "analytics are not supported by this registry")
		return
	}
	days := defaultAnalyticsDays
	if raw := r.URL.Query().Get("days"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > storage.AnalyticsRetentionDays {
			httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, fmt.Sprintf("days must be between 1 and %d", storage.AnalyticsRetentionDays))
			return
		}
		days = value
	}
	data, err := reader.Analytics(r.Context(), days)
	if err != nil {
		d.Log.Error("admin analytics query failed", "err", err)
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not read analytics")
		return
	}
	requests, errors, byRoute, err := apiRequestTotals()
	if err != nil {
		d.Log.Error("admin analytics metrics failed", "err", err)
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not read analytics")
		return
	}
	report := adminAnalytics{
		Days: days, NodeID: d.Cluster.LocalNode().ID, NodesIncluded: 1, NodeErrors: []analyticsNodeError{},
		Daily: data.Daily, Pages: data.Pages, Countries: data.Countries, Versions: data.Versions,
		APIRequestsSinceStart: requests, APIErrorsSinceStart: errors, APIRequestsByRoute: byRoute,
		Sketches: data.Sketches,
	}
	if !isForwarded(r.Context()) {
		report = d.clusterAdminAnalytics(r.Context(), days, report)
		report.Sketches = nil
	}
	writeJSON(w, http.StatusOK, report)
}

// clusterAdminAnalytics adds every reachable peer's report to local. Counts
// that can double-count a browser seen by several nodes (unique visitors,
// systems, country visitors) come from merged sketches when more than one
// node contributed; a single node keeps its exact counts.
func (d Deps) clusterAdminAnalytics(ctx context.Context, days int, local adminAnalytics) adminAnalytics {
	results, _ := d.fanOutPeerGET(ctx, func(membership.Member) string {
		return "/v1/admin/analytics?days=" + strconv.Itoa(days)
	})
	if local.Sketches == nil {
		local.Sketches = &storage.AnalyticsSketches{}
	}
	for _, m := range []*map[string][]byte{&local.Sketches.Visitors, &local.Sketches.Systems, &local.Sketches.Countries} {
		if *m == nil {
			*m = map[string][]byte{}
		}
	}
	day := make(map[string]int, len(local.Daily))
	for i, d := range local.Daily {
		day[d.Date] = i
	}
	pages := map[string]int64{}
	for _, p := range local.Pages {
		pages[p.Page] += p.PageViews
	}
	countries := map[string]storage.AnalyticsCountry{}
	for _, c := range local.Countries {
		countries[c.Country] = c
	}
	versions := map[string]int64{}
	for _, v := range local.Versions {
		versions[v.Version] += v.Connections
	}
	routes := map[string]apiRouteCount{}
	for _, r := range local.APIRequestsByRoute {
		routes[r.Route] = r
	}

	fail := func(id, msg string) {
		local.NodeErrors = append(local.NodeErrors, analyticsNodeError{NodeID: id, Error: msg})
	}
	for _, result := range results {
		id := result.member.ID
		if result.err != nil {
			fail(id, result.err.Error())
			continue
		}
		if result.status != http.StatusOK {
			fail(id, "node answered "+result.statusLine)
			continue
		}
		var peer adminAnalytics
		if err := json.Unmarshal(result.body, &peer); err != nil || peer.Sketches == nil {
			fail(id, "node returned invalid analytics")
			continue
		}
		// Merge into copies first so a malformed peer changes nothing.
		merged := storage.AnalyticsSketches{Visitors: clone(local.Sketches.Visitors), Systems: clone(local.Sketches.Systems), Countries: clone(local.Sketches.Countries)}
		if storage.MergeSketchMaps(merged.Visitors, peer.Sketches.Visitors) != nil ||
			storage.MergeSketchMaps(merged.Systems, peer.Sketches.Systems) != nil ||
			storage.MergeSketchMaps(merged.Countries, peer.Sketches.Countries) != nil {
			fail(id, "node returned an invalid visitor summary")
			continue
		}
		local.Sketches = &merged
		local.NodesIncluded++
		for _, pd := range peer.Daily {
			if i, ok := day[pd.Date]; ok {
				local.Daily[i].PageViews += pd.PageViews
				local.Daily[i].NodeConnections += pd.NodeConnections
				local.Daily[i].DemoConnections += pd.DemoConnections
				local.Daily[i].UniqueVisitorsCapped = local.Daily[i].UniqueVisitorsCapped || pd.UniqueVisitorsCapped
			}
		}
		for _, p := range peer.Pages {
			pages[p.Page] += p.PageViews
		}
		for _, c := range peer.Countries {
			entry := countries[c.Country]
			entry.Country = c.Country
			entry.PageViews += c.PageViews
			countries[c.Country] = entry
		}
		for _, v := range peer.Versions {
			versions[v.Version] += v.Connections
		}
		local.APIRequestsSinceStart += peer.APIRequestsSinceStart
		local.APIErrorsSinceStart += peer.APIErrorsSinceStart
		for _, r := range peer.APIRequestsByRoute {
			entry := routes[r.Route]
			entry.Route = r.Route
			entry.Count += r.Count
			entry.Errors += r.Errors
			routes[r.Route] = entry
		}
	}

	if local.NodesIncluded > 1 {
		estimate := func(sketches map[string][]byte, key string, exact int64) int64 {
			if sketch, ok := sketches[key]; ok {
				if n, err := storage.EstimateUniqueVisitors(sketch); err == nil {
					return n
				}
			}
			return exact
		}
		for i := range local.Daily {
			date := local.Daily[i].Date
			local.Daily[i].UniqueVisitors = estimate(local.Sketches.Visitors, date, local.Daily[i].UniqueVisitors)
			local.Daily[i].Systems = estimate(local.Sketches.Systems, date, local.Daily[i].Systems)
		}
		for code := range local.Sketches.Countries {
			entry := countries[code]
			entry.Country = code
			entry.Visitors = estimate(local.Sketches.Countries, code, entry.Visitors)
			countries[code] = entry
		}
	}

	local.Pages = local.Pages[:0]
	for page, views := range pages {
		local.Pages = append(local.Pages, storage.AnalyticsPage{Page: page, PageViews: views})
	}
	sort.Slice(local.Pages, func(i, j int) bool {
		if local.Pages[i].PageViews == local.Pages[j].PageViews {
			return local.Pages[i].Page < local.Pages[j].Page
		}
		return local.Pages[i].PageViews > local.Pages[j].PageViews
	})
	if len(local.Pages) > 20 {
		local.Pages = local.Pages[:20]
	}
	local.Countries = local.Countries[:0]
	for _, c := range countries {
		local.Countries = append(local.Countries, c)
	}
	storage.SortCountries(local.Countries)
	local.Versions = local.Versions[:0]
	for v, n := range versions {
		local.Versions = append(local.Versions, storage.AnalyticsVersion{Version: v, Connections: n})
	}
	storage.SortVersions(local.Versions)
	local.APIRequestsByRoute = local.APIRequestsByRoute[:0]
	for _, r := range routes {
		local.APIRequestsByRoute = append(local.APIRequestsByRoute, r)
	}
	sortRoutes(local.APIRequestsByRoute)
	sort.Slice(local.NodeErrors, func(i, j int) bool { return local.NodeErrors[i].NodeID < local.NodeErrors[j].NodeID })
	return local
}

func clone(m map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(m))
	for k, v := range m {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

// sortRoutes puts routes with the most server errors first, then the busiest.
func sortRoutes(routes []apiRouteCount) {
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Errors != routes[j].Errors {
			return routes[i].Errors > routes[j].Errors
		}
		if routes[i].Count == routes[j].Count {
			return routes[i].Route < routes[j].Route
		}
		return routes[i].Count > routes[j].Count
	})
}

func apiRequestTotals() (uint64, uint64, []apiRouteCount, error) {
	families, err := metrics.Registry.Gather()
	if err != nil {
		return 0, 0, nil, err
	}
	byRoute := make(map[string]apiRouteCount)
	var requests, errors uint64
	for _, family := range families {
		if family.GetName() != "atlas_http_requests_total" {
			continue
		}
		for _, sample := range family.Metric {
			count := uint64(sample.GetCounter().GetValue())
			requests += count
			route, code := "", ""
			for _, label := range sample.Label {
				switch label.GetName() {
				case "route":
					route = label.GetValue()
				case "code":
					code = label.GetValue()
				}
			}
			entry := byRoute[route]
			entry.Route = route
			entry.Count += count
			if strings.HasPrefix(code, "5") {
				entry.Errors += count
				errors += count
			}
			byRoute[route] = entry
		}
	}
	routes := make([]apiRouteCount, 0, len(byRoute))
	for _, route := range byRoute {
		routes = append(routes, route)
	}
	sortRoutes(routes)
	return requests, errors, routes, nil
}

func isBrowserID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

// analyticsPage keeps only a path's first segment ("/docs/x?y" -> "/docs"),
// so new console pages are counted without a list to maintain and no query
// or identifier is ever stored.
func analyticsPage(path string) string {
	path, _, _ = strings.Cut(path, "?")
	path, _, _ = strings.Cut(path, "#")
	section, _, _ := strings.Cut(strings.Trim(path, "/"), "/")
	if section == "" || len(section) > 64 || strings.ContainsAny(section, " \t\n\r%") {
		return "/"
	}
	return "/" + section
}
