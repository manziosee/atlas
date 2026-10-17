// Package gossip will carry application-level state (service registrations,
// routing data) over the epidemic broadcast layer, separate from membership.
//
// Phase 2 roadmap:
//   - Bounded broadcast queue with retransmit limit λ·log(n) per update
//   - Push-pull anti-entropy over TCP for large state
//   - CRDT merge (LWW-register / OR-set) for conflict-free replication
package gossip
