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
	available := make([]storage.Service, 0, len(services))
	for _, service := range services {
		if service.Health == storage.HealthHealthy && service.Routable() {
			available = append(available, service)
		}
	}
	if len(available) == 0 {
		for _, service := range services {
			if service.Health == storage.HealthUnknown && service.Routable() {
				available = append(available, service)
			}
		}
	}
	if len(available) == 0 {
		return storage.Service{}, ErrNoInstance
	}
	if region != "" {
		local := make([]storage.Service, 0, len(available))
		for _, service := range available {
			if service.Region == region {
				local = append(local, service)
			}
		}
		if len(local) > 0 {
			available = local
		}
	}
	if latency != nil {
		var best time.Duration
		for _, service := range available {
			observed := latency(service.NodeID)
			if observed > 0 && (best == 0 || observed < best) {
				best = observed
			}
		}
		if best > 0 {
			nearest := make([]storage.Service, 0, len(available))
			for _, service := range available {
				if latency(service.NodeID) == best {
					nearest = append(nearest, service)
				}
			}
			available = nearest
		}
	}
	sort.Slice(available, func(i, j int) bool { return available[i].ID < available[j].ID })
	if key == "" {
		index := (s.next.Add(1) - 1) % uint64(len(available))
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
