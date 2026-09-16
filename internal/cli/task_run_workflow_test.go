//go:build integration

package cli_test

import (
	"errors"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These journeys use real CLI routing, profile resolution, workflow orchestration,
// agent context/completion, and stores over memory-backed Orpheus persistence.
// Task, Git, process liveness, and agent execution are supplied collaborators.
// Review outcomes are supplied explicitly, not produced by the real pipeline.
// Real Git mutation, process supervision, and review execution have separate contracts.
func TestIntegrationTaskRunCompletionLeavesTaskAwaitingManualReview(t *testing.T) {
	is := assert.New(t)
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-completion"))
	completion := aCompletion()
	fixture.withCompletingAgent(completion)
	fixture.withSuppliedManualReview()

	stdout, stderr, err := fixture.execute("task", "run", "op-completion")
	require.NoError(t, err, "task run; stderr: %s", stderr)
	statusOutput, statusStderr, statusErr := fixture.execute("status", "--no-truncate")
	require.NoError(t, statusErr, "status; stderr: %s", statusStderr)

	finalState, finalTask := fixture.loadFinalTask("op-completion")
	require.Len(t, finalState.Runs, 1)
	run := finalState.Runs[0]
	is.Equal(taskstate.RunStatusSucceeded, run.Status)
	is.NotNil(run.Execution.FinishedAt)
	is.Contains(stdout, "Recorded completion for op-completion")
	assertCompletionRecorded(t, completion, run.Completion)
	is.Empty(run.Completion.Commit)
	is.Empty(run.Completion.CommitError)
	is.Equal(taskmodel.StatusInProgress, finalTask.Status)
	assertWaitingForManualReview(t, finalState)
	assertTaskNotPublished(t, finalState, finalTask)
	fixture.assertAgentRanInTaskWorktree("op-completion")
	fixture.assertIsTaskInAgentContext("op-completion")
	assertStatusShowsManualReview(t, statusOutput, "op-completion")
}

func TestIntegrationTaskRunAgentFailureRecordsFailedAttempt(t *testing.T) {
	is := assert.New(t)
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-failure"))
	agentFailure := errors.New("agent failed")
	fixture.withFailingAgent(agentFailure)

	stdout, stderr, err := fixture.execute("task", "run", "op-failure")

	is.ErrorIs(err, agentFailure)
	is.Empty(stdout)
	is.Contains(stderr, "== Agent run: implementation (run attempt 1) ==")
	finalState, finalTask := fixture.loadFinalTask("op-failure")
	require.Len(t, finalState.Runs, 1)
	run := finalState.Runs[0]
	is.Equal(taskstate.RunStatusFailed, run.Status)
	is.Equal(taskstate.RunStatusFailed, run.Execution.Status)
	is.NotNil(run.Execution.FinishedAt)
	is.Nil(run.Completion)
	is.Equal(taskmodel.StatusInProgress, finalTask.Status)
	is.Empty(finalState.Reviews)
	assertTaskNotPublished(t, finalState, finalTask)
	assertEventTypes(t, finalState.Events,
		taskstate.EventWorktreeCreated, taskstate.EventRunStarted, taskstate.EventRunFinished,
	)
	require.Len(t, finalState.Events, 3)
	is.Equal(taskstate.RunStatusFailed, finalState.Events[2].Status)
}

func TestIntegrationTaskRunSuccessfulExitWithoutCompletionAllowsOrdinaryRetry(t *testing.T) {
	is := assert.New(t)
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-retry"))
	fixture.withAgentExitingWithoutCompletion(2)

	_, stderr, err := fixture.execute("task", "run", "op-retry")
	require.NoError(t, err, "first task run; stderr: %s", stderr)
	first, _ := fixture.loadFinalTask("op-retry")
	require.Len(t, first.Runs, 1)
	is.Equal(taskstate.RunStatusSucceeded, first.Runs[0].Status)
	is.Nil(first.Runs[0].Completion)
	is.Empty(first.Reviews)

	_, stderr, err = fixture.execute("task", "run", "op-retry")
	require.NoError(t, err, "retry task run; stderr: %s", stderr)

	retried, finalTask := fixture.loadFinalTask("op-retry")
	require.Len(t, retried.Runs, 2)
	is.Equal(first.Runs[0], retried.Runs[0], "retry must preserve the first attempt")
	is.Equal(2, retried.Runs[1].Attempt)
	is.Equal(taskstate.RunStatusSucceeded, retried.Runs[1].Status)
	is.Nil(retried.Runs[1].Completion)
	is.Equal(first.GitFacts, retried.GitFacts)
	is.Equal(first.WorkDirectory, retried.WorkDirectory)
	is.Equal(gitmeta.TaskWorktreeLifecycleReused, fixture.git.lastLifecycle())
	is.Equal(taskmodel.StatusInProgress, finalTask.Status)
	is.Empty(retried.Reviews)
	assertTaskNotPublished(t, retried, finalTask)
	assertEventTypes(t, retried.Events,
		taskstate.EventWorktreeCreated, taskstate.EventRunStarted, taskstate.EventRunFinished,
		taskstate.EventWorktreeReused, taskstate.EventRunStarted, taskstate.EventRunFinished,
	)
}

func TestIntegrationTaskRunAbsentProcessesInterruptPreviousAttemptBeforeRetry(t *testing.T) {
	is := assert.New(t)
	fixture := newTaskWorkflowFixture(t, anInProgressTask("op-recovery"))
	const supervisorPID, childPID = 100, 101
	fixture.seedRunningAttempt("op-recovery", supervisorPID, childPID)
	fixture.withAbsentProcesses(supervisorPID, childPID)
	fixture.withAgentExitingWithoutCompletion(1)

	_, stderr, err := fixture.execute("task", "run", "op-recovery")
	require.NoError(t, err, "task run; stderr: %s", stderr)

	is.Contains(stderr, "reconciled interrupted implementation attempt")
	recovered, finalTask := fixture.loadFinalTask("op-recovery")
	require.Len(t, recovered.Runs, 2)
	is.Equal(taskstate.RunStatusInterrupted, recovered.Runs[0].Status)
	is.Equal(childPID, recovered.Runs[0].Execution.ChildPID)
	is.Equal(2, recovered.Runs[1].Attempt)
	is.Equal(taskstate.RunStatusSucceeded, recovered.Runs[1].Status)
	is.Equal(gitmeta.TaskWorktreeLifecycleReused, fixture.git.lastLifecycle())
	require.GreaterOrEqual(t, len(recovered.Events), 2)
	interruption := recovered.Events[1]
	is.Equal(taskstate.EventRunInterrupted, interruption.Type)
	is.Equal(1, interruption.Attempt)
	is.Equal("task_run", interruption.InterruptionTrigger)
	is.Equal("supervisor_and_child_pids_absent", interruption.InterruptionReason)
	is.Equal(map[int]int{supervisorPID: 1, childPID: 1}, fixture.probedPIDs)
	is.Empty(recovered.Reviews)
	assertTaskNotPublished(t, recovered, finalTask)
}

func TestIntegrationTaskRunBlockingReviewDispatchesTargetedRepair(t *testing.T) {
	is := assert.New(t)
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-follow-up"))
	implementation, repair := aCompletion(), aRepairCompletion()
	finding := aBlockingFinding()
	fixture.withCompletingAgent(implementation, repair)
	fixture.withSuppliedKeptBlockerThenManualReview(finding)

	_, stderr, err := fixture.execute("task", "run", "op-follow-up")
	require.NoError(t, err, "task run; stderr: %s", stderr)

	is.Equal(2, fixture.reviewPipelineCalls, "implementation and repair reviews")
	loaded, finalTask := fixture.loadFinalTask("op-follow-up")
	finalTaskIDs := fixture.loadFinalTaskIDs()
	require.Len(t, loaded.Runs, 2)
	is.Equal(taskstate.RunStatusSucceeded, loaded.Runs[0].Status)
	is.Equal(taskstate.RunStatusSucceeded, loaded.Runs[1].Status)
	assertCompletionRecorded(t, implementation, loaded.Runs[0].Completion)
	assertCompletionRecorded(t, repair, loaded.Runs[1].Completion)
	is.Equal(4243, loaded.Runs[1].Execution.ChildPID, "repair child PID")
	followUp := loaded.Runs[1].ReviewFollowUp
	require.NotNil(t, followUp)
	is.Equal(1, followUp.ReviewAttempt)
	is.Equal([]int{0}, followUp.FindingIndexes)
	fixture.assertRepairContextContainsFinding(finding)
	fixture.assertRepairSession("op-follow-up", loaded.Runs[1].Execution)
	is.Equal(gitmeta.TaskWorktreeLifecycleReused, fixture.git.lastLifecycle())
	latest, ok := taskstate.LatestReview(loaded)
	require.True(t, ok, "review after repair")
	is.Equal(taskstate.ReviewStatusWaitingForManual, latest.Status)
	is.Equal(taskmodel.StatusInProgress, finalTask.Status)
	is.Equal([]string{"op-follow-up"}, finalTaskIDs, "repair must not create another task")
	assertTaskNotPublished(t, loaded, finalTask)
}
