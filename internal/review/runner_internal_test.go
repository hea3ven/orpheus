package review

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestRunHunkBackedManualCommandCanceledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	exitCode, notes, err := runHunkBackedManualCommand(PipelineRunOptions{
		Context: ctx,
		RepoID:  "alpha",
		TaskID:  "op-1",
		Branch:  "main",
		Workdir: testutil.CanonicalTempDir(t),
		Pipeline: Pipeline{
			Name: "standard",
		},
		Stdout: io.Discard,
		Stderr: io.Discard,
	}, Step{
		Kind:      KindManual,
		Name:      "inspect",
		Command:   "sh",
		Args:      []string{"-c", "true"},
		HunkNotes: true,
	}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runHunkBackedManualCommand error = %v, want context canceled", err)
	}
	if exitCode != nil {
		t.Fatalf("exit code = %v, want nil", *exitCode)
	}
	if notes != nil {
		t.Fatalf("notes = %#v, want nil", notes)
	}
}
