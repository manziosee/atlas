# ADR-0002: Authenticated encryption for all gossip (AES-256-GCM keyring)

- **Status:** Accepted
- **Date:** 2026-10-18

## Context

Gossip crosses the public internet between regions. Unauthenticated
membership traffic would let anyone declare nodes dead, inject fake nodes or
map the cluster topology.

## Decision

- Seal every datagram with **AES-256-GCM** (AEAD): confidentiality and
  integrity in one primitive, hardware accelerated (AES-NI / ARMv8 CE).
- Envelope: `version(1) ‖ nonce(12, random) ‖ ciphertext ‖ tag(16)`.
- AAD = `atlas/gossip/v1` so ciphertexts cannot be confused with another
  protocol sharing the key.
- **Keyring**: 1 primary (encrypt) + up to 3 secondary (decrypt-only) keys for
  rolling rotation without downtime.
- Decrypt **before** parse: unauthenticated bytes never reach the decoder.
- Timestamp in every message, rejected outside `ATLAS_MAX_CLOCK_SKEW`.

## Consequences

- ✅ Rogue hosts without the key cannot affect the cluster.
- ✅ Rotation is three config pushes, no restart storm.
- ⚠️ 29 bytes overhead per packet.
- ⚠️ Random 96-bit nonces: safe for ~2³² messages per key; rotate keys
  periodically (we recommend quarterly, or at once if a key leaks).
- ⚠️ Shared key = flat trust. Per-node identity is a future ADR.

## Alternatives considered

- **DTLS per peer:** per-node identity, but needs handshakes with n peers and
  state per pair, which works against gossip's connectionless model.
- **WireGuard mesh only:** good where it exists (Fly.io 6PN), but leaves
  Atlas insecure everywhere else. We do both: defense in depth.
- **ChaCha20-Poly1305:** equally strong; AES-GCM chosen for hardware
  acceleration on our server targets. The version byte allows switching.
