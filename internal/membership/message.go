package membership

import (
	"errors"
	"fmt"
)

type msgType string

const (
	// msgPing probes liveness; the receiver answers with msgAck.
	msgPing msgType = "ping"
	// msgAck answers a ping with the same sequence number.
	msgAck msgType = "ack"
	// msgSync pushes member state; no reply.
	msgSync msgType = "sync"
	// msgPull pushes member state and asks for a msgSync reply
	// (used for joining and periodic anti-entropy).
	msgPull msgType = "pull"
)

// maxMembersPerMessage bounds decoding work per packet.
const maxMembersPerMessage = 512

// message is the gossip wire format. It travels JSON-encoded inside the
// encrypted transport envelope. (A compact binary codec is a roadmap item.)
type message struct {
	Type    msgType  `json:"t"`
	Seq     uint64   `json:"seq,omitempty"`
	From    Member   `json:"from"`
	Members []Member `json:"m,omitempty"`
	// SentAt (unix ms) lets receivers reject stale or replayed packets
	// outside the configured clock-skew window.
	SentAt int64 `json:"ts"`
}

func (m *message) validate() error {
	switch m.Type {
	case msgPing, msgAck, msgSync, msgPull:
	default:
		return fmt.Errorf("membership: unknown message type %q", m.Type)
	}
	if err := m.From.validate(); err != nil {
		return err
	}
	if len(m.Members) > maxMembersPerMessage {
		return errors.New("membership: too many members in message")
	}
	for _, mb := range m.Members {
		if err := mb.validate(); err != nil {
			return err
		}
	}
	return nil
}
