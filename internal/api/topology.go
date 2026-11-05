package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/manziosee/atlas/internal/httperr"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/version"
)

// Location is a node's position for maps (ATLAS_LOCATION).
type Location struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// PeerLatency is one node's view of a peer: its state and the smoothed
// round-trip time of successful probes, when measured.
type PeerLatency struct {
	ID     string           `json:"id"`
	Region string           `json:"region,omitempty"`
	State  membership.State `json:"state"`
	RTTMs  *float64         `json:"rtt_ms,omitempty"`
}

// NodeTopology is what one node sees: itself and its latency to each peer.
type NodeTopology struct {
	Node     membership.Member `json:"node"`
	Location *Location         `json:"location,omitempty"`
	Version  string            `json:"version"`
	Peers    []PeerLatency     `json:"peers"`
	At       time.Time         `json:"at"`
}

// ClusterNode is one entry of a cluster-wide topology: the node's own view,
// or why it could not be read.
type ClusterNode struct {
	ID    string        `json:"id"`
	View  *NodeTopology `json:"view,omitempty"`
	Error string        `json:"error,omitempty"`
}

// maxTopologyFanout bounds how many peers one cluster-wide request asks.
const maxTopologyFanout = 128

func (d Deps) localTopology() NodeTopology {
	self := d.Cluster.LocalNode()
	view := NodeTopology{Node: self, Location: d.Location, Version: version.Version, At: time.Now().UTC(), Peers: []PeerLatency{}}
	reader, _ := d.Cluster.(interface{ Latency(string) time.Duration })
	for _, member := range d.Cluster.Members() {
		if member.ID == self.ID {
			continue
		}
		peer := PeerLatency{ID: member.ID, Region: member.Region, State: member.State}
		if reader != nil {
			if rtt := reader.Latency(member.ID); rtt > 0 {
				ms := float64(rtt.Microseconds()) / 1000
				peer.RTTMs = &ms
			}
		}
		view.Peers = append(view.Peers, peer)
	}
	sort.Slice(view.Peers, func(i, j int) bool { return view.Peers[i].ID < view.Peers[j].ID })
	return view
}

// topology serves GET /v1/topology. With scope=cluster it also asks every
// live peer for its own view, using the caller's credential, so the console
// can draw the full latency matrix from one connection.
func (d Deps) topology(w http.ResponseWriter, r *http.Request) {
	switch scope := r.URL.Query().Get("scope"); scope {
	case "", "node":
		writeJSON(w, http.StatusOK, d.localTopology())
	case "cluster":
		if isForwarded(r.Context()) {
			// A peer's fan-out request: answer for this node only.
			writeJSON(w, http.StatusOK, d.localTopology())
			return
		}
		nodes, truncated := d.clusterTopology(r.Context())
		writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes, "truncated": truncated, "at": time.Now().UTC()})
	default:
		httperr.Write(w, http.StatusBadRequest, httperr.CodeValidation, "scope must be node or cluster")
	}
}

func (d Deps) clusterTopology(ctx context.Context) ([]ClusterNode, bool) {
	self := d.Cluster.LocalNode()
	local := d.localTopology()
	nodes := []ClusterNode{{ID: self.ID, View: &local}}
	var targets []membership.Member
	for _, member := range d.Cluster.Members() {
		if member.ID == self.ID {
			continue
		}
		if member.State != membership.StateAlive && member.State != membership.StateSuspect {
			nodes = append(nodes, ClusterNode{ID: member.ID, Error: "node is " + member.State.String()})
			continue
		}
		targets = append(targets, member)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
	truncated := len(targets) > maxTopologyFanout
	if truncated {
		for _, member := range targets[maxTopologyFanout:] {
			nodes = append(nodes, ClusterNode{ID: member.ID, Error: "not queried: cluster larger than the fan-out limit"})
		}
		targets = targets[:maxTopologyFanout]
	}

	results := make([]ClusterNode, len(targets))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for i, member := range targets {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = d.peerTopology(ctx, member)
		})
	}
	wg.Wait()
	nodes = append(nodes, results...)
	sort.SliceStable(nodes[1:], func(i, j int) bool { return nodes[1+i].ID < nodes[1+j].ID })
	return nodes, truncated
}

func (d Deps) peerTopology(ctx context.Context, member membership.Member) ClusterNode {
	result := ClusterNode{ID: member.ID}
	token := security.BearerToken(ctx)
	switch {
	case d.Forwarder == nil:
		result.Error = "peer requests are disabled on this node"
		return result
	case member.APIAddr == "":
		result.Error = "node does not advertise an API address (older version)"
		return result
	case token == "":
		result.Error = "no credential to forward"
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// The target is a cluster member's API address from authenticated gossip.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(member.APIAddr, "/")+"/v1/topology", nil) // #nosec G704 -- see above.
	if err != nil {
		result.Error = err.Error()
		return result
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(ForwardedByHeader, d.Cluster.LocalNode().ID)
	resp, err := d.Forwarder.Do(req) // #nosec G704 -- the target is a cluster member's gossiped API address.
	if err != nil {
		result.Error = "unreachable: " + err.Error()
		return result
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxForwardResponse))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if resp.StatusCode != http.StatusOK {
		var body httperr.Body
		if json.Unmarshal(raw, &body) == nil && body.Code == httperr.CodeRouteNotFound {
			result.Error = "node runs a version without the topology API"
		} else {
			result.Error = "node answered " + resp.Status
		}
		return result
	}
	var view NodeTopology
	if err := json.Unmarshal(raw, &view); err != nil {
		result.Error = "invalid answer: " + err.Error()
		return result
	}
	result.View = &view
	return result
}
