package logging_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/logging"
)

func TestNewDefaultSuppressesDebug(t *testing.T) {
	buf := new(bytes.Buffer)
	logger := logging.New(buf, logging.Config{})

	logger.Debug("debug should not appear", "component", "test")

	if got := buf.String(); got != "" {
		t.Fatalf("default logger emitted debug output: %q", got)
	}
}

func TestNewVerboseEmitsDebug(t *testing.T) {
	buf := new(bytes.Buffer)
	logger := logging.New(buf, logging.Config{Verbose: true})

	logger.Debug("debug should appear", "component", "test")

	got := buf.String()
	if !strings.Contains(got, "level=DEBUG") {
		t.Fatalf("verbose logger output missing debug level: %q", got)
	}
	if !strings.Contains(got, "msg=\"debug should appear\"") {
		t.Fatalf("verbose logger output missing message: %q", got)
	}
}

func TestDiscardLoggerEmitsNothing(t *testing.T) {
	logger := logging.Discard()

	logger.Debug("debug should not appear", "component", "test")
	logger.Info("info should not appear", "component", "test")
	logger.Warn("warn should not appear", "component", "test")
}

func TestNewNilWriterIsSafe(t *testing.T) {
	logger := logging.New(nil, logging.Config{Verbose: true})

	logger.Debug("debug with nil writer", "component", "test")
}
