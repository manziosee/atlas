package security

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func key(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	kr, err := NewKeyring(key(t))
	if err != nil {
		t.Fatal(err)
	}
	aad := []byte("atlas/test")
	msg := []byte("hello cluster")

	sealed, err := kr.Seal(msg, aad)
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed) != len(msg)+kr.Overhead() {
		t.Fatalf("sealed length %d, want %d", len(sealed), len(msg)+kr.Overhead())
	}
	got, err := kr.Open(sealed, aad)
	if err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestNonceUniqueness(t *testing.T) {
	kr, _ := NewKeyring(key(t))
	a, _ := kr.Seal([]byte("x"), nil)
	b, _ := kr.Seal([]byte("x"), nil)
	if bytes.Equal(a, b) {
		t.Fatal("identical ciphertexts for identical plaintexts: nonce reuse")
	}
}

func TestOpenRejectsTampering(t *testing.T) {
	kr, _ := NewKeyring(key(t))
	sealed, _ := kr.Seal([]byte("payload"), []byte("aad"))

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 0xff
	if _, err := kr.Open(tampered, []byte("aad")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("tampered ciphertext: err = %v, want ErrDecrypt", err)
	}
	if _, err := kr.Open(sealed, []byte("other-aad")); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("wrong aad: err = %v, want ErrDecrypt", err)
	}
	if _, err := kr.Open([]byte{wireVersion, 1, 2}, nil); err == nil {
		t.Fatal("short message accepted")
	}
}

func TestOpenRejectsForeignKey(t *testing.T) {
	a, _ := NewKeyring(key(t))
	b, _ := NewKeyring(key(t))
	sealed, _ := a.Seal([]byte("secret"), nil)
	if _, err := b.Open(sealed, nil); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("err = %v, want ErrDecrypt", err)
	}
}

func TestRotation(t *testing.T) {
	oldKey, newKey := key(t), key(t)
	sender, _ := NewKeyring(oldKey)

	// Step 1: receiver learns the new key as secondary.
	receiver, _ := NewKeyring(oldKey, newKey)
	// Step 2: sender promotes the new key.
	if err := sender.Rotate(newKey); err != nil {
		t.Fatal(err)
	}
	sealed, _ := sender.Seal([]byte("rotated"), nil)
	if _, err := receiver.Open(sealed, nil); err != nil {
		t.Fatalf("receiver could not open message under rotated key: %v", err)
	}
}

func TestNewKeyringValidation(t *testing.T) {
	if _, err := NewKeyring(); err == nil {
		t.Fatal("empty keyring accepted")
	}
	if _, err := NewKeyring([]byte("short")); err == nil {
		t.Fatal("short key accepted")
	}
}
