package cli

import (
	"bufio"
	"bytes"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/review"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/spf13/cobra"
)

type fakeManualReviewRecorder struct {
	latest       taskstate.ReviewAttempt
	flows        []publication.IntegrationFlow
	destinations []string
}

func (r *fakeManualReviewRecorder) LatestReview() (taskstate.ReviewAttempt, error) {
	return r.latest, nil
}

func (r *fakeManualReviewRecorder) SetIntegrationFlow(flow publication.IntegrationFlow) (taskstate.Finalization, error) {
	r.flows = append(r.flows, flow)
	return taskstate.Finalization{IntegrationFlow: flow}, nil
}

func (r *fakeManualReviewRecorder) SetIntegrationDestination(destination string) (taskstate.Finalization, error) {
	r.destinations = append(r.destinations, destination)
	return taskstate.Finalization{DestinationBranch: destination}, nil
}

func (r *fakeManualReviewRecorder) RecordFinding(taskstate.ReviewFinding) (taskstate.ReviewAttempt, error) {
	return r.latest, nil
}

func (r *fakeManualReviewRecorder) PromoteAdvisoryFinding(int) (taskstate.ReviewAttempt, error) {
	return r.latest, nil
}

func TestManualReviewKeepsSelectedIntegrationFlowWithinPromptLoop(t *testing.T) {
	t.Parallel()

	stderr := new(bytes.Buffer)
	command := &cobra.Command{}
	command.SetErr(stderr)
	recorder := &fakeManualReviewRecorder{latest: taskstate.ReviewAttempt{Attempt: 1}}
	session := manualReviewSession{
		command:           command,
		recorder:          recorder,
		taskID:            "op-1",
		integrationFlow:   publication.IntegrationFlowPullRequest,
		destinationBranch: "main",
	}
	reader := bufio.NewReader(bytes.NewBufferString("d\nrelease/next\n\n\n"))

	if _, done, err := session.handleManualReviewAction("integration", reader, manualReviewActions{}); err != nil || done {
		t.Fatalf("select direct merge = done %t, error %v", done, err)
	}
	if _, done, err := session.handleManualReviewAction("integration", reader, manualReviewActions{}); err != nil || done {
		t.Fatalf("keep selected flow = done %t, error %v", done, err)
	}
	if got, want := recorder.flows, []publication.IntegrationFlow{publication.IntegrationFlowDirectMerge, publication.IntegrationFlowDirectMerge}; !slices.Equal(got, want) {
		t.Fatalf("recorded flows = %#v, want %#v", got, want)
	}
	if got, want := recorder.destinations, []string{"release/next"}; !slices.Equal(got, want) {
		t.Fatalf("recorded destinations = %#v, want %#v", got, want)
	}
	if got := stderr.String(); !strings.Contains(got, "Effective publication integration flow: pull-request") || !strings.Contains(got, "Effective publication integration flow: direct-merge") {
		t.Fatalf("prompt output = %q, want initial and selected flows", got)
	}
	if !strings.Contains(stderr.String(), "Effective publication destination: main") || !strings.Contains(stderr.String(), "Effective publication destination: release/next") {
		t.Fatalf("prompt output = %q, want initial and selected destinations", stderr.String())
	}
}

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
		target: tasktarget.Target{Branch: "main"},
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
