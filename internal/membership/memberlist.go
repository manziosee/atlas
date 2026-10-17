package membership

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/manziosee/atlas/internal/metrics"
	"github.com/manziosee/atlas/internal/transport"
)

const (
	// maxPiggyback is how many member records ride on each ping/ack/sync.
	maxPiggyback = 32
	// maxFullSync is how many records a pull reply carries.
	maxFullSync = 160
	// antiEntropyEvery: one in N gossip rounds is a push-pull exchange.
	antiEntropyEvery = 10
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
}

// Transport is the packet layer Memberlist depends on.
type Transport interface {
	Send(addr string, payload []byte) error
	Packets() <-chan transport.Packet
}

// Memberlist maintains this node's view of the cluster.
type Memberlist struct {
	cfg Config
	tr  Transport
	log *slog.Logger

	mu      sync.RWMutex
	self    Member
	members map[string]*Member // excludes self

	seq   atomic.Uint64
	ackMu sync.Mutex
	acks  map[uint64]chan struct{}

	events chan Event
}

// New creates a Memberlist. Call Run to start it.
func New(cfg Config, tr Transport, log *slog.Logger) *Memberlist {
	now := time.Now()
	m := &Memberlist{
		cfg: cfg,
		tr:  tr,
		log: log.With("component", "membership"),
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
		members: make(map[string]*Member),
		acks:    make(map[uint64]chan struct{}),
		events:  make(chan Event, 256),
	}
	m.seq.Store(rand.Uint64())
	return m
}

// Events streams membership changes. Events are dropped if the consumer
// falls behind; state is always available via Members.
func (m *Memberlist) Events() <-chan Event { return m.events }

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

// Run drives the protocol until ctx is cancelled, then announces a graceful
// leave so peers do not have to wait for failure detection.
func (m *Memberlist) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { m.receiveLoop(ctx) })
	wg.Go(func() { every(ctx, m.cfg.ProbeInterval, m.probe) })
	wg.Go(func() { m.gossipLoop(ctx) })
	wg.Go(func() { every(ctx, 250*time.Millisecond, func(context.Context) { m.tick() }) })
	wg.Wait()
	m.leave()
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
			if err := m.send(addr, msgPull, 0, m.sample(maxPiggyback, "")); err != nil {
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
			m.handle(p)
		}
	}
}

// probe implements the SWIM failure detector: ping one random member and
// mark it suspect if no ack arrives within ProbeTimeout.
//
// TODO(phase-2): indirect probes (ping-req via k peers) to tolerate
// asymmetric network faults before raising suspicion.
func (m *Memberlist) probe(ctx context.Context) {
	target, ok := m.randomMember(func(mb *Member) bool {
		return mb.State == StateAlive || mb.State == StateSuspect
	})
	if !ok {
		return
	}

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
	if err := m.send(target.Addr, msgPing, seq, m.sample(maxPiggyback, target.ID)); err != nil {
		m.log.Debug("probe send failed", "target", target.ID, "err", err)
	}

	timer := time.NewTimer(m.cfg.ProbeTimeout)
	defer timer.Stop()
	select {
	case <-ch:
		metrics.Probes.WithLabelValues("ack").Inc()
		metrics.ProbeRTT.Observe(time.Since(start).Seconds())
	case <-timer.C:
		metrics.Probes.WithLabelValues("timeout").Inc()
		m.suspect(target.ID, target.Incarnation)
	case <-ctx.Done():
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
				_ = m.send(peer.Addr, msgPull, 0, m.sample(maxFullSync, ""))
			}
			continue
		}
		for _, peer := range m.randomMembers(m.cfg.Fanout, live) {
			_ = m.send(peer.Addr, msgSync, 0, m.sample(maxPiggyback, ""))
		}
	}
}

// tick promotes expired suspects to dead, reaps old dead members and
// refreshes gauges.
func (m *Memberlist) tick() {
	now := time.Now()
	counts := map[State]int{StateAlive: 1} // self

	m.mu.Lock()
	for id, mb := range m.members {
		switch {
		case mb.State == StateSuspect && now.Sub(mb.Since) > m.cfg.SuspicionTimeout:
			m.setState(mb, StateDead)
		case (mb.State == StateDead || mb.State == StateLeft) && now.Sub(mb.Since) > m.cfg.ReapAfter:
			delete(m.members, id)
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

func (m *Memberlist) leave() {
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

	for _, addr := range peers {
		_ = m.send(addr, msgSync, 0, nil)
	}
	m.log.Info("left cluster", "notified", len(peers))
}

// --- message handling -----------------------------------------------------

func (m *Memberlist) handle(p transport.Packet) {
	var msg message
	if err := json.Unmarshal(p.Payload, &msg); err != nil || msg.validate() != nil {
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

	m.merge(msg.From)
	for _, mb := range msg.Members {
		m.merge(mb)
	}

	switch msg.Type {
	case msgPing:
		_ = m.send(msg.From.Addr, msgAck, msg.Seq, m.sample(maxPiggyback, msg.From.ID))
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
	case msgPull:
		_ = m.send(msg.From.Addr, msgSync, 0, m.sample(maxFullSync, msg.From.ID))
	case msgSync:
	}
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
		metrics.StateTransitions.WithLabelValues(mb.State.String()).Inc()
		m.emit(EventJoin, mb)
		m.log.Info("member discovered", "member", mb.ID, "addr", mb.Addr, "region", mb.Region, "state", mb.State)
		return
	}

	if in.Incarnation < cur.Incarnation || (in.Incarnation == cur.Incarnation && in.State <= cur.State) {
		return
	}
	cur.Incarnation, cur.Addr, cur.Region = in.Incarnation, in.Addr, in.Region
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
	metrics.StateTransitions.WithLabelValues(s.String()).Inc()
	m.emit(EventUpdate, *mb)
	m.log.Info("member state changed", "member", mb.ID, "from", prev, "to", s, "incarnation", mb.Incarnation)
}

func (m *Memberlist) emit(t EventType, mb Member) {
	select {
	case m.events <- Event{Type: t, Member: mb, At: time.Now()}:
	default:
	}
}

// --- helpers --------------------------------------------------------------

func (m *Memberlist) send(addr string, t msgType, seq uint64, members []Member) error {
	self := m.LocalNode()
	self.Since = time.Time{}
	b, err := json.Marshal(message{Type: t, Seq: seq, From: self, Members: members, SentAt: time.Now().UnixMilli()})
	if err != nil {
		return err
	}
	return m.tr.Send(addr, b)
}

// sample returns up to n member records to piggyback, always including the
// record for mustInclude (so a suspected target learns of it and refutes).
func (m *Memberlist) sample(n int, mustInclude string) []Member {
	m.mu.RLock()
	defer m.mu.RUnlock()
	all := make([]Member, 0, len(m.members))
	for _, mb := range m.members {
		c := *mb
		c.Since = time.Time{}
		all = append(all, c)
	}
	rand.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	if i := slices.IndexFunc(all, func(mb Member) bool { return mb.ID == mustInclude }); i > 0 {
		all[0], all[i] = all[i], all[0]
	}
	if len(all) > n {
		all = all[:n]
	}
	return all
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
