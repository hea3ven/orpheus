package logging

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"time"
)

const (
	StatusStarted         = "started"
	StatusSuccess         = "success"
	StatusFailure         = "failure"
	StatusExpectedAbsence = "expected_absence"
)

// Span records paired debug diagnostic events for one semantic operation.
type Span struct {
	logger *slog.Logger
	msg    string
	start  time.Time
	attrs  []slog.Attr
}

// Start emits a start event and returns a span that should be finished once.
func Start(ctx context.Context, logger *slog.Logger, msg string, attrs ...slog.Attr) Span {
	span := Span{logger: logger, msg: msg, start: time.Now(), attrs: append([]slog.Attr{}, attrs...)}
	if logger != nil && logger.Enabled(ctx, slog.LevelDebug) {
		logger.LogAttrs(ctx, slog.LevelDebug, msg+" started", append(span.attrs, slog.String("status", StatusStarted))...)
	}
	return span
}

// Finish emits the finish event for a span with status, duration, and optional attrs.
func (s Span) Finish(ctx context.Context, status string, attrs ...slog.Attr) {
	if s.logger == nil || !s.logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	if status == "" {
		status = StatusSuccess
	}
	finishAttrs := make([]slog.Attr, 0, len(s.attrs)+len(attrs)+2)
	finishAttrs = append(finishAttrs, s.attrs...)
	finishAttrs = append(finishAttrs,
		slog.String("status", status),
		slog.Int64("duration_ms", time.Since(s.start).Milliseconds()),
	)
	finishAttrs = append(finishAttrs, attrs...)
	s.logger.LogAttrs(ctx, slog.LevelDebug, s.msg+" finished", finishAttrs...)
}

// FinishError emits a finish event and derives success/failure from err.
func (s Span) FinishError(ctx context.Context, err error, attrs ...slog.Attr) {
	status := StatusSuccess
	if err != nil {
		status = StatusFailure
	}
	s.Finish(ctx, status, attrs...)
}

// FinishExpectedAbsence emits an expected-absence finish event when err is non-nil and expected.
func (s Span) FinishExpectedAbsence(ctx context.Context, err error, attrs ...slog.Attr) {
	if err == nil {
		s.Finish(ctx, StatusSuccess, attrs...)
		return
	}
	s.Finish(ctx, StatusExpectedAbsence, attrs...)
}

// ExitCode returns a subprocess exit code when err carries one.
func ExitCode(err error) (int, bool) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), true
	}
	return 0, false
}
