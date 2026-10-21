package membership

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/manziosee/atlas/internal/storage"
)

var protobufMagic = [...]byte{'A', 'T', 'P', 'B', 1}

// marshalMessage encodes new membership packets using the versioned protobuf
// wire schema. The version marker lets receivers continue accepting legacy
// JSON packets during an upgrade.
func marshalMessage(msg message) ([]byte, error) {
	b := append([]byte(nil), protobufMagic[:]...)
	b = appendVarintField(b, 1, messageTypeCode(msg.Type))
	if msg.Seq != 0 {
		b = appendVarintField(b, 2, msg.Seq)
	}
	b = appendBytesField(b, 3, marshalMember(msg.From))
	for _, member := range msg.Members {
		b = appendBytesField(b, 4, marshalMember(member))
	}
	for _, service := range msg.Services {
		b = appendBytesField(b, 5, marshalService(service))
	}
	if msg.Target != nil {
		b = appendBytesField(b, 6, marshalMember(*msg.Target))
	}
	if msg.SentAt != 0 {
		if msg.SentAt < 0 {
			return nil, errors.New("membership: negative message timestamp")
		}
		b = appendVarintField(b, 7, uint64(msg.SentAt))
	}
	if len(msg.RegistryDigest) > 0 {
		b = appendBytesField(b, 8, msg.RegistryDigest)
	}
	if msg.PushPullReply {
		b = appendVarintField(b, 9, 1)
	}
	return b, nil
}

func unmarshalMessage(data []byte, msg *message) error {
	if len(data) >= 4 && bytes.Equal(data[:4], protobufMagic[:4]) {
		if len(data) < len(protobufMagic) || data[4] != protobufMagic[4] {
			return errors.New("membership: unsupported protobuf wire version")
		}
		return unmarshalProtoMessage(data[len(protobufMagic):], msg)
	}
	return json.Unmarshal(data, msg)
}

func messageTypeCode(t msgType) uint64 {
	switch t {
	case msgPing:
		return 1
	case msgAck:
		return 2
	case msgSync:
		return 3
	case msgPull:
		return 4
	case msgPingReq:
		return 5
	case msgPushPullRequest:
		return 6
	case msgPushPullState:
		return 7
	default:
		return 0
	}
}

func messageTypeFromCode(code uint64) (msgType, error) {
	switch code {
	case 1:
		return msgPing, nil
	case 2:
		return msgAck, nil
	case 3:
		return msgSync, nil
	case 4:
		return msgPull, nil
	case 5:
		return msgPingReq, nil
	case 6:
		return msgPushPullRequest, nil
	case 7:
		return msgPushPullState, nil
	default:
		return "", fmt.Errorf("membership: unknown protobuf message type %d", code)
	}
}

func unmarshalProtoMessage(data []byte, msg *message) error {
	for len(data) > 0 {
		number, typ, consumed := protowire.ConsumeTag(data)
		if consumed < 0 {
			return protowire.ParseError(consumed)
		}
		data = data[consumed:]
		switch number {
		case 1, 2, 7, 9:
			if typ != protowire.VarintType {
				return errors.New("membership: invalid protobuf varint field")
			}
			value, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch number {
			case 1:
				var err error
				msg.Type, err = messageTypeFromCode(value)
				if err != nil {
					return err
				}
			case 2:
				msg.Seq = value
			case 7:
				if value > 1<<63-1 {
					return errors.New("membership: message timestamp is out of range")
				}
				msg.SentAt = signedInt64FromVarint(value)
			case 9:
				if value != 1 {
					return errors.New("membership: invalid protobuf push-pull reply flag")
				}
				msg.PushPullReply = true
			}
		case 3, 4, 5, 6, 8:
			if typ != protowire.BytesType {
				return errors.New("membership: invalid protobuf bytes field")
			}
			value, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch number {
			case 3:
				if err := unmarshalMember(value, &msg.From); err != nil {
					return err
				}
			case 4:
				if len(msg.Members) >= maxMembersPerStreamMessage {
					return errors.New("membership: too many protobuf members")
				}
				var member Member
				if err := unmarshalMember(value, &member); err != nil {
					return err
				}
				msg.Members = append(msg.Members, member)
			case 5:
				if len(msg.Services) >= maxServicesPerStreamMessage {
					return errors.New("membership: too many protobuf services")
				}
				var service storage.Service
				if err := unmarshalService(value, &service); err != nil {
					return err
				}
				msg.Services = append(msg.Services, service)
			case 6:
				msg.Target = new(Member)
				if err := unmarshalMember(value, msg.Target); err != nil {
					return err
				}
			case 8:
				if len(value) != 32 {
					return errors.New("membership: invalid registry digest length")
				}
				msg.RegistryDigest = append(msg.RegistryDigest[:0], value...)
			}
		default:
			n := protowire.ConsumeFieldValue(number, typ, data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
		}
	}
	return nil
}

func marshalMember(member Member) []byte {
	var b []byte
	b = appendStringField(b, 1, member.ID)
	b = appendStringField(b, 2, member.Addr)
	if member.Region != "" {
		b = appendStringField(b, 3, member.Region)
	}
	b = appendVarintField(b, 4, uint64(member.State))
	if member.Incarnation != 0 {
		b = appendVarintField(b, 5, member.Incarnation)
	}
	return b
}

func unmarshalMember(data []byte, member *Member) error {
	for len(data) > 0 {
		number, typ, consumed := protowire.ConsumeTag(data)
		if consumed < 0 {
			return protowire.ParseError(consumed)
		}
		data = data[consumed:]
		if number == 4 || number == 5 {
			if typ != protowire.VarintType {
				return errors.New("membership: invalid protobuf member varint")
			}
			value, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			if number == 4 {
				if value > uint64(StateLeft) {
					return errors.New("membership: invalid protobuf state")
				}
				member.State = State(value)
			} else {
				member.Incarnation = value
			}
			continue
		}
		if number >= 1 && number <= 3 {
			if typ != protowire.BytesType {
				return errors.New("membership: invalid protobuf member string")
			}
			value, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch number {
			case 1:
				member.ID = string(value)
			case 2:
				member.Addr = string(value)
			case 3:
				member.Region = string(value)
			}
			continue
		}
		n := protowire.ConsumeFieldValue(number, typ, data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]
	}
	return nil
}

func marshalService(service storage.Service) []byte {
	var b []byte
	b = appendStringField(b, 1, service.ID)
	b = appendStringField(b, 2, service.Name)
	b = appendStringField(b, 3, service.NodeID)
	b = appendStringField(b, 4, service.Address)
	if service.Region != "" {
		b = appendStringField(b, 5, service.Region)
	}
	if service.Version != "" {
		b = appendStringField(b, 6, service.Version)
	}
	for _, tag := range service.Tags {
		b = appendStringField(b, 7, tag)
	}
	if service.HealthCheck != "" {
		b = appendStringField(b, 8, service.HealthCheck)
	}
	b = appendVarintField(b, 9, healthCode(service.Health))
	if service.Revision != 0 {
		b = appendVarintField(b, 10, service.Revision)
	}
	if service.Deleted {
		b = appendVarintField(b, 11, 1)
	}
	if !service.UpdatedAt.IsZero() {
		b = appendVarintField(b, 12, uint64(service.UpdatedAt.UnixNano()))
	}
	if !service.CheckedAt.IsZero() {
		b = appendVarintField(b, 13, uint64(service.CheckedAt.UnixNano()))
	}
	return b
}

func unmarshalService(data []byte, service *storage.Service) error {
	for len(data) > 0 {
		number, typ, consumed := protowire.ConsumeTag(data)
		if consumed < 0 {
			return protowire.ParseError(consumed)
		}
		data = data[consumed:]
		if number >= 1 && number <= 8 {
			if typ != protowire.BytesType {
				return errors.New("membership: invalid protobuf service string")
			}
			value, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			text := string(value)
			switch number {
			case 1:
				service.ID = text
			case 2:
				service.Name = text
			case 3:
				service.NodeID = text
			case 4:
				service.Address = text
			case 5:
				service.Region = text
			case 6:
				service.Version = text
			case 7:
				service.Tags = append(service.Tags, text)
			case 8:
				service.HealthCheck = text
			}
			continue
		}
		if number >= 9 && number <= 13 {
			if typ != protowire.VarintType {
				return errors.New("membership: invalid protobuf service varint")
			}
			value, n := protowire.ConsumeVarint(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			switch number {
			case 9:
				var err error
				service.Health, err = healthFromCode(value)
				if err != nil {
					return err
				}
			case 10:
				service.Revision = value
			case 11:
				if value > 1 {
					return errors.New("membership: invalid protobuf deleted flag")
				}
				service.Deleted = value == 1
			case 12:
				service.UpdatedAt = time.Unix(0, signedInt64FromVarint(value)).UTC()
			case 13:
				service.CheckedAt = time.Unix(0, signedInt64FromVarint(value)).UTC()
			}
			continue
		}
		n := protowire.ConsumeFieldValue(number, typ, data)
		if n < 0 {
			return protowire.ParseError(n)
		}
		data = data[n:]
	}
	return nil
}

// signedInt64FromVarint preserves the two's-complement bit pattern used by
// protobuf int64 timestamp fields.
func signedInt64FromVarint(value uint64) int64 {
	return int64(value) // #nosec G115 -- protobuf int64 uses two's-complement varints.
}

func healthCode(health storage.Health) uint64 {
	switch health {
	case storage.HealthUnknown:
		return 1
	case storage.HealthHealthy:
		return 2
	case storage.HealthUnhealthy:
		return 3
	default:
		return 0
	}
}

func healthFromCode(code uint64) (storage.Health, error) {
	switch code {
	case 1:
		return storage.HealthUnknown, nil
	case 2:
		return storage.HealthHealthy, nil
	case 3:
		return storage.HealthUnhealthy, nil
	default:
		return "", fmt.Errorf("membership: invalid protobuf service health %d", code)
	}
}

func appendVarintField(dst []byte, number protowire.Number, value uint64) []byte {
	dst = protowire.AppendTag(dst, number, protowire.VarintType)
	return protowire.AppendVarint(dst, value)
}

func appendStringField(dst []byte, number protowire.Number, value string) []byte {
	return appendBytesField(dst, number, []byte(value))
}

func appendBytesField(dst []byte, number protowire.Number, value []byte) []byte {
	dst = protowire.AppendTag(dst, number, protowire.BytesType)
	return protowire.AppendBytes(dst, value)
}
