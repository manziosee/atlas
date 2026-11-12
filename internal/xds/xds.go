// Package xds serves the Atlas registry to Envoy over the REST variant of
// the xDS protocol (v3): Cluster Discovery (CDS) turns every service into an
// EDS cluster, and Endpoint Discovery (EDS) lists its instances.
//
// Envoy polls POST /v3/discovery:clusters and /v3/discovery:endpoints with a
// JSON DiscoveryRequest and receives a JSON DiscoveryResponse. Each instance
// becomes an endpoint whose locality is its region, whose load-balancing
// weight is its traffic weight and whose health follows Atlas: unhealthy and
// draining instances are marked so, and instances of nodes that are not alive
// are DEGRADED, which Envoy uses only when too few healthy endpoints remain.
// Version and metadata are published under the envoy.lb namespace for subset
// load balancing.
package xds

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/manziosee/atlas/internal/storage"
)

// Resource type URLs.
const (
	ClusterType    = "type.googleapis.com/envoy.config.cluster.v3.Cluster"
	AssignmentType = "type.googleapis.com/envoy.config.endpoint.v3.ClusterLoadAssignment"
)

// Registry lists active services.
type Registry interface {
	List(context.Context, storage.Filter) ([]storage.Service, error)
}

// Config describes how Envoy reaches this server and how generated clusters
// behave.
type Config struct {
	// XDSCluster is the name of the static cluster in Envoy's bootstrap that
	// points at this listener; generated clusters fetch endpoints through it.
	XDSCluster string
	// RefreshDelay is how often Envoy polls for endpoints (default 1s).
	RefreshDelay time.Duration
	// ConnectTimeout for generated clusters (default 1s).
	ConnectTimeout time.Duration
	// OwnerAlive reports whether a node is alive; nil treats all as alive.
	OwnerAlive func(nodeID string) bool
	// Resolve looks up a hostname address; nil uses the system resolver.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	Log     *slog.Logger
}

// Handler serves CDS and EDS for registry.
func Handler(registry Registry, cfg Config) http.Handler {
	if cfg.XDSCluster == "" {
		cfg.XDSCluster = "atlas_xds"
	}
	if cfg.RefreshDelay <= 0 {
		cfg.RefreshDelay = time.Second
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = time.Second
	}
	if cfg.Resolve == nil {
		cfg.Resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &server{registry: registry, cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/discovery:clusters", s.clusters)
	mux.HandleFunc("POST /v3/discovery:endpoints", s.endpoints)
	return mux
}

type server struct {
	registry Registry
	cfg      Config
}

// DiscoveryRequest is the subset of envoy.service.discovery.v3.DiscoveryRequest
// this server reads. Envoy's JSON uses either proto field names or lowerCamelCase.
type DiscoveryRequest struct {
	VersionInfo   string   `json:"version_info"`
	ResourceNames []string `json:"resource_names"`
	TypeURL       string   `json:"type_url"`
}

// UnmarshalJSON accepts both proto field names and lowerCamelCase, as Envoy
// may send either.
func (r *DiscoveryRequest) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	pick := func(names ...string) json.RawMessage {
		for _, name := range names {
			if value, ok := raw[name]; ok {
				return value
			}
		}
		return nil
	}
	for _, field := range []struct {
		dst   any
		names []string
	}{
		{&r.VersionInfo, []string{"version_info", "versionInfo"}},
		{&r.ResourceNames, []string{"resource_names", "resourceNames"}},
		{&r.TypeURL, []string{"type_url", "typeUrl"}},
	} {
		if value := pick(field.names...); value != nil {
			if err := json.Unmarshal(value, field.dst); err != nil {
				return err
			}
		}
	}
	return nil
}

// DiscoveryResponse mirrors envoy.service.discovery.v3.DiscoveryResponse.
type DiscoveryResponse struct {
	VersionInfo string           `json:"version_info"`
	Resources   []map[string]any `json:"resources"`
	TypeURL     string           `json:"type_url"`
	Nonce       string           `json:"nonce"`
}

func (s *server) clusters(w http.ResponseWriter, r *http.Request) {
	request, ok := readRequest(w, r)
	if !ok {
		return
	}
	services, err := s.registry.List(r.Context(), storage.Filter{})
	if err != nil {
		s.fail(w, err)
		return
	}
	var names []string
	for _, service := range services {
		if !slices.Contains(names, service.Name) {
			names = append(names, service.Name)
		}
	}
	slices.Sort(names)
	resources := make([]map[string]any, 0, len(names))
	for _, name := range names {
		if len(request.ResourceNames) > 0 && !slices.Contains(request.ResourceNames, name) {
			continue
		}
		resources = append(resources, s.cluster(name))
	}
	s.respond(w, ClusterType, resources)
}

func (s *server) cluster(name string) map[string]any {
	return map[string]any{
		"@type":           ClusterType,
		"name":            name,
		"type":            "EDS",
		"connect_timeout": duration(s.cfg.ConnectTimeout),
		"lb_policy":       "ROUND_ROBIN",
		"eds_cluster_config": map[string]any{
			"service_name": name,
			"eds_config": map[string]any{
				"resource_api_version": "V3",
				"api_config_source": map[string]any{
					"api_type":              "REST",
					"transport_api_version": "V3",
					"cluster_names":         []string{s.cfg.XDSCluster},
					"refresh_delay":         duration(s.cfg.RefreshDelay),
				},
			},
		},
	}
}

func (s *server) endpoints(w http.ResponseWriter, r *http.Request) {
	request, ok := readRequest(w, r)
	if !ok {
		return
	}
	services, err := s.registry.List(r.Context(), storage.Filter{})
	if err != nil {
		s.fail(w, err)
		return
	}
	byName := map[string][]storage.Service{}
	for _, service := range services {
		byName[service.Name] = append(byName[service.Name], service)
	}
	names := request.ResourceNames
	if len(names) == 0 {
		for name := range byName {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	resources := make([]map[string]any, 0, len(names))
	for _, name := range names {
		resources = append(resources, s.assignment(r.Context(), name, byName[name]))
	}
	s.respond(w, AssignmentType, resources)
}

// assignment builds a ClusterLoadAssignment. A requested name with no
// instances gets an empty assignment, so Envoy clears stale endpoints.
func (s *server) assignment(ctx context.Context, name string, services []storage.Service) map[string]any {
	byRegion := map[string][]map[string]any{}
	for _, service := range services {
		host, portText, err := net.SplitHostPort(service.Address)
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			continue
		}
		addrs, hostname := s.addresses(ctx, host)
		for _, addr := range addrs {
			endpoint := map[string]any{
				"address": map[string]any{"socket_address": map[string]any{"address": addr.String(), "port_value": port}},
			}
			if hostname != "" {
				endpoint["hostname"] = hostname
			}
			byRegion[service.Region] = append(byRegion[service.Region], map[string]any{
				"endpoint":              endpoint,
				"health_status":         s.health(service),
				"load_balancing_weight": max(service.TrafficWeight, 1),
				"metadata":              map[string]any{"filter_metadata": map[string]any{"envoy.lb": lbMetadata(service)}},
			})
		}
	}
	regions := make([]string, 0, len(byRegion))
	for region := range byRegion {
		regions = append(regions, region)
	}
	slices.Sort(regions)
	localities := make([]map[string]any, 0, len(regions))
	for _, region := range regions {
		localities = append(localities, map[string]any{
			"locality":     map[string]any{"region": region},
			"lb_endpoints": byRegion[region],
		})
	}
	return map[string]any{"@type": AssignmentType, "cluster_name": name, "endpoints": localities}
}

// addresses returns the IPs for host. Envoy EDS endpoints must be IPs, so a
// hostname is resolved here and also returned to be set as the endpoint's
// hostname (used for TLS SNI and host rewriting).
func (s *server) addresses(ctx context.Context, host string) ([]netip.Addr, string) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr.Unmap()}, ""
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	addrs, err := s.cfg.Resolve(ctx, host)
	if err != nil {
		s.cfg.Log.Warn("xds could not resolve service host", "host", host, "err", err)
		return nil, ""
	}
	for i := range addrs {
		addrs[i] = addrs[i].Unmap()
	}
	slices.SortFunc(addrs, func(a, b netip.Addr) int { return a.Compare(b) })
	return slices.Compact(addrs), host
}

func (s *server) health(service storage.Service) string {
	switch {
	case service.Draining:
		return "DRAINING"
	case service.Health == storage.HealthUnhealthy:
		return "UNHEALTHY"
	case s.cfg.OwnerAlive != nil && !s.cfg.OwnerAlive(service.NodeID):
		return "DEGRADED"
	case service.Health == storage.HealthHealthy:
		return "HEALTHY"
	default:
		return "UNKNOWN"
	}
}

// lbMetadata exposes the instance to Envoy subset load balancing. Metadata
// keys come first so atlas.* keys are never overwritten by them.
func lbMetadata(service storage.Service) map[string]any {
	out := make(map[string]any, len(service.Metadata)+3)
	for key, value := range service.Metadata {
		out[key] = value
	}
	out["atlas.id"] = service.ID
	out["atlas.node"] = service.NodeID
	if service.Version != "" {
		out["version"] = service.Version
	}
	return out
}

func (s *server) respond(w http.ResponseWriter, typeURL string, resources []map[string]any) {
	encoded, err := json.Marshal(resources)
	if err != nil {
		s.fail(w, err)
		return
	}
	sum := sha256.Sum256(encoded)
	version := hex.EncodeToString(sum[:8])
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(DiscoveryResponse{VersionInfo: version, Resources: resources, TypeURL: typeURL, Nonce: version})
}

func (s *server) fail(w http.ResponseWriter, err error) {
	s.cfg.Log.Error("xds request failed", "err", err)
	http.Error(w, "registry unavailable", http.StatusServiceUnavailable)
}

func readRequest(w http.ResponseWriter, r *http.Request) (DiscoveryRequest, bool) {
	var request DiscoveryRequest
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(body).Decode(&request); err != nil && err != io.EOF {
		http.Error(w, "invalid DiscoveryRequest JSON", http.StatusBadRequest)
		return request, false
	}
	return request, true
}

// duration formats d as a protobuf JSON Duration, such as "1.5s".
func duration(d time.Duration) string {
	return strings.TrimSuffix(strings.TrimRight(strconv.FormatFloat(d.Seconds(), 'f', 9, 64), "0"), ".") + "s"
}
