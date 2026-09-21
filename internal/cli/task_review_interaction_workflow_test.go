//go:build integration

package cli_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type reviewCandidateFunc func(context.Context, workflow.ReviewLifecycleStore, workflow.ReviewAttemptContext, string) error

func (f reviewCandidateFunc) ValidateReviewCandidate(ctx context.Context, store workflow.ReviewLifecycleStore, attempt workflow.ReviewAttemptContext, dir string) error {
	return f(ctx, store, attempt, dir)
}

func TestIntegrationWorkflowTaskRunReviewFollowUpAllowsDirtyMainTarget(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-followup", "Review approval", "Finalize after approval.")
	paths := fixture.paths
	runStore := taskstate.NewStore(paths)
	reviewAttempt, err := runStore.StartReview("alpha", "op-followup")
	must.NoError(err)
	_, err = runStore.RecordReviewFinding("alpha", "op-followup", reviewAttempt.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Title: "Fix bug", Description: "The reviewed change still has a blocker.", SuggestedAction: "Patch the dirty candidate changes."})
	must.NoError(err)
	_, err = runStore.FinishReview("alpha", "op-followup", reviewAttempt.Attempt, taskstate.ReviewStatusBlocked)
	must.NoError(err)
	fixture.configureImplementer("followup", agent.Profile{Command: "unused-implementer"})
	fixture.agent.outcomes = append(fixture.agent.outcomes, semanticAgentOutcome{exitWithoutCompletion: true})

	stdout, stderr := fixture.run("", "task", "run", "op-followup")

	is.Contains(stderr, "Autonomous review follow-up for op-followup targets review attempt 1 finding(s) 1.")
	is.Empty(stdout)
	is.Equal("reviewed\n", fixture.candidate.contents)
	is.True(fixture.candidate.hasCandidateChanges)
	must.Len(fixture.agent.launches, 1)
	is.Equal(taskWorkflowRepoRoot, fixture.agent.launches[0].dir)
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-followup.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	must.Len(latest.Findings, 1)
	is.Equal(2, latest.Findings[0].TargetedByRunAttempt)
	must.Len(state.Runs, 2)
	is.Equal("main", state.GitFacts.Branch)
	is.Equal(taskWorkflowRepoRoot, state.GitFacts.Worktree)
	is.Equal("Resolving issues in op-followup Ready for task done", state.Runs[1].Execution.SessionName)
	must.NotNil(state.Runs[1].ReviewFollowUp)
	is.Equal([]int{0}, state.Runs[1].ReviewFollowUp.FindingIndexes)
}

func TestIntegrationWorkflowTaskReviewRejectsStaleMetadataMirror(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review approval", "Finalize after approval.")
	paths := fixture.paths
	item := fixture.backend.tasks["op-main"]
	item.Metadata["orpheus.worktree"] = "/fixture/task-workflow/repos/stale"
	fixture.backend.tasks["op-main"] = item

	stdout, stderr, err := fixture.runError("a\n", "task", "run", "op-main")

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "metadata target is invalid")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	_, ok := taskstate.LatestReview(state)
	is.False(ok)
}

func TestIntegrationWorkflowTaskReviewRejectsCandidatePreflightFailuresBeforeStartingReview(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*reviewWorkflowFixture)
		want    string
	}{
		{
			name: "staged changes",
			prepare: func(f *reviewWorkflowFixture) {
				f.options.Dependencies.ReviewCandidate = reviewCandidateFunc(func(context.Context, workflow.ReviewLifecycleStore, workflow.ReviewAttemptContext, string) error {
					return errors.New("review requires a clean Git index; rerun `orpheus task run <task-id>` after unstaging changes")
				})
			},
			want: "review requires a clean Git index",
		},
		{
			name: "missing candidate changes",
			prepare: func(f *reviewWorkflowFixture) {
				f.options.Dependencies.ReviewCandidate = reviewCandidateFunc(func(context.Context, workflow.ReviewLifecycleStore, workflow.ReviewAttemptContext, string) error {
					return errors.New("worktree has no candidate changes to review and task has no recorded finalization commit")
				})
			},
			want: "has no candidate changes to review",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			fixture := newReviewWorkflowFixture(t, "op-main", "Review preflight", "Reject bad candidate.")
			paths := fixture.paths
			test.prepare(fixture)

			stdout, stderr, err := fixture.runError("a\n", "task", "run", "op-main")

			must.Error(err)
			is.Empty(stdout)
			is.Empty(stderr)
			is.ErrorContains(err, test.want)
			var state taskstate.TaskState
			must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
			_, ok := taskstate.LatestReview(state)
			is.False(ok)
		})
	}
}

func TestIntegrationWorkflowTaskReviewMarksFailedWhenCandidateChangesMutateDuringManualStep(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review restore", "Restore mutated candidates.")
	paths := fixture.paths
	fixture.options.Dependencies.ReviewEffects.CaptureCandidate = func(context.Context, string, *slog.Logger, ...slog.Attr) (review.CandidateCheck, error) {
		return func() error {
			return errors.New("review step mutated candidate changes; restored the pre-step snapshot and marked review failed")
		}, nil
	}

	stdout, stderr, err := fixture.runError("b\nMutating finding\nThe step changed files\nRestore it\nf\n", "task", "run", "op-main")

	must.Error(err)
	is.Empty(stdout)
	is.Contains(stderr, "Review action")
	is.ErrorContains(err, "review step mutated candidate changes")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusFailed, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationWorkflowTaskReviewPassingCheckContinuesToManualStep(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review checks", "Run checks before approval.")
	paths := fixture.paths
	check := fixture.check("check", checkResult{stdout: "check stdout 1 unit\n", stderr: "check stderr review\n"})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "check", "name": "unit", "command": check}, {"kind": "manual", "name": "approval"}}})

	stdout, stderr := fixture.run("a\n", "task", "run", "--pipeline", "standard", "op-main")

	is.Contains(stdout, "check stdout 1 unit")
	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "== Review step: unit (check) ==")
	is.Contains(stderr, "◆ REVIEW STEP · approval (manual)")
	is.Contains(stderr, "check stderr review")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Steps, 2)
	is.Equal("unit", latest.Steps[0].Name)
	must.NotNil(latest.Steps[0].ExitCode)
	is.Equal(0, *latest.Steps[0].ExitCode)
	is.Equal("approval", latest.Steps[1].Name)
	is.Empty(latest.Findings)
}

func TestIntegrationWorkflowTaskReviewConfirmedManualCommandRunsAndRecordsStep(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review manual command", "Confirm before command.")
	paths := fixture.paths
	manual := fixture.check("manual", checkResult{stdout: "manual command ran inspect\n"})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "manual", "name": "inspect", "command": manual}}})

	stdout, stderr := fixture.run("\na\n", "task", "run", "--pipeline", "standard", "op-main")

	is.Contains(stdout, "manual command ran inspect")
	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "◆ REVIEW STEP · inspect (manual)")
	is.Contains(stderr, "Run manual command for step \"inspect\"")
	is.Contains(stderr, "Review action")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Steps, 1)
	is.Equal("manual", latest.Steps[0].Kind)
	is.Equal("inspect", latest.Steps[0].Name)
	must.NotNil(latest.Steps[0].ExitCode)
	is.Equal(0, *latest.Steps[0].ExitCode)
}

func TestIntegrationWorkflowTaskReviewImportsHunkNotesWithSelectedDisposition(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		note       review.HunkNote
		wantStatus taskstate.ReviewStatus
		wantType   taskstate.FindingType
		wantOutput string
	}{
		{name: "blocking", input: "\nb\nf\n", note: review.HunkNote{NoteID: "user:1", FilePath: "README.md", NewRange: []int{12, 12}, Body: "This must be fixed before publication."}, wantStatus: taskstate.ReviewStatusBlocked, wantType: taskstate.FindingTypeBlocking, wantOutput: "Imported Hunk note user:1 as blocking finding."},
		{name: "advisory", input: "\nv\na\n", note: review.HunkNote{NoteID: "user:2", FilePath: "docs.md", OldRange: []int{4, 5}, Body: "Consider tightening this wording later."}, wantStatus: taskstate.ReviewStatusPassed, wantType: taskstate.FindingTypeAdvisory, wantOutput: "Imported Hunk note user:2 as advisory finding."},
		{name: "empty", input: "\na\n", note: review.HunkNote{}, wantStatus: taskstate.ReviewStatusPassed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			fixture := newReviewWorkflowFixture(t, "op-main", "Review Hunk", "Import Hunk notes.")
			paths := fixture.paths
			var notes []review.HunkNote
			if test.note.NoteID != "" {
				notes = []review.HunkNote{test.note}
			}
			manual := fixture.hunkCommand("hunk-manual", hunkCommandResult{notes: notes, stdout: "hunk command ran\n"})
			fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "manual", "name": "inspect", "command": manual, "hunk_notes": true}}})
			if test.wantStatus == taskstate.ReviewStatusBlocked {
				fixture.budget(1)
			}

			stdout, stderr := fixture.run(test.input, "task", "run", "--pipeline", "standard", "op-main")

			if test.note.NoteID != "" {
				is.Contains(stdout, "hunk command ran")
				is.Contains(stderr, "Captured 1 Hunk note(s)")
				is.Contains(stderr, test.wantOutput)
			} else {
				is.NotContains(stderr, "Captured 1 Hunk note")
			}
			var state taskstate.TaskState
			must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
			latest, ok := taskstate.LatestReview(state)
			must.True(ok)
			is.Equal(test.wantStatus, latest.Status)
			if test.note.NoteID == "" {
				is.Empty(latest.Findings)
				return
			}
			must.Len(latest.Findings, 1)
			is.Equal(test.wantType, latest.Findings[0].Type)
			is.Contains(latest.Findings[0].Description, "Note ID: "+test.note.NoteID)
		})
	}
}

func TestIntegrationWorkflowTaskReviewImportsHunkSeparateTaskNoteAndCreatesFollowUp(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review Hunk follow-up", "Import separate-task notes.")
	paths := fixture.paths
	manual := fixture.hunkCommand("hunk-manual", hunkCommandResult{notes: []review.HunkNote{{NoteID: "user:3", FilePath: "internal/app.go", NewRange: []int{30, 31}, Body: "This helper extraction can be separate."}}})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "manual", "name": "inspect", "command": manual, "hunk_notes": true}}})
	input := strings.Join([]string{"", "t", "Extract helper", "Extract the helper later.", "Helper extraction has tests.", "a", "a", ""}, "\n")

	stdout, stderr := fixture.run(input, "task", "run", "--pipeline", "standard", "op-main")

	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "Imported Hunk note user:3 as separate-task finding.")
	is.Contains(stderr, "Created follow-up Bead op-41 for review finding 1.")
	must.Len(fixture.tasks.created, 1)
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeSeparateTask, latest.Findings[0].Type)
	is.Equal("op-41", latest.Findings[0].CreatedTaskID)
	is.NotNil(latest.Findings[0].CreatedTaskAt)
}

func TestIntegrationWorkflowTaskReviewDeclinedManualCommandAbortsWithoutRunningCommand(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review decline", "Decline command.")
	paths := fixture.paths
	manual := fixture.check("manual")
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "manual", "name": "inspect", "command": manual}}})

	stdout, stderr := fixture.run("n\n", "task", "run", "--pipeline", "standard", "op-main")

	is.Empty(stdout)
	is.Contains(stderr, "Run manual command for step \"inspect\"")
	is.Contains(stderr, "Review aborted for op-main.")
	is.NotContains(stderr, "Review action")
	is.Empty(fixture.checkCalls)
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusAborted, latest.Status)
	is.Empty(latest.Steps)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationWorkflowTaskReviewManualCommandEOFConfirmationHandlesUnavailableInput(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantMessage string
		wantStatus  taskstate.ReviewStatus
	}{
		{name: "empty EOF", input: "", wantMessage: "Review for op-main is waiting for manual step \"inspect\" because manual review input is unavailable.", wantStatus: taskstate.ReviewStatusWaitingForManual},
		{name: "decline without newline", input: "n", wantMessage: "Review aborted for op-main.", wantStatus: taskstate.ReviewStatusAborted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			fixture := newReviewWorkflowFixture(t, "op-main", "Review EOF", "Abort on EOF.")
			paths := fixture.paths
			manual := fixture.check("manual")
			fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "manual", "name": "inspect", "command": manual}}})

			stdout, stderr, err := fixture.runError(test.input, "task", "run", "--pipeline", "standard", "op-main")

			must.NoError(err)
			is.Empty(stdout)
			is.Contains(stderr, "Run manual command for step \"inspect\"")
			is.Contains(stderr, test.wantMessage)
			is.NotContains(stderr, "Review action")
			is.Empty(fixture.checkCalls)
			var state taskstate.TaskState
			must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
			latest, ok := taskstate.LatestReview(state)
			must.True(ok)
			is.Equal(test.wantStatus, latest.Status)
			is.Empty(latest.Steps)
		})
	}
}

func TestIntegrationWorkflowTaskReviewNonZeroCheckRecordsBlockingFindingAndStops(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review failed check", "Block on check failure.")
	paths := fixture.paths
	check := fixture.check("check", checkResult{code: 7, stdout: "failing check output\n"})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "check", "name": "unit", "command": check}, {"kind": "manual", "name": "approval"}}})

	stdout, stderr := fixture.run("", "task", "run", "--pipeline", "standard", "op-main")

	is.Contains(stdout, "failing check output")
	is.Contains(stderr, "== Review step: unit (check) ==")
	is.Contains(stderr, "Automated blocker decisions for op-main were interrupted")
	is.Contains(stderr, "Review blocked for op-main by check \"unit\".")
	is.NotContains(stderr, "Review action")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	is.True(latest.AutomatedBlockerDecisionInterrupted)
	must.Len(latest.Steps, 1)
	must.NotNil(latest.Steps[0].ExitCode)
	is.Equal(7, *latest.Steps[0].ExitCode)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Equal("unit", latest.Findings[0].Step)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationWorkflowTaskReviewAgentReviewStepCapturesUsage(t *testing.T) {
	tests := []struct {
		name       string
		harness    string
		model      string
		sessionID  string
		logPath    string
		tokens     int
		usageCost  *taskstate.AgentUsageCost
		wantReason string
	}{
		{name: "codex", harness: "codex", model: "gpt-5", sessionID: "review-session", logPath: "/semantic/codex-review.jsonl", tokens: 190, wantReason: "matched_codex_session"},
		{name: "pi", harness: "pi", model: "openai-codex/gpt-5.5", sessionID: "review-pi-session", logPath: "/semantic/pi-review.jsonl", tokens: 180, usageCost: &taskstate.AgentUsageCost{AmountMicroUSD: 1240}, wantReason: "matched_pi_session"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			fixture := newReviewWorkflowFixture(t, "op-main", "Review agent", "Run attached reviewer.")
			paths := fixture.paths
			fixture.configureAgentProfiles(agent.AgentDefaults{Implementer: "reviewer", Reviewer: "reviewer"}, map[string]agent.Profile{"reviewer": {Harness: test.harness, Model: test.model}})
			fixture.agent.outcomes = append(fixture.agent.outcomes, semanticAgentOutcome{review: true})
			fixture.options.Dependencies.CaptureUsage = func(opts agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
				if opts.Harness != test.harness || opts.ExecutionDir != taskWorkflowRepoRoot {
					return taskstate.RecordRunUsageOptions{UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureUnknown, Reason: fmt.Sprintf("unexpected capture request: %#v", opts)}}
				}
				return taskstate.RecordRunUsageOptions{Session: &taskstate.AgentSession{ID: test.sessionID, LogPath: test.logPath}, Usage: &taskstate.AgentUsage{TotalTokens: test.tokens}, UsageCost: test.usageCost, UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: test.wantReason, CandidateCount: 1}}
			}
			fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "agent_review", "name": "ai-review"}}})

			stdout, _ := fixture.run("", "task", "run", "op-main")

			is.Contains(stdout, "Finalized op-main")
			var state taskstate.TaskState
			must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
			latest, ok := taskstate.LatestReview(state)
			must.True(ok)
			must.Len(latest.Steps, 1)
			must.NotNil(latest.Steps[0].Execution)
			execution := latest.Steps[0].Execution
			is.Equal(taskstate.RunStatusSucceeded, execution.Status)
			is.Equal(test.harness, execution.Harness)
			is.Equal(test.model, execution.Model)
			must.NotNil(execution.Session)
			is.Equal(test.sessionID, execution.Session.ID)
			is.Equal(test.logPath, execution.Session.LogPath)
			must.NotNil(execution.Usage)
			is.Equal(test.tokens, execution.Usage.TotalTokens)
			if test.usageCost != nil {
				must.NotNil(execution.UsageCost)
				is.Equal(test.usageCost.AmountMicroUSD, execution.UsageCost.AmountMicroUSD)
			}
			is.Equal(taskstate.UsageCaptureCaptured, execution.UsageCapture.Status)
			is.Equal(test.wantReason, execution.UsageCapture.Reason)
			is.Equal(1, execution.UsageCapture.CandidateCount)
		})
	}
}
