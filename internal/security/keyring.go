// Package security holds Atlas' cryptographic and HTTP hardening primitives:
// the gossip keyring (AES-256-GCM), TLS/mTLS configuration with hot reload,
// bearer-token authentication, per-client rate limiting and security headers.
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
)

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

// wireVersion is the first byte of every sealed message, allowing the
// envelope format to evolve without ambiguity.
const wireVersion byte = 1

// MaxKeys bounds the keyring so retired keys do not accumulate forever.
const MaxKeys = 4

var (
	// ErrDecrypt is returned when no key in the ring authenticates a message.
	// It deliberately carries no detail to avoid acting as an oracle.
	ErrDecrypt = errors.New("security: message authentication failed")

	errShort   = errors.New("security: message too short")
	errVersion = errors.New("security: unsupported envelope version")
)

// Keyring encrypts with a single primary key and decrypts with any key in the
// ring, enabling zero-downtime key rotation:
//
//  1. Add the new key as secondary on every node (all nodes can now read it).
//  2. Promote it to primary on every node (all nodes now write with it).
//  3. Remove the old key.
//
// Envelope: version(1) || nonce(12) || ciphertext || tag(16).
type Keyring struct {
	mu    sync.RWMutex
	aeads []cipher.AEAD
}

// NewKeyring builds a keyring; keys[0] becomes the primary.
func NewKeyring(keys ...[]byte) (*Keyring, error) {
	if len(keys) == 0 {
		return nil, errors.New("security: keyring needs at least one key")
	}
	if len(keys) > MaxKeys {
		return nil, fmt.Errorf("security: keyring holds at most %d keys", MaxKeys)
	}
	kr := &Keyring{}
	for i, k := range keys {
		a, err := newAEAD(k)
		if err != nil {
			return nil, fmt.Errorf("security: key %d: %w", i, err)
		}
		kr.aeads = append(kr.aeads, a)
	}
	return kr, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Rotate installs key as the new primary, keeping previous keys for
// decryption (oldest are dropped beyond MaxKeys).
func (k *Keyring) Rotate(key []byte) error {
	a, err := newAEAD(key)
	if err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.aeads = append([]cipher.AEAD{a}, k.aeads...)
	if len(k.aeads) > MaxKeys {
		k.aeads = k.aeads[:MaxKeys]
	}
	return nil
}

// Overhead is the number of bytes Seal adds to a plaintext.
func (k *Keyring) Overhead() int {
	k.mu.RLock()
	defer k.mu.RUnlock()
	a := k.aeads[0]
	return 1 + a.NonceSize() + a.Overhead()
}

// Seal encrypts and authenticates plaintext, binding it to aad (which is
// authenticated but not transmitted). A fresh random nonce is used per call.
func (k *Keyring) Seal(plaintext, aad []byte) ([]byte, error) {
	k.mu.RLock()
	a := k.aeads[0]
	k.mu.RUnlock()

	ns := a.NonceSize()
	out := make([]byte, 1+ns, 1+ns+len(plaintext)+a.Overhead())
	out[0] = wireVersion
	if _, err := rand.Read(out[1:]); err != nil {
		return nil, fmt.Errorf("security: nonce: %w", err)
	}
	return a.Seal(out, out[1:1+ns], plaintext, aad), nil //nolint:gosec // G407 false positive: nonce filled by crypto/rand above
}

// Open verifies and decrypts msg, trying the primary key first.
func (k *Keyring) Open(msg, aad []byte) ([]byte, error) {
	k.mu.RLock()
	aeads := k.aeads
	k.mu.RUnlock()

	if len(msg) < 1 {
		return nil, errShort
	}
	if msg[0] != wireVersion {
		return nil, errVersion
	}
	for _, a := range aeads {
		ns := a.NonceSize()
		if len(msg) < 1+ns+a.Overhead() {
			return nil, errShort
		}
		if pt, err := a.Open(nil, msg[1:1+ns], msg[1+ns:], aad); err == nil {
			return pt, nil
		}
	}
	return nil, ErrDecrypt
}
