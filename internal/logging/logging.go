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
	logger, _, err := NewDynamic(w, level, format)
	return logger, err
}

// NewDynamic returns a logger and its mutable level. The LevelVar can be
// updated safely at runtime after configuration reload.
func NewDynamic(w io.Writer, level, format string) (*slog.Logger, *slog.LevelVar, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(strings.ToUpper(level))); err != nil {
		return nil, nil, fmt.Errorf("logging: invalid level %q", level)
	}
	levelVar := &slog.LevelVar{}
	levelVar.Set(lvl)
	opts := &slog.HandlerOptions{Level: levelVar}

	switch strings.ToLower(format) {
	case "json", "":
		return slog.New(slog.NewJSONHandler(w, opts)), levelVar, nil
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), levelVar, nil
	default:
		return nil, nil, fmt.Errorf("logging: invalid format %q (want json|text)", format)
	}
}
