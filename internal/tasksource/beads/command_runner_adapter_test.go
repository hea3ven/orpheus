//go:build integration

package beads_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/tasksource/beads"
	"github.com/hea3ven/orpheus/internal/testguard"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestIntegrationAdapterContractCommandRunnerSanitizesBeadsEnvironment(t *testing.T) {
	t.Parallel()

	binDir := testutil.CanonicalTempDir(t)
	bin := filepath.Join(binDir, "bd")
	if err := testguard.WriteExecutable(bin, []byte("#!/bin/sh\nprintf 'BEADS_DIR=%s\\n' \"${BEADS_DIR-unset}\"\nprintf 'BD_NON_INTERACTIVE=%s\\n' \"${BD_NON_INTERACTIVE-unset}\"\nprintf 'BD_DISABLE_METRICS=%s\\n' \"${BD_DISABLE_METRICS-unset}\"\n")); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	for _, tc := range []struct {
		name    string
		runner  beads.CommandRunner
		metrics string
	}{
		{name: "explicit path", runner: beads.CommandRunner{Environment: []string{"BEADS_DIR=/fixture/wrong", "PATH=" + binDir}}, metrics: "unset"},
		{name: "isolated fixture", runner: isolatedBeadsRunner(t, bin), metrics: "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.runner.Run(testutil.CanonicalTempDir(t), "context")
			if err != nil {
				t.Fatalf("run fake bd: %v", err)
			}
			if strings.Contains(result.Stdout, "BEADS_DIR=/fixture/wrong") || !strings.Contains(result.Stdout, "BEADS_DIR=unset") {
				t.Fatalf("stdout = %q, want sanitized BEADS_DIR", result.Stdout)
			}
			if !strings.Contains(result.Stdout, "BD_NON_INTERACTIVE=1") {
				t.Fatalf("stdout = %q, want BD_NON_INTERACTIVE=1", result.Stdout)
			}
			if !strings.Contains(result.Stdout, "BD_DISABLE_METRICS="+tc.metrics+"\n") {
				t.Fatalf("stdout = %q, want BD_DISABLE_METRICS=%s", result.Stdout, tc.metrics)
			}
		})
	}
}
