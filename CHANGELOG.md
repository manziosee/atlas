# Changelog

All notable changes to Atlas are listed here. Versions follow
[Semantic Versioning](https://semver.org/); before 1.0, a minor version may
include breaking changes, which are called out.

## [Unreleased]

## [0.1.0] - 2026-11-15

The first tagged release of Atlas: a service registry and discovery system
in a single Go binary, with no external database or consensus cluster to run.

### Cluster
- SWIM-style gossip membership with indirect probes, suspicion, refutation and
  encrypted, authenticated packets; TCP anti-entropy keeps replicas converged.
- Every node holds the full registry in SQLite and serves reads locally;
  writes go to the owning node and are forwarded from any node.
- Node admission allowlist, eviction of failed nodes, and a zero-setup dev
  mode that generates its own key and token.

### Registry
- Register, heartbeat (TTL), update with revision checks (`If-Match`),
  replace, drain and deregister, with idempotency keys for safe retries.
- HTTP, HTTPS, TCP and gRPC health checks with per-service intervals and
  thresholds.
- Key/value metadata on every instance, filterable with `meta=key:value`.
- Per-service and cluster-wide change history.

### Routing
- Health-aware lookups with region affinity, consistent hashing, weighted
  canary routing, stale-owner policies and latency-aware selection with a 5 ms
  default tolerance.
- DNS discovery (A, AAAA and SRV) and client-side load balancing in Go,
  including a gRPC resolver.

### Integrations
- Envoy control plane over REST xDS v3 (CDS and EDS).
- Signed webhooks for service and membership events, with retries.
- Resumable Server-Sent Events and gRPC watch streams.
- Kubernetes controller and Helm chart, a Terraform provider, and a Docker
  agent that registers containers from their labels.

### Clients
- Go, Python, Node.js/TypeScript, Java and C#/.NET clients covering the whole
  API.

### Operations and security
- TLS and mutual TLS with certificate reloading, scoped and expiring API
  credentials created at runtime, rate limiting and an admin audit log.
- Online backups, Prometheus metrics and structured logs.
- Atlas Console: a web UI for the cluster map, services, live activity,
  lookups and node administration.
- Signed multi-architecture container images with SBOMs and build provenance.

[Unreleased]: https://github.com/manziosee/atlas/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/manziosee/atlas/releases/tag/v0.1.0
