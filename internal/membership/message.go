package membership

import (
	"errors"
	"fmt"

	"github.com/manziosee/atlas/internal/storage"
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
	// msgPingReq asks a peer to probe Target on behalf of the sender.
	msgPingReq msgType = "ping-req"
	// msgPushPullRequest is exchanged over authenticated TCP for full membership sync.
	msgPushPullRequest msgType = "push-pull-request"
	// msgPushPullState carries the bounded full membership view over TCP.
	msgPushPullState msgType = "push-pull-state"
)

// maxMembersPerMessage bounds decoding work per packet.
const maxMembersPerMessage = 512
const maxServicesPerMessage = 64
const maxMembersPerStreamMessage = 100_000
const maxServicesPerStreamMessage = 100_000

// message is the gossip wire format. It travels as versioned protobuf wire
// data inside the encrypted transport envelope; receivers also accept legacy JSON.
type message struct {
	Type           msgType           `json:"t"`
	Seq            uint64            `json:"seq,omitempty"`
	From           Member            `json:"from"`
	Members        []Member          `json:"m,omitempty"`
	Services       []storage.Service `json:"s,omitempty"`
	RegistryDigest []byte            `json:"rd,omitempty"`
	PushPullReply  bool              `json:"pr,omitempty"`
	// Target is set only on ping-req messages.
	Target *Member `json:"target,omitempty"`
	// SentAt (unix ms) lets receivers reject stale or replayed packets
	// outside the configured clock-skew window.
	SentAt int64 `json:"ts"`
}

func (m *message) validate() error {
	switch m.Type {
	case msgPing, msgAck, msgSync, msgPull, msgPingReq, msgPushPullRequest, msgPushPullState:
	default:
		return fmt.Errorf("membership: unknown message type %q", m.Type)
	}
	if err := m.From.validate(); err != nil {
		return err
	}
	maxMembers := maxMembersPerMessage
	if m.Type == msgPushPullRequest || m.Type == msgPushPullState {
		maxMembers = maxMembersPerStreamMessage
	}
	if len(m.Members) > maxMembers {
		return errors.New("membership: too many members in message")
	}
	for _, mb := range m.Members {
		if err := mb.validate(); err != nil {
			return err
		}
	}
	maxServices := maxServicesPerMessage
	if m.Type == msgPushPullRequest || m.Type == msgPushPullState {
		maxServices = maxServicesPerStreamMessage
		if len(m.RegistryDigest) != 32 {
			return errors.New("membership: full state missing registry digest")
		}
		if m.Type == msgPushPullRequest && len(m.Services) != 0 {
			return errors.New("membership: push-pull request must not contain services")
		}
		if m.Type != msgPushPullState && m.PushPullReply {
			return errors.New("membership: reply flag is only valid on push-pull state")
		}
	}
	if len(m.Services) > maxServices {
		return errors.New("membership: too many services in message")
	}
	for _, service := range m.Services {
		if err := service.Validate(); err != nil {
			return fmt.Errorf("membership: invalid service record: %w", err)
		}
	}
	if m.Type == msgPingReq {
		if m.Target == nil {
			return errors.New("membership: ping-req missing target")
		}
		if err := m.Target.validate(); err != nil {
			return err
		}
	}
	return nil
}
