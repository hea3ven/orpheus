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
	"github.com/hea3ven/orpheus/internal/workflow"
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

func TestRenderManualReviewContextUsesStructuredPlainAndInteractiveOutput(t *testing.T) {
	originalTerminal := taskReviewOutputIsTerminal
	originalColorDisabled := manualReviewColorIsDisabled
	t.Cleanup(func() {
		taskReviewOutputIsTerminal = originalTerminal
		manualReviewColorIsDisabled = originalColorDisabled
	})
	manualReviewColorIsDisabled = func() bool { return false }

	context := workflow.ReviewManualStepContext{
		Task:   taskmodel.Task{ID: "op-42", Title: "Make review context readable"},
		Step:   review.Step{Kind: review.KindManual, Name: "manual"},
		Review: taskstate.ReviewAttempt{Attempt: 3},
		TaskState: taskstate.TaskState{
			Runs: []taskstate.RunAttempt{
				{Attempt: 1, Completion: &taskstate.Completion{
					Summary:              "Original implementation summary",
					Description:          "Original implementation description",
					TechnicalExplanation: "Original implementation technical explanation",
				}},
				{Attempt: 2, ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 2}, Completion: &taskstate.Completion{
					Summary:              "Latest fix summary",
					Description:          "Latest fix description",
					TechnicalExplanation: "Latest fix technical explanation",
				}},
			},
			Reviews: []taskstate.ReviewAttempt{{
				Attempt: 3,
				Findings: []taskstate.ReviewFinding{
					{Type: taskstate.FindingTypeAdvisory, Step: "manual", Title: "Current finding", Description: "Current finding description", SuggestedAction: "Current finding action"},
					{Type: taskstate.FindingTypeBlocking, Step: "checks", Title: "Earlier blocker", Description: "Earlier blocker description", SuggestedAction: "Earlier blocker action"},
					{Type: taskstate.FindingTypeAdvisory, Step: "ai-review", Title: "Earlier advisory", Description: "Earlier advisory description", SuggestedAction: "Earlier advisory action"},
				},
			}},
		},
		GitStatus: " M internal/cli/task.go\n?? notes.md\n",
	}

	plain := new(bytes.Buffer)
	command := &cobra.Command{}
	command.SetOut(plain)
	command.SetErr(plain)
	if err := renderManualReviewContext(command, context); err != nil {
		t.Fatalf("renderManualReviewContext() error = %v", err)
	}
	if _, err := promptManualCommandConfirmation(command, bufio.NewReader(strings.NewReader("y\n")), review.Step{Name: "manual", Command: "hunk", Args: []string{"diff"}}); err != nil {
		t.Fatalf("promptManualCommandConfirmation() error = %v", err)
	}
	output := plain.String()
	for _, want := range []string{
		"◆ REVIEW STEP · manual (manual)\nTASK  op-42 — Make review context readable",
		"○ ORIGINAL COMPLETION\nOriginal implementation summary\n\nDESCRIPTION\nOriginal implementation description\n\nTECHNICAL EXPLANATION\nOriginal implementation technical explanation",
		"● LATEST FIX COMPLETION\nLatest fix summary\n\nDESCRIPTION\nLatest fix description\n\nTECHNICAL EXPLANATION\nLatest fix technical explanation",
		"≡ GIT STATUS --SHORT\n M internal/cli/task.go\n?? notes.md",
		"▲ RECORDED FINDINGS FOR THIS STEP\nFinding 1 · manual · advisory\nCurrent finding\n\nDESCRIPTION\nCurrent finding description\n\nSUGGESTED ACTION\nCurrent finding action",
		"▲ OPEN BLOCKERS FROM EARLIER STEPS\nFinding 2 · checks · blocking\nEarlier blocker\n\nDESCRIPTION\nEarlier blocker description\n\nSUGGESTED ACTION\nEarlier blocker action",
		"▲ PRIOR UNRESOLVED ADVISORIES\nFinding 3 · ai-review · advisory\nEarlier advisory\n\nDESCRIPTION\nEarlier advisory description\n\nSUGGESTED ACTION\nEarlier advisory action",
		"↳ NEXT  Run manual command for step \"manual\" (\"hunk\" \"diff\")? [Y/n]: ",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("plain output = %q, want %q", output, want)
		}
	}
	if strings.Contains(output, "\x1b[") {
		t.Errorf("plain output contains ANSI escape sequence: %q", output)
	}

	interactive := new(bytes.Buffer)
	command.SetOut(interactive)
	command.SetErr(interactive)
	taskReviewOutputIsTerminal = func(io.Writer) bool { return true }
	if err := renderManualReviewContext(command, context); err != nil {
		t.Fatalf("render interactive context: %v", err)
	}
	if _, err := promptManualCommandConfirmation(command, bufio.NewReader(strings.NewReader("y\n")), review.Step{Name: "manual", Command: "hunk"}); err != nil {
		t.Fatalf("prompt interactive command: %v", err)
	}
	for _, want := range []string{"\x1b[34m◆ REVIEW STEP", "\x1b[32m● LATEST FIX COMPLETION", "\x1b[33m▲ RECORDED", "\x1b[36m↳ NEXT", "\x1b[1mop-42", "\x1b[2mDESCRIPTION"} {
		if !strings.Contains(interactive.String(), want) {
			t.Errorf("interactive output = %q, want ANSI-styled %q", interactive.String(), want)
		}
	}

	manualReviewColorIsDisabled = originalColorDisabled
	t.Setenv("NO_COLOR", "1")
	noColor := new(bytes.Buffer)
	command.SetOut(noColor)
	command.SetErr(noColor)
	if err := renderManualReviewContext(command, context); err != nil {
		t.Fatalf("render NO_COLOR context: %v", err)
	}
	if strings.Contains(noColor.String(), "\x1b[") {
		t.Errorf("NO_COLOR output contains ANSI escape sequence: %q", noColor.String())
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
