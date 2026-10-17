// Package logging builds the structured logger used across Atlas.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// New returns a slog.Logger writing to w at the given level ("debug", "info",
// "warn", "error") in the given format ("json" or "text").
func New(w io.Writer, level, format string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(strings.ToUpper(level))); err != nil {
		return nil, fmt.Errorf("logging: invalid level %q", level)
	}
	opts := &slog.HandlerOptions{Level: lvl}

	switch strings.ToLower(format) {
	case "json", "":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("logging: invalid format %q (want json|text)", format)
	}
}
