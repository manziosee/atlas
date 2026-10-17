// Package routing will answer "where is service X?" from local state.
//
// Phase 4 roadmap:
//   - Consistent-hash ring (virtual nodes) for keyspace ownership
//   - Latency-aware selection from probe RTTs (EWMA)
//   - Region affinity: prefer healthy instances in the caller's region
package routing
