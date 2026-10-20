// Package storage persists service registrations and their replicated
// revisions in SQLite. It uses a pure-Go driver so node binaries stay
// CGO-free and can run in the distroless image.
package storage
