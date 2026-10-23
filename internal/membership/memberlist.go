package membership

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/bits"
	"math/rand/v2"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/manziosee/atlas/internal/metrics"
	"github.com/manziosee/atlas/internal/storage"
	"github.com/manziosee/atlas/internal/transport"
)

const (
	// maxPiggyback is how many member records ride on each ping/ack/sync.
	maxPiggyback = 32
	// maxFullSync is how many records a pull reply carries.
	maxFullSync = 160
	// antiEntropyEvery: one in N gossip rounds is a push-pull exchange.
	antiEntropyEvery    = 10
	indirectProbeFanout = 3
	maxPendingRelays    = 1024
	maxBroadcastQueue   = 4096
	maxServicePiggyback = 2
	maxServiceSync      = 10
)

// Config tunes the protocol. See internal/config for the env mapping.
type Config struct {
	NodeID           string
	Region           string
	AdvertiseAddr    string
	ProbeInterval    time.Duration
	ProbeTimeout     time.Duration
	SuspicionTimeout time.Duration
	GossipInterval   time.Duration
	Fanout           int
	ReapAfter        time.Duration
	MaxClockSkew     time.Duration
	AllowedNodeIDs   []string
	Services         ServiceStore
}

type ServiceStore interface {
	Snapshot(context.Context, int) ([]storage.Service, error)
	RegistryDigest(context.Context) ([32]byte, error)
	ReplicationSnapshot(context.Context) ([]storage.Service, error)
	Merge(context.Context, storage.Service) error
}

// Transport is the packet layer Memberlist depends on.
type Transport interface {
	Send(addr string, payload []byte) error
	Packets() <-chan transport.Packet
}

type streamTransport interface {
	SendStream(ctx context.Context, addr string, payload []byte) error
}

// Memberlist maintains this node's view of the cluster.
type Memberlist struct {
	cfg          Config
	tr           Transport
	log          *slog.Logger
	services     ServiceStore
	admissionMu  sync.RWMutex
	allowedNodes map[string]struct{}

	mu          sync.RWMutex
	self        Member
	members     map[string]*Member // excludes self
	memberOrder []string           // random-sampling index, protected by mu

	seq         atomic.Uint64
	localHealth atomic.Int32
	ackMu       sync.Mutex
	acks        map[uint64]chan struct{}

	rttMu sync.RWMutex
	rtts  map[string]time.Duration

	relayMu sync.Mutex
	relays  map[relayKey]relay

	broadcastMu    sync.Mutex
	broadcasts     map[string]broadcastRecord
	broadcastOrder []string

	events      chan Event
	eventMu     sync.Mutex
	subscribers map[uint64]chan Event
	nextSubID   uint64
}

type relayKey struct {
	seq      uint64
	targetID string
}

type relay struct {
	addr      string
	requester string
	seq       uint64
	expires   time.Time
}

type broadcastRecord struct {
	member        Member
	transmissions int
}

// New creates a Memberlist. Call Run to start it.
func New(cfg Config, tr Transport, log *slog.Logger) *Memberlist {
	now := time.Now()
	m := &Memberlist{
		cfg:      cfg,
		tr:       tr,
		log:      log.With("component", "membership"),
		services: cfg.Services,
		allowedNodes: func() map[string]struct{} {
			if len(cfg.AllowedNodeIDs) == 0 {
				return nil
			}
			allowed := make(map[string]struct{}, len(cfg.AllowedNodeIDs))
			for _, id := range cfg.AllowedNodeIDs {
				allowed[id] = struct{}{}
			}
			return allowed
		}(),
		self: Member{
			ID:     cfg.NodeID,
			Addr:   cfg.AdvertiseAddr,
			Region: cfg.Region,
			State:  StateAlive,
			// Starting from wall-clock millis guarantees a restarted node
			// outranks whatever the cluster remembers about its previous life.
			Incarnation: uint64(now.UnixMilli()), //nolint:gosec // positive by construction
			Since:       now,
		},
		members:     make(map[string]*Member),
		acks:        make(map[uint64]chan struct{}),
		rtts:        make(map[string]time.Duration),
		relays:      make(map[relayKey]relay),
		broadcasts:  make(map[string]broadcastRecord),
		events:      make(chan Event, 256),
		subscribers: make(map[uint64]chan Event),
	}
	m.seq.Store(rand.Uint64())
	return m
}

// Events streams membership changes. Events are dropped if the consumer
// falls behind; state is always available via Members.
func (m *Memberlist) Events() <-chan Event { return m.events }

// Subscribe creates an independent bounded event stream. Events are dropped
// for a slow subscriber; callers can always fetch the current state snapshot.
func (m *Memberlist) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer < 1 {
		buffer = 1
	}
	m.eventMu.Lock()
	m.nextSubID++
	id := m.nextSubID
	ch := make(chan Event, buffer)
	m.subscribers[id] = ch
	m.eventMu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			m.eventMu.Lock()
			if subscriber, ok := m.subscribers[id]; ok {
				delete(m.subscribers, id)
				close(subscriber)
			}
			m.eventMu.Unlock()
		})
	}
}

// LocalNode returns this node's own record.
func (m *Memberlist) LocalNode() Member {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.self
}

// Members returns all known members including self, sorted by ID.
func (m *Memberlist) Members() []Member {
	m.mu.RLock()
	out := make([]Member, 0, len(m.members)+1)
	out = append(out, m.self)
	for _, mb := range m.members {
		out = append(out, *mb)
	}
	m.mu.RUnlock()
	slices.SortFunc(out, func(a, b Member) int { return strings.Compare(a.ID, b.ID) })
	return out
}

var (
	ErrUnknownMember = errors.New("membership: unknown member")
	ErrMemberAlive   = errors.New("membership: member is alive")
	ErrEvictSelf     = errors.New("membership: a node cannot evict itself")
)

// Evict marks a failed or departed member as left so peers stop probing it
// and it is reaped after ReapAfter. Alive members are refused: they would
// refute the claim by raising their incarnation and rejoin immediately.
func (m *Memberlist) Evict(id string) (Member, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == m.self.ID {
		return Member{}, ErrEvictSelf
	}
	mb, ok := m.members[id]
	if !ok {
		return Member{}, ErrUnknownMember
	}
	if mb.State == StateAlive {
		return *mb, ErrMemberAlive
	}
	if mb.State != StateLeft {
		m.setState(mb, StateLeft)
	}
	return *mb, nil
}

// Latency returns this node's EWMA observation of the peer's successful probe
// round-trip time. A zero value means no successful probe has been recorded.
func (m *Memberlist) Latency(id string) time.Duration {
	m.rttMu.RLock()
	defer m.rttMu.RUnlock()
	return m.rtts[id]
}

func (m *Memberlist) recordLatency(id string, sample time.Duration) {
	if sample <= 0 {
		return
	}
	m.rttMu.Lock()
	previous := m.rtts[id]
	if previous == 0 {
		m.rtts[id] = sample
	} else {
		m.rtts[id] = previous + (sample-previous)/8
	}
	value := m.rtts[id]
	m.rttMu.Unlock()
	metrics.PeerRTT.WithLabelValues(id).Set(value.Seconds())
}

// Run drives the protocol until ctx is cancelled, then announces a graceful
// leave so peers do not have to wait for failure detection.
func (m *Memberlist) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { m.receiveLoop(ctx) })
	wg.Go(func() { every(ctx, m.cfg.ProbeInterval, m.probe) })
	wg.Go(func() { m.gossipLoop(ctx) })
	wg.Go(func() { every(ctx, 250*time.Millisecond, func(context.Context) { m.tick() }) })
	wg.Wait()
	m.leave(ctx)
}

// Join contacts seed addresses ("host:port"; hostnames may resolve to many
// IPs, e.g. a Kubernetes headless service or Fly.io .internal DNS) and
// requests their state. It returns how many peers were contacted.
func (m *Memberlist) Join(ctx context.Context, seeds []string) (int, error) {
	self := m.LocalNode().Addr
	var errs []error
	contacted := 0
	for _, s := range seeds {
		host, port, err := net.SplitHostPort(s)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		ips, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil {
			errs = append(errs, fmt.Errorf("resolve seed %q: %w", s, err))
			continue
		}
		for _, ip := range ips {
			addr := net.JoinHostPort(ip, port)
			if addr == self {
				continue
			}
			if err := m.send(ctx, addr, msgPull, 0, m.sample(maxPiggyback, "")); err != nil {
				errs = append(errs, err)
				continue
			}
			contacted++
		}
	}
	if contacted == 0 {
		return 0, errors.Join(append(errs, errors.New("membership: no seed reachable"))...)
	}
	return contacted, nil
}

// --- protocol loops -------------------------------------------------------

func every(ctx context.Context, d time.Duration, fn func(context.Context)) {
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fn(ctx)
		}
	}
}

func (m *Memberlist) receiveLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-m.tr.Packets():
			if !ok {
				return
			}
			m.handle(ctx, p)
		}
	}
}

// probe implements a direct probe followed by indirect probes through peers.
// A target is suspected only if neither path produces an acknowledgement.
func (m *Memberlist) probe(ctx context.Context) {
	target, ok := m.randomMember(func(mb *Member) bool {
		return mb.State == StateAlive || mb.State == StateSuspect
	})
	if !ok {
		return
	}
	m.probeMember(ctx, target)
}

func (m *Memberlist) probeMember(ctx context.Context, target Member) {
	seq := m.seq.Add(1)
	ch := make(chan struct{}, 1)
	m.ackMu.Lock()
	m.acks[seq] = ch
	m.ackMu.Unlock()
	defer func() {
		m.ackMu.Lock()
		delete(m.acks, seq)
		m.ackMu.Unlock()
	}()

	start := time.Now()
	probeTimeout := m.localProbeTimeout()
	if err := m.send(ctx, target.Addr, msgPing, seq, m.sample(maxPiggyback, target.ID)); err != nil {
		m.log.Debug("probe send failed", "target", target.ID, "err", err)
	}

	// Reserve half the timeout for direct probing and half for helpers. This
	// keeps the overall failure-detection budget bounded by ProbeTimeout.
	directWait := probeTimeout / 2
	if directWait <= 0 {
		directWait = probeTimeout
	}
	timer := time.NewTimer(directWait)
	defer timer.Stop()
	select {
	case <-ch:
		m.probeSucceeded(target.ID, "ack", start)
	case <-timer.C:
		helpers := m.randomMembers(indirectProbeFanout, func(mb *Member) bool {
			return mb.ID != target.ID && mb.State == StateAlive
		})
		sent := 0
		for _, helper := range helpers {
			if err := m.sendPingReq(helper.Addr, seq, target); err == nil {
				sent++
			}
		}
		indirectWait := probeTimeout - directWait
		if sent == 0 || indirectWait <= 0 {
			if indirectWait > 0 {
				timer.Reset(indirectWait)
				select {
				case <-ch:
					m.probeSucceeded(target.ID, "ack", start)
					return
				case <-timer.C:
				case <-ctx.Done():
					return
				}
			}
			m.probeFailed()
			metrics.Probes.WithLabelValues("timeout").Inc()
			m.suspect(target.ID, target.Incarnation)
			return
		}
		timer.Reset(indirectWait)
		select {
		case <-ch:
			m.probeSucceeded(target.ID, "indirect_ack", start)
		case <-timer.C:
			m.probeFailed()
			metrics.Probes.WithLabelValues("timeout").Inc()
			m.suspect(target.ID, target.Incarnation)
		case <-ctx.Done():
		}
	case <-ctx.Done():
	}
}

const maxLocalHealthScore = 8

func (m *Memberlist) localProbeTimeout() time.Duration {
	score := m.localHealth.Load()
	return m.cfg.ProbeTimeout * time.Duration(1+score)
}

func (m *Memberlist) probeSucceeded(targetID, result string, start time.Time) {
	metrics.Probes.WithLabelValues(result).Inc()
	rtt := time.Since(start)
	metrics.ProbeRTT.Observe(rtt.Seconds())
	m.recordLatency(targetID, rtt)
	for {
		score := m.localHealth.Load()
		if score == 0 || m.localHealth.CompareAndSwap(score, score-1) {
			metrics.LocalHealthScore.Set(float64(max(0, score-1)))
			return
		}
	}
}

func (m *Memberlist) probeFailed() {
	for {
		score := m.localHealth.Load()
		if score >= maxLocalHealthScore || m.localHealth.CompareAndSwap(score, score+1) {
			metrics.LocalHealthScore.Set(float64(min(maxLocalHealthScore, score+1)))
			return
		}
	}
}

func (m *Memberlist) gossipLoop(ctx context.Context) {
	t := time.NewTicker(m.cfg.GossipInterval)
	defer t.Stop()
	for round := 1; ; round++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		live := func(mb *Member) bool { return mb.State == StateAlive || mb.State == StateSuspect }
		if round%antiEntropyEvery == 0 {
			if peer, ok := m.randomMember(live); ok {
				if _, supportsStream := m.tr.(streamTransport); supportsStream {
					if err := m.sendFullState(ctx, peer.Addr, msgPushPullRequest, nil, false); err != nil {
						m.log.Warn("TCP push-pull failed", "peer", peer.ID, "err", err)
					}
				} else {
					_ = m.send(ctx, peer.Addr, msgPull, 0, m.sample(maxFullSync, ""))
				}
			}
			continue
		}
		for _, peer := range m.randomMembers(m.cfg.Fanout, live) {
			_ = m.send(ctx, peer.Addr, msgSync, 0, m.sample(maxPiggyback, ""))
		}
	}
}

// tick promotes expired suspects to dead, reaps old dead members and
// refreshes gauges.
func (m *Memberlist) tick() {
	now := time.Now()
	counts := map[State]int{StateAlive: 1} // self
	m.relayMu.Lock()
	for key, r := range m.relays {
		if now.After(r.expires) {
			delete(m.relays, key)
		}
	}
	m.relayMu.Unlock()

	m.mu.Lock()
	for id, mb := range m.members {
		switch {
		case mb.State == StateSuspect && now.Sub(mb.Since) > m.cfg.SuspicionTimeout:
			m.setState(mb, StateDead)
		case (mb.State == StateDead || mb.State == StateLeft) && now.Sub(mb.Since) > m.cfg.ReapAfter:
			delete(m.members, id)
			if i := slices.Index(m.memberOrder, id); i >= 0 {
				m.memberOrder = slices.Delete(m.memberOrder, i, i+1)
			}
			m.rttMu.Lock()
			delete(m.rtts, id)
			m.rttMu.Unlock()
			metrics.PeerRTT.DeleteLabelValues(id)
			m.emit(EventReap, *mb)
			continue
		}
		counts[mb.State]++
	}
	m.mu.Unlock()

	for s := StateAlive; s <= StateLeft; s++ {
		metrics.Members.WithLabelValues(s.String()).Set(float64(counts[s]))
	}
}

func (m *Memberlist) leave(ctx context.Context) {
	m.mu.Lock()
	m.self.State = StateLeft
	m.self.Incarnation++
	var peers []string
	for _, mb := range m.members {
		if mb.State == StateAlive || mb.State == StateSuspect {
			peers = append(peers, mb.Addr)
		}
	}
	m.mu.Unlock()

	leaveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	for _, addr := range peers {
		_ = m.send(leaveCtx, addr, msgSync, 0, nil)
	}
	m.log.Info("left cluster", "notified", len(peers))
}

// --- message handling -----------------------------------------------------

func (m *Memberlist) handle(ctx context.Context, p transport.Packet) {
	var msg message
	if err := unmarshalMessage(p.Payload, &msg); err != nil || msg.validate() != nil {
		metrics.PacketsRejected.WithLabelValues("decode").Inc()
		return
	}
	if skew := p.ReceivedAt.Sub(time.UnixMilli(msg.SentAt)); skew > m.cfg.MaxClockSkew || skew < -m.cfg.MaxClockSkew {
		metrics.PacketsRejected.WithLabelValues("clock_skew").Inc()
		return
	}
	if msg.From.ID == m.cfg.NodeID {
		return
	}
	if !m.allowed(msg.From.ID) {
		metrics.PacketsRejected.WithLabelValues("admission").Inc()
		m.log.Warn("rejected message from unadmitted node", "node", msg.From.ID)
		return
	}
	if (msg.Type == msgPushPullRequest || msg.Type == msgPushPullState) && !p.Stream {
		metrics.PacketsRejected.WithLabelValues("decode").Inc()
		return
	}

	m.merge(msg.From)
	for _, mb := range msg.Members {
		if m.allowed(mb.ID) {
			m.merge(mb)
		}
	}
	if m.services != nil {
		for _, service := range msg.Services {
			if err := m.services.Merge(ctx, service); err != nil {
				m.log.Warn("could not merge service record", "service", service.ID, "err", err)
			}
		}
	}

	switch msg.Type {
	case msgPing:
		_ = m.send(ctx, msg.From.Addr, msgAck, msg.Seq, m.sample(maxPiggyback, msg.From.ID))
	case msgAck:
		m.ackMu.Lock()
		ch := m.acks[msg.Seq]
		m.ackMu.Unlock()
		if ch != nil {
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		key := relayKey{seq: msg.Seq, targetID: msg.From.ID}
		m.relayMu.Lock()
		r, ok := m.relays[key]
		if ok {
			delete(m.relays, key)
		}
		m.relayMu.Unlock()
		if ok {
			_ = m.send(ctx, r.addr, msgAck, r.seq, m.sample(maxPiggyback, r.requester))
		}
	case msgPull:
		_ = m.sendWithServiceLimit(ctx, msg.From.Addr, msgSync, 0, m.sample(maxFullSync, msg.From.ID), maxServiceSync)
	case msgPingReq:
		if msg.Target != nil {
			probeSeq := m.seq.Add(1)
			key := relayKey{seq: probeSeq, targetID: msg.Target.ID}
			m.relayMu.Lock()
			if len(m.relays) >= maxPendingRelays {
				m.relayMu.Unlock()
				return
			}
			m.relays[key] = relay{addr: msg.From.Addr, requester: msg.From.ID, seq: msg.Seq, expires: time.Now().Add(m.cfg.ProbeTimeout)}
			m.relayMu.Unlock()
			if err := m.send(ctx, msg.Target.Addr, msgPing, probeSeq, m.sample(maxPiggyback, msg.Target.ID)); err != nil {
				m.relayMu.Lock()
				delete(m.relays, key)
				m.relayMu.Unlock()
			}
		}
	case msgPushPullRequest:
		if err := m.sendFullState(ctx, msg.From.Addr, msgPushPullState, msg.RegistryDigest, true); err != nil {
			m.log.Warn("TCP push-pull response failed", "peer", msg.From.ID, "err", err)
		}
	case msgPushPullState:
		if msg.PushPullReply && m.services != nil {
			digestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			localDigest, err := m.services.RegistryDigest(digestCtx)
			cancel()
			if err == nil && !bytes.Equal(localDigest[:], msg.RegistryDigest) {
				if err := m.sendFullState(ctx, msg.From.Addr, msgPushPullState, msg.RegistryDigest, false); err != nil {
					m.log.Warn("TCP push-pull registry repair failed", "peer", msg.From.ID, "err", err)
				}
			} else if err != nil {
				m.log.Warn("could not compare registry digest", "peer", msg.From.ID, "err", err)
			}
		}
	case msgSync:
	}
}

// sendFullState exchanges a complete bounded membership snapshot over the
// authenticated TCP stream transport. UDP remains the fast path for gossip.
func (m *Memberlist) sendFullState(ctx context.Context, addr string, kind msgType, peerDigest []byte, reply bool) error {
	stream, ok := m.tr.(streamTransport)
	if !ok {
		return errors.New("membership: transport does not support TCP push-pull")
	}
	members := m.Members()
	for i := range members {
		members[i].Since = time.Time{}
	}
	var digest [32]byte
	var services []storage.Service
	if m.services != nil {
		snapshotCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		var err error
		digest, err = m.services.RegistryDigest(snapshotCtx)
		if err == nil && kind == msgPushPullState && !bytes.Equal(peerDigest, digest[:]) {
			services, err = m.services.ReplicationSnapshot(snapshotCtx)
		}
		cancel()
		if err != nil {
			return fmt.Errorf("membership: snapshot registry for push-pull: %w", err)
		}
	}
	payload, err := marshalMessage(message{Type: kind, From: m.LocalNode(), Members: members, Services: services, RegistryDigest: digest[:], PushPullReply: reply, SentAt: time.Now().UnixMilli()})
	if err != nil {
		return fmt.Errorf("membership: encode full state: %w", err)
	}
	if len(payload) > transport.MaxStreamPayload {
		return errors.New("membership: full state exceeds TCP sync limit")
	}
	if err := stream.SendStream(ctx, addr, payload); err != nil {
		return err
	}
	m.markBroadcastsSent(members)
	return nil
}

func (m *Memberlist) allowed(nodeID string) bool {
	m.admissionMu.RLock()
	defer m.admissionMu.RUnlock()
	if m.allowedNodes == nil {
		return true
	}
	_, ok := m.allowedNodes[nodeID]
	return ok
}

// SetAllowedNodeIDs atomically updates node admission. An empty list allows
// every peer that can authenticate with the cluster gossip key.
func (m *Memberlist) SetAllowedNodeIDs(ids []string) {
	var allowed map[string]struct{}
	if len(ids) > 0 {
		allowed = make(map[string]struct{}, len(ids))
		for _, id := range ids {
			allowed[id] = struct{}{}
		}
	}
	m.admissionMu.Lock()
	m.allowedNodes = allowed
	m.admissionMu.Unlock()
}

// merge applies a remote view of one member using SWIM precedence rules:
// higher incarnation wins; on equal incarnation the "worse" state wins
// (alive < suspect < dead < left).
func (m *Memberlist) merge(in Member) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if in.ID == m.self.ID {
		// Someone thinks we are suspect/dead: refute by bumping our
		// incarnation; the new record spreads via piggybacking.
		if in.State != StateAlive && in.Incarnation >= m.self.Incarnation && m.self.State == StateAlive {
			m.self.Incarnation = in.Incarnation + 1
			m.queueBroadcast(m.self)
			metrics.Refutations.Inc()
			m.log.Info("refuted suspicion", "claimed_state", in.State, "incarnation", m.self.Incarnation)
		}
		return
	}

	cur, ok := m.members[in.ID]
	if !ok {
		if in.State == StateDead || in.State == StateLeft {
			return // never learn about a member only to bury it
		}
		mb := in
		mb.Since = time.Now()
		m.members[in.ID] = &mb
		m.memberOrder = append(m.memberOrder, in.ID)
		m.queueBroadcast(mb)
		metrics.StateTransitions.WithLabelValues(mb.State.String()).Inc()
		m.emit(EventJoin, mb)
		m.log.Info("member discovered", "member", mb.ID, "addr", mb.Addr, "region", mb.Region, "state", mb.State)
		return
	}

	if in.Incarnation < cur.Incarnation || (in.Incarnation == cur.Incarnation && in.State <= cur.State) {
		return
	}
	cur.Incarnation, cur.Addr, cur.Region = in.Incarnation, in.Addr, in.Region
	m.queueBroadcast(*cur)
	if in.State != cur.State {
		m.setState(cur, in.State)
	}
}

func (m *Memberlist) suspect(id string, incarnation uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mb, ok := m.members[id]; ok && mb.State == StateAlive && mb.Incarnation == incarnation {
		m.setState(mb, StateSuspect)
	}
}

// setState must be called with m.mu held.
func (m *Memberlist) setState(mb *Member, s State) {
	prev := mb.State
	mb.State, mb.Since = s, time.Now()
	m.queueBroadcast(*mb)
	metrics.StateTransitions.WithLabelValues(s.String()).Inc()
	m.emit(EventUpdate, *mb)
	m.log.Info("member state changed", "member", mb.ID, "from", prev, "to", s, "incarnation", mb.Incarnation)
}

func (m *Memberlist) emit(t EventType, mb Member) {
	event := Event{Type: t, Member: mb, At: time.Now()}
	select {
	case m.events <- event:
	default:
	}
	m.eventMu.Lock()
	for _, subscriber := range m.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
	m.eventMu.Unlock()
}

// --- helpers --------------------------------------------------------------

func (m *Memberlist) send(ctx context.Context, addr string, t msgType, seq uint64, members []Member) error {
	serviceLimit := maxServicePiggyback
	if t == msgPull {
		serviceLimit = maxServiceSync
	}
	return m.sendWithServiceLimit(ctx, addr, t, seq, members, serviceLimit)
}

func (m *Memberlist) sendWithServiceLimit(ctx context.Context, addr string, t msgType, seq uint64, members []Member, serviceLimit int) error {
	self := m.LocalNode()
	self.Since = time.Time{}
	var services []storage.Service
	if m.services != nil {
		snapshotCtx, cancel := context.WithTimeout(ctx, time.Second)
		var err error
		services, err = m.services.Snapshot(snapshotCtx, serviceLimit)
		cancel()
		if err != nil {
			m.log.Warn("could not snapshot service records", "err", err)
		}
	}
	b, err := marshalMessage(message{Type: t, Seq: seq, From: self, Members: members, Services: services, SentAt: time.Now().UnixMilli()})
	if err != nil {
		return err
	}
	// Keep ordinary gossip packets below the IPv6 minimum MTU. Large membership
	// snapshots converge over subsequent bounded packets instead of fragmenting.
	for len(b) > transport.MaxPayload && (len(members) > 0 || len(services) > 0) {
		if len(members) > 0 {
			members = members[:len(members)-1]
		} else {
			services = services[:len(services)-1]
		}
		b, err = marshalMessage(message{Type: t, Seq: seq, From: self, Members: members, Services: services, SentAt: time.Now().UnixMilli()})
		if err != nil {
			return err
		}
	}
	if len(b) > transport.MaxPayload {
		return transport.ErrPayloadTooLarge
	}
	if err := m.tr.Send(addr, b); err != nil {
		return err
	}
	m.markBroadcastsSent(members)
	return nil
}

func (m *Memberlist) sendPingReq(addr string, seq uint64, target Member) error {
	self := m.LocalNode()
	self.Since = time.Time{}
	target.Since = time.Time{}
	b, err := marshalMessage(message{Type: msgPingReq, Seq: seq, From: self, Target: &target, SentAt: time.Now().UnixMilli()})
	if err != nil {
		return err
	}
	return m.tr.Send(addr, b)
}

// sample returns up to n member records to piggyback, always including the
// record for mustInclude (so a suspected target learns of it and refutes).
func (m *Memberlist) sample(n int, mustInclude string) []Member {
	if n <= 0 {
		return nil
	}
	selected := m.nextBroadcasts(n)
	selectedIDs := make(map[string]struct{}, n)
	for _, mb := range selected {
		selectedIDs[mb.ID] = struct{}{}
	}
	m.mu.RLock()
	if mustInclude != "" {
		if _, ok := selectedIDs[mustInclude]; !ok {
			if member, exists := m.members[mustInclude]; exists {
				if len(selected) == n {
					delete(selectedIDs, selected[len(selected)-1].ID)
					selected = selected[:len(selected)-1]
				}
				copy := *member
				copy.Since = time.Time{}
				selected = append(selected, copy)
				selectedIDs[mustInclude] = struct{}{}
			}
		}
	}
	// Sample IDs directly so hot gossip sends do not allocate or shuffle a
	// full cluster-view copy for each packet.
	for attempts, maxAttempts := 0, max(8*n, 32); len(selected) < n && attempts < maxAttempts && len(m.memberOrder) > 0; attempts++ {
		id := m.memberOrder[rand.IntN(len(m.memberOrder))]
		if _, exists := selectedIDs[id]; exists {
			continue
		}
		if member, exists := m.members[id]; exists {
			copy := *member
			copy.Since = time.Time{}
			selected = append(selected, copy)
			selectedIDs[id] = struct{}{}
		}
	}
	// Fill a small final gap deterministically if queued records represented
	// most of the distinct members in a small cluster.
	if len(selected) < n {
		for _, id := range m.memberOrder {
			if len(selected) >= n {
				break
			}
			if _, exists := selectedIDs[id]; exists {
				continue
			}
			if member, exists := m.members[id]; exists {
				copy := *member
				copy.Since = time.Time{}
				selected = append(selected, copy)
				selectedIDs[id] = struct{}{}
			}
		}
	}
	m.mu.RUnlock()
	return selected
}

// queueBroadcast records the newest state for a member. The queue is bounded;
// when full it drops the oldest pending member update and increments a metric.
func (m *Memberlist) queueBroadcast(member Member) {
	member.Since = time.Time{}
	m.broadcastMu.Lock()
	defer m.broadcastMu.Unlock()
	if current, ok := m.broadcasts[member.ID]; ok {
		if member.Incarnation < current.member.Incarnation ||
			(member.Incarnation == current.member.Incarnation && member.State < current.member.State) {
			return
		}
		if member.Incarnation == current.member.Incarnation && member.State == current.member.State &&
			member.Addr == current.member.Addr && member.Region == current.member.Region {
			return
		}
		current.member = member
		current.transmissions = 0
		m.broadcasts[member.ID] = current
		return
	}
	if len(m.broadcasts) >= maxBroadcastQueue {
		for len(m.broadcastOrder) > 0 {
			oldest := m.broadcastOrder[0]
			m.broadcastOrder = m.broadcastOrder[1:]
			if _, exists := m.broadcasts[oldest]; exists {
				delete(m.broadcasts, oldest)
				metrics.BroadcastsDropped.Inc()
				break
			}
		}
	}
	m.broadcasts[member.ID] = broadcastRecord{member: member}
	if !slices.Contains(m.broadcastOrder, member.ID) {
		m.broadcastOrder = append(m.broadcastOrder, member.ID)
	}
	metrics.BroadcastQueue.Set(float64(len(m.broadcasts)))
}

// nextBroadcasts rotates each pending record at most once per message. Retry
// accounting happens only after the transport confirms a successful send.
func (m *Memberlist) nextBroadcasts(limit int) []Member {
	if limit <= 0 {
		return nil
	}
	m.broadcastMu.Lock()
	defer m.broadcastMu.Unlock()
	out := make([]Member, 0, min(limit, len(m.broadcasts)))
	remaining := len(m.broadcastOrder)
	for remaining > 0 && len(out) < limit {
		remaining--
		id := m.broadcastOrder[0]
		m.broadcastOrder = m.broadcastOrder[1:]
		record, ok := m.broadcasts[id]
		if !ok {
			continue
		}
		out = append(out, record.member)
		m.broadcastOrder = append(m.broadcastOrder, id)
	}
	metrics.BroadcastQueue.Set(float64(len(m.broadcasts)))
	return out
}

func (m *Memberlist) markBroadcastsSent(members []Member) {
	if len(members) == 0 {
		return
	}
	m.mu.RLock()
	clusterSize := len(m.members) + 1
	m.mu.RUnlock()
	maxTransmissions := bits.Len(uint(clusterSize-1)) + 1
	m.broadcastMu.Lock()
	defer m.broadcastMu.Unlock()
	for _, sent := range members {
		record, ok := m.broadcasts[sent.ID]
		if !ok || record.member.Incarnation != sent.Incarnation || record.member.State != sent.State {
			continue
		}
		record.transmissions++
		if record.transmissions >= maxTransmissions {
			delete(m.broadcasts, sent.ID)
		} else {
			m.broadcasts[sent.ID] = record
		}
	}
	if len(m.broadcasts) == 0 {
		m.broadcastOrder = m.broadcastOrder[:0]
	}
	metrics.BroadcastQueue.Set(float64(len(m.broadcasts)))
}

func (m *Memberlist) randomMember(filter func(*Member) bool) (Member, bool) {
	ms := m.randomMembers(1, filter)
	if len(ms) == 0 {
		return Member{}, false
	}
	return ms[0], true
}

func (m *Memberlist) randomMembers(n int, filter func(*Member) bool) []Member {
	m.mu.RLock()
	candidates := make([]Member, 0, len(m.members))
	for _, mb := range m.members {
		if filter(mb) {
			candidates = append(candidates, *mb)
		}
	}
	m.mu.RUnlock()
	rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	if len(candidates) > n {
		candidates = candidates[:n]
	}
	return candidates
}
