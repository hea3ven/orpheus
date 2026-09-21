//go:build integration

package cli_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	reviewconfig "github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
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
	is.Contains(stderr, "reviewed.txt")
	is.NotContains(stderr, "git diff --stat:")
	is.Contains(stderr, "◆ REVIEW STEP · local-review (manual)")
	is.Contains(stderr, "Review action [a=approve, b=block, v=advisory, t=task, q=abort]")
	is.NotContains(stderr, "p=promote advisory")
	is.Contains(stderr, "Choose approve, block, advisory, task, or abort.")
	is.Contains(stdout, "Finalized op-main")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	is.Empty(latest.Findings)
	is.NotEmpty(taskstate.FinalizationFacts(state).Commit)
	_, task := fixture.loadFinalTask("op-main")
	is.Equal(taskmodel.StatusClosed, task.Status)
	is.Equal([]string{"op-main"}, fixture.tasks.closed)
	is.Equal([]string{"main"}, fixture.candidate.pushes)
}

func TestIntegrationWorkflowTaskReviewManualContextShowsOriginalAndLatestFollowUpCompletion(t *testing.T) {
	is := assert.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Original implementation", "Implemented the main task.")
	paths := fixture.paths
	repoPath := taskWorkflowRepoRoot
	recordReviewFollowUpCompletion(t, paths, "alpha", "op-main", repoPath, 1, "First fix", "Addressed the first review blocker.")
	recordReviewFollowUpCompletion(t, paths, "alpha", "op-main", repoPath, 2, "Latest fix", "Addressed the most recent review blocker.")

	stdout, stderr := fixture.run("a\n", "task", "run", "op-main")

	is.Contains(stderr, "○ ORIGINAL COMPLETION\nOriginal implementation")
	is.Contains(stderr, "DESCRIPTION\nImplemented the main task.")
	is.Contains(stderr, "TECHNICAL EXPLANATION\nTechnical explanation.")
	is.Contains(stderr, "● LATEST FIX COMPLETION\nLatest fix")
	is.Contains(stderr, "DESCRIPTION\nAddressed the most recent review blocker.")
	is.Contains(stderr, "TECHNICAL EXPLANATION\nTechnical explanation.")
	is.NotContains(stderr, "Latest completion: Latest fix")
	is.NotContains(stderr, "Latest fix completion: First fix")
	is.Contains(stdout, "Finalized op-main")

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
	is.Equal(headBefore, fixture.candidate.head)
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

	is.Contains(stdout, "Finalized op-main")
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

	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "Created follow-up Bead op-41 for review finding 1.")
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
	is.Contains(stderr, "Failed to create follow-up Bead for review finding 1")
	is.Equal(headBefore, fixture.candidate.head)
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
	is.Equal(headBefore, fixture.candidate.head)
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

func TestIntegrationWorkflowTaskReviewInvalidReviewAgentConfigDoesNotStartFreshAttempt(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review agent config", "Validate preflight.")
	paths := fixture.paths
	fixture.pipelines("standard", map[string][]map[string]any{
		"standard": {{"kind": "agent_review", "name": "ai-review"}},
	})

	stdout, stderr, err := fixture.runError("", "task", "run", "op-main")

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "load agent profiles")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	_, ok := taskstate.LatestReview(state)
	is.False(ok, "fresh invalid agent config must not persist a review attempt")
}

func TestIntegrationWorkflowTaskReviewInvalidReviewAgentConfigDoesNotResumeManualAttempt(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review agent resume", "Validate preflight.")
	paths := fixture.paths
	fixture.pipelines("standard", map[string][]map[string]any{
		"standard": {
			{"kind": "agent_review", "name": "ai-review"},
			{"kind": "manual", "name": "inspect"},
		},
	})
	runStore := taskstate.NewStore(paths)
	reviewAttempt, err := runStore.StartReviewWithOptions("alpha", "op-main", taskstate.StartReviewOptions{
		Pipeline: "standard",
		Step:     "ai-review",
	})
	must.NoError(err)
	_, err = runStore.RecordReviewStep("alpha", "op-main", reviewAttempt.Attempt, taskstate.RecordReviewStepOptions{
		Kind: "agent_review",
		Name: "ai-review",
	})
	must.NoError(err)
	_, err = runStore.PauseReviewForManual("alpha", "op-main", reviewAttempt.Attempt, "inspect")
	must.NoError(err)

	stdout, stderr, err := fixture.runError("", "task", "run", "op-main")

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "load agent profiles")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(reviewAttempt.Attempt, latest.Attempt)
	is.Equal(taskstate.ReviewStatusWaitingForManual, latest.Status)
	is.Equal("inspect", latest.Step)
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
			is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
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

func TestIntegrationWorkflowTaskReviewCheckBlockerKeepAcceptsEOFAnswer(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review keep EOF", "Keep blocker without newline.")
	paths := fixture.paths

	check := fixture.check("check", checkResult{code: 7})
	must.NoError(state.SeedMemoryConfigYAML(paths, reviewconfig.ConfigFile, map[string]any{
		"reviews": map[string]any{
			"default_pipeline":               "standard",
			"max_autonomous_review_attempts": 1,
			"pipelines": map[string]any{
				"standard": map[string]any{"steps": []map[string]any{{"kind": "check", "name": "unit", "command": check}}},
			},
		},
	}))

	stdout, stderr := fixture.run("k", "task", "run", "--pipeline", "standard", "op-main")

	is.Empty(stdout)
	is.Contains(stderr, "Decision for finding 1")
	is.Contains(stderr, "Autonomous review attempt budget exhausted")
	is.NotContains(stderr, "Automated blocker decisions for op-main were interrupted")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	is.True(latest.AutomatedBlockerDecisionKept)
	is.False(latest.AutomatedBlockerDecisionInterrupted)
	is.True(latest.AutonomousBudgetExhausted)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationWorkflowTaskReviewCheckBlockerReasonAcceptsEOFAnswer(t *testing.T) {
	tests := []struct {
		name          string
		input         string
		wantType      taskstate.FindingType
		wantDowngrade string
		wantWaiver    string
		wantPrompt    string
	}{
		{
			name:          "downgrade",
			input:         "d\nFalse positive for this task.",
			wantType:      taskstate.FindingTypeAdvisory,
			wantDowngrade: "False positive for this task.",
			wantPrompt:    "Downgrade reason",
		},
		{
			name:       "waive",
			input:      "w\nKnown flaky check.",
			wantType:   taskstate.FindingTypeBlocking,
			wantWaiver: "Known flaky check.",
			wantPrompt: "Waiver reason",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			fixture := newReviewWorkflowFixture(t, "op-main", "Review reason EOF", "Accept blocker reason.")
			paths := fixture.paths

			check := fixture.check("check", checkResult{code: 7})
			fixture.pipelines("standard", map[string][]map[string]any{
				"standard": {{"kind": "check", "name": "unit", "command": check}},
			})

			stdout, stderr := fixture.run(test.input, "task", "run", "--pipeline", "standard", "op-main")

			is.Contains(stdout, "Finalized op-main")
			is.Contains(stderr, test.wantPrompt)
			is.NotContains(stderr, "Automated blocker decisions for op-main were interrupted")

			var state taskstate.TaskState
			must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
			latest, ok := taskstate.LatestReview(state)
			must.True(ok)
			is.Equal(taskstate.ReviewStatusPassed, latest.Status)
			must.Len(latest.Findings, 1)
			is.Equal(test.wantType, latest.Findings[0].Type)
			is.Equal(test.wantDowngrade, latest.Findings[0].DowngradeReason)
			is.Equal(test.wantWaiver, latest.Findings[0].Waiver)
			is.NotEmpty(taskstate.FinalizationFacts(state).Commit)
		})
	}
}

func TestIntegrationWorkflowTaskReviewCheckBlockerDowngradeContinuesPipeline(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review check downgrade", "Downgrade blocker.")
	paths := fixture.paths

	check := fixture.check("check", checkResult{code: 7})
	fixture.pipelines("standard", map[string][]map[string]any{
		"standard": []map[string]any{
			{"kind": "check", "name": "unit", "command": check},
			{"kind": "manual", "name": "approval"},
		},
	})

	stdout, stderr := fixture.run("d\nFalse positive for this task.\na\n", "task", "run", "--pipeline", "standard", "op-main")

	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "Automated blocking findings from step \"unit\"")
	is.Contains(stderr, "Decision for finding 1")
	is.Contains(stderr, "◆ REVIEW STEP · approval (manual)")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeAdvisory, latest.Findings[0].Type)
	is.Equal("False positive for this task.", latest.Findings[0].DowngradeReason)
	is.NotEmpty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationWorkflowTaskReviewCheckBlockerWaiverContinuesPipeline(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review check waiver", "Waive blocker.")
	paths := fixture.paths

	check := fixture.check("check", checkResult{code: 7})
	fixture.pipelines("standard", map[string][]map[string]any{
		"standard": []map[string]any{
			{"kind": "check", "name": "unit", "command": check},
			{"kind": "manual", "name": "approval"},
		},
	})

	stdout, stderr := fixture.run("c\nKnown flaky check.\na\n", "task", "run", "--pipeline", "standard", "op-main")

	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "Automated blocking findings from step \"unit\"")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Equal("Known flaky check.", latest.Findings[0].Waiver)
	is.NotEmpty(taskstate.FinalizationFacts(state).Commit)
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

func TestIntegrationWorkflowTaskRunAfterInterruptedAutomatedBlockerDecisionRequiresFreshReview(t *testing.T) {
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
	is.Contains(stderr, "Fresh review blocker dispositions for op-main were interrupted")
	is.Empty(fixture.agent.launches)

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	must.Len(state.Runs, 1)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
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
	reviewAttempt, err := stateStore.StartReviewWithOptions("alpha", "op-main", taskstate.StartReviewOptions{Pipeline: "standard", Step: "unit"})
	must.NoError(err)
	_, err = stateStore.RecordReviewStep("alpha", "op-main", reviewAttempt.Attempt, taskstate.RecordReviewStepOptions{Kind: taskstate.ReviewStepKindCheck, Name: "unit"})
	must.NoError(err)
	_, err = stateStore.RecordReviewFinding("alpha", "op-main", reviewAttempt.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Step: "unit", Title: "Check \"unit\" failed", Description: "Fix the failing check."})
	must.NoError(err)

	stdout, stderr := fixture.run("k\n", "task", "run", "op-main")

	is.Empty(stdout)
	is.Contains(stderr, "Open blocking findings from the latest review")
	is.Contains(stderr, "Finding 1 from step unit")
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
	strictCheck := fixture.check("strict", checkResult{code: 7, stdout: "strict pipeline\n"}, checkResult{code: 7, stdout: "strict pipeline\n"})
	fixture.pipelines("default", map[string][]map[string]any{
		"default": {{"kind": "check", "name": "default", "command": defaultCheck}},
		"strict":  {{"kind": "check", "name": "strict", "command": strictCheck}},
	})

	firstStdout, firstStderr := fixture.run("", "task", "run", "--pipeline", "strict", "op-main")
	is.Contains(firstStdout, "strict pipeline")
	is.NotContains(firstStdout, "default pipeline")
	is.Contains(firstStderr, "Automated blocker decisions for op-main were interrupted")

	recoveryStdout, recoveryStderr := fixture.run("a\nFixed outside Orpheus.\nd\nStrict failure accepted.", "task", "run", "op-main")

	is.Contains(recoveryStdout, "strict pipeline")
	is.NotContains(recoveryStdout, "default pipeline")
	is.Contains(recoveryStdout, "Finalized op-main")
	is.Contains(recoveryStderr, "Open blocking findings from the latest review")
	is.Contains(recoveryStderr, "Finding 1 from step strict")
	is.Contains(recoveryStderr, "Title: Check \"strict\" failed")
	is.Contains(recoveryStderr, "== Review step: strict (check) ==")
	is.NotContains(recoveryStderr, "== Review step: default (check) ==")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	must.Len(state.Reviews, 2)
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
	reviewAttempt, err := runStore.StartReviewWithOptions("alpha", "op-main", taskstate.StartReviewOptions{
		Pipeline: "standard",
		Step:     "lint",
	})
	must.NoError(err)
	_, err = runStore.RecordReviewStep("alpha", "op-main", reviewAttempt.Attempt, taskstate.RecordReviewStepOptions{
		Kind: "check",
		Name: "lint",
	})
	must.NoError(err)
	_, err = runStore.PauseReviewForManual("alpha", "op-main", reviewAttempt.Attempt, "inspect")
	must.NoError(err)

	stdout, stderr := fixture.run("a\n", "task", "run", "op-main")

	is.Contains(stdout, "Finalized op-main")
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
	reviewAttempt, err := runStore.StartReviewWithOptions("alpha", "op-main", taskstate.StartReviewOptions{
		Pipeline: "standard",
		Step:     "inspect",
	})
	must.NoError(err)
	_, err = runStore.PauseReviewForManual("alpha", "op-main", reviewAttempt.Attempt, "inspect")
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
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review override", "Use CLI-selected pipeline.")
	paths := fixture.paths
	repo := taskWorkflowRepository()
	repo.ReviewPipeline = "repo"
	fixture.withRegisteredRepos(repo)

	fail := "must-not-run"
	pass := fixture.check("cli", checkResult{stdout: "cli pipeline\n"})
	fixture.pipelines("global", map[string][]map[string]any{
		"global": []map[string]any{{"kind": "check", "name": "global", "command": fail}},
		"repo":   []map[string]any{{"kind": "check", "name": "repo", "command": fail}},
		"cli":    []map[string]any{{"kind": "check", "name": "cli", "command": pass}},
	})

	stdout, stderr := fixture.run("", "task", "run", "--pipeline", "cli", "op-main")

	is.Contains(stdout, "cli pipeline")
	is.NotContains(stdout, "wrong pipeline")
	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "== Review step: cli (check) ==")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal("cli", latest.Pipeline)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
}

func TestIntegrationWorkflowTaskReviewPipelineAliasResolvesToGlobalPipeline(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review alias", "Use alias-selected pipeline.")
	paths := fixture.paths
	repo := taskWorkflowRepository()
	repo.ReviewPipeline = "repo"
	repo.ReviewPipelineAliases = map[string]string{"quick": "cli"}
	fixture.withRegisteredRepos(repo)

	fail := "must-not-run"
	pass := fixture.check("alias", checkResult{stdout: "alias pipeline\n"})
	fixture.pipelines("global", map[string][]map[string]any{
		"global": {{"kind": "check", "name": "global", "command": fail}},
		"repo":   {{"kind": "check", "name": "repo", "command": fail}},
		"cli":    {{"kind": "check", "name": "cli", "command": pass}},
	})

	stdout, stderr := fixture.run("", "task", "run", "--pipeline", "quick", "op-main")

	is.Contains(stdout, "alias pipeline")
	is.NotContains(stdout, "wrong pipeline")
	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "== Review step: cli (check) ==")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal("cli", latest.Pipeline)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
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
