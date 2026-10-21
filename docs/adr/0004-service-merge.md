# ADR-0004: Deterministic service record merge

- **Status:** Accepted
- **Date:** 2026-10-20

## Context

Service records replicate asynchronously. Nodes can accept updates on separate
partitions and later exchange snapshots. A merge must converge regardless of
message order, preserve deletion tombstones, and avoid relying on synchronized
wall clocks for every comparison.

## Decision

Treat each service instance as a revisioned last-write-wins register. Compare
records by the tuple `(revision, updated_at, node_id, deleted, canonical
record)`, taking the maximum. The canonical record is JSON for the stable
service struct with its local-only `checked_at` value cleared. A missing update
time from an older peer uses a deterministic revision-derived fallback.

TCP push-pull exchanges SHA-256 digests of sorted, length-framed records. On a
mismatch, bounded snapshots include tombstones; the requester returns its
snapshot once so records present on either side are merged. Digests exclude
`checked_at`, because it is node-local health-check bookkeeping.

## Consequences

- Merge is idempotent and order independent for records with the same instance
  ID, provided all replicas use this ordering.
- Revisions are monotonic per instance; a larger revision takes precedence.
- Equal revisions resolve by update time, owner ID, deletion flag, and stable
  content ordering.
- Health-check timestamps are not replicated state.
- A full repair is bounded by 100,000 rows and the existing 16 MiB stream cap;
  oversized registries fail repair explicitly rather than silently claiming
  convergence.
- This is per-instance LWW state, not an observed-remove set for service
  identities. Concurrent reuse of one instance ID is resolved by the tuple.
