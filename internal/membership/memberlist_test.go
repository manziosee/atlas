package membership

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/manziosee/atlas/internal/storage"
	"github.com/manziosee/atlas/internal/transport"
)

func FuzzMessageValidate(f *testing.F) {
	f.Add(`{"t":"ping","from":{"id":"node","addr":"127.0.0.1:7946","region":"test","state":"alive","incarnation":1},"ts":1}`)
	f.Add(`{"t":"ping-req","from":{"id":"node","addr":"127.0.0.1:7946","state":"alive","incarnation":1},"target":{"id":"peer","addr":"127.0.0.1:7947","state":"alive","incarnation":2},"ts":1}`)
	f.Fuzz(func(t *testing.T, raw string) {
		var msg message
		if err := unmarshalMessage([]byte(raw), &msg); err == nil {
			_ = msg.validate()
		}
	})
}

// fakeNet is an in-memory lossless network with partition support.
type fakeNet struct {
	mu      sync.Mutex
	nodes   map[string]*fakeTransport
	down    map[string]bool
	blocked map[string]bool
}

type fakeTransport struct {
	net  *fakeNet
	addr string
	ch   chan transport.Packet
}

func newFakeNet() *fakeNet {
	return &fakeNet{nodes: map[string]*fakeTransport{}, down: map[string]bool{}, blocked: map[string]bool{}}
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

func (n *fakeNet) heal(addr string) {
	n.mu.Lock()
	delete(n.down, addr)
	n.mu.Unlock()
}

func (n *fakeNet) block(from, to string) {
	n.mu.Lock()
	n.blocked[from+"->"+to] = true
	n.mu.Unlock()
}

func (t *fakeTransport) Packets() <-chan transport.Packet { return t.ch }

func (t *fakeTransport) Send(addr string, p []byte) error {
	t.net.mu.Lock()
	dst, drop := t.net.nodes[addr], t.net.down[addr] || t.net.down[t.addr] || t.net.blocked[t.addr+"->"+addr]
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

func (t *fakeTransport) SendStream(_ context.Context, addr string, p []byte) error {
	t.net.mu.Lock()
	dst, drop := t.net.nodes[addr], t.net.down[addr] || t.net.down[t.addr] || t.net.blocked[t.addr+"->"+addr]
	t.net.mu.Unlock()
	if dst == nil || drop {
		return nil
	}
	select {
	case dst.ch <- transport.Packet{Payload: bytes.Clone(p), ReceivedAt: time.Now(), Stream: true}:
	default:
	}
	return nil
}

func testConfig(id, addr string) Config {
	return Config{
		NodeID: id, Region: "test", AdvertiseAddr: addr,
		ProbeInterval: 100 * time.Millisecond, ProbeTimeout: 100 * time.Millisecond,
		SuspicionTimeout: 500 * time.Millisecond, GossipInterval: 20 * time.Millisecond,
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
	waitFor(t, 10*time.Second, func() bool {
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

func TestIndirectProbeSurvivesDirectLinkFailure(t *testing.T) {
	fn := newFakeNet()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })

	nodes := make([]*Memberlist, 3)
	for i := range nodes {
		addr := fmt.Sprintf("127.0.0.1:%d", 7100+i)
		cfg := testConfig(fmt.Sprintf("indirect-%d", i), addr)
		cfg.ProbeInterval = time.Hour // drive the probe below explicitly
		nodes[i] = New(cfg, fn.attach(addr), log)
		wg.Go(func() { nodes[i].Run(ctx) })
	}
	for _, n := range nodes[1:] {
		if _, err := n.Join(ctx, []string{nodes[0].LocalNode().Addr}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 2*time.Second, func() bool {
		for _, n := range nodes {
			if len(n.Members()) != 3 {
				return false
			}
		}
		return true
	}, "cluster did not converge")

	requester, target := nodes[0], nodes[1]
	fn.block(requester.LocalNode().Addr, target.LocalNode().Addr)
	fn.block(target.LocalNode().Addr, requester.LocalNode().Addr)
	requester.probeMember(ctx, target.LocalNode())
	if got := targetState(requester, target.LocalNode().ID); got != StateAlive {
		t.Fatalf("target state after indirect probe = %s, want alive", got)
	}
}

func TestServiceRecordsReplicateThroughGossip(t *testing.T) {
	fn := newFakeNet()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	stores := make([]*storage.Store, 3)
	nodes := make([]*Memberlist, 3)
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		for _, store := range stores {
			_ = store.Close()
		}
	})

	for i := range nodes {
		store, err := storage.Open(ctx, ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		stores[i] = store
		addr := fmt.Sprintf("127.0.0.1:%d", 7200+i)
		cfg := testConfig(fmt.Sprintf("services-%d", i), addr)
		cfg.Services = store
		nodes[i] = New(cfg, fn.attach(addr), log)
		wg.Go(func() { nodes[i].Run(ctx) })
	}
	for _, n := range nodes[1:] {
		if _, err := n.Join(ctx, []string{nodes[0].LocalNode().Addr}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, 2*time.Second, func() bool {
		for _, n := range nodes {
			if len(n.Members()) != 3 {
				return false
			}
		}
		return true
	}, "cluster did not converge")

	if _, err := stores[1].Register(ctx, storage.Service{
		ID: "api-1", Name: "api-service", NodeID: nodes[1].LocalNode().ID,
		Address: "10.0.0.2:8080", Region: "test", Health: storage.HealthHealthy,
	}); err != nil {
		t.Fatal(err)
	}
	for i := range stores {
		waitFor(t, 2*time.Second, func() bool {
			service, err := stores[i].Get(ctx, "api-1")
			return err == nil && service.Name == "api-service"
		}, fmt.Sprintf("service record did not replicate to node %d", i))
	}
}

func TestTCPPushPullRepairsFullMembershipView(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fn := newFakeNet()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	first := New(testConfig("sync-1", "127.0.0.1:7251"), fn.attach("127.0.0.1:7251"), log)
	second := New(testConfig("sync-2", "127.0.0.1:7252"), fn.attach("127.0.0.1:7252"), log)
	first.merge(Member{ID: "sync-2", Addr: "127.0.0.1:7252", State: StateAlive, Incarnation: 1})
	second.merge(Member{ID: "sync-1", Addr: "127.0.0.1:7251", State: StateAlive, Incarnation: 1})
	second.merge(Member{ID: "sync-3", Addr: "127.0.0.1:7253", State: StateAlive, Incarnation: 1})
	wg.Go(func() { first.receiveLoop(ctx) })
	wg.Go(func() { second.receiveLoop(ctx) })
	if err := first.sendFullState(context.Background(), second.LocalNode().Addr, msgPushPullRequest, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, time.Second, func() bool { return targetState(first, "sync-3") == StateAlive }, "TCP push-pull did not repair the missing member")
}

func TestTCPPushPullRepairsRegistryDigestMismatch(t *testing.T) {
	ctx := context.Background()
	firstStore, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = firstStore.Close() })
	secondStore, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	_, err = firstStore.Register(ctx, storage.Service{ID: "payments-1", Name: "payments", NodeID: "registry-1", Address: "127.0.0.1:8080", Region: "eu", Health: storage.HealthHealthy})
	if err != nil {
		t.Fatal(err)
	}
	_, err = secondStore.Register(ctx, storage.Service{ID: "payments-2", Name: "payments", NodeID: "registry-2", Address: "127.0.0.1:8081", Region: "eu", Health: storage.HealthHealthy})
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fn := newFakeNet()
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); wg.Wait() })
	firstConfig := testConfig("registry-1", "127.0.0.1:7261")
	firstConfig.Services = firstStore
	secondConfig := testConfig("registry-2", "127.0.0.1:7262")
	secondConfig.Services = secondStore
	first := New(firstConfig, fn.attach(firstConfig.AdvertiseAddr), log)
	second := New(secondConfig, fn.attach(secondConfig.AdvertiseAddr), log)
	first.merge(Member{ID: secondConfig.NodeID, Addr: secondConfig.AdvertiseAddr, State: StateAlive, Incarnation: 1})
	second.merge(Member{ID: firstConfig.NodeID, Addr: firstConfig.AdvertiseAddr, State: StateAlive, Incarnation: 1})
	wg.Go(func() { first.receiveLoop(ctx) })
	wg.Go(func() { second.receiveLoop(ctx) })
	if err := first.sendFullState(context.Background(), secondConfig.AdvertiseAddr, msgPushPullRequest, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		firstRemote, firstErr := secondStore.Get(context.Background(), "payments-1")
		secondRemote, secondErr := firstStore.Get(context.Background(), "payments-2")
		return firstErr == nil && secondErr == nil && firstRemote.Name == "payments" && secondRemote.Name == "payments"
	}, "push-pull did not repair both sides of registry digest mismatch")
}

func targetState(m *Memberlist, id string) State {
	for _, member := range m.Members() {
		if member.ID == id {
			return member.State
		}
	}
	return StateDead
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

func TestBroadcastQueueRetransmitsAndPrioritizesUpdates(t *testing.T) {
	m := New(testConfig("self", "127.0.0.1:1"), newFakeNet().attach("x"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	first := Member{ID: "first", Addr: "127.0.0.1:2", State: StateAlive, Incarnation: 1}
	second := Member{ID: "second", Addr: "127.0.0.1:3", State: StateAlive, Incarnation: 1}
	m.merge(first)
	m.merge(second)

	got := m.sample(1, "")
	if len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("first sample = %+v, want queued update %q", got, first.ID)
	}
	m.markBroadcastsSent(got)

	// The three-member cluster gets ceil(log2(3))+1 total transmissions.
	for transmission := 2; transmission <= 3; transmission++ {
		batch := m.nextBroadcasts(2)
		if len(batch) != 2 || batch[0].ID != second.ID || batch[1].ID != first.ID {
			t.Fatalf("transmission %d = %+v, want second then first", transmission, batch)
		}
		m.markBroadcastsSent(batch)
	}
	batch := m.nextBroadcasts(1)
	if len(batch) != 1 || batch[0].ID != second.ID {
		t.Fatalf("last pending transmission = %+v, want %q", batch, second.ID)
	}
	m.markBroadcastsSent(batch)
	if batch := m.nextBroadcasts(1); len(batch) != 0 {
		t.Fatalf("queue retained exhausted records: %+v", batch)
	}
}

func TestBroadcastQueueIsBounded(t *testing.T) {
	m := New(testConfig("self", "127.0.0.1:1"), newFakeNet().attach("x"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	for i := 0; i <= maxBroadcastQueue; i++ {
		m.queueBroadcast(Member{ID: fmt.Sprintf("node-%04d", i), Addr: "127.0.0.1:2", State: StateAlive, Incarnation: 1})
	}
	m.broadcastMu.Lock()
	defer m.broadcastMu.Unlock()
	if len(m.broadcasts) != maxBroadcastQueue {
		t.Fatalf("queue size = %d, want %d", len(m.broadcasts), maxBroadcastQueue)
	}
	if _, exists := m.broadcasts["node-0000"]; exists {
		t.Fatal("oldest broadcast was not evicted when the queue filled")
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

func TestPeerLatencyUsesEWMA(t *testing.T) {
	m := New(testConfig("self", "127.0.0.1:1"), newFakeNet().attach("x"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.recordLatency("peer", 100*time.Millisecond)
	m.recordLatency("peer", 180*time.Millisecond)
	if got, want := m.Latency("peer"), 110*time.Millisecond; got != want {
		t.Fatalf("EWMA latency = %s, want %s", got, want)
	}
	if got := m.Latency("unseen"); got != 0 {
		t.Fatalf("unseen peer latency = %s, want zero", got)
	}
}

func TestAdmissionRejectsUnknownNodesAndFiltersPiggyback(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fn := newFakeNet()
	cfg := testConfig("self", "127.0.0.1:1")
	cfg.AllowedNodeIDs = []string{"self", "trusted"}
	m := New(cfg, fn.attach("127.0.0.1:1"), log)
	packetFor := func(from Member, members []Member) transport.Packet {
		t.Helper()
		wire, err := json.Marshal(message{Type: msgSync, From: from, Members: members, SentAt: time.Now().UnixMilli()})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		return transport.Packet{Payload: wire, ReceivedAt: now}
	}
	trusted := Member{ID: "trusted", Addr: "127.0.0.1:2", State: StateAlive, Incarnation: 1}
	unknown := Member{ID: "unknown", Addr: "127.0.0.1:3", State: StateAlive, Incarnation: 1}
	m.handle(context.Background(), packetFor(trusted, []Member{unknown}))
	if len(m.Members()) != 2 || targetState(m, "unknown") != StateDead {
		t.Fatalf("allowlisted sender admitted unexpected members: %+v", m.Members())
	}
	m.handle(context.Background(), packetFor(unknown, nil))
	if len(m.Members()) != 2 {
		t.Fatalf("unallowlisted sender changed membership: %+v", m.Members())
	}
}

func TestAllowedNodeIDsCanBeReloaded(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fn := newFakeNet()
	cfg := testConfig("self", "127.0.0.1:1")
	cfg.AllowedNodeIDs = []string{"self", "old-peer"}
	m := New(cfg, fn.attach("127.0.0.1:1"), log)
	m.SetAllowedNodeIDs([]string{"self", "new-peer"})
	if m.allowed("old-peer") || !m.allowed("new-peer") {
		t.Fatal("reloaded node admission list was not applied")
	}
}

func TestEventSubscribersReceiveIndependentCopies(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := New(testConfig("self", "127.0.0.1:1"), newFakeNet().attach("127.0.0.1:1"), log)
	first, cancelFirst := m.Subscribe(4)
	defer cancelFirst()
	second, cancelSecond := m.Subscribe(4)
	defer cancelSecond()
	m.merge(Member{ID: "peer", Addr: "127.0.0.1:2", State: StateAlive, Incarnation: 1})
	for i, stream := range []<-chan Event{first, second} {
		select {
		case event := <-stream:
			if event.Type != EventJoin || event.Member.ID != "peer" {
				t.Fatalf("subscriber %d received %+v", i, event)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d did not receive event", i)
		}
	}
}

func TestLifeguardLocalHealthAdaptsProbeDeadline(t *testing.T) {
	m := New(testConfig("self", "127.0.0.1:1"), newFakeNet().attach("x"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	base := m.localProbeTimeout()
	m.probeFailed()
	if got, want := m.localProbeTimeout(), 2*base; got != want {
		t.Fatalf("deadline after missed probe = %s, want %s", got, want)
	}
	for range maxLocalHealthScore + 2 {
		m.probeFailed()
	}
	if m.localHealth.Load() != maxLocalHealthScore {
		t.Fatalf("local health score = %d, want cap %d", m.localHealth.Load(), maxLocalHealthScore)
	}
	m.probeSucceeded("peer", "ack", time.Now().Add(-time.Millisecond))
	if m.localHealth.Load() != maxLocalHealthScore-1 {
		t.Fatalf("local health score after success = %d, want %d", m.localHealth.Load(), maxLocalHealthScore-1)
	}
}

func TestSendTrimsGossipToIPv6MTUPayload(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	network := newFakeNet()
	sender := network.attach("127.0.0.1:1")
	receiver := network.attach("127.0.0.1:2")
	m := New(testConfig("self", sender.addr), sender, log)
	members := make([]Member, 32)
	for i := range members {
		members[i] = Member{ID: fmt.Sprintf("peer-%03d-%0128s", i, "x"), Addr: "127.0.0.1:7946", State: StateAlive, Incarnation: uint64(i + 1)}
	}
	if err := m.sendWithServiceLimit(context.Background(), receiver.addr, msgSync, 1, members, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case packet := <-receiver.ch:
		if len(packet.Payload) > transport.MaxPayload {
			t.Fatalf("payload length %d exceeds IPv6-safe limit %d", len(packet.Payload), transport.MaxPayload)
		}
		var got message
		if err := unmarshalMessage(packet.Payload, &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Members) == len(members) {
			t.Fatal("oversized member set was not trimmed")
		}
	default:
		t.Fatal("no packet delivered")
	}
}

func TestEvictOnlyRemovesFailedPeers(t *testing.T) {
	fn := newFakeNet()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	m := New(testConfig("self", "127.0.0.1:7301"), fn.attach("127.0.0.1:7301"), log)
	m.merge(Member{ID: "peer", Addr: "127.0.0.1:7302", State: StateAlive, Incarnation: 1})

	if _, err := m.Evict("self"); !errors.Is(err, ErrEvictSelf) {
		t.Fatalf("evict self err = %v", err)
	}
	if _, err := m.Evict("ghost"); !errors.Is(err, ErrUnknownMember) {
		t.Fatalf("evict unknown err = %v", err)
	}
	if _, err := m.Evict("peer"); !errors.Is(err, ErrMemberAlive) {
		t.Fatalf("evict alive err = %v", err)
	}
	m.merge(Member{ID: "peer", Addr: "127.0.0.1:7302", State: StateDead, Incarnation: 1})
	evicted, err := m.Evict("peer")
	if err != nil || evicted.State != StateLeft {
		t.Fatalf("evict dead = %+v %v, want left", evicted, err)
	}
}

// After a partition both sides mark each other dead and stop gossiping to
// each other; only a seed pull across the healed link brings them back.
func TestRejoinHealsPartitionAfterBothSidesDeclaredDead(t *testing.T) {
	const size = 3
	fn, nodes := startCluster(t, size)
	isolated := nodes[size-1]
	waitFor(t, 10*time.Second, func() bool { return countState(nodes[0], StateAlive) == size }, "cluster did not converge")

	fn.partition(isolated.LocalNode().Addr)
	waitFor(t, 10*time.Second, func() bool {
		return countState(nodes[0], StateDead) == 1 && countState(isolated, StateDead) == size-1
	}, "partition was not detected on both sides")

	fn.heal(isolated.LocalNode().Addr)
	time.Sleep(time.Second)
	if countState(nodes[0], StateAlive) == size {
		t.Fatal("cluster healed without a rejoin; the test no longer covers the stuck case")
	}

	ctx := context.Background()
	if err := nodes[0].Rejoin(ctx, []string{isolated.LocalNode().Addr}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, func() bool {
		for _, n := range nodes {
			if countState(n, StateAlive) != size {
				return false
			}
		}
		return true
	}, "cluster did not converge after rejoin")
}
