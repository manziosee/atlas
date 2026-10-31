// Package routing selects a healthy service instance from replicated registry
// records, preferring local-region instances and supporting stable key affinity.
package routing

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/manziosee/atlas/internal/storage"
)

var ErrNoInstance = errors.New("routing: no healthy or unknown service instances")

const virtualNodes = 128

type point struct {
	hash uint64
	id   string
}

// Selector is safe for concurrent use. Requests with a key use a consistent
// hash ring; requests without one rotate through the sorted candidate set.
type Selector struct {
	next atomic.Uint64
}

func NewSelector() *Selector { return &Selector{} }

// StalePolicy decides how to treat instances whose owning node is not alive
// (suspect, dead, departed or unknown). Their records may outlive the process
// until the owner returns or the orphan reaper removes them.
type StalePolicy string

const (
	// StaleInclude treats stale instances like any other.
	StaleInclude StalePolicy = "include"
	// StalePreferLive uses stale instances only when no instance has a live
	// owner. This is the default.
	StalePreferLive StalePolicy = "prefer-live"
	// StaleExclude never returns stale instances.
	StaleExclude StalePolicy = "exclude"
)

// LatencyMode decides whether measured node latency narrows the candidates.
type LatencyMode string

const (
	// LatencyNearest keeps the candidates on the nearest measured nodes,
	// within Policy.Tolerance of the best measurement.
	LatencyNearest LatencyMode = "nearest"
	// LatencyOff ignores latency.
	LatencyOff LatencyMode = "off"
)

// Policy configures instance selection. The zero value is StalePreferLive
// with LatencyNearest and no tolerance.
type Policy struct {
	Stale     StalePolicy
	Latency   LatencyMode
	Tolerance time.Duration
}

// ParsePolicy validates textual settings; empty values keep the defaults.
func ParsePolicy(stale, latency string, tolerance time.Duration) (Policy, error) {
	policy := Policy{Stale: StalePolicy(stale), Latency: LatencyMode(latency), Tolerance: tolerance}
	if policy.Stale == "" {
		policy.Stale = StalePreferLive
	}
	if policy.Latency == "" {
		policy.Latency = LatencyNearest
	}
	switch policy.Stale {
	case StaleInclude, StalePreferLive, StaleExclude:
	default:
		return Policy{}, errors.New("routing: stale policy must be include, prefer-live or exclude")
	}
	switch policy.Latency {
	case LatencyNearest, LatencyOff:
	default:
		return Policy{}, errors.New("routing: latency mode must be nearest or off")
	}
	if tolerance < 0 || tolerance > time.Minute {
		return Policy{}, errors.New("routing: latency tolerance must be between 0 and 1m")
	}
	return policy, nil
}

// Options describes one selection.
type Options struct {
	Region string
	Key    string
	// Latency returns the measured round-trip time to a node; zero means
	// unmeasured. Nil disables latency ranking.
	Latency func(nodeID string) time.Duration
	// OwnerAlive reports whether a node is alive. Nil treats every owner as
	// alive.
	OwnerAlive func(nodeID string) bool
	Policy     Policy
}

// Select chooses a healthy instance, preferring the requested region. Unknown
// health is used only when no healthy candidate exists; unhealthy and draining
// records are never returned.
func (s *Selector) Select(services []storage.Service, region, key string) (storage.Service, error) {
	return s.SelectWithLatency(services, region, key, nil)
}

// SelectWithLatency ranks candidates by EWMA probe RTT after applying region
// affinity. Candidates with unknown RTT remain eligible only when no candidate
// has a measurement. Consistent hashing then provides key affinity among the
// nearest candidates.
func (s *Selector) SelectWithLatency(services []storage.Service, region, key string, latency func(nodeID string) time.Duration) (storage.Service, error) {
	return s.Choose(services, Options{Region: region, Key: key, Latency: latency, Policy: Policy{Stale: StaleInclude, Latency: LatencyNearest}})
}

// Choose applies, in order: routability (never draining or unhealthy), the
// stale policy, the health tier (healthy, else unknown), region affinity, the
// latency policy, and finally consistent hashing on Key or round-robin.
func (s *Selector) Choose(services []storage.Service, opts Options) (storage.Service, error) {
	policy := opts.Policy
	if policy.Stale == "" {
		policy.Stale = StalePreferLive
	}
	routable := make([]storage.Service, 0, len(services))
	for _, service := range services {
		if service.Routable() {
			routable = append(routable, service)
		}
	}
	if opts.OwnerAlive != nil && policy.Stale != StaleInclude {
		live := make([]storage.Service, 0, len(routable))
		for _, service := range routable {
			if opts.OwnerAlive(service.NodeID) {
				live = append(live, service)
			}
		}
		if len(live) > 0 || policy.Stale == StaleExclude {
			routable = live
		}
	}
	available := make([]storage.Service, 0, len(routable))
	for _, service := range routable {
		if service.Health == storage.HealthHealthy {
			available = append(available, service)
		}
	}
	if len(available) == 0 {
		for _, service := range routable {
			if service.Health == storage.HealthUnknown {
				available = append(available, service)
			}
		}
	}
	if len(available) == 0 {
		return storage.Service{}, ErrNoInstance
	}
	if opts.Region != "" {
		local := make([]storage.Service, 0, len(available))
		for _, service := range available {
			if service.Region == opts.Region {
				local = append(local, service)
			}
		}
		if len(local) > 0 {
			available = local
		}
	}
	if opts.Latency != nil && policy.Latency != LatencyOff {
		available = nearest(available, opts.Latency, policy.Tolerance)
	}
	return s.pick(available, opts.Key)
}

// nearest keeps candidates within tolerance of the best measured latency.
// Unmeasured candidates are kept only when nothing is measured.
func nearest(available []storage.Service, latency func(string) time.Duration, tolerance time.Duration) []storage.Service {
	var best time.Duration
	for _, service := range available {
		observed := latency(service.NodeID)
		if observed > 0 && (best == 0 || observed < best) {
			best = observed
		}
	}
	if best == 0 {
		return available
	}
	kept := make([]storage.Service, 0, len(available))
	for _, service := range available {
		if observed := latency(service.NodeID); observed > 0 && observed <= best+tolerance {
			kept = append(kept, service)
		}
	}
	return kept
}

func (s *Selector) pick(available []storage.Service, key string) (storage.Service, error) {
	sort.Slice(available, func(i, j int) bool { return available[i].ID < available[j].ID })
	if key == "" {
		index := (s.next.Add(1) - 1) % uint64(len(available)) // #nosec G115 -- len is never negative.
		return available[index], nil
	}

	ring := make([]point, 0, len(available)*virtualNodes)
	for _, service := range available {
		for replica := range virtualNodes {
			ring = append(ring, point{hash: hash64(service.ID + "#" + strconv.Itoa(replica)), id: service.ID})
		}
	}
	sort.Slice(ring, func(i, j int) bool {
		if ring[i].hash == ring[j].hash {
			return ring[i].id < ring[j].id
		}
		return ring[i].hash < ring[j].hash
	})
	keyHash := hash64(key)
	index := sort.Search(len(ring), func(i int) bool { return ring[i].hash >= keyHash })
	if index == len(ring) {
		index = 0
	}
	for _, service := range available {
		if service.ID == ring[index].id {
			return service, nil
		}
	}
	return storage.Service{}, ErrNoInstance
}

func hash64(value string) uint64 {
	sum := sha256.Sum256([]byte(value))
	return binary.BigEndian.Uint64(sum[:8])
}
