// Package events fans out membership and service-registry changes to API
// streams. Every event gets an ID of the form "<epoch>.<sequence>"; a client
// that reconnects with its last ID receives what it missed from a bounded
// in-memory backlog, or a reset when the gap cannot be filled.
package events

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/manziosee/atlas/internal/membership"
	"github.com/manziosee/atlas/internal/storage"
)

// Kinds group event types for subscription filters.
const (
	KindMember  = "member"
	KindService = "service"
)

// Service event types. Membership events keep their original types: join,
// update and reap.
const (
	ServiceRegistered   = "service.registered"
	ServiceUpdated      = "service.updated"
	ServiceHealth       = "service.health"
	ServiceDeregistered = "service.deregistered"
)

// Event is one change. Exactly one of Member and Service is set.
type Event struct {
	ID      string             `json:"id"`
	Type    string             `json:"type"`
	Kind    string             `json:"kind"`
	At      time.Time          `json:"at"`
	Member  *membership.Member `json:"member,omitempty"`
	Service *storage.Service   `json:"service,omitempty"`
}

// FromMember converts a membership event.
func FromMember(event membership.Event) Event {
	member := event.Member
	return Event{Type: string(event.Type), Kind: KindMember, At: event.At, Member: &member}
}

// FromServiceChange classifies a committed registry write. It reports false
// for writes clients cannot observe, such as a tombstone for an unknown ID.
func FromServiceChange(change storage.Change) (Event, bool) {
	kind, ok := change.Kind()
	if !ok {
		return Event{}, false
	}
	after := change.After
	after.CheckedAt, after.HeartbeatAt = time.Time{}, time.Time{}
	return Event{Type: "service." + kind, Kind: KindService, At: after.UpdatedAt, Service: &after}, true
}

// Broker assigns event IDs, keeps a ring of recent events and delivers them to
// subscribers. Publish never blocks: a subscriber that falls behind is closed
// with Lagged set, and its client resumes from its last event ID.
type Broker struct {
	epoch string

	mu     sync.Mutex
	seq    uint64
	ring   []Event
	next   int // ring index of the next write
	filled bool
	subs   map[*Subscription]struct{}
}

// Subscription receives events published after Subscribe returned.
type Subscription struct {
	C      <-chan Event
	ch     chan Event
	lagged bool
	broker *Broker
}

// Lagged reports whether the broker closed C because the reader fell behind.
// Read it only after C is closed.
func (s *Subscription) Lagged() bool {
	s.broker.mu.Lock()
	defer s.broker.mu.Unlock()
	return s.lagged
}

// Close stops delivery. It is safe to call more than once.
func (s *Subscription) Close() {
	s.broker.mu.Lock()
	defer s.broker.mu.Unlock()
	if _, ok := s.broker.subs[s]; ok {
		delete(s.broker.subs, s)
		close(s.ch)
	}
}

// NewBroker keeps the last capacity events for resumption.
func NewBroker(capacity int) *Broker {
	if capacity < 1 {
		capacity = 1
	}
	var epoch [4]byte
	_, _ = rand.Read(epoch[:])
	return &Broker{epoch: hex.EncodeToString(epoch[:]), ring: make([]Event, capacity), subs: make(map[*Subscription]struct{})}
}

// Subscribers returns the number of attached subscriptions.
func (b *Broker) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// Publish assigns the next ID and delivers event to every subscriber.
func (b *Broker) Publish(event Event) Event {
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	event.ID = b.epoch + "." + strconv.FormatUint(b.seq, 10)
	b.ring[b.next] = event
	b.next = (b.next + 1) % len(b.ring)
	if b.next == 0 {
		b.filled = true
	}
	for sub := range b.subs {
		select {
		case sub.ch <- event:
		default:
			sub.lagged = true
			delete(b.subs, sub)
			close(sub.ch)
		}
	}
	return event
}

// Subscribe registers a subscriber. With a non-empty lastID it also returns
// the retained events published after lastID. reset is true when lastID came
// from another node or process, or is older than the retained backlog; the
// caller should then re-read current state instead of replaying.
func (b *Broker) Subscribe(lastID string, buffer int) (sub *Subscription, backlog []Event, reset bool) {
	if buffer < 1 {
		buffer = 1
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan Event, buffer)
	sub = &Subscription{C: ch, ch: ch, broker: b}
	b.subs[sub] = struct{}{}
	if lastID == "" {
		return sub, nil, false
	}
	epoch, seqText, ok := strings.Cut(lastID, ".")
	last, err := strconv.ParseUint(seqText, 10, 64)
	if !ok || err != nil || epoch != b.epoch || last > b.seq {
		return sub, nil, true
	}
	retained := uint64(b.next) // #nosec G115 -- a ring index is never negative.
	if b.filled {
		retained = uint64(len(b.ring))
	}
	oldest := b.seq - retained + 1
	if last+1 < oldest {
		return sub, nil, true
	}
	for seq := last + 1; seq <= b.seq; seq++ {
		index := (b.next - int(b.seq-seq) - 1 + len(b.ring)) % len(b.ring) // #nosec G115 -- distance is below len(b.ring).
		backlog = append(backlog, b.ring[index])
	}
	return sub, backlog, false
}
