# Security Policy

## Reporting a vulnerability

**Do not open a public issue.** Use GitHub's
[private vulnerability reporting](https://docs.github.com/en/code-security/security-advisories/guidance-on-reporting-and-writing-information-about-vulnerabilities/privately-reporting-a-security-vulnerability)
on this repository, or email **manziosee@gmail.com**.

Include what you found, how to reproduce it, and the impact you think it
has. You will get an acknowledgement within 72 hours and a fix or mitigation
plan within 14 days for confirmed high/critical issues. We are happy to credit
reporters in the advisory.

| Version | Supported |
|---------|-----------|
| `main`  | ✅        |
| < 1.0   | Best effort (pre-release) |

## Threat model

Atlas runs on many machines across untrusted networks (the public internet
between regions). We assume an attacker can **observe, drop, replay, inject
and modify** any packet between nodes and can reach any exposed port.

| Asset | Threat | Control |
|-------|--------|---------|
| Gossip traffic | Eavesdropping, forging membership (e.g. declaring nodes dead), injecting rogue nodes | Every UDP datagram is sealed with **AES-256-GCM** using a cluster key; unauthenticated packets are dropped *before* parsing. AAD binds ciphertexts to the protocol (`atlas/gossip/v1`). |
| Gossip traffic | Replay of captured packets | Each message carries a send timestamp; packets outside `ATLAS_MAX_CLOCK_SKEW` are dropped. SWIM incarnation numbers make replayed *old* state lose to newer state. |
| Gossip key | Compromise / staleness | Keyring with primary + up to 3 decrypt-only keys enables zero-downtime rotation. Keys must be 256-bit random (`atlas-cli keygen`); weak or missing keys stop the node at startup. |
| Gossip parser | Memory/CPU exhaustion via crafted packets | Bounded datagram size, max 512 members per message, field length limits, bounded receive queue with drop-on-backpressure. |
| HTTP API | Unauthorized access | Bearer tokens (≥ 32 chars) stored only as SHA-256 digests, compared in **constant time** across all tokens. Optional **mTLS** (client certs required). |
| HTTP API | Downgrade / interception | **TLS 1.3 only**. Production mode refuses to start without TLS (unless explicitly declared terminated by a trusted proxy). Certificates hot-reload for short-lived certs. |
| HTTP API | Brute force, DoS, slowloris | Per-IP token-bucket rate limiting (bounded table), header/read/write/idle timeouts, 16 KiB header cap, 1 MiB body cap. |
| HTTP API | Browser-based attacks | `nosniff`, `X-Frame-Options: DENY`, strict CSP, `no-referrer`, `no-store`, HSTS on TLS. |
| Metrics | Information disclosure | Served on a separate port, intended for private networks only; K8s NetworkPolicy restricts it to the `monitoring` namespace. Route labels use mux patterns, never raw paths (no cardinality attacks). |
| Container | Escape / lateral movement | Distroless static image (no shell), non-root UID 65532, read-only root FS, all capabilities dropped, `no-new-privileges`, seccomp `RuntimeDefault`. |
| Kubernetes | Lateral movement, privilege escalation | Pod Security `restricted`, default-deny NetworkPolicies, no service-account token, PDB, resource limits. |
| Supply chain | Malicious dependency / image tampering | Minimal deps, `go mod verify`, govulncheck, CodeQL, gitleaks, image CVE scan, Dependabot, OpenSSF Scorecard; releases are multi-arch with SBOM, SLSA provenance and **keyless Cosign signatures**. |
| Secrets | Leakage via VCS or images | `.env`, keys and certs are git- and docker-ignored; CI scans history with gitleaks; K8s secrets come from generators/External Secrets, Fly.io from `fly secrets`. |

### Known limitations (tracked on the roadmap)

- **Shared-key trust model.** Any holder of the gossip key is a full cluster
  member. Per-node identities (mTLS for gossip or signed membership
  records) are planned.
- **No indirect probing yet.** A single lossy link can raise a suspicion; the
  suspicion timeout and refutation mitigate false positives. SWIM `ping-req`
  is planned for Phase 2.
- **Rate limiting trusts the TCP peer address.** Behind a proxy, all clients
  share the proxy's bucket; `X-Forwarded-For` is deliberately ignored until a
  trusted-proxy list is implemented.
- **Clock-skew window.** Replay protection depends on loosely synchronized
  clocks (NTP). Packets can be replayed within the window, but incarnation
  ordering makes that harmless for membership state.

## Verifying a release image

```bash
cosign verify ghcr.io/manziosee/atlas:<version> \
  --certificate-identity-regexp 'https://github.com/manziosee/atlas/.github/workflows/release.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com

gh attestation verify oci://ghcr.io/manziosee/atlas:<version> --owner manziosee
```
