package membership

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/manziosee/atlas/internal/storage"
)

func TestMessageCodecRoundTrip(t *testing.T) {
	checked := time.Unix(1_700_000_001, 123).UTC()
	updated := time.Unix(1_700_000_000, 456).UTC()
	target := Member{ID: "target", Addr: "127.0.0.1:7947", Region: "eu", State: StateSuspect, Incarnation: 9}
	credentialDigest := sha256.Sum256([]byte("codec credential"))
	want := message{
		Type: msgPushPullState, Seq: 42,
		RegistryDigest: bytes.Repeat([]byte{0x5a}, 32), PushPullReply: true,
		From:    Member{ID: "sender", Addr: "127.0.0.1:7946", APIAddr: "https://127.0.0.1:8080", Region: "af", State: StateAlive, Incarnation: 8},
		Members: []Member{{ID: "member", Addr: "127.0.0.1:7948", State: StateDead, Incarnation: 10}},
		Services: []storage.Service{
			{ID: "svc-1", Name: "api", NodeID: "sender", Address: "127.0.0.1:8080", Region: "af", Version: "v2", Tags: []string{"blue", "public"}, HealthCheck: "http://127.0.0.1:8080/health", Health: storage.HealthUnhealthy, Revision: 11, Deleted: true, CheckIntervalSeconds: 7, UnhealthyThreshold: 4, HealthyThreshold: 3, Draining: true, TrafficWeight: 17, UpdatedAt: updated, CheckedAt: checked},
			{ID: "svc-2", Name: "api", NodeID: "sender", Address: "127.0.0.1:8081", Region: "af", Health: storage.HealthUnknown, Revision: 12, TTLSeconds: 30},
		},
		Credentials: []storage.ManagedCredential{{
			ID: hex.EncodeToString(credentialDigest[:12]), Name: "deployment", Digest: credentialDigest[:],
			Scopes: []string{"read", "write"}, CreatedAt: updated, UpdatedAt: updated, ExpiresAt: updated.Add(time.Hour),
		}},
		CredentialDigest: bytes.Repeat([]byte{0x6b}, 32),
		Target:           &target, SentAt: 1_700_000_002_000,
	}
	wire, err := marshalMessage(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(wire, protobufMagic[:]) {
		t.Fatalf("wire missing protobuf version prefix: %x", wire[:min(len(wire), len(protobufMagic))])
	}
	var got message
	if err := unmarshalMessage(wire, &got); err != nil {
		t.Fatal(err)
	}
	if err := got.validate(); err != nil {
		t.Fatalf("decoded message is invalid: %v", err)
	}
	if got.Type != want.Type || got.Seq != want.Seq || got.From != want.From || got.SentAt != want.SentAt || !bytes.Equal(got.RegistryDigest, want.RegistryDigest) || !bytes.Equal(got.CredentialDigest, want.CredentialDigest) || got.PushPullReply != want.PushPullReply || len(got.Members) != 1 || got.Members[0] != want.Members[0] || len(got.Services) != 2 || len(got.Credentials) != 1 {
		t.Fatalf("message header/member mismatch: got %#v", got)
	}
	if got.Credentials[0].ID != want.Credentials[0].ID || !bytes.Equal(got.Credentials[0].Digest, want.Credentials[0].Digest) || got.Credentials[0].Name != want.Credentials[0].Name || len(got.Credentials[0].Scopes) != 2 {
		t.Fatalf("managed credential mismatch: got %#v", got.Credentials[0])
	}
	if got.Target == nil || *got.Target != target {
		t.Fatalf("target mismatch: got %#v", got.Target)
	}
	gotService := got.Services[0]
	wantService := want.Services[0]
	if gotService.ID != wantService.ID || gotService.Name != wantService.Name || gotService.NodeID != wantService.NodeID || gotService.Address != wantService.Address || gotService.Region != wantService.Region || gotService.Version != wantService.Version || gotService.HealthCheck != wantService.HealthCheck || gotService.Health != wantService.Health || gotService.Revision != wantService.Revision || gotService.Deleted != wantService.Deleted || gotService.TTLSeconds != 0 || gotService.CheckIntervalSeconds != 7 || gotService.UnhealthyThreshold != 4 || gotService.HealthyThreshold != 3 || !gotService.Draining || gotService.TrafficWeight != 17 || !gotService.UpdatedAt.Equal(updated) || !gotService.CheckedAt.Equal(checked) || len(gotService.Tags) != 2 || gotService.Tags[0] != "blue" || gotService.Tags[1] != "public" {
		t.Fatalf("service mismatch: got %#v", gotService)
	}
	if got.Services[1].TTLSeconds != 30 || got.Services[1].ID != "svc-2" {
		t.Fatalf("TTL service mismatch: got %#v", got.Services[1])
	}
}

func TestMessageCodecLegacyJSONAndVersion(t *testing.T) {
	want := message{Type: msgPing, From: Member{ID: "legacy", Addr: "127.0.0.1:7946", State: StateAlive, Incarnation: 1}, SentAt: 1}
	legacy, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got message
	if err := unmarshalMessage(legacy, &got); err != nil || got.Type != want.Type || got.From != want.From {
		t.Fatalf("legacy JSON decode got %#v, err %v", got, err)
	}
	badVersion := append([]byte(nil), protobufMagic[:]...)
	badVersion[4]++
	if err := unmarshalMessage(badVersion, &message{}); err == nil {
		t.Fatal("unsupported protobuf version accepted")
	}
}

func TestServiceMetadataCodec(t *testing.T) {
	service := storage.Service{
		ID: "svc-1", Name: "api", NodeID: "n1", Address: "10.0.0.1:80", Region: "af", Health: storage.HealthHealthy,
		Revision: 3, TrafficWeight: 1, Metadata: map[string]string{"team": "payments", "protocol": "grpc", "empty": ""},
	}
	encoded := marshalService(service)
	if !bytes.Equal(encoded, marshalService(service)) {
		t.Fatal("metadata encoding is not deterministic")
	}
	var decoded storage.Service
	if err := unmarshalService(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Metadata) != 3 || decoded.Metadata["team"] != "payments" || decoded.Metadata["protocol"] != "grpc" {
		t.Fatalf("decoded metadata = %v", decoded.Metadata)
	}
	if value, ok := decoded.Metadata["empty"]; !ok || value != "" {
		t.Fatalf("empty metadata value lost: %v", decoded.Metadata)
	}

	var plain storage.Service
	if err := unmarshalService(marshalService(storage.Service{ID: "x", Name: "x", NodeID: "n", Address: "1.2.3.4:5", Health: storage.HealthUnknown}), &plain); err != nil || plain.Metadata != nil {
		t.Fatalf("service without metadata decoded as %v, err %v", plain.Metadata, err)
	}

	bad := marshalService(storage.Service{ID: "x", Name: "x", NodeID: "n", Address: "1.2.3.4:5", Health: storage.HealthUnknown})
	var entry []byte
	entry = appendStringField(entry, 1, "bad key")
	entry = appendStringField(entry, 2, "v")
	bad = appendBytesField(bad, 20, entry)
	if err := unmarshalService(bad, &storage.Service{}); err == nil {
		t.Fatal("invalid metadata key from a peer was accepted")
	}
}
