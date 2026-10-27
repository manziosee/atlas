package api

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	atlasv1 "github.com/manziosee/atlas/api/v1"
	"github.com/manziosee/atlas/internal/httperr"
	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/security"
	"github.com/manziosee/atlas/internal/storage"
)

// pairCluster is one node's view of a two-node cluster. HTTP handlers read
// it on server goroutines while tests change the peer, so it is locked.
type pairCluster struct {
	mu         sync.Mutex
	self, peer membership.Member
}

func (c *pairCluster) LocalNode() membership.Member {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.self
}

func (c *pairCluster) Members() []membership.Member {
	c.mu.Lock()
	defer c.mu.Unlock()
	return []membership.Member{c.self, c.peer}
}

func (c *pairCluster) setPeerAPIAddr(addr string) {
	c.mu.Lock()
	c.peer.APIAddr = addr
	c.mu.Unlock()
}

type node struct {
	store   *storage.Store
	cluster *pairCluster
	deps    Deps
	server  *httptest.Server
}

// newPair starts nodes n1 and n2 that know each other's API addresses.
func newPair(t *testing.T) (*node, *node) {
	t.Helper()
	expiry := time.Now().Add(time.Hour)
	auth, err := security.NewCredentialAuthenticator([]security.Credential{
		{Token: readToken, Scopes: []security.Scope{security.ScopeRead}, ExpiresAt: expiry},
		{Token: writeToken, Scopes: []security.Scope{security.ScopeRead, security.ScopeWrite}, ExpiresAt: expiry},
	})
	if err != nil {
		t.Fatal(err)
	}
	nodes := make([]*node, 2)
	for i, id := range []string{"n1", "n2"} {
		store, err := storage.Open(context.Background(), filepath.Join(t.TempDir(), id+".db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		n := &node{store: store, cluster: &pairCluster{self: membership.Member{ID: id, Addr: "10.0.0.1:7946", State: membership.StateAlive}}}
		n.deps = Deps{
			Cluster: n.cluster, Registry: store, Auth: auth,
			RateLimiter: security.NewRateLimiter(1000, 1000),
			Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
			Forwarder:   &http.Client{Timeout: 5 * time.Second},
		}
		n.server = httptest.NewServer(NewHandler(n.deps))
		t.Cleanup(n.server.Close)
		n.cluster.self.APIAddr = n.server.URL
		nodes[i] = n
	}
	nodes[0].cluster.peer, nodes[1].cluster.peer = nodes[1].cluster.self, nodes[0].cluster.self
	return nodes[0], nodes[1]
}

// ownedBy registers a service on owner and replicates it to replica, as
// gossip would.
func ownedBy(t *testing.T, owner, replica *node, service storage.Service) storage.Service {
	t.Helper()
	service.NodeID = owner.cluster.self.ID
	stored, err := owner.store.Register(context.Background(), service)
	if err != nil {
		t.Fatal(err)
	}
	if err := replica.store.Merge(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	return stored
}

// reply is the part of a response the tests inspect.
type reply struct {
	StatusCode int
	Header     http.Header
}

func call(t *testing.T, n *node, method, path, body string, headers ...string) (reply, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, n.server.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+writeToken)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return reply{StatusCode: resp.StatusCode, Header: resp.Header}, string(raw)
}

func TestWritesThroughAnyNodeReachTheOwner(t *testing.T) {
	n1, n2 := newPair(t)
	ctx := context.Background()
	ownedBy(t, n2, n1, storage.Service{ID: "pay-1", Name: "payments", Address: "10.1.0.1:80", TTLSeconds: 30})

	resp, body := call(t, n1, http.MethodPatch, "/v1/services/pay-1", `{"version":"2.0","tags":["blue"]}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"node_id":"n2"`) || !strings.Contains(body, `"version":"2.0"`) {
		t.Fatalf("PATCH through n1 = %d %s", resp.StatusCode, body)
	}
	if got, _ := n2.store.Get(ctx, "pay-1"); got.Version != "2.0" || len(got.Tags) != 1 {
		t.Fatalf("owner record after forwarded PATCH = %+v", got)
	}
	if resp.Header.Get("ETag") != `"2"` {
		t.Fatalf("forwarded PATCH ETag = %q", resp.Header.Get("ETag"))
	}

	if resp, body := call(t, n1, http.MethodPatch, "/v1/services/pay-1", `{"version":"3.0"}`, "If-Match", `"1"`); resp.StatusCode != http.StatusPreconditionFailed || !strings.Contains(body, httperr.CodePreconditionFailed) {
		t.Fatalf("stale forwarded PATCH = %d %s", resp.StatusCode, body)
	}
	if resp, body := call(t, n1, http.MethodPatch, "/v1/services/pay-1", `{"address":"bad"}`); resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, httperr.CodeValidation) {
		t.Fatalf("invalid forwarded PATCH = %d %s", resp.StatusCode, body)
	}

	if resp, body := call(t, n1, http.MethodPost, "/v1/services/pay-1/heartbeat", ""); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"health":"healthy"`) {
		t.Fatalf("forwarded heartbeat = %d %s", resp.StatusCode, body)
	}
	if resp, body := call(t, n1, http.MethodPut, "/v1/services/pay-1", `{"name":"payments","address":"10.1.0.1:80","ttl_seconds":30,"version":"4.0"}`); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"version":"4.0"`) {
		t.Fatalf("forwarded PUT = %d %s", resp.StatusCode, body)
	}
	if resp, body := call(t, n1, http.MethodPost, "/v1/services", `{"id":"pay-1","name":"payments","address":"10.1.0.1:80","ttl_seconds":30,"version":"4.0"}`); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"node_id":"n2"`) {
		t.Fatalf("forwarded re-registration = %d %s", resp.StatusCode, body)
	}

	if resp, body := call(t, n1, http.MethodDelete, "/v1/services/pay-1", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("forwarded DELETE = %d %s", resp.StatusCode, body)
	}
	if _, err := n2.store.Get(ctx, "pay-1"); err == nil {
		t.Fatal("owner still has the service after a forwarded DELETE")
	}
}

func TestForwardingFallsBackWithoutLoops(t *testing.T) {
	n1, n2 := newPair(t)
	ownedBy(t, n2, n1, storage.Service{ID: "cart-1", Name: "cart", Address: "10.1.0.2:80"})

	// A request that was already forwarded is never forwarded again.
	if resp, body := call(t, n1, http.MethodPatch, "/v1/services/cart-1", `{"version":"x"}`, ForwardedByHeader, "n9"); resp.StatusCode != http.StatusConflict || !strings.Contains(body, httperr.CodeNotOwner) {
		t.Fatalf("pre-forwarded request = %d %s", resp.StatusCode, body)
	}

	// An owner that is alive in gossip but not answering is reported as such.
	n1.cluster.setPeerAPIAddr("http://" + closedAddr(t))
	if resp, body := call(t, n1, http.MethodPatch, "/v1/services/cart-1", `{"version":"x"}`); resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, httperr.CodeOwnerUnreachable) {
		t.Fatalf("unreachable owner = %d %s", resp.StatusCode, body)
	}

	// Without an API address (an older peer) the client sees not_owner.
	n1.cluster.setPeerAPIAddr("")
	if resp, body := call(t, n1, http.MethodPatch, "/v1/services/cart-1", `{"version":"x"}`); resp.StatusCode != http.StatusConflict || !strings.Contains(body, `"owner_node":"n2"`) {
		t.Fatalf("owner without API address = %d %s", resp.StatusCode, body)
	}
}

func closedAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

func TestTypedGRPCUpdateIsForwarded(t *testing.T) {
	n1, n2 := newPair(t)
	ownedBy(t, n2, n1, storage.Service{ID: "grpc-1", Name: "grpc", Address: "10.1.0.3:80"})

	server := NewGRPCServer(n1.deps, nil)
	listener := bufconn.Listen(1 << 20)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := atlasv1.NewRegistryServiceClient(conn)
	ctx := withToken(context.Background(), writeToken)

	updated, err := client.UpdateService(ctx, &atlasv1.UpdateServiceRequest{
		Id: "grpc-1", Spec: &atlasv1.ServiceSpec{Draining: true}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"draining"}},
	})
	if err != nil || !updated.GetDraining() || updated.GetNodeId() != "n2" {
		t.Fatalf("forwarded gRPC drain = %v %v", updated, err)
	}
	if got, _ := n2.store.Get(context.Background(), "grpc-1"); !got.Draining {
		t.Fatalf("owner record after forwarded gRPC update = %+v", got)
	}
}
