package cli

import (
	"bufio"
	"bytes"
	"io"
	"log/slog"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/review"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/spf13/cobra"
)

func TestTaskReviewPipelinePresentationRequiresBothOutputStreamsTerminal(t *testing.T) {
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	original := taskReviewOutputIsTerminal
	taskReviewOutputIsTerminal = func(writer io.Writer) bool {
		return writer == stderr
	}
	t.Cleanup(func() { taskReviewOutputIsTerminal = original })

	command := &cobra.Command{}
	command.SetOut(stdout)
	command.SetErr(stderr)

	presentation := taskReviewPipelinePresentation(
		command,
		minimalTaskReviewStart(),
		bufio.NewReader(bytes.NewReader(nil)),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if presentation.InteractiveOutput {
		t.Fatal("InteractiveOutput = true, want false when stdout is redirected")
	}

	taskReviewOutputIsTerminal = func(writer io.Writer) bool {
		return writer == stdout || writer == stderr
	}
	presentation = taskReviewPipelinePresentation(
		command,
		minimalTaskReviewStart(),
		bufio.NewReader(bytes.NewReader(nil)),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if !presentation.InteractiveOutput {
		t.Fatal("InteractiveOutput = false, want true when stdout and stderr are terminals")
	}
}

func minimalTaskReviewStart() taskReviewStart {
	return taskReviewStart{
		target: workflow.Target{Branch: "main"},
		review: taskstate.ReviewAttempt{
			Attempt:  1,
			Pipeline: "standard",
		},
		pipeline: review.Pipeline{Name: "standard"},
		resolvedCtx: resolvedTaskContext{
			Resolved: taskmodel.ResolvedTaskSource{
				TaskID: "op-1",
				Source: taskmodel.RepositorySource{
					Repository: taskmodel.Repository{ID: "alpha"},
				},
			},
			Task:           taskmodel.Task{ID: "op-1", Title: "Review output"},
			RegisteredRepo: registry.Repo{ID: "alpha"},
		},
	}
}
