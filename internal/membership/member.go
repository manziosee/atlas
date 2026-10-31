// Package membership implements a SWIM-inspired cluster membership protocol:
// randomized probing for failure detection, suspicion with refutation via
// incarnation numbers, and epidemic (gossip) dissemination of state changes.
package membership

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"
)

// State is a member's liveness state. The numeric order is the precedence
// used when two updates carry the same incarnation number.
type State uint8

const (
	StateAlive State = iota
	StateSuspect
	StateDead
	StateLeft
)

var stateNames = [...]string{"alive", "suspect", "dead", "left"}

func (s State) String() string {
	if int(s) < len(stateNames) {
		return stateNames[s]
	}
	return "unknown"
}

// Live reports whether a member in this state may still be serving: alive,
// or suspect but not yet declared dead.
func (s State) Live() bool { return s == StateAlive || s == StateSuspect }

// LiveSet returns the IDs of self and every live member. Routing, DNS and
// write forwarding all decide "is the owner still there?" with it.
func LiveSet(self Member, members []Member) map[string]bool {
	live := make(map[string]bool, len(members)+1)
	live[self.ID] = true
	for _, member := range members {
		if member.State.Live() {
			live[member.ID] = true
		}
	}
	return live
}

// MarshalText implements encoding.TextMarshaler.
func (s State) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (s *State) UnmarshalText(b []byte) error {
	for i, n := range stateNames {
		if n == string(b) {
			*s = State(i)
			return nil
		}
	}
	return fmt.Errorf("membership: unknown state %q", b)
}

// Member describes one node as seen by the local node.
type Member struct {
	ID          string `json:"id"`
	Addr        string `json:"addr"`
	Region      string `json:"region"`
	State       State  `json:"state"`
	Incarnation uint64 `json:"incarnation"`
	// APIAddr is the base URL of the member's HTTP API (for example
	// http://10.0.0.4:8080). Nodes use it to forward writes to the owner of a
	// service. It is empty for members running older versions.
	APIAddr string `json:"api_addr,omitempty"`
	// Since is the local time of the last state change. It is not
	// authoritative across nodes and is ignored on receipt.
	Since time.Time `json:"since,omitzero"`
}

const (
	maxIDLen     = 128
	maxRegionLen = 64
	maxAddrLen   = 256
)

func (m Member) validate() error {
	if m.ID == "" || len(m.ID) > maxIDLen {
		return errors.New("membership: invalid member id")
	}
	if len(m.Region) > maxRegionLen || len(m.Addr) > maxAddrLen || len(m.APIAddr) > maxAddrLen {
		return errors.New("membership: region or address too long")
	}
	if _, _, err := net.SplitHostPort(m.Addr); err != nil {
		return fmt.Errorf("membership: invalid member addr %q", m.Addr)
	}
	if m.APIAddr != "" && ValidateAPIAddr(m.APIAddr) != nil {
		return fmt.Errorf("membership: invalid member API address %q", m.APIAddr)
	}
	if m.State > StateLeft {
		return errors.New("membership: invalid state")
	}
	return nil
}

// ValidateAPIAddr accepts an http or https origin: scheme and host with an
// optional port, and nothing else.
func ValidateAPIAddr(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("membership: API address must be an http(s) origin such as http://10.0.0.4:8080")
	}
	return nil
}

// EventType classifies membership events.
type EventType string

const (
	EventJoin   EventType = "join"
	EventUpdate EventType = "update"
	EventReap   EventType = "reap"
)

// Event is emitted on every observed membership change. It feeds the
// dashboard event stream.
type Event struct {
	Type   EventType `json:"type"`
	Member Member    `json:"member"`
	At     time.Time `json:"at"`
}
