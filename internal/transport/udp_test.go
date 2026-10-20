package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/manziosee/atlas/internal/security"
)

func newKeyring(t *testing.T) *security.Keyring {
	t.Helper()
	k := make([]byte, security.KeySize)
	_, _ = rand.Read(k)
	kr, err := security.NewKeyring(k)
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

func listen(t *testing.T, kr *security.Keyring) *UDP {
	t.Helper()
	u, err := Listen("127.0.0.1:0", kr, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = u.Close() })
	return u
}

func TestSendReceive(t *testing.T) {
	kr := newKeyring(t)
	a, b := listen(t, kr), listen(t, kr)

	if err := a.Send(b.LocalAddr().String(), []byte("ping")); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-b.Packets():
		if !bytes.Equal(p.Payload, []byte("ping")) {
			t.Fatalf("payload %q", p.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for packet")
	}
}

func TestSendStreamReceive(t *testing.T) {
	kr := newKeyring(t)
	a, b := listen(t, kr), listen(t, kr)
	if err := a.SendStream(context.Background(), b.LocalAddr().String(), []byte("full state")); err != nil {
		t.Fatal(err)
	}
	select {
	case packet := <-b.Packets():
		if !packet.Stream || !bytes.Equal(packet.Payload, []byte("full state")) {
			t.Fatalf("stream packet = %+v, want authenticated full-state payload", packet)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for TCP stream packet")
	}
}

func TestDropsPacketsFromForeignCluster(t *testing.T) {
	a, b := listen(t, newKeyring(t)), listen(t, newKeyring(t))
	if err := a.Send(b.LocalAddr().String(), []byte("intruder")); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-b.Packets():
		t.Fatalf("accepted packet from foreign key: %q", p.Payload)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestRejectsOversizePayload(t *testing.T) {
	a := listen(t, newKeyring(t))
	if err := a.Send("127.0.0.1:1", make([]byte, MaxPayload+1)); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("err = %v, want ErrPayloadTooLarge", err)
	}
}

func TestDropsOversizeAuthenticatedDatagram(t *testing.T) {
	kr := newKeyring(t)
	sender, receiver := listen(t, kr), listen(t, kr)
	sealed, err := kr.Seal(make([]byte, MaxPayload+1), aad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sender.conn.WriteToUDP(sealed, receiver.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	select {
	case packet := <-receiver.Packets():
		t.Fatalf("received oversized authenticated payload (%d bytes)", len(packet.Payload))
	case <-time.After(100 * time.Millisecond):
	}
}
