// Package storage will persist the local service registry in SQLite
// (modernc.org/sqlite, pure Go, so builds stay CGO-free and the runtime
// image stays distroless/static).
//
// Phase 3 roadmap:
//   - Schema migrations embedded with go:embed
//   - WAL mode, synchronous=NORMAL, busy_timeout
//   - Snapshot + restore for fast node bootstrap
package storage
