//go:build integration

package agentexec_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	if err := testguard.WriteExecutable(probe, []byte("#!/bin/sh\nprintf 'stdout:%s/%s' \"$INVOCATION_SCOPE\" \"$AGENT_VALUE\"\nprintf 'stderr:%s' \"$AGENT_VALUE\" >&2\nprintf '%s|%s|%s' \"$PWD\" \"$2\" \"$3\" > \"$1\"\nexit 7\n")); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	var exits []agentexec.ProcessExit
	var outputAtExit string
	before := time.Now().UTC()

	err := (agentexec.AttachedLauncher{Environment: []string{"PATH=" + binDir, "INVOCATION_SCOPE=isolated", "AGENT_VALUE=stale"}}).Run(
		context.Background(),
		agentexec.Command{Name: "probe", Command: "probe", Args: []string{marker, "first", "second"}},
		agentexec.LaunchOptions{
			Dir: workdir, Env: []string{"AGENT_VALUE=forwarded"}, Stdout: stdout, Stderr: stderr,
			OnExit: func(exit agentexec.ProcessExit) {
				exits = append(exits, exit)
				outputAtExit = stdout.String() + "|" + stderr.String()
			},
		},
	)

	if err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("Run() error = %v, want exit status 7", err)
	}
	require.Len(t, exits, 1)
	assert.Positive(t, exits[0].PID)
	assert.Equal(t, 7, exits[0].Code)
	assert.False(t, exits[0].At.Before(before))
	assert.False(t, exits[0].At.After(time.Now().UTC()))
	assert.Equal(t, "stdout:isolated/forwarded|stderr:forwarded", outputAtExit, "exit must be observed after output has drained")
	if stdout.String() != "stdout:isolated/forwarded" || stderr.String() != "stderr:forwarded" {
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

func TestIntegrationAdapterContractAttachedLauncherMissingExecutableDoesNotReportProcessStarted(t *testing.T) {
	t.Parallel()
	started, exited := false, false
	launcher := agentexec.AttachedLauncher{Environment: []string{"PATH=" + testutil.CanonicalTempDir(t)}}

	err := launcher.Run(context.Background(), agentexec.Command{Name: "missing", Command: "missing-agent"}, agentexec.LaunchOptions{
		Dir:     testutil.CanonicalTempDir(t),
		OnStart: func(int) error { started = true; return nil },
		OnExit:  func(agentexec.ProcessExit) { exited = true },
	})

	var startError *agentexec.StartError
	require.ErrorAs(t, err, &startError)
	assert.False(t, started)
	assert.False(t, exited, "a start failure has no process exit to observe")
}
