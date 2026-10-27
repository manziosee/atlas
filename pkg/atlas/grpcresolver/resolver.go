// Package grpcresolver lets gRPC clients dial Atlas services by name:
//
//	grpcresolver.Register(client) // once, at startup
//	conn, err := grpc.NewClient("atlas:///payments?region=eu&tag=v2",
//		grpc.WithTransportCredentials(creds))
//
// The resolver watches the service in Atlas and keeps the connection's
// address list equal to its routable instances (healthy, not draining,
// preferring the region), and selects round_robin load balancing unless the
// dial options choose another policy.
package grpcresolver

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"

	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/serviceconfig"

	"github.com/manziosee/atlas/pkg/atlas"
)

// Scheme is the target scheme this resolver handles.
const Scheme = "atlas"

const roundRobinConfig = `{"loadBalancingConfig":[{"round_robin":{}}]}`

// Register installs a builder for atlas:/// targets in gRPC's global
// resolver registry. Call it once before dialing; use NewBuilder with
// grpc.WithResolvers to scope it to one connection instead.
func Register(client *atlas.Client) {
	resolver.Register(NewBuilder(client))
}

// NewBuilder returns a resolver builder backed by client.
func NewBuilder(client *atlas.Client) resolver.Builder {
	return &builder{client: client}
}

type builder struct{ client *atlas.Client }

func (*builder) Scheme() string { return Scheme }

// Build parses atlas:///<service>?region=<region>&tag=<tag>&tag=<tag>.
func (b *builder) Build(target resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	name := strings.Trim(target.URL.Path, "/")
	if name == "" {
		name = target.URL.Opaque
	}
	if name == "" || strings.Contains(name, "/") {
		return nil, errors.New("grpcresolver: target must be atlas:///<service>")
	}
	query, err := url.ParseQuery(target.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	watcher, err := b.client.WatchInstances(context.Background(), name, atlas.InstanceOptions{
		Region: query.Get("region"), Tags: query["tag"],
	})
	if err != nil {
		return nil, err
	}
	r := &atlasResolver{cc: cc, watcher: watcher, name: name}
	r.serviceConfig = cc.ParseServiceConfig(roundRobinConfig)
	watcher.OnChange(r.update)
	return r, nil
}

type atlasResolver struct {
	cc            resolver.ClientConn
	watcher       *atlas.InstanceWatcher
	name          string
	serviceConfig *serviceconfig.ParseResult

	mu     sync.Mutex
	closed bool
}

func (r *atlasResolver) update(instances []atlas.Service) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if len(instances) == 0 {
		r.cc.ReportError(errors.New("grpcresolver: no routable instances of " + r.name))
		return
	}
	addresses := make([]resolver.Address, 0, len(instances))
	for _, instance := range instances {
		addresses = append(addresses, resolver.Address{Addr: instance.Address})
	}
	_ = r.cc.UpdateState(resolver.State{Addresses: addresses, ServiceConfig: r.serviceConfig})
}

// ResolveNow re-reads the instance list.
func (r *atlasResolver) ResolveNow(resolver.ResolveNowOptions) { r.watcher.Refresh() }

// Close stops watching the service.
func (r *atlasResolver) Close() {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	r.watcher.Close()
}
