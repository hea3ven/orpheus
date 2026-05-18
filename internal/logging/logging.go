package logging

import (
	"context"
	"io"
	"log/slog"
)

// Config is the MVP logging configuration passed in by the CLI layer.
type Config struct {
	// Verbose enables debug-level application diagnostics.
	Verbose bool
}

// New constructs an application diagnostic logger for the supplied writer.
//
// The CLI should pass stderr as the writer so command output remains separate
// from diagnostics. A nil writer is treated as io.Discard to keep tests and
// non-terminal callers safe by default.
func New(w io.Writer, cfg Config) *slog.Logger {
	if w == nil {
		w = io.Discard
	}

	level := slog.LevelInfo
	if cfg.Verbose {
		level = slog.LevelDebug
	}

	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
}

// Discard returns a no-op logger suitable for tests.
func Discard() *slog.Logger {
	return slog.New(discardHandler{})
}

type discardHandler struct{}

func (discardHandler) Enabled(context.Context, slog.Level) bool { return false }

func (discardHandler) Handle(context.Context, slog.Record) error { return nil }

func (discardHandler) WithAttrs([]slog.Attr) slog.Handler { return discardHandler{} }

func (discardHandler) WithGroup(string) slog.Handler { return discardHandler{} }
