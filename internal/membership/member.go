// Package membership implements a SWIM-inspired cluster membership protocol:
// randomized probing for failure detection, suspicion with refutation via
// incarnation numbers, and epidemic (gossip) dissemination of state changes.
package membership

import (
	"errors"
	"fmt"
	"net"
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
	if len(m.Region) > maxRegionLen || len(m.Addr) > maxAddrLen {
		return errors.New("membership: region or address too long")
	}
	if _, _, err := net.SplitHostPort(m.Addr); err != nil {
		return fmt.Errorf("membership: invalid member addr %q", m.Addr)
	}
	if m.State > StateLeft {
		return errors.New("membership: invalid state")
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
