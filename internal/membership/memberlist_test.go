package membership

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/manziosee/atlas/internal/transport"
)

// fakeNet is an in-memory lossless network with partition support.
type fakeNet struct {
	mu    sync.Mutex
	nodes map[string]*fakeTransport
	down  map[string]bool
}

type fakeTransport struct {
	net  *fakeNet
	addr string
	ch   chan transport.Packet
}

func newFakeNet() *fakeNet {
	return &fakeNet{nodes: map[string]*fakeTransport{}, down: map[string]bool{}}
}

func (n *fakeNet) attach(addr string) *fakeTransport {
	n.mu.Lock()
	defer n.mu.Unlock()
	t := &fakeTransport{net: n, addr: addr, ch: make(chan transport.Packet, 1024)}
	n.nodes[addr] = t
	return t
}

func (n *fakeNet) partition(addr string) {
	n.mu.Lock()
	n.down[addr] = true
	n.mu.Unlock()
}

func (t *fakeTransport) Packets() <-chan transport.Packet { return t.ch }

func (t *fakeTransport) Send(addr string, p []byte) error {
	t.net.mu.Lock()
	dst, drop := t.net.nodes[addr], t.net.down[addr] || t.net.down[t.addr]
	t.net.mu.Unlock()
	if dst == nil || drop {
		return nil
	}
	select {
	case dst.ch <- transport.Packet{Payload: bytes.Clone(p), ReceivedAt: time.Now()}:
	default:
	}
	return nil
}

func testConfig(id, addr string) Config {
	return Config{
		NodeID: id, Region: "test", AdvertiseAddr: addr,
		ProbeInterval: 50 * time.Millisecond, ProbeTimeout: 20 * time.Millisecond,
		SuspicionTimeout: 200 * time.Millisecond, GossipInterval: 20 * time.Millisecond,
		Fanout: 3, ReapAfter: time.Minute, MaxClockSkew: 5 * time.Second,
	}
}

func startCluster(t *testing.T, n int) (*fakeNet, []*Memberlist) {
	t.Helper()
	fn := newFakeNet()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })

	nodes := make([]*Memberlist, n)
	for i := range n {
		addr := fmt.Sprintf("127.0.0.1:%d", 7000+i)
		nodes[i] = New(testConfig(fmt.Sprintf("node-%d", i), addr), fn.attach(addr), log)
		wg.Go(func() { nodes[i].Run(ctx) })
	}
	for _, nd := range nodes[1:] {
		if _, err := nd.Join(ctx, []string{"127.0.0.1:7000"}); err != nil {
			t.Fatal(err)
		}
	}
	return fn, nodes
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(msg)
}

func countState(m *Memberlist, s State) int {
	n := 0
	for _, mb := range m.Members() {
		if mb.State == s {
			n++
		}
	}
	return n
}

func TestClusterConverges(t *testing.T) {
	const size = 8
	_, nodes := startCluster(t, size)
	waitFor(t, 3*time.Second, func() bool {
		for _, n := range nodes {
			if countState(n, StateAlive) != size {
				return false
			}
		}
		return true
	}, "cluster did not converge")
}

func TestFailureDetection(t *testing.T) {
	const size = 5
	fn, nodes := startCluster(t, size)
	waitFor(t, 3*time.Second, func() bool { return countState(nodes[1], StateAlive) == size }, "no convergence")

	fn.partition(nodes[size-1].LocalNode().Addr)
	waitFor(t, 3*time.Second, func() bool {
		for _, n := range nodes[:size-1] {
			if countState(n, StateDead) != 1 {
				return false
			}
		}
		return true
	}, "partitioned node was not declared dead by all peers")
}

func TestMergePrecedence(t *testing.T) {
	m := New(testConfig("self", "127.0.0.1:1"), newFakeNet().attach("x"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	peer := Member{ID: "p", Addr: "127.0.0.1:2", State: StateAlive, Incarnation: 5}
	get := func() Member { return *m.members["p"] }

	m.merge(peer)
	m.merge(Member{ID: "p", Addr: peer.Addr, State: StateSuspect, Incarnation: 5})
	if get().State != StateSuspect {
		t.Fatal("suspect at same incarnation must override alive")
	}
	m.merge(Member{ID: "p", Addr: peer.Addr, State: StateAlive, Incarnation: 5})
	if get().State != StateSuspect {
		t.Fatal("alive at same incarnation must not override suspect")
	}
	m.merge(Member{ID: "p", Addr: peer.Addr, State: StateAlive, Incarnation: 6})
	if get().State != StateAlive {
		t.Fatal("higher incarnation must override (refutation)")
	}
	m.merge(Member{ID: "p", Addr: peer.Addr, State: StateDead, Incarnation: 4})
	if get().State != StateAlive {
		t.Fatal("stale incarnation must be ignored")
	}
}

func TestSelfRefutation(t *testing.T) {
	m := New(testConfig("self", "127.0.0.1:1"), newFakeNet().attach("x"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	inc := m.LocalNode().Incarnation
	m.merge(Member{ID: "self", Addr: "127.0.0.1:1", State: StateSuspect, Incarnation: inc})
	if got := m.LocalNode().Incarnation; got != inc+1 {
		t.Fatalf("incarnation = %d, want %d", got, inc+1)
	}
}
