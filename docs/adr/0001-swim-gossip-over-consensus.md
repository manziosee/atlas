# ADR-0001: SWIM-style gossip instead of centralized consensus

- **Status:** Accepted
- **Date:** 2026-10-18

## Context

Atlas answers "where does service X run?" for nodes spread across continents
(JNB, FRA, SIN, IAD). Cross-region RTTs are 150–300 ms and links partition.
Discovery must keep working in every region during a partition, and a lookup
must never pay a cross-continent round trip.

## Decision

Use a **SWIM-inspired membership protocol** with epidemic dissemination:

- Every node keeps a full local copy of membership state. Reads are local.
- Failure detection: each period, probe one random member and suspect it if
  no ack arrives. Suspects become dead after a timeout unless they refute.
- Refutation through **incarnation numbers**: only the node itself can
  increase its incarnation, so it can always override rumors about itself.
- Dissemination: state piggybacks on probes, plus push gossip to `k` random
  peers and periodic push-pull anti-entropy.

## Consequences

- ✅ O(1) per-node probe load, O(log n) rounds to spread an update.
- ✅ Available under partition (AP): each side keeps serving its view.
- ✅ No leader, no quorum, no single point of failure.
- ⚠️ Eventually consistent: nodes briefly disagree after changes.
- ⚠️ Application state needs conflict resolution (planned CRDTs, ADR TBD).

## Alternatives considered

- **Raft/etcd/Consul servers:** strong consistency, but every write (and
  linearizable read) crosses regions and a minority partition stops writing.
- **All-to-all heartbeats:** O(n²) traffic; does not scale past tens of nodes.
- **hashicorp/memberlist:** production-grade, but building the protocol is the
  point of this project. Its design (Lifeguard) is our reference.
