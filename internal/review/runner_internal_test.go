package review

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/logging"
)

func TestRunHunkBackedManualCommandCanceledBeforeStartLogsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var diagnostics bytes.Buffer
	exitCode, notes, err := runHunkBackedManualCommand(PipelineRunOptions{
		Context: ctx,
		Logger:  logging.New(&diagnostics, logging.Config{Verbose: true}),
		RepoID:  "alpha",
		TaskID:  "op-1",
		Branch:  "main",
		Workdir: t.TempDir(),
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

	logs := diagnostics.String()
	for _, want := range []string{
		`msg="review command finished"`,
		`operation=hunk_manual_command`,
		`status=canceled`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("diagnostics missing %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, `status=start_failure`) {
		t.Fatalf("diagnostics logged start failure for canceled command:\n%s", logs)
	}
}
