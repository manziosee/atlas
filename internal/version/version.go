// Package version exposes build metadata injected at link time via -ldflags.
package version

import "fmt"

// These are overwritten by the build (see Makefile and Dockerfile).
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a human-readable build identifier.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", Version, Commit, Date)
}
