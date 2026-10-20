// Package gossip documents application-level state replication over the
// membership protocol's epidemic transport.
//
// Service records use a revisioned LWW-register merge with a deterministic
// total order. TCP push-pull compares bounded registry digests and repairs
// mismatches with snapshots that include tombstones.
package gossip
