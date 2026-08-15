//go:build integration

package beads_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/beads"
)

func TestIntegrationCommandRunnerSanitizesBeadsEnvironment(t *testing.T) {
	t.Parallel()

	binDir := t.TempDir()
	bin := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf 'BEADS_DIR=%s\\n' \"${BEADS_DIR-unset}\"\nprintf 'BD_NON_INTERACTIVE=%s\\n' \"${BD_NON_INTERACTIVE-unset}\"\n"), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	result, err := beads.CommandRunner{
		Environment: []string{"BEADS_DIR=/tmp/wrong", "PATH=" + binDir},
	}.Run(t.TempDir(), "context")
	if err != nil {
		t.Fatalf("run fake bd: %v", err)
	}
	if strings.Contains(result.Stdout, "BEADS_DIR=/tmp/wrong") || !strings.Contains(result.Stdout, "BEADS_DIR=unset") {
		t.Fatalf("stdout = %q, want sanitized BEADS_DIR", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "BD_NON_INTERACTIVE=1") {
		t.Fatalf("stdout = %q, want BD_NON_INTERACTIVE=1", result.Stdout)
	}
}
