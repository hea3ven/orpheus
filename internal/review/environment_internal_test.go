//go:build integration

package review

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestIntegrationHunkNotesUseScopedEnvironment(t *testing.T) {
	t.Parallel()

	binDir := t.TempDir()
	binary := filepath.Join(binDir, "hunk")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n[ \"$HUNK_SCOPE\" = isolated ] || exit 17\nprintf '{\"comments\":[]}'\n"), 0o755); err != nil {
		t.Fatalf("write hunk: %v", err)
	}
	comments, err := captureHunkUserNotes(context.Background(), t.TempDir(), nil, []string{"PATH=" + binDir, "HUNK_SCOPE=isolated"})
	if err != nil {
		t.Fatalf("capture hunk notes: %v", err)
	}
	if len(comments) != 0 {
		t.Fatalf("comments = %#v, want empty", comments)
	}
}

func TestIntegrationReviewCommandUsesScopedEnvironment(t *testing.T) {
	t.Parallel()

	binDir := t.TempDir()
	binary := filepath.Join(binDir, "review-check")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s/%s' \"$INVOCATION_SCOPE\" \"$STEP_SCOPE\"\n"), 0o755); err != nil {
		t.Fatalf("write review check: %v", err)
	}
	var output bytes.Buffer
	exitCode, err := runStepCommandWithOutput(PipelineRunOptions{
		Context:     context.Background(),
		Workdir:     t.TempDir(),
		Environment: []string{"PATH=" + binDir, "INVOCATION_SCOPE=isolated", "STEP_SCOPE=stale"},
		Stdout:      io.Discard,
		Stderr:      io.Discard,
	}, Step{Command: "review-check"}, []string{"STEP_SCOPE=step"}, &output, io.Discard)
	if err != nil {
		t.Fatalf("run review command: %v", err)
	}
	if exitCode == nil || *exitCode != 0 {
		t.Fatalf("exit code = %v, want 0", exitCode)
	}
	if got := output.String(); got != "isolated/step" {
		t.Fatalf("review command environment = %q, want scoped values", got)
	}
}
