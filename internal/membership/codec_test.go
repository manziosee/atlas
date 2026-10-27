package membership

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/manziosee/atlas/internal/storage"
)

func TestMessageCodecRoundTrip(t *testing.T) {
	checked := time.Unix(1_700_000_001, 123).UTC()
	updated := time.Unix(1_700_000_000, 456).UTC()
	target := Member{ID: "target", Addr: "127.0.0.1:7947", Region: "eu", State: StateSuspect, Incarnation: 9}
	want := message{
		Type: msgPushPullState, Seq: 42,
		RegistryDigest: bytes.Repeat([]byte{0x5a}, 32), PushPullReply: true,
		From:     Member{ID: "sender", Addr: "127.0.0.1:7946", APIAddr: "https://127.0.0.1:8080", Region: "af", State: StateAlive, Incarnation: 8},
		Members:  []Member{{ID: "member", Addr: "127.0.0.1:7948", State: StateDead, Incarnation: 10}},
		Services: []storage.Service{{ID: "svc-1", Name: "api", NodeID: "sender", Address: "127.0.0.1:8080", Region: "af", Version: "v2", Tags: []string{"blue", "public"}, HealthCheck: "http://127.0.0.1:8080/health", Health: storage.HealthUnhealthy, Revision: 11, Deleted: true, TTLSeconds: 30, CheckIntervalSeconds: 7, UnhealthyThreshold: 4, HealthyThreshold: 3, Draining: true, UpdatedAt: updated, CheckedAt: checked}},
		Target:   &target, SentAt: 1_700_000_002_000,
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
	if got.Type != want.Type || got.Seq != want.Seq || got.From != want.From || got.SentAt != want.SentAt || !bytes.Equal(got.RegistryDigest, want.RegistryDigest) || got.PushPullReply != want.PushPullReply || len(got.Members) != 1 || got.Members[0] != want.Members[0] || len(got.Services) != 1 {
		t.Fatalf("message header/member mismatch: got %#v", got)
	}
	if got.Target == nil || *got.Target != target {
		t.Fatalf("target mismatch: got %#v", got.Target)
	}
	gotService := got.Services[0]
	wantService := want.Services[0]
	if gotService.ID != wantService.ID || gotService.Name != wantService.Name || gotService.NodeID != wantService.NodeID || gotService.Address != wantService.Address || gotService.Region != wantService.Region || gotService.Version != wantService.Version || gotService.HealthCheck != wantService.HealthCheck || gotService.Health != wantService.Health || gotService.Revision != wantService.Revision || gotService.Deleted != wantService.Deleted || gotService.TTLSeconds != 30 || gotService.CheckIntervalSeconds != 7 || gotService.UnhealthyThreshold != 4 || gotService.HealthyThreshold != 3 || !gotService.Draining || !gotService.UpdatedAt.Equal(updated) || !gotService.CheckedAt.Equal(checked) || len(gotService.Tags) != 2 || gotService.Tags[0] != "blue" || gotService.Tags[1] != "public" {
		t.Fatalf("service mismatch: got %#v", gotService)
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
