package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/manziosee/atlas/internal/httperr"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/metrics"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
)

const defaultAnalyticsDays = 30

type pageViewRecorder interface {
	RecordPageView(context.Context, time.Time, string, string) error
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
	Days                  int                     `json:"days"`
	NodeID                string                  `json:"node_id"`
	NodesIncluded         int                     `json:"nodes_included"`
	NodeErrors            []analyticsNodeError    `json:"node_errors"`
	Daily                 []storage.AnalyticsDay  `json:"daily"`
	Pages                 []storage.AnalyticsPage `json:"pages"`
	APIRequestsSinceStart uint64                  `json:"api_requests_since_start"`
	APIErrorsSinceStart   uint64                  `json:"api_errors_since_start"`
	APIRequestsByRoute    []apiRouteCount         `json:"api_requests_by_route"`
}

type analyticsNodeError struct {
	NodeID string `json:"node_id"`
	Error  string `json:"error"`
}

type analyticsPeerResult struct {
	nodeID string
	report adminAnalytics
	err    string
}

type pageViewEvent struct {
	VisitorID string `json:"visitor_id"`
	Path      string `json:"path"`
}

func (d Deps) recordPageView(w http.ResponseWriter, r *http.Request) {
	recorder, ok := d.Registry.(pageViewRecorder)
	if !ok {
		httperr.Write(w, http.StatusNotImplemented, httperr.CodeUnavailable, "page view analytics are not supported by this registry")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		httperr.Write(w, http.StatusUnsupportedMediaType, httperr.CodeValidation, "content type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var event pageViewEvent
	if !decodeBody(w, r, &event) {
		return
	}
	if !isBrowserID(event.VisitorID) {
		httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "visitor_id must be a UUID")
		return
	}
	at := time.Now().UTC()
	digest := sha256.Sum256([]byte(at.Format("2006-01-02") + ":" + event.VisitorID))
	if err := recorder.RecordPageView(r.Context(), at, analyticsPage(event.Path), hex.EncodeToString(digest[:])); err != nil {
		d.Log.Error("analytics page view could not be recorded", "err", err)
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not record page view")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (d Deps) adminAnalytics(w http.ResponseWriter, r *http.Request) {
	if _, ok := d.Registry.(analyticsReader); !ok {
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
	report, err := d.localAdminAnalytics(r.Context(), days)
	if err != nil {
		d.Log.Error("admin analytics query failed", "err", err)
		httperr.Write(w, http.StatusInternalServerError, httperr.CodeInternal, "could not read analytics")
		return
	}
	if !isForwarded(r.Context()) {
		report = d.clusterAdminAnalytics(r.Context(), days, report)
	}
	writeJSON(w, http.StatusOK, report)
}

func (d Deps) localAdminAnalytics(ctx context.Context, days int) (adminAnalytics, error) {
	reader, ok := d.Registry.(analyticsReader)
	if !ok {
		return adminAnalytics{}, fmt.Errorf("analytics are not supported by this registry")
	}
	data, err := reader.Analytics(ctx, days)
	if err != nil {
		return adminAnalytics{}, fmt.Errorf("read analytics: %w", err)
	}
	requests, errors, byRoute, err := apiRequestTotals()
	if err != nil {
		return adminAnalytics{}, fmt.Errorf("read API request metrics: %w", err)
	}
	return adminAnalytics{
		Days: days, NodeID: d.Cluster.LocalNode().ID, NodesIncluded: 1,
		NodeErrors: []analyticsNodeError{}, Daily: data.Daily, Pages: data.Pages,
		APIRequestsSinceStart: requests, APIErrorsSinceStart: errors,
		APIRequestsByRoute: byRoute,
	}, nil
}

func (d Deps) clusterAdminAnalytics(ctx context.Context, days int, local adminAnalytics) adminAnalytics {
	self := d.Cluster.LocalNode()
	var targets []membership.Member
	for _, member := range d.Cluster.Members() {
		if member.ID == self.ID {
			continue
		}
		if member.State != membership.StateAlive && member.State != membership.StateSuspect {
			local.NodeErrors = append(local.NodeErrors, analyticsNodeError{NodeID: member.ID, Error: "node is " + member.State.String()})
			continue
		}
		targets = append(targets, member)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	if len(targets) > maxTopologyFanout {
		for _, member := range targets[maxTopologyFanout:] {
			local.NodeErrors = append(local.NodeErrors, analyticsNodeError{NodeID: member.ID, Error: "not queried: cluster exceeds the analytics fan-out limit"})
		}
		targets = targets[:maxTopologyFanout]
	}

	results := make([]analyticsPeerResult, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	for i, member := range targets {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = d.peerAdminAnalytics(ctx, member, days)
		})
	}
	wg.Wait()

	dailyByDate := make(map[string]int, len(local.Daily))
	for i, day := range local.Daily {
		dailyByDate[day.Date] = i
	}
	pagesByName := make(map[string]int64, len(local.Pages))
	for _, page := range local.Pages {
		pagesByName[page.Page] += page.PageViews
	}
	routesByName := make(map[string]apiRouteCount, len(local.APIRequestsByRoute))
	for _, route := range local.APIRequestsByRoute {
		routesByName[route.Route] = route
	}

	for _, result := range results {
		if result.err != "" {
			local.NodeErrors = append(local.NodeErrors, analyticsNodeError{NodeID: result.nodeID, Error: result.err})
			continue
		}
		local.NodesIncluded++
		for _, day := range result.report.Daily {
			if index, ok := dailyByDate[day.Date]; ok {
				local.Daily[index].PageViews += day.PageViews
				local.Daily[index].UniqueVisitors += day.UniqueVisitors
			}
		}
		for _, page := range result.report.Pages {
			pagesByName[page.Page] += page.PageViews
		}
		local.APIRequestsSinceStart += result.report.APIRequestsSinceStart
		local.APIErrorsSinceStart += result.report.APIErrorsSinceStart
		for _, route := range result.report.APIRequestsByRoute {
			entry := routesByName[route.Route]
			entry.Route = route.Route
			entry.Count += route.Count
			entry.Errors += route.Errors
			routesByName[route.Route] = entry
		}
	}

	local.Pages = local.Pages[:0]
	for page, views := range pagesByName {
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
	local.APIRequestsByRoute = local.APIRequestsByRoute[:0]
	for _, route := range routesByName {
		local.APIRequestsByRoute = append(local.APIRequestsByRoute, route)
	}
	sort.Slice(local.APIRequestsByRoute, func(i, j int) bool {
		if local.APIRequestsByRoute[i].Errors != local.APIRequestsByRoute[j].Errors {
			return local.APIRequestsByRoute[i].Errors > local.APIRequestsByRoute[j].Errors
		}
		if local.APIRequestsByRoute[i].Count == local.APIRequestsByRoute[j].Count {
			return local.APIRequestsByRoute[i].Route < local.APIRequestsByRoute[j].Route
		}
		return local.APIRequestsByRoute[i].Count > local.APIRequestsByRoute[j].Count
	})
	sort.Slice(local.NodeErrors, func(i, j int) bool { return local.NodeErrors[i].NodeID < local.NodeErrors[j].NodeID })
	return local
}

func (d Deps) peerAdminAnalytics(ctx context.Context, member membership.Member, days int) analyticsPeerResult {
	result := analyticsPeerResult{nodeID: member.ID}
	token := security.BearerToken(ctx)
	switch {
	case d.Forwarder == nil:
		result.err = "peer requests are disabled on this node"
	case member.APIAddr == "":
		result.err = "node does not advertise an API address"
	case token == "":
		result.err = "no credential to forward"
	default:
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		target := strings.TrimRight(member.APIAddr, "/") + "/v1/admin/analytics?days=" + strconv.Itoa(days)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil) // #nosec G704 -- target is a cluster member's API address from authenticated gossip.
		if err != nil {
			result.err = "could not prepare peer request"
			break
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set(ForwardedByHeader, d.Cluster.LocalNode().ID)
		resp, err := d.Forwarder.Do(req) // #nosec G704 -- the target is a cluster member's gossiped API address.
		if err != nil {
			result.err = "unreachable"
			d.Log.Warn("admin analytics peer request failed", "node", member.ID, "err", err)
			break
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, maxForwardResponse))
		if err != nil {
			result.err = "could not read peer response"
			d.Log.Warn("admin analytics peer response failed", "node", member.ID, "err", err)
			break
		}
		if resp.StatusCode != http.StatusOK {
			result.err = "node answered " + resp.Status
			break
		}
		if err := json.Unmarshal(raw, &result.report); err != nil {
			result.err = "node returned invalid analytics"
			d.Log.Warn("admin analytics peer response was invalid", "node", member.ID, "err", err)
		}
	}
	return result
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
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Errors != routes[j].Errors {
			return routes[i].Errors > routes[j].Errors
		}
		if routes[i].Count == routes[j].Count {
			return routes[i].Route < routes[j].Route
		}
		return routes[i].Count > routes[j].Count
	})
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
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

func analyticsPage(path string) string {
	path, _, _ = strings.Cut(path, "?")
	path, _, _ = strings.Cut(path, "#")
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		return "/"
	}
	switch path {
	case "/about", "/api", "/security", "/terms", "/connect", "/dashboard", "/nodes", "/topology", "/services", "/activity", "/lookup", "/integrations", "/admin", "/admin/analytics", "/contributing", "/docs":
		return path
	default:
		if strings.HasPrefix(path, "/docs/") {
			return "/docs/:slug"
		}
		return "/other"
	}
}
