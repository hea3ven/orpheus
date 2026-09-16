//go:build integration

package cli_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationTaskRunAutonomousReviewFollowUpRepairsCheckAndPublishes(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewDispatchFixture(t, "op-auto")
	paths := fixture.paths
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "check", "name": "status", "command": fixture.candidateCheck()}}})
	fixture.budget(4)
	fixture.completingRepairs("repairer", 2, true)
	stdout, stderr := fixture.run("k\n", "task", "run", "--repo-root", "op-auto")

	is.Contains(stdout, "Published op-auto")
	is.Contains(stderr, "Review blocked for op-auto by check \"status\".")
	is.Contains(stderr, "Autonomous review follow-up for op-auto targets review attempt 1 finding(s) 1.")
	is.NotContains(stderr, "Autonomous review attempt budget exhausted")
	is.Equal("pass\n", fixture.candidate.contents)
	is.NotEmpty(fixture.candidate.head)
	finalTask, err := fixture.tasks.Get(context.Background(), "op-auto")
	must.NoError(err)
	is.NotEmpty(finalTask.OrpheusMetadata().PRURL)
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-auto.yaml"), &state))
	must.Len(state.Runs, 2)
	must.Len(state.Reviews, 2)
	is.False(state.Runs[0].Execution.Interactive)
	is.False(state.Runs[1].Execution.Interactive)
	must.NotNil(state.Runs[1].ReviewFollowUp)
	is.Equal([]int{0}, state.Runs[1].ReviewFollowUp.FindingIndexes)
	is.Equal(2, state.Reviews[0].Findings[0].TargetedByRunAttempt)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	is.False(latest.AutonomousBudgetExhausted)
	is.NotEmpty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationTaskRunAttachedManualBlockerRepairsAndApprovalFinalizes(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewDispatchFixture(t, "op-manual")
	paths := fixture.paths
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "manual", "name": "approval"}}})
	fixture.budget(4)
	fixture.completingRepairs("repairer", 2, false)
	stdout, stderr, err := fixture.runError("b\nManual blocker\nRepair the implementation before approval.\nApply the fix.\nf\na\n", "task", "run", "--repo-root", "op-manual")

	must.NoError(err)
	is.Contains(stdout, "Recorded completion for op-manual")
	is.Contains(stdout, "Published op-manual")
	is.Contains(stderr, "== Agent run: implementation (run attempt 1) ==")
	is.Contains(stderr, "== Agent run: review follow-up (run attempt 2; review attempt 1; required blocking findings 1; advisory opportunities -) ==")
	is.Contains(stderr, "◆ REVIEW STEP · approval (manual)")
	is.Contains(stderr, "Review blocked for op-manual.")
	is.Contains(stderr, "Autonomous review follow-up for op-manual targets review attempt 1 finding(s) 1.")
	is.NotContains(stdout, "starting a fresh review")
	is.NotContains(stdout, "launching targeted repair")
	is.NotContains(stderr, "Resume with `orpheus task run op-manual`")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-manual.yaml"), &state))
	must.Len(state.Runs, 2)
	must.Len(state.Reviews, 2)
	must.NotNil(state.Runs[1].ReviewFollowUp)
	is.Equal(1, state.Runs[1].ReviewFollowUp.ReviewAttempt)
	is.Equal([]int{0}, state.Runs[1].ReviewFollowUp.FindingIndexes)
	is.Equal(2, state.Reviews[0].Findings[0].TargetedByRunAttempt)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	is.NotEmpty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationTaskReviewManualBlockerExhaustsBudgetWithoutExtraLaunch(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-manual-loop", "Manual loop", "Repair manual blockers.")
	paths := fixture.paths
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "manual", "name": "approval"}}})
	fixture.budget(2)
	fixture.completingRepairs("repairer", 1, false)
	stdout, stderr := fixture.run(strings.Join([]string{
		"b", "First blocker", "Fix the first issue.", "Repair it.", "f",
		"b", "Second blocker", "Fix the second issue.", "Repair it.", "f", "",
	}, "\n"), "task", "run", "op-manual-loop")

	is.Contains(stdout, "Recorded completion for op-manual-loop")
	is.Equal(2, strings.Count(stderr, "Review blocked for op-manual-loop."))
	is.Contains(stderr, "== Agent run: review follow-up (run attempt 2; review attempt 1; required blocking findings 1; advisory opportunities -) ==")
	is.Contains(stderr, "Autonomous review attempt budget exhausted for op-manual-loop after 2 review attempt(s).")
	is.NotContains(stderr, "run attempt 3")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-manual-loop.yaml"), &state))
	must.Len(state.Runs, 2)
	must.Len(state.Reviews, 2)
	must.NotNil(state.Runs[1].ReviewFollowUp)
	is.Equal(1, state.Runs[1].ReviewFollowUp.ReviewAttempt)
	is.Equal([]int{0}, state.Runs[1].ReviewFollowUp.FindingIndexes)
	is.Equal(2, state.Reviews[0].Findings[0].TargetedByRunAttempt)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	is.True(latest.AutonomousBudgetExhausted)
	is.Zero(latest.Findings[0].TargetedByRunAttempt)
}

func TestIntegrationTaskRunPreservedManualBlockerExhaustsFreshBudget(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-manual-budget", "Manual budget", "Keep the manual blocker.")
	paths := fixture.paths
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "manual", "name": "approval"}}})
	fixture.budget(1)
	runStore := taskstate.NewStore(paths)
	reviewAttempt, err := runStore.StartReviewWithOptions("alpha", "op-manual-budget", taskstate.StartReviewOptions{
		Pipeline: "standard", Step: "approval",
	})
	must.NoError(err)
	_, err = runStore.RecordReviewFinding("alpha", "op-manual-budget", reviewAttempt.Attempt, taskstate.ReviewFinding{
		Type: taskstate.FindingTypeBlocking, Title: "Manual blocker", Description: "Repair before approval.", Step: "approval",
	})
	must.NoError(err)
	_, err = runStore.FinishReview("alpha", "op-manual-budget", reviewAttempt.Attempt, taskstate.ReviewStatusBlocked)
	must.NoError(err)
	stdout, stderr := fixture.run("", "task", "run", "op-manual-budget")

	is.Empty(stdout)
	is.Contains(stderr, "Autonomous review attempt budget exhausted for op-manual-budget after 1 review attempt(s).")
	is.Empty(fixture.agent.launches)

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-manual-budget.yaml"), &state))
	must.Len(state.Runs, 1)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.True(latest.AutonomousBudgetExhausted)
	is.Zero(latest.Findings[0].TargetedByRunAttempt)
}

func TestIntegrationTaskRunAutonomousReviewLoopExhaustsPersistentCheckBlockers(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewDispatchFixture(t, "op-stubborn")
	paths := fixture.paths
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "check", "name": "unit", "command": fixture.candidateCheck()}}})
	fixture.budget(2)
	fixture.completingRepairs("repairer", 2, false)
	stdout, stderr := fixture.run("k\nk\n", "task", "run", "--repo-root", "op-stubborn")

	is.Contains(stdout, "Recorded completion for op-stubborn")
	is.NotContains(stdout, "Finalized op-stubborn")
	is.Equal(2, strings.Count(stderr, "Review blocked for op-stubborn by check \"unit\"."))
	is.Contains(stderr, "Autonomous review follow-up for op-stubborn targets review attempt 1 finding(s) 1.")
	is.Contains(stderr, "Autonomous review attempt budget exhausted for op-stubborn after 2 review attempt(s).")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-stubborn.yaml"), &state))
	must.Len(state.Runs, 2)
	must.Len(state.Reviews, 2)
	is.Equal(2, state.Reviews[0].Findings[0].TargetedByRunAttempt)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	is.True(latest.AutonomousBudgetExhausted)
	must.Len(latest.Findings, 1)
	is.Zero(latest.Findings[0].TargetedByRunAttempt)
	is.Empty(taskstate.FinalizationFacts(state).Commit)

	showStdout, showStderr := fixture.run("", "task", "show", "review", "op-stubborn", "2")
	is.Empty(showStderr)
	is.Contains(showStdout, "Autonomous review: attempt budget exhausted")
	is.Contains(showStdout, "Next step: autonomous review attempts are exhausted")

	taskStdout, taskStderr := fixture.run("", "task", "show", "op-stubborn")
	is.Empty(taskStderr)
	is.Contains(taskStdout, "Review attempt 2 blocked (autonomous review budget exhausted)")
}

func TestIntegrationTaskReviewResumedAutonomousFollowUpPreservesSelectedImplementer(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewDispatchFixture(t, "op-resume")
	paths := fixture.paths
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "manual", "name": "approval"}, {"kind": "check", "name": "status", "command": fixture.candidateCheck()}}})
	fixture.budget(4)
	fixture.completingRepairs("selected", 2, true)
	runStdout, runStderr := fixture.run("", "task", "run", "--repo-root", "--agent", "selected", "op-resume")
	is.Contains(runStdout, "Recorded completion for op-resume")
	is.Contains(runStderr, "Review for op-resume is waiting for manual step \"approval\"")
	is.Equal("fail\n", fixture.candidate.contents)

	fixture.configureAgentProfiles(agent.AgentDefaults{Implementer: "other"}, map[string]agent.Profile{"selected": {Command: "unused-implementer"}, "other": {Command: "unused-other"}})

	reviewStdout, reviewStderr := fixture.run("a\nk\na\n", "task", "run", "op-resume")
	is.Contains(reviewStdout, "Published op-resume")
	is.Contains(reviewStderr, "Resuming review attempt 1 at manual step \"approval\".")
	is.Contains(reviewStderr, "Review blocked for op-resume by check \"status\".")
	is.Contains(reviewStderr, "Autonomous review follow-up for op-resume targets review attempt 1 finding(s) 1.")
	is.NotContains(reviewStderr, "Autonomous review attempt budget exhausted")
	is.Equal("pass\n", fixture.candidate.contents)

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-resume.yaml"), &state))
	must.Len(state.Runs, 2)
	is.Equal("selected", state.Runs[0].Execution.Agent)
	is.Equal("selected", state.Runs[1].Execution.Agent)
	must.NotNil(state.Runs[1].ReviewFollowUp)
	is.Equal([]int{0}, state.Runs[1].ReviewFollowUp.FindingIndexes)
	must.Len(state.Reviews, 2)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	is.NotEmpty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationTaskRunResumesPausedAutomatedBlockerDecision(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review pause", "Resume the blocker decision.")
	paths := fixture.paths
	first := fixture.check("first", checkResult{})
	blocked := fixture.check("blocked", checkResult{code: 7}, checkResult{code: 7})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "check", "name": "first", "command": first}, {"kind": "check", "name": "blocked", "command": blocked}}})
	firstStdout, firstStderr := fixture.run("p\n", "task", "run", "op-main")
	is.Empty(firstStdout)
	is.Contains(firstStderr, "Automated blocker decisions for op-main are paused; resume with `orpheus task run op-main`.")

	stateStore := taskstate.NewStore(paths)
	state, err := stateStore.Load("alpha", "op-main")
	must.NoError(err)
	paused, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusWaitingForAutomatedDecision, paused.Status)
	must.Len(paused.Steps, 2)
	must.Len(paused.Findings, 1)

	secondStdout, secondStderr := fixture.run("r\np\n", "task", "run", "op-main")
	is.Empty(secondStdout)
	is.Contains(secondStderr, "Resuming review attempt 1 at automated blocker decision for step \"blocked\".")
	is.Contains(secondStderr, "Finding 1:")
	is.Contains(secondStderr, "Decision for finding 1 [k=keep, d=downgrade advisory, w=waive/cancel, r=restart step, p=pause]:")
	is.Contains(secondStderr, "Automated blocker decisions for op-main are paused; resume with `orpheus task run op-main`.")

	state, err = stateStore.Load("alpha", "op-main")
	must.NoError(err)
	pausedAgain, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(paused.Attempt, pausedAgain.Attempt)
	is.Equal(taskstate.ReviewStatusWaitingForAutomatedDecision, pausedAgain.Status)
	must.Len(pausedAgain.Steps, 2)
	must.Len(pausedAgain.Findings, 1)

	finalStdout, finalStderr := fixture.run("w\nAccepted for this task.\n", "task", "run", "op-main")
	is.Contains(finalStdout, "Finalized op-main")
	is.Contains(finalStderr, "Resuming review attempt 1 at automated blocker decision for step \"blocked\".")
	is.Contains(finalStderr, "Finding 1:")

	state, err = stateStore.Load("alpha", "op-main")
	must.NoError(err)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(paused.Attempt, latest.Attempt)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Steps, 2)
	must.Len(latest.Findings, 1)
	is.Equal("Accepted for this task.", latest.Findings[0].Waiver)
	is.Equal([]string{"first", "blocked", "blocked"}, fixture.checkCalls)
}
