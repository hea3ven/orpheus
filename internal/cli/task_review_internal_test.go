package cli

import (
	"bufio"
	"bytes"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/review"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
	"time"
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
	if presentation.OpenOutput(true).Stdout != stdout {
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
	if presentation.OpenOutput(true).Stdout == stdout {
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

	snapshot := testutil.ReviewableWork("alpha", "op-42", "/fixture/task", "orpheus/op-42")
	original := snapshot.Workflows[0].Phases[1].(*work.ImplementationPhase)
	original.Completion.Summary = "Original implementation summary"
	original.Completion.Description = "Original implementation description"
	original.Completion.TechnicalExplanation = "Original implementation technical explanation"
	latest := *original
	completion := *original.Completion
	completion.Summary, completion.Description, completion.TechnicalExplanation = "Latest fix summary", "Latest fix description", "Latest fix technical explanation"
	completion.CompletedAt = completion.CompletedAt.Add(time.Minute)
	latest.Completion = &completion
	latest.Execution.StartedAt = latest.Execution.StartedAt.Add(time.Minute)
	zero := 0
	latest.Execution.Process = &work.ExitedProcess{At: original.Execution.Process.(*work.ExitedProcess).At.Add(time.Minute), Code: &zero}
	latest.Output = &work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: "fixture-fix", Tree: "fixture-fix-tree"}, RetainedRef: "refs/orpheus/fixture-fix"}
	snapshot.Workflows[0].Phases = append(snapshot.Workflows[0].Phases, &latest)
	view, err := taskstate.Project(snapshot)
	require.NoError(t, err)
	context := workflow.ReviewManualStepContext{
		Task:   taskmodel.Task{ID: "op-42", Title: "Make review context readable"},
		Step:   review.Step{Kind: review.KindManual, Name: "manual"},
		Review: taskstate.ReviewAttempt{Attempt: 3},
		TaskState: taskstate.TaskState{

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
	context.TaskState.Work = view.Work
	context.TaskState.Runs = view.Runs

	ansi := regexp.MustCompile("\x1b\\[[0-9;]*m")

	for _, mode := range []struct {
		name              string
		terminal, noColor bool
	}{
		{name: "redirected plain"},
		{name: "interactive", terminal: true},
		{name: "NO_COLOR interactive", terminal: true, noColor: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			taskReviewOutputIsTerminal = func(io.Writer) bool { return mode.terminal }
			manualReviewColorIsDisabled = func() bool { return false }
			if mode.noColor {
				t.Setenv("NO_COLOR", "1")
				manualReviewColorIsDisabled = originalColorDisabled
			}
			var output bytes.Buffer
			command := &cobra.Command{}
			command.SetOut(&output)
			command.SetErr(&output)
			if err := renderManualReviewContext(command, context); err != nil {
				t.Fatalf("render manual context: %v", err)
			}
			confirmed, err := promptManualCommandConfirmation(command, bufio.NewReader(strings.NewReader("y\n")), review.Step{Name: "manual", Command: "hunk", Args: []string{"diff"}})
			if err != nil || !confirmed {
				t.Fatalf("confirmation = %t, %v", confirmed, err)
			}
			raw := output.String()
			wantANSI := mode.terminal && !mode.noColor
			hasANSI := strings.Contains(raw, "\x1b[")
			if hasANSI != wantANSI {
				t.Fatalf("ANSI enabled = %t, want %t: %q", hasANSI, wantANSI, raw)
			}
			plain := ansi.ReplaceAllString(raw, "")
			cursor := 0
			for _, fact := range []string{
				"ORIGINAL COMPLETION", "Original implementation summary", "Original implementation description", "Original implementation technical explanation",
				"LATEST FIX COMPLETION", "Latest fix summary", "Latest fix description", "Latest fix technical explanation",
				"GIT STATUS --SHORT", " M internal/cli/task.go", "?? notes.md",
				"RECORDED FINDINGS FOR THIS STEP", "manual", "advisory", "Current finding", "Current finding description", "Current finding action",
				"OPEN BLOCKERS FROM EARLIER STEPS", "checks", "blocking", "Earlier blocker", "Earlier blocker description", "Earlier blocker action",
				"PRIOR UNRESOLVED ADVISORIES", "ai-review", "advisory", "Earlier advisory", "Earlier advisory description", "Earlier advisory action",
				"NEXT", "Run manual command", "\"manual\"", "\"hunk\"", "\"diff\"", "[Y/n]",
			} {
				offset := strings.Index(plain[cursor:], fact)
				if offset < 0 {
					t.Fatalf("missing or out-of-order fact %q:\n%s", fact, plain)
				}
				cursor += offset + len(fact)
			}
		})
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
