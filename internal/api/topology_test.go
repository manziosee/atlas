package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/security"
)

// latencyCluster is a membership view with measured probe latencies.
type latencyCluster struct {
	self    membership.Member
	members []membership.Member
	rtt     map[string]time.Duration
}

func (c latencyCluster) LocalNode() membership.Member { return c.self }
func (c latencyCluster) Members() []membership.Member {
	return append([]membership.Member{c.self}, c.members...)
}
func (c latencyCluster) Latency(id string) time.Duration { return c.rtt[id] }

func topologyHandler(cluster Cluster, location *Location) http.Handler {
	return NewHandler(Deps{
		Cluster:     cluster,
		Auth:        security.NewTokenAuthenticator([]string{token}),
		RateLimiter: security.NewRateLimiter(1000, 1000),
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Forwarder:   &http.Client{Timeout: 2 * time.Second},
		Location:    location,
	})
}

func getJSON(t *testing.T, h http.Handler, path string, out any) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if out != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
	}
	return rec.Code
}

func TestTopologyLocalView(t *testing.T) {
	cluster := latencyCluster{
		self: membership.Member{ID: "a", Region: "cdg", State: membership.StateAlive},
		members: []membership.Member{
			{ID: "c", Region: "iad", State: membership.StateSuspect},
			{ID: "b", Region: "cdg", State: membership.StateAlive},
		},
		rtt: map[string]time.Duration{"b": 1500 * time.Microsecond},
	}
	var view NodeTopology
	if code := getJSON(t, topologyHandler(cluster, &Location{Lat: 48.86, Lon: 2.35}), "/v1/topology", &view); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if view.Node.ID != "a" || view.Location == nil || view.Location.Lat != 48.86 || len(view.Peers) != 2 {
		t.Fatalf("view = %+v", view)
	}
	if view.Peers[0].ID != "b" || view.Peers[0].RTTMs == nil || *view.Peers[0].RTTMs != 1.5 {
		t.Fatalf("peer b = %+v", view.Peers[0])
	}
	if view.Peers[1].ID != "c" || view.Peers[1].RTTMs != nil || view.Peers[1].State != membership.StateSuspect {
		t.Fatalf("unmeasured peer c = %+v", view.Peers[1])
	}
	if code := getJSON(t, topologyHandler(cluster, nil), "/v1/topology?scope=galaxy", nil); code != http.StatusBadRequest {
		t.Fatalf("bad scope = %d", code)
	}
}

func TestTopologyClusterFanOut(t *testing.T) {
	// Peer b serves its own view; peer c is gone; peer d is unreachable.
	var forwardedBy string
	peerB := topologyHandler(latencyCluster{
		self:    membership.Member{ID: "b", State: membership.StateAlive},
		members: []membership.Member{{ID: "a", State: membership.StateAlive}},
		rtt:     map[string]time.Duration{"a": 2 * time.Millisecond},
	}, nil)
	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwardedBy = r.Header.Get(ForwardedByHeader)
		peerB.ServeHTTP(w, r)
	}))
	defer serverB.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	a := topologyHandler(latencyCluster{
		self: membership.Member{ID: "a", State: membership.StateAlive},
		members: []membership.Member{
			{ID: "b", State: membership.StateAlive, APIAddr: serverB.URL},
			{ID: "c", State: membership.StateDead, APIAddr: "http://192.0.2.1:1"},
			{ID: "d", State: membership.StateAlive, APIAddr: closed.URL},
			{ID: "e", State: membership.StateAlive},
		},
		rtt: map[string]time.Duration{"b": 3 * time.Millisecond},
	}, nil)

	var body struct {
		Nodes     []ClusterNode `json:"nodes"`
		Truncated bool          `json:"truncated"`
	}
	if code := getJSON(t, a, "/v1/topology?scope=cluster", &body); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	got := map[string]ClusterNode{}
	for _, n := range body.Nodes {
		got[n.ID] = n
	}
	if body.Nodes[0].ID != "a" || got["a"].View == nil || len(body.Nodes) != 5 {
		t.Fatalf("nodes = %+v", body.Nodes)
	}
	if got["b"].View == nil || got["b"].View.Peers[0].ID != "a" || *got["b"].View.Peers[0].RTTMs != 2 || forwardedBy != "a" {
		t.Fatalf("peer b = %+v, forwarded by %q", got["b"], forwardedBy)
	}
	for id, want := range map[string]string{"c": "node is dead", "e": "older version"} {
		if got[id].View != nil || got[id].Error == "" || !contains(got[id].Error, want) {
			t.Errorf("%s = %+v, want error containing %q", id, got[id], want)
		}
	}
	if got["d"].View != nil || !contains(got["d"].Error, "unreachable") {
		t.Errorf("d = %+v", got["d"])
	}

	// A forwarded request never fans out again.
	req := httptest.NewRequest(http.MethodGet, "/v1/topology?scope=cluster", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(ForwardedByHeader, "z")
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	var single NodeTopology
	if err := json.Unmarshal(rec.Body.Bytes(), &single); err != nil || single.Node.ID != "a" {
		t.Fatalf("forwarded cluster request = %d %s", rec.Code, rec.Body.String())
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
