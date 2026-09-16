//go:build integration

package agentexec_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/testguard"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAttachedLauncherStreamsBeforeExitAndReapsCanceledChild(t *testing.T) {
	t.Parallel()
	dir := testutil.CanonicalTempDir(t)
	probe := filepath.Join(dir, "stream-probe")
	require.NoError(t, testguard.WriteExecutable(probe, []byte("#!/bin/sh\nprintf 'ready\\n'\nwhile :; do :; done\n")))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output := &cancelingOutput{cancel: cancel}
	var pid int

	err := (agentexec.AttachedLauncher{Environment: []string{"PATH=" + dir}}).Run(ctx,
		agentexec.Command{Name: "stream-probe", Command: probe},
		agentexec.LaunchOptions{Dir: dir, Stdout: output, OnStart: func(child int) error { pid = child; return nil }},
	)

	require.Error(t, err)
	assert.False(t, agentexec.IsStartError(err))
	assert.ErrorIs(t, ctx.Err(), context.Canceled, "output must arrive before the deadline or child exit")
	assert.Equal(t, "ready\n", output.buffer.String())
	require.Positive(t, pid)
	liveness, probeErr := agentexec.ProbePID(pid)
	require.NoError(t, probeErr)
	assert.Equal(t, agentexec.ProcessAbsent, liveness, "Run must wait for the canceled direct child")
}

type cancelingOutput struct {
	buffer bytes.Buffer
	cancel context.CancelFunc
}

func (w *cancelingOutput) Write(p []byte) (int, error) {
	n, err := w.buffer.Write(p)
	w.cancel()
	return n, err
}
