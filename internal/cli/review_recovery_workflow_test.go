//go:build integration

package cli_test

import (
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/cli"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationReviewPrimaryProcessRecoveryKeepsFindingsForAudit(t *testing.T) {
	for _, test := range []struct {
		name       string
		liveness   agentexec.ProcessLiveness
		wantStatus taskstate.ReviewStatus
		guidance   string
	}{
		{"absent", agentexec.ProcessAbsent, taskstate.ReviewStatusFailed, "candidate may contain reviewer mutations"},
		{"live", agentexec.ProcessLive, taskstate.ReviewStatusRunning, "still active"},
		{"unknown", agentexec.ProcessUnknown, taskstate.ReviewStatusRunning, "cannot automatically recover"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReviewWorkflowFixture(t, "op-recovery", "Review interrupted", "Preserve the candidate.")
			attempt, err := fixture.taskStore.StartReviewWithOptions("alpha", "op-recovery", taskstate.StartReviewOptions{Pipeline: "standard", Step: "ai-review"})
			require.NoError(t, err)
			execution := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, Agent: "reviewer", StartedAt: attempt.StartedAt, SupervisorPID: 100, ChildPID: 101}
			_, err = fixture.taskStore.RecordReviewStep("alpha", "op-recovery", attempt.Attempt, taskstate.RecordReviewStepOptions{Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &execution})
			require.NoError(t, err)
			_, err = fixture.taskStore.RecordReviewFinding("alpha", "op-recovery", attempt.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Title: "Unfinished finding", Description: "Do not repair automatically.", Step: "ai-review"})
			require.NoError(t, err)
			fixture.options.Dependencies.ProcessProbe = func(pid int) (agentexec.ProcessLiveness, error) {
				assert.Contains(t, []int{100, 101}, pid)
				return test.liveness, nil
			}
			// Recovery must run even when replacement configuration is invalid.
			fixture.setConfig("reviews", map[string]any{"max_autonomous_review_attempts": 0})

			stdout, stderr := fixture.run("", "task", "run", "op-recovery")

			assert.Contains(t, stdout+stderr, test.guidance)
			loaded, task := fixture.loadFinalTask("op-recovery")
			require.Len(t, loaded.Reviews, 1)
			latest := loaded.Reviews[0]
			assert.Equal(t, test.wantStatus, latest.Status)
			require.Len(t, latest.Findings, 1)
			assert.Equal(t, "Unfinished finding", latest.Findings[0].Title)
			assert.Zero(t, latest.Findings[0].TargetedByRunAttempt)
			assert.Equal(t, test.liveness == agentexec.ProcessAbsent, taskstate.PrimaryReviewExecutionInterrupted(latest))
			assert.Empty(t, fixture.agent.launches)
			assertTaskNotPublished(t, loaded, task)
			if test.liveness == agentexec.ProcessAbsent {
				stdout, _ := fixture.run("", "task", "show", "review", "op-recovery", "1")
				assert.Contains(t, stdout, "audit-only")
				assert.Contains(t, stdout, "Unfinished finding")
			}
		})
	}
}

func TestIntegrationReviewFailedRepairCanRetryWithoutLosingBlocker(t *testing.T) {
	for _, test := range []struct {
		name    string
		outcome semanticAgentOutcome
		status  taskstate.RunStatus
	}{
		{"runtime failure", semanticAgentOutcome{err: errors.New("repair failed")}, taskstate.RunStatusFailed},
		{"start failure", semanticAgentOutcome{startFailure: &agentexec.StartError{Err: errors.New("repair did not start")}}, taskstate.RunStatusFailed},
		{"no completion", semanticAgentOutcome{exitWithoutCompletion: true}, taskstate.RunStatusSucceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReviewWorkflowFixture(t, "op-retry", "Initial work", "Needs repair.")
			fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "manual", "name": "approval"}}})
			fixture.budget(1)
			fixture.run("b\nBlocker\nFix it.\nRepair it.\nf\n", "task", "run", "op-retry")
			fixture.budget(2)
			fixture.completingRepairs("repairer", 1, false)
			fixture.agent.outcomes[0] = test.outcome

			_, _, err := fixture.runError("", "task", "run", "op-retry")
			if test.outcome.exitWithoutCompletion {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			failed, _ := fixture.loadFinalTask("op-retry")
			require.Len(t, failed.Runs, 2)
			assert.Equal(t, test.status, failed.Runs[1].Status)
			assert.Nil(t, failed.Runs[1].Completion)
			indexes, eligible := taskstate.UntargetedBlockingFindingIndexesForFollowUpInState(failed, failed.Reviews[0])
			assert.True(t, eligible)
			assert.Equal(t, []int{0}, indexes)

			fixture.completingRepairs("repairer", 1, true)
			stdout, _ := fixture.run("a\n", "task", "run", "op-retry")

			assert.Contains(t, stdout, "Published op-retry")
			retried, _ := fixture.loadFinalTask("op-retry")
			require.Len(t, retried.Runs, 3)
			assert.Equal(t, failed.Runs[1], retried.Runs[1])
			require.NotNil(t, retried.Runs[2].ReviewFollowUp)
			assert.Equal(t, []int{0}, retried.Runs[2].ReviewFollowUp.FindingIndexes)
			assert.Equal(t, 3, retried.Reviews[0].Findings[0].TargetedByRunAttempt)
			assert.Equal(t, taskstate.ReviewStatusPassed, retried.Reviews[1].Status)
		})
	}
}

func TestIntegrationTaskReviewFollowUpHeaderWriteFailureRecordsStartFailure(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-followup-header", "Follow-up header", "Repair the blocker.")
	paths := fixture.paths
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "manual", "name": "approval"}}})
	fixture.budget(2)
	fixture.configureImplementer("followup-header", agent.Profile{Command: "unused-implementer"})
	runStore := taskstate.NewStore(paths)
	reviewAttempt, err := runStore.StartReviewWithOptions("alpha", "op-followup-header", taskstate.StartReviewOptions{
		Pipeline: "standard", Step: "approval",
	})
	must.NoError(err)
	_, err = runStore.RecordReviewFinding("alpha", "op-followup-header", reviewAttempt.Attempt, taskstate.ReviewFinding{
		Type: taskstate.FindingTypeBlocking, Title: "Manual blocker", Description: "Repair before approval.", Step: "approval",
	})
	must.NoError(err)
	_, err = runStore.FinishReview("alpha", "op-followup-header", reviewAttempt.Attempt, taskstate.ReviewStatusBlocked)
	must.NoError(err)

	command := cli.NewRootCommandWithOptions(fixture.options)
	command.SetIn(strings.NewReader(""))
	command.SetOut(io.Discard)
	command.SetErr(failingReviewHeaderWriter{})
	command.SetArgs([]string{"task", "run", "op-followup-header"})
	err = command.Execute()
	must.Error(err)
	is.ErrorContains(err, "render agent run header")
	is.ErrorContains(err, "agent header output unavailable")
	is.Empty(fixture.agent.launches)

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-followup-header.yaml"), &state))
	must.Len(state.Runs, 2)
	latestRun, ok := taskstate.LatestRun(state)
	must.True(ok)
	is.Equal(taskstate.RunStatusFailed, latestRun.Status)
	must.NotNil(latestRun.ReviewFollowUp)
	must.NotEmpty(state.Events)
	lastEvent := state.Events[len(state.Events)-1]
	is.Equal(taskstate.EventRunStartFailed, lastEvent.Type)
	is.Contains(lastEvent.Error, "agent header output unavailable")
	latestReview, ok := taskstate.LatestReview(state)
	must.True(ok)
	indexes, eligible := taskstate.UntargetedBlockingFindingIndexesForFollowUpInState(state, latestReview)
	is.True(eligible)
	is.Equal([]int{0}, indexes)
}

type failingReviewHeaderWriter struct{}

func (failingReviewHeaderWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "== Agent run:") {
		return 0, errors.New("agent header output unavailable")
	}
	return len(p), nil
}
