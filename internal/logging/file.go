package logging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// appendWriter opens only while writing an event, so commands need no logging
// shutdown hook (including on errors). O_APPEND and one Write per event allow
// overlapping Orpheus processes to share the file without truncation.
type appendWriter struct {
	path   string
	stderr io.Writer
	mu     sync.Mutex
	failed bool
}

func (w *appendWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failed {
		return len(data), nil
	}
	if err := appendFileEvent(w.path, data); err != nil {
		w.failed = true
		_, _ = fmt.Fprintf(w.stderr, "Warning: file logging disabled for %s: %v\n", w.path, err)
	}
	// Diagnostics must never interrupt the command, even after storage failure.
	return len(data), nil
}

func appendFileEvent(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	n, err := file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return errors.Join(err, file.Close())
}

type fileHandler struct {
	writer io.Writer
	level  slog.Level
	attrs  []slog.Attr
	group  string
}

func (h *fileHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *fileHandler) Handle(_ context.Context, record slog.Record) error {
	attrs := append([]slog.Attr{}, h.attrs...)
	record.Attrs(func(attr slog.Attr) bool {
		attrs = flattenAttr(attrs, h.group, attr)
		return true
	})
	var component, operation string
	var details strings.Builder
	for _, attr := range attrs {
		switch attr.Key {
		case "component":
			component = attr.Value.String()
		case "operation":
			operation = attr.Value.String()
		default:
			details.WriteByte(' ')
			details.WriteString(quoteToken(attr.Key))
			details.WriteByte('=')
			details.WriteString(quoteToken(attr.Value.String()))
		}
	}
	var line strings.Builder
	fmt.Fprintf(&line, "%s %-5s [%d] ", record.Time.UTC().Format("2006-01-02 15:04:05.000Z"), record.Level, os.Getpid())
	if component != "" || operation != "" {
		label := component
		if operation != "" {
			if label != "" {
				label += "/"
			}
			label += operation
		}
		line.WriteString(quoteToken(label))
		line.WriteString(": ")
	}
	message := record.Message
	if strings.ContainsAny(message, "\r\n\t|\"\\") || strings.ContainsFunc(message, unicode.IsControl) {
		message = strconv.Quote(message)
	}
	line.WriteString(message)
	if details.Len() > 0 {
		line.WriteString(" |")
		line.WriteString(details.String())
	}
	line.WriteByte('\n')
	_, err := h.writer.Write([]byte(line.String()))
	return err
}

func (h *fileHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append([]slog.Attr{}, h.attrs...)
	for _, attr := range attrs {
		clone.attrs = flattenAttr(clone.attrs, h.group, attr)
	}
	return &clone
}

func (h *fileHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	clone := *h
	clone.group += name + "."
	return &clone
}

func flattenAttr(attrs []slog.Attr, group string, attr slog.Attr) []slog.Attr {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return attrs
	}
	if attr.Value.Kind() == slog.KindGroup {
		if attr.Key != "" {
			group += attr.Key + "."
		}
		for _, child := range attr.Value.Group() {
			attrs = flattenAttr(attrs, group, child)
		}
		return attrs
	}
	attr.Key = group + attr.Key
	return append(attrs, attr)
}

func quoteToken(value string) string {
	if value == "" || strings.ContainsAny(value, "=\"\\|[]") || strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) {
		return strconv.Quote(value)
	}
	return value
}

// fanoutHandler filters each destination independently, including when only
// the file accepts DEBUG events. Logger.Enabled must not use the terminal alone.
type fanoutHandler []slog.Handler

func (h fanoutHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h fanoutHandler) Handle(ctx context.Context, record slog.Record) error {
	for _, handler := range h {
		if handler.Enabled(ctx, record.Level) {
			_ = handler.Handle(ctx, record.Clone())
		}
	}
	return nil
}

func (h fanoutHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := make(fanoutHandler, len(h))
	for i, handler := range h {
		clone[i] = handler.WithAttrs(attrs)
	}
	return clone
}

func (h fanoutHandler) WithGroup(name string) slog.Handler {
	clone := make(fanoutHandler, len(h))
	for i, handler := range h {
		clone[i] = handler.WithGroup(name)
	}
	return clone
}
