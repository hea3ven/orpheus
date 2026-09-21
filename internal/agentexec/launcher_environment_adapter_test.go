//go:build integration

package agentexec_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/testguard"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAdapterContractAttachedLauncherForwardsWorkingDirectoryArgumentsAndStreams(t *testing.T) {
	t.Parallel()

	workdir := testutil.CanonicalTempDir(t)
	marker := filepath.Join(testutil.CanonicalTempDir(t), "invocation")
	binDir := testutil.CanonicalTempDir(t)
	probe := filepath.Join(binDir, "probe")
	if err := testguard.WriteExecutable(probe, []byte("#!/bin/sh\nprintf 'stdout:%s' \"$AGENT_VALUE\"\nprintf 'stderr:%s' \"$AGENT_VALUE\" >&2\nprintf '%s|%s|%s' \"$PWD\" \"$2\" \"$3\" > \"$1\"\nexit 7\n")); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)

	err := (agentexec.AttachedLauncher{Environment: []string{"PATH=" + binDir}}).Run(
		context.Background(),
		agentexec.Command{Name: "probe", Command: "probe", Args: []string{marker, "first", "second"}},
		agentexec.LaunchOptions{Dir: workdir, Env: []string{"AGENT_VALUE=forwarded"}, Stdout: stdout, Stderr: stderr},
	)

	if err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("Run() error = %v, want exit status 7", err)
	}
	if stdout.String() != "stdout:forwarded" || stderr.String() != "stderr:forwarded" {
		t.Fatalf("streams = %q/%q, want forwarded output", stdout, stderr)
	}
	content, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatalf("read invocation marker: %v", readErr)
	}
	if got, want := string(content), workdir+"|first|second"; got != want {
		t.Fatalf("invocation = %q, want %q", got, want)
	}
}

func TestIntegrationAdapterContractAttachedLauncherUsesConfiguredEnvironment(t *testing.T) {
	t.Parallel()

	output := filepath.Join(testutil.CanonicalTempDir(t), "environment")
	binDir := testutil.CanonicalTempDir(t)
	probe := filepath.Join(binDir, "probe")
	if err := testguard.WriteExecutable(probe, []byte("#!/bin/sh\nprintf '%s/%s' \"$INVOCATION_SCOPE\" \"$COMMAND_SCOPE\" > \"$1\"\n")); err != nil {
		t.Fatalf("write probe: %v", err)
	}

	launcher := agentexec.AttachedLauncher{Environment: []string{"PATH=" + binDir, "INVOCATION_SCOPE=isolated", "COMMAND_SCOPE=stale"}}
	err := launcher.Run(context.Background(), agentexec.Command{Name: "probe", Command: "probe", Args: []string{output}}, agentexec.LaunchOptions{
		Dir: testutil.CanonicalTempDir(t),
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

func TestIntegrationAdapterContractAttachedLauncherMissingExecutableDoesNotReportProcessStarted(t *testing.T) {
	t.Parallel()
	started := false
	launcher := agentexec.AttachedLauncher{Environment: []string{"PATH=" + testutil.CanonicalTempDir(t)}}

	err := launcher.Run(context.Background(), agentexec.Command{Name: "missing", Command: "missing-agent"}, agentexec.LaunchOptions{
		Dir:     testutil.CanonicalTempDir(t),
		OnStart: func(int) error { started = true; return nil },
	})

	var startError *agentexec.StartError
	require.ErrorAs(t, err, &startError)
	assert.False(t, started)
}
