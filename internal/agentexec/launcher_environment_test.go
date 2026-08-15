//go:build integration

package agentexec_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/agentexec"
)

func TestIntegrationAttachedLauncherUsesConfiguredEnvironment(t *testing.T) {
	t.Parallel()

	output := filepath.Join(t.TempDir(), "environment")
	binDir := t.TempDir()
	probe := filepath.Join(binDir, "probe")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nprintf '%s/%s' \"$INVOCATION_SCOPE\" \"$COMMAND_SCOPE\" > \"$1\"\n"), 0o755); err != nil {
		t.Fatalf("write probe: %v", err)
	}

	launcher := agentexec.AttachedLauncher{Environment: []string{"PATH=" + binDir, "INVOCATION_SCOPE=isolated", "COMMAND_SCOPE=stale"}}
	err := launcher.Run(context.Background(), agentexec.Command{Name: "probe", Command: "probe", Args: []string{output}}, agentexec.LaunchOptions{
		Dir: t.TempDir(),
		Env: []string{"COMMAND_SCOPE=command"},
	})
	if err != nil {
		t.Fatalf("run probe: %v", err)
	}
	content, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read probe output: %v", err)
	}
	if got := string(content); got != "isolated/command" {
		t.Fatalf("probe environment = %q, want invocation and command values", got)
	}
}
