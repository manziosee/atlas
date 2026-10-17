package transport

import (
	"bytes"
	"crypto/rand"
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
	if err := a.Send("127.0.0.1:1", make([]byte, MaxPayload+1)); err != ErrPayloadTooLarge {
		t.Fatalf("err = %v, want ErrPayloadTooLarge", err)
	}
}
