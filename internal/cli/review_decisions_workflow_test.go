//go:build integration

package cli_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskReviewApproveFinalizesAndRecordsPassedAttempt(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review approval", "Finalize after approval.")
	paths := fixture.paths

	stdout, stderr := fixture.run("x\na\n", "task", "run", "op-main")

	is.Contains(stderr, "TASK  op-main — Ready for task done")
	is.Contains(stderr, "● LATEST COMPLETION\nReview approval")
	is.Contains(stderr, "DESCRIPTION\nFinalize after approval.")
	is.Contains(stderr, "TECHNICAL EXPLANATION\nTechnical explanation.")
	is.NotContains(stderr, "Original completion:")
	is.NotContains(stderr, "Latest fix completion:")
	is.Contains(stderr, "≡ GIT STATUS --SHORT")
	is.Contains(stderr, "(clean)")
	is.NotContains(stderr, "git diff --stat:")
	is.Contains(stderr, "◆ REVIEW STEP · local-review (manual)")
	is.Contains(stderr, "Review action [a=approve, b=block, v=advisory, t=task, q=abort]")
	is.NotContains(stderr, "p=promote advisory")
	is.Contains(stderr, "Choose approve, block, advisory, task, or abort.")
	is.Contains(stdout, "Review passed for op-main")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	is.Empty(latest.Findings)
	assertUnconsentedPublication(t, state)
	_, task := fixture.loadFinalTask("op-main")
	is.Equal(taskmodel.StatusInProgress, task.Status)
	is.Empty(fixture.tasks.closed)
	is.Empty(fixture.candidate.pushes)
}

func TestIntegrationWorkflowTaskReviewManualContextShowsOriginalAndLatestFollowUpCompletion(t *testing.T) {
	is := assert.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Original implementation", "Implemented the main task.")
	fixture.budget(3)
	fixture.completingRepairs("repairer", 2, false)
	fixture.agent.outcomes[0].completion.Summary = "First fix"
	fixture.agent.outcomes[0].completion.Description = "Addressed the first review blocker."
	fixture.agent.outcomes[1].completion.Summary = "Latest fix"
	fixture.agent.outcomes[1].completion.Description = "Addressed the most recent review blocker."
	stdout, stderr := fixture.run("b\nFirst blocker\nRepair one.\nFix one.\nf\nb\nSecond blocker\nRepair two.\nFix two.\nf\na\n", "task", "run", "op-main")

	is.Contains(stderr, "○ ORIGINAL COMPLETION\nOriginal implementation")
	is.Contains(stderr, "DESCRIPTION\nImplemented the main task.")
	is.Contains(stderr, "TECHNICAL EXPLANATION\nTechnical explanation.")
	is.Contains(stderr, "● LATEST FIX COMPLETION\nLatest fix")
	is.Contains(stderr, "DESCRIPTION\nAddressed the most recent review blocker.")
	is.Contains(stderr, "TECHNICAL EXPLANATION\nTechnical explanation.")
	is.NotContains(stderr, "Latest completion: Latest fix")
	is.NotContains(stderr, "Latest fix completion: First fix")
	is.Contains(stdout, "Review passed for op-main")

	message := fixture.candidate.commits[0]
	is.Equal("Original implementation\n\nImplemented the main task.", message)
}

func TestIntegrationWorkflowTaskReviewBlockingFindingBlocksWithoutFinalizing(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review blocking", "Do not finalize.")
	paths := fixture.paths
	headBefore := fixture.candidate.head

	fixture.budget(1)

	input := strings.Join([]string{
		"b",
		"Bug",
		"Must fix",
		"Patch it",
		"v",
		"Nit",
		"Small cleanup remains",
		"Consider later",
		"t",
		"Follow-up",
		"Extract helper later",
		"Track separately",
		"Create helper extraction task",
		"Extract the helper in a focused follow-up.",
		"Helper extraction has acceptance tests.",
		"f",
		"",
	}, "\n")
	stdout, stderr := fixture.run(input, "task", "run", "op-main")

	is.Empty(stdout)
	is.Contains(stderr, "Review action [f=finish/block, b=block, v=advisory, t=task, q=abort]")
	is.Contains(stderr, "Review blocked for op-main.")
	is.NotEqual(headBefore, fixture.candidate.head)
	is.Equal("output-1", fixture.candidate.head)
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	must.Len(latest.Findings, 3)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Equal(taskstate.FindingTypeAdvisory, latest.Findings[1].Type)
	is.Equal(taskstate.FindingTypeSeparateTask, latest.Findings[2].Type)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationWorkflowTaskReviewAdvisoryAndSeparateTaskFindingsDoNotBlockApproval(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review advisory", "Finalize with notes.")
	paths := fixture.paths

	input := strings.Join([]string{
		"v",
		"Nit",
		"Small cleanup remains",
		"Consider later",
		"t",
		"Follow-up",
		"Extract helper later",
		"Track separately",
		"Create helper extraction task",
		"Extract the helper in a focused follow-up.",
		"Helper extraction has acceptance tests.",
		"a",
		"n",
		"",
	}, "\n")
	stdout, stderr := fixture.run(input, "task", "run", "op-main")

	is.Contains(stdout, "Review passed for op-main")
	is.Contains(stderr, "Finding title:")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Findings, 2)
	is.Equal(taskstate.FindingTypeAdvisory, latest.Findings[0].Type)
	is.Equal(taskstate.FindingTypeSeparateTask, latest.Findings[1].Type)
	is.Empty(latest.Findings[1].CreatedTaskID)
	is.Nil(latest.Findings[1].CreatedTaskAt)
}

func TestIntegrationWorkflowTaskReviewCreatesSelectedSeparateTaskFollowUp(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review follow-up", "Create follow-up task.")
	paths := fixture.paths

	input := strings.Join([]string{
		"t",
		"Follow-up",
		"Extract helper later",
		"",
		"Extract helper",
		"Extract the helper later.",
		"Helper extraction has tests.",
		"a",
		"1",
		"",
	}, "\n")
	stdout, stderr := fixture.run(input, "task", "run", "op-main")

	is.Contains(stdout, "Review passed for op-main")
	is.Contains(stderr, "Created follow-up task op-41 for review finding 1.")
	must.Len(fixture.tasks.created, 1)
	is.Equal(taskmodel.CreateOptions{Title: "Extract helper", Description: "Extract the helper later.\n\nProvenance:\nDiscovered during review of op-main in repository alpha (review attempt 1, finding 1). Review step: local-review.", AcceptanceCriteria: "Helper extraction has tests.", IssueType: taskmodel.IssueTypeTask}, fixture.tasks.created[0])
	created, err := fixture.tasks.Get(context.Background(), "op-41")
	must.NoError(err)
	is.Equal(taskmodel.StatusOpen, created.Status)
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal("op-41", latest.Findings[0].CreatedTaskID)
	is.NotNil(latest.Findings[0].CreatedTaskAt)
}

func TestIntegrationWorkflowTaskReviewCanAbortWhenSeparateTaskCreationFails(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review follow-up", "Creation can fail.")
	paths := fixture.paths
	fixture.tasks.createError = errors.New("database locked")
	headBefore := fixture.candidate.head

	input := strings.Join([]string{
		"t",
		"Follow-up",
		"Extract helper later",
		"",
		"Extract helper",
		"Extract the helper later.",
		"Helper extraction has tests.",
		"a",
		"1",
		"n",
	}, "\n")
	stdout, stderr := fixture.run(input, "task", "run", "op-main")

	is.Empty(stdout)
	is.Contains(stderr, "Failed to create follow-up task for review finding 1")
	is.NotEqual(headBefore, fixture.candidate.head)
	is.Equal("output-1", fixture.candidate.head)
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusAborted, latest.Status)
	must.Len(latest.Findings, 1)
	is.Empty(latest.Findings[0].CreatedTaskID)
	is.Nil(latest.Findings[0].CreatedTaskAt)
	is.Empty(fixture.tasks.created)
	is.Equal([]string{"op-main"}, fixture.loadFinalTaskIDs())
	is.Empty(fixture.tasks.closed)
	is.Empty(fixture.candidate.pushes)
}

func TestIntegrationWorkflowTaskReviewAbortDoesNotFinalize(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review abort", "Do not finalize.")
	paths := fixture.paths
	headBefore := fixture.candidate.head

	stdout, stderr := fixture.run("q\n", "task", "run", "op-main")

	is.Empty(stdout)
	is.Contains(stderr, "Review aborted for op-main.")
	is.NotEqual(headBefore, fixture.candidate.head)
	is.Equal("output-1", fixture.candidate.head)
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusAborted, latest.Status)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationWorkflowTaskReviewManualInputLossReplaysRecordedFindings(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review replay", "Replay manual findings.")
	paths := fixture.paths

	fixture.budget(1)

	firstInput := strings.Join([]string{
		"v",
		"Existing note",
		"Remember this during replay",
		"Consider later",
		"b",
		"Existing blocker",
		"Fix this before completing the review",
		"Address the blocking issue",
		"",
	}, "\n")
	firstStdout, firstStderr := fixture.run(firstInput, "task", "run", "op-main")

	is.Empty(firstStdout)
	is.Contains(firstStderr, "Review for op-main is waiting for manual step \"local-review\" because manual review input is unavailable.")
	var waiting taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &waiting))
	paused, ok := taskstate.LatestReview(waiting)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusWaitingForManual, paused.Status)
	must.Len(paused.Findings, 2)
	is.Equal("Existing note", paused.Findings[0].Title)
	is.Equal("Existing blocker", paused.Findings[1].Title)

	stdout, stderr := fixture.run("f\n", "task", "run", "op-main")

	is.Empty(stdout)
	is.Contains(stderr, "Resuming review attempt 1 at manual step \"local-review\".")
	is.Contains(stderr, "▲ RECORDED FINDINGS FOR THIS STEP")
	is.Contains(stderr, "Finding 1 · local-review · advisory\nExisting note")
	is.Contains(stderr, "Finding 2 · local-review · blocking\nExisting blocker")
	is.Contains(stderr, "Review action [f=finish/block, b=block, v=advisory, t=task, q=abort]")
	is.Contains(stderr, "Review blocked for op-main.")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(paused.Attempt, latest.Attempt)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	must.Len(latest.Findings, 2)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[1].Type)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationWorkflowTaskReviewCheckBlockerReasonEOFRecordsInterrupted(t *testing.T) {
	tests := []struct {
		name  string
		input string
		label string
	}{
		{name: "downgrade", input: "d\n", label: "Downgrade reason"},
		{name: "waive", input: "w\n", label: "Waiver reason"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			fixture := newReviewWorkflowFixture(t, "op-main", "Review reason EOF", "Interrupt blocker reason.")
			paths := fixture.paths

			check := fixture.check("check", checkResult{code: 7})
			fixture.pipelines("standard", map[string][]map[string]any{
				"standard": []map[string]any{{"kind": "check", "name": "unit", "command": check}},
			})

			stdout, stderr := fixture.run(test.input, "task", "run", "--pipeline", "standard", "op-main")

			is.Empty(stdout)
			is.Contains(stderr, test.label)
			is.Contains(stderr, "Automated blocker decisions for op-main were interrupted")
			is.Contains(stderr, "Review blocked for op-main by check \"unit\".")

			var state taskstate.TaskState
			must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
			latest, ok := taskstate.LatestReview(state)
			must.True(ok)
			is.Equal(taskstate.ReviewStatusWaitingForAutomatedDecision, latest.Status)
			is.True(latest.AutomatedBlockerDecisionInterrupted)
			is.False(latest.AutomatedBlockerDecisionKept)
			must.Len(latest.Findings, 1)
			is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
			is.Empty(latest.Findings[0].DowngradeReason)
			is.Empty(latest.Findings[0].Waiver)
			is.Empty(taskstate.FinalizationFacts(state).Commit)
		})
	}
}

func TestIntegrationWorkflowTaskReviewCheckStartFailureMarksOperationalFailure(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review missing check", "Fail operationally.")
	fixture.check("definitely-missing-orpheus-check", checkResult{err: errors.New("executable not found")})
	paths := fixture.paths
	fixture.pipelines("standard", map[string][]map[string]any{
		"standard": []map[string]any{
			{"kind": "check", "name": "missing", "command": "definitely-missing-orpheus-check"},
		},
	})

	stdout, stderr, err := fixture.runError("", "task", "run", "--pipeline", "standard", "op-main")

	must.Error(err)
	is.Empty(stdout)
	is.Contains(stderr, "== Review step: missing (check) ==")
	is.ErrorContains(err, "start check step \"missing\"")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusFailed, latest.Status)
	must.Len(latest.Steps, 1)
	is.Nil(latest.Steps[0].ExitCode)
	is.Empty(latest.Findings)
}

func TestIntegrationWorkflowTaskRunAfterInterruptedAutomatedBlockerDecisionResumesItsExactPendingDecision(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review interrupted", "Do not launch follow-up.")
	paths := fixture.paths

	check := fixture.check("check", checkResult{code: 7})
	fixture.pipelines("standard", map[string][]map[string]any{
		"standard": []map[string]any{{"kind": "check", "name": "unit", "command": check}},
	})

	reviewStdout, reviewStderr := fixture.run("", "task", "run", "--pipeline", "standard", "op-main")
	is.Empty(reviewStdout)
	is.Contains(reviewStderr, "Automated blocker decisions for op-main were interrupted")
	stdout, stderr, err := fixture.runError("", "task", "run", "op-main")
	must.NoError(err)
	is.Empty(stdout)
	is.Contains(stderr, "Automated blocker decisions for op-main were interrupted")
	is.Empty(fixture.agent.launches)

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	must.Len(state.Runs, 1)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusWaitingForAutomatedDecision, latest.Status)
	is.True(latest.AutomatedBlockerDecisionInterrupted)
	must.Len(latest.Findings, 1)
	is.Zero(latest.Findings[0].TargetedByRunAttempt)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationWorkflowTaskRunRecoversHardStoppedAutomatedBlockerDecision(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Hard-stopped review", "Recover persisted blocker decision.")
	paths := fixture.paths
	fixture.pipelines("standard", map[string][]map[string]any{
		"standard": {{"kind": "check", "name": "unit", "command": "must-not-run"}},
	})

	stateStore := taskstate.NewStore(paths)
	reviewAttempt, err := beginConfiguredFixtureReview(fixture, stateStore, "op-main", taskstate.StartReviewOptions{Pipeline: "standard", Step: "unit"})
	must.NoError(err)
	_, err = stateStore.RecordReviewStep("alpha", "op-main", reviewAttempt.Attempt, taskstate.RecordReviewStepOptions{Address: reviewAttempt.Address, Kind: taskstate.ReviewStepKindCheck, Name: "unit", ExitCode: fixtureExitCode(7), Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Step: "unit", Title: "Check \"unit\" failed", Description: "Fix the failing check."}}})
	must.NoError(err)
	fixture.budget(1)

	stdout, stderr := fixture.run("k\n", "task", "run", "op-main")

	is.Empty(stdout)
	is.Contains(stderr, "Resuming review attempt 1 at automated blocker decision")
	is.Contains(stderr, "Finding 1:")
	is.NotContains(stderr, "== Agent run: implementation")
	is.Empty(fixture.agent.launches)

	state, err := stateStore.Load("alpha", "op-main")
	must.NoError(err)
	must.Len(state.Runs, 1)
	must.Len(state.Reviews, 1)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	is.True(latest.AutomatedBlockerDecisionKept)
	is.False(latest.AutomatedBlockerDecisionInterrupted)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Zero(latest.Findings[0].TargetedByRunAttempt)
}

func TestIntegrationWorkflowTaskReviewInterruptedAutomatedBlockerRecoveryReusesRecordedPipeline(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review strict recovery", "Recover with selected pipeline.")
	paths := fixture.paths
	repo := taskWorkflowRepository()
	repo.ReviewPipeline = "default"
	fixture.withRegisteredRepos(repo)

	defaultCheck := "must-not-run"
	strictCheck := fixture.check("strict", checkResult{code: 7, stdout: "strict pipeline\n"})
	fixture.pipelines("default", map[string][]map[string]any{
		"default": {{"kind": "check", "name": "default", "command": defaultCheck}},
		"strict":  {{"kind": "check", "name": "strict", "command": strictCheck}},
	})

	firstStdout, firstStderr := fixture.run("", "task", "run", "--pipeline", "strict", "op-main")
	is.Contains(firstStdout, "strict pipeline")
	is.NotContains(firstStdout, "default pipeline")
	is.Contains(firstStderr, "Automated blocker decisions for op-main were interrupted")

	recoveryStdout, recoveryStderr := fixture.run("d\nStrict failure accepted.\n", "task", "run", "op-main")

	is.NotContains(recoveryStdout, "strict pipeline", "the completed native check is not rerun")
	is.NotContains(recoveryStdout, "default pipeline")
	is.Contains(recoveryStdout, "Review passed for op-main")
	is.Contains(recoveryStderr, "Resuming review attempt 1 at automated blocker decision for step \"strict\".")
	is.Contains(recoveryStderr, "Finding 1:")
	is.Contains(recoveryStderr, "Title: Check \"strict\" failed")
	is.NotContains(recoveryStderr, "== Review step: strict (check) ==")
	is.NotContains(recoveryStderr, "== Review step: default (check) ==")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	must.Len(state.Reviews, 1)
	is.Equal("strict", state.Reviews[0].Pipeline)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	is.Equal("strict", latest.Pipeline)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeAdvisory, latest.Findings[0].Type)
	is.Equal("Strict failure accepted.", latest.Findings[0].DowngradeReason)
}

func TestIntegrationWorkflowTaskReviewResumesManualWaitingAttempt(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review resume", "Resume manual gate.")
	paths := fixture.paths
	repo := taskWorkflowRepository()
	repo.ReviewPipeline = "standard"
	fixture.withRegisteredRepos(repo)

	fail := "must-not-run"
	fixture.pipelines("standard", map[string][]map[string]any{
		"standard": {
			{"kind": "check", "name": "lint", "command": fail},
			{"kind": "manual", "name": "inspect"},
		},
	})
	runStore := taskstate.NewStore(paths)
	reviewAttempt, err := beginConfiguredFixtureReview(fixture, runStore, "op-main", taskstate.StartReviewOptions{
		Pipeline: "standard",
		Step:     "lint",
	})
	must.NoError(err)
	_, err = runStore.RecordReviewStep("alpha", "op-main", reviewAttempt.Attempt, taskstate.RecordReviewStepOptions{Address: reviewAttempt.Address,
		ExitCode: fixtureExitCode(0), Kind: "check",
		Name: "lint",
	})
	must.NoError(err)
	_, err = seedFixtureManualEntry(runStore, "alpha", "op-main", reviewAttempt.Attempt, "inspect")
	must.NoError(err)

	stdout, stderr := fixture.run("a\n", "task", "run", "op-main")

	is.Contains(stdout, "Review passed for op-main")
	is.Contains(stderr, "Resuming review attempt 1 at manual step \"inspect\".")
	is.Contains(stderr, "◆ REVIEW STEP · inspect (manual)")
	is.NotContains(stdout, "check reran")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(reviewAttempt.Attempt, latest.Attempt)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
}

func TestIntegrationWorkflowTaskReviewRejectsConflictingPipelineForManualWaitingAttempt(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review conflict", "Reject replacement.")
	paths := fixture.paths
	fixture.pipelines("standard", map[string][]map[string]any{
		"standard": {{"kind": "manual", "name": "inspect"}},
		"other":    {{"kind": "manual", "name": "other-inspect"}},
	})
	runStore := taskstate.NewStore(paths)
	reviewAttempt, err := beginConfiguredFixtureReview(fixture, runStore, "op-main", taskstate.StartReviewOptions{
		Pipeline: "standard",
		Step:     "inspect",
	})
	must.NoError(err)
	_, err = seedFixtureManualEntry(runStore, "alpha", "op-main", reviewAttempt.Attempt, "inspect")
	must.NoError(err)

	stdout, _, err := fixture.runError("", "task", "run", "--pipeline", "other", "op-main")

	must.Error(err)
	is.Empty(stdout)
	is.ErrorContains(err, "--pipeline cannot affect the selected workflow path")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusWaitingForManual, latest.Status)
	is.Equal("standard", latest.Pipeline)
	is.Equal("inspect", latest.Step)
}

func TestIntegrationWorkflowTaskReviewPipelineOverridePrecedence(t *testing.T) {
	for _, selection := range []string{"cli", "quick"} {
		t.Run(selection, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			fixture := newReviewWorkflowFixture(t, "op-main", "Review override", "Use CLI-selected pipeline.")
			paths := fixture.paths
			repo := taskWorkflowRepository()
			repo.ReviewPipeline = "repo"
			repo.ReviewPipelineAliases = map[string]string{"quick": "cli"}
			fixture.withRegisteredRepos(repo)

			fail := "must-not-run"
			pass := fixture.check("cli", checkResult{stdout: "cli pipeline\n"})
			fixture.pipelines("global", map[string][]map[string]any{
				"global": []map[string]any{{"kind": "check", "name": "global", "command": fail}},
				"repo":   []map[string]any{{"kind": "check", "name": "repo", "command": fail}},
				"cli":    []map[string]any{{"kind": "check", "name": "cli", "command": pass}},
			})

			stdout, stderr := fixture.run("", "task", "run", "--pipeline", selection, "op-main")

			is.Contains(stdout, "cli pipeline")
			is.NotContains(stdout, "wrong pipeline")
			is.Contains(stdout, "Review passed for op-main")
			is.Contains(stderr, "== Review step: cli (check) ==")

			var state taskstate.TaskState
			must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
			latest, ok := taskstate.LatestReview(state)
			must.True(ok)
			is.Equal("cli", latest.Pipeline)
			is.Equal(taskstate.ReviewStatusPassed, latest.Status)
		})
	}
}

func TestIntegrationWorkflowTaskReviewUnknownPipelineIncludesRepoAliases(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review pipeline", "Validate pipeline selection.")
	repo := taskWorkflowRepository()
	repo.ReviewPipelineAliases = map[string]string{"quick": "standard"}
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "manual", "name": "standard-review"}}})
	fixture.withRegisteredRepos(repo)

	stdout, _, err := fixture.runError("", "task", "run", "--pipeline", "unknown", "op-main")

	must.Error(err)
	is.Empty(stdout)
	is.ErrorContains(err, `CLI --pipeline "unknown" does not match a configured review pipeline`)
	is.ErrorContains(err, "configured pipelines: standard")
	is.ErrorContains(err, "configured repo aliases: quick=standard")
}
func TestIntegrationWorkflowTaskReviewInvalidAgentConfigPreservesFreshAndWaitingState(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		t.Run(fmt.Sprintf("waiting_%t", waiting), func(t *testing.T) {
			fixture := newReviewWorkflowFixture(t, "op-main", "Review preflight", "Validate before persistence.")
			fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "agent_review", "name": "ai-review"}, {"kind": "manual", "name": "inspect"}}})
			view, _ := fixture.loadFinalTask("op-main")
			capture := workflow.ImplementationService{Paths: fixture.paths, Store: workstate.NewStore(fixture.paths), Git: fixture.candidate}
			_, captureErr := capture.Capture(context.Background(), view.Runs[0].Address)
			require.NoError(t, captureErr)
			if waiting {
				attempt, err := beginConfiguredFixtureReview(fixture, fixture.taskStore, "op-main", taskstate.StartReviewOptions{Pipeline: "standard", Step: "ai-review"})
				require.NoError(t, err)
				_, err = fixture.taskStore.RecordReviewStep("alpha", "op-main", attempt.Attempt, taskstate.RecordReviewStepOptions{Address: attempt.Address, Kind: "agent_review", Name: "ai-review", Execution: &taskstate.AgentExecution{Agent: "fixture-reviewer", Status: taskstate.RunStatusSucceeded, StartedAt: attempt.StartedAt, Process: &work.ExitedProcess{Code: fixtureExitCode(0), At: attempt.StartedAt}}})
				require.NoError(t, err)
				_, err = seedFixtureManualEntry(fixture.taskStore, "alpha", "op-main", attempt.Attempt, "inspect")
				require.NoError(t, err)
			}
			before, item := fixture.loadFinalTask("op-main")

			stdout, stderr, err := fixture.runError("", "task", "run", "op-main")

			require.ErrorContains(t, err, "load agent profiles")
			assert.Empty(t, stdout)
			assert.Empty(t, stderr)
			after, got := fixture.loadFinalTask("op-main")
			assert.Equal(t, before, after)
			assert.Equal(t, item, got)
			assert.Empty(t, fixture.agent.launches)
		})
	}
}

func TestIntegrationWorkflowTaskReviewCheckDecisionsPersistDispositionAndContinuePipeline(t *testing.T) {
	for _, test := range []struct {
		name, input               string
		manual, keep              bool
		wantType                  taskstate.FindingType
		downgrade, waiver, prompt string
	}{
		{name: "keep EOF", input: "k", keep: true, wantType: taskstate.FindingTypeBlocking, prompt: "Decision for finding 1"},
		{name: "downgrade EOF", input: "d\nFalse positive for this task.", wantType: taskstate.FindingTypeAdvisory, downgrade: "False positive for this task.", prompt: "Downgrade reason"},
		{name: "waive EOF", input: "w\nKnown flaky check.", wantType: taskstate.FindingTypeBlocking, waiver: "Known flaky check.", prompt: "Waiver reason"},
		{name: "downgrade continues", input: "d\nFalse positive for this task.\na\n", manual: true, wantType: taskstate.FindingTypeAdvisory, downgrade: "False positive for this task.", prompt: "Downgrade reason"},
		{name: "waiver alias continues", input: "c\nKnown flaky check.\na\n", manual: true, wantType: taskstate.FindingTypeBlocking, waiver: "Known flaky check.", prompt: "Waiver reason"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReviewWorkflowFixture(t, "op-main", "Check decisions", "Record operator disposition.")
			check := fixture.check("check", checkResult{code: 7})
			steps := []map[string]any{{"kind": "check", "name": "unit", "command": check}}
			if test.manual {
				steps = append(steps, map[string]any{"kind": "manual", "name": "approval"})
			}
			fixture.pipelines("standard", map[string][]map[string]any{"standard": steps})
			if test.keep {
				fixture.budget(1)
			}

			stdout, stderr := fixture.run(test.input, "task", "run", "--pipeline", "standard", "op-main")

			assert.Contains(t, stderr, test.prompt)
			assert.NotContains(t, stderr, "Automated blocker decisions for op-main were interrupted")
			state, _ := fixture.loadFinalTask("op-main")
			latest, ok := taskstate.LatestReview(state)
			require.True(t, ok)
			require.Len(t, latest.Findings, 1)
			assert.Equal(t, test.wantType, latest.Findings[0].Type)
			assert.Equal(t, test.downgrade, latest.Findings[0].DowngradeReason)
			assert.Equal(t, test.waiver, latest.Findings[0].Waiver)
			assert.False(t, latest.AutomatedBlockerDecisionInterrupted)
			if test.keep {
				assert.Empty(t, stdout)
				assert.Equal(t, taskstate.ReviewStatusBlocked, latest.Status)
				assert.True(t, latest.AutomatedBlockerDecisionKept)
				assert.True(t, latest.AutonomousBudgetExhausted)
				assert.Contains(t, stderr, "Autonomous review attempt budget exhausted")
				assert.Empty(t, taskstate.FinalizationFacts(state).Commit)
			} else {
				assert.Equal(t, taskstate.ReviewStatusPassed, latest.Status)
				assert.Contains(t, stdout, "Review passed for op-main")
				assertUnconsentedPublication(t, state)
				if test.manual {
					assert.Contains(t, stderr, "◆ REVIEW STEP · approval (manual)")
				}
			}
		})
	}
}
