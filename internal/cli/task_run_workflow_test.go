//go:build integration

package cli_test

import (
	"encoding/json"
	"errors"
	"io"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/review"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These journeys use real CLI routing, profile resolution, workflow orchestration,
// agent context/completion, review pipeline execution, and stores over memory-backed
// Orpheus persistence. Task, Git, process liveness, and agent execution are supplied
// collaborators. Real Git mutation and process supervision have separate contracts.
func TestIntegrationWorkflowTaskRunCompletionLeavesTaskAwaitingManualReview(t *testing.T) {
	is := assert.New(t)
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-completion"))
	_, workdir := fixture.expectedTarget("op-completion")
	fixture.options.Dependencies.Terminal.InputIsTerminal = func(io.Reader) bool { return false }
	completion := aCompletion()
	fixture.withCompletingAgent(completion)
	fixture.agent.afterCompletion = func() error {
		live, err := fixture.taskStore.Load("alpha", "op-completion")
		if err != nil {
			return err
		}
		require.Len(t, live.Runs, 1)
		is.Equal(taskstate.RunStatusRunning, live.Runs[0].Status)
		require.NotNil(t, live.Runs[0].Completion)
		is.Empty(live.Runs[0].Completion.Commit, "handoff cannot capture while its child remains active")
		is.Empty(fixture.git.captured)
		return nil
	}
	fixture.withRealReviewPipeline()
	fixture.options.Dependencies.ReviewStatus = fixture.git.ShortStatus
	fixture.git.diffStats = map[[2]string]string{
		{"initial-commit", "output-1"}: " reviewed.txt | 2 ++\n 1 file changed, 2 insertions(+)\n",
	}

	fixture.git.captureLeavesChanges = true
	stdout, stderr, err := fixture.execute("task", "run", "op-completion")
	require.ErrorContains(t, err, "uncommitted changes")
	recorded, _ := fixture.loadFinalTask("op-completion")
	require.Len(t, recorded.Runs, 1)
	require.NotNil(t, recorded.Runs[0].Completion)
	is.Equal("output-1", recorded.Runs[0].Completion.Commit, "hook leftovers cannot prevent recording successful output")
	is.Empty(recorded.Reviews, "refused admission must not start review")
	is.Zero(fixture.reviewPipelineRuns)
	is.True(fixture.git.hasCandidateChanges, "admission must preserve hook leftovers")
	require.Len(t, fixture.agent.launches, 1)
	require.Len(t, fixture.git.captured, 1)

	// Explicit operator cleanup admits the recorded output, not another implementer.
	fixture.git.hasCandidateChanges = false
	continuedOut, continuedErrOut, err := fixture.execute("task", "run", "op-completion")
	require.ErrorIs(t, err, review.ErrInteractionRequired, "task run; stderr: %s", continuedErrOut)
	stdout, stderr = stdout+continuedOut, stderr+continuedErrOut
	require.Len(t, fixture.agent.launches, 1)
	require.Len(t, fixture.git.captured, 1)
	statusOutput, statusStderr, statusErr := fixture.execute("status", "--no-truncate")
	require.NoError(t, statusErr, "status; stderr: %s", statusStderr)

	finalState, finalTask := fixture.loadFinalTask("op-completion")
	require.Len(t, finalState.Runs, 1)
	run := finalState.Runs[0]
	is.Equal(taskstate.RunStatusSucceeded, run.Status)
	is.NotNil(run.Execution.FinishedAt)
	is.Contains(stdout, "Recorded completion for op-completion")
	for _, fact := range []string{"PREPARATION", "WORKDIR", "orpheus/op-completion", "initial-commit", "IMPLEMENTATION", "implement (agent)", "REVIEW", "REVISION", "R1 | output-1", "Full task | initial-commit -> output-1", "local-review (manual)"} {
		is.Contains(stderr, fact)
	}
	is.Contains(stderr, "TASK CHANGES SINCE BASELINE\ninitial-commit -> output-1\n reviewed.txt | 2 ++\n 1 file changed, 2 insertions(+)")
	is.Contains(stderr, "GIT STATUS --SHORT (LIVE CHECKOUT)\n(clean)")
	is.Equal([]diffStatRequest{{dir: workdir, baseline: "initial-commit", revision: "output-1"}}, fixture.git.diffStatRequests)
	assertCompletionRecorded(t, completion, run.Completion)
	is.Equal("output-1", run.Completion.Commit)
	require.NotNil(t, finalState.Work)
	require.Len(t, fixture.git.captured, 1)
	is.Equal(completion.CommitSubject+"\n\n"+completion.CommitBody, fixture.git.captured[0].Message)
	effort := finalState.Work.Workflows[0].Phases[1].(*work.ImplementationPhase)
	observed, ok := effort.Execution.Process.(*work.ExitedProcess)
	require.True(t, ok)
	is.Equal(0, *observed.Code)
	require.NotNil(t, effort.Completion)
	is.False(effort.Completion.CompletedAt.After(observed.At))
	output, err := finalState.Work.Workflows[0].Output(work.OutputRef{Phase: 1})
	require.NoError(t, err)
	is.Equal("output-tree-1", output.Tree)
	is.Equal("refs/orpheus/revisions/616c706861/6f702d636f6d706c6574696f6e/0/1/output", output.RetainedRef)
	reviewScope := finalState.Work.Workflows[0].Phases[2].(*work.ReviewPhase).Scope
	is.Equal(work.OutputRef{Phase: 1}, reviewScope.Output)
	is.Nil(reviewScope.DeltaFrom)
	is.Empty(run.Completion.CommitError)
	is.Equal(taskmodel.StatusInProgress, finalTask.Status)
	assertWaitingForManualReview(t, finalState)
	assertTaskNotPublished(t, finalState, finalTask)
	fixture.assertAgentRanInTaskWorktree("op-completion")
	fixture.assertIsTaskInAgentContext("op-completion")
	is.Equal("initial-commit", finalState.BaselineCommit)
	is.Contains(fixture.agent.contexts[0], "- Task baseline: initial-commit")
	inspection, _, showErr := fixture.execute("task", "show", "op-completion")
	require.NoError(t, showErr)
	is.Contains(inspection, "initial-commit")
	is.Contains(inspection, "orpheus/op-completion")
	assertStatusShowsManualReview(t, statusOutput, "op-completion")

	_, _, err = fixture.execute("task", "block", "op-completion")
	require.ErrorContains(t, err, "review boundary")
	preserved, preservedTask := fixture.loadFinalTask("op-completion")
	is.Equal(finalState, preserved, "blocking refusal preserves review and completion history")
	is.Equal(finalTask, preservedTask)

	fixture.git.hasStagedChanges = true
	_, _, err = fixture.execute("task", "run", "op-completion")

	require.ErrorContains(t, err, "review requires a clean Git index")
	rejected, rejectedTask := fixture.loadFinalTask("op-completion")
	is.Equal(finalState, rejected, "staged changes must not resume the paused review")
	is.Equal(finalTask, rejectedTask)
	is.Equal(1, fixture.reviewPipelineRuns)
	is.True(fixture.git.hasStagedChanges)

	fixture.git.hasStagedChanges = false
	_, stderr, err = fixture.execute("task", "run", "op-completion")

	require.ErrorIs(t, err, review.ErrInteractionRequired, "resume review; stderr: %s", stderr)
	is.Contains(stderr, "REVIEW | resumed")
	is.Contains(stderr, "Full task | initial-commit -> output-1")
	is.Contains(stderr, "TASK CHANGES SINCE BASELINE\ninitial-commit -> output-1\n reviewed.txt | 2 ++\n 1 file changed, 2 insertions(+)")
	is.Contains(stderr, "GIT STATUS --SHORT (LIVE CHECKOUT)\n(clean)")
	is.Equal([]diffStatRequest{
		{dir: workdir, baseline: "initial-commit", revision: "output-1"},
		{dir: workdir, baseline: "initial-commit", revision: "output-1"},
	}, fixture.git.diffStatRequests)
	resumed, resumedTask := fixture.loadFinalTask("op-completion")
	require.Len(t, resumed.Reviews, 1, "resumption must retain the existing attempt")
	is.Equal(finalState.Reviews[0].Attempt, resumed.Reviews[0].Attempt)
	is.Equal(finalState.Reviews[0].StartedAt, resumed.Reviews[0].StartedAt)
	is.Equal(finalTask, resumedTask)
	is.Equal(2, fixture.reviewPipelineRuns)
	is.Len(fixture.git.captured, 1, "review resumption must not create another implementation revision")
	assertWaitingForManualReview(t, resumed)
	assertTaskNotPublished(t, resumed, resumedTask)

	fixture.options.Dependencies.Terminal.InputIsTerminal = func(io.Reader) bool { return true }
	stdout, stderr, err = fixture.executeWithInput("a\nq\n", "task", "run", "op-completion")
	require.NoError(t, err, "interactive continuation; stderr: %s", stderr)
	is.Contains(stdout, "publication awaits explicit authorization")
	publication, pendingTask := fixture.loadFinalTask("op-completion")
	assertUnconsentedPublication(t, publication)
	is.Len(publication.Work.Workflows[0].Phases, 4)
	is.Len(publication.Reviews, 1)
	inspectJSON, _, err := fixture.execute("task", "show", "workflow", "op-completion", "--json")
	require.NoError(t, err)
	var view workflow.View
	require.NoError(t, json.Unmarshal([]byte(inspectJSON), &view))
	require.NotNil(t, view.Pending)
	is.Equal(workflow.InteractionPublication, view.Pending.ID.Kind)
	is.Equal(workflow.TaskRunActionAwaitDecision, view.Next.Action)
	is.Equal("output-1", view.Revision.Code.Commit)
	is.NotEmpty(view.PublicationContent)
	is.False(view.Publication.Consented)
	statusOutput, _, err = fixture.execute("status", "--no-truncate")
	require.NoError(t, err)
	is.Contains(statusOutput, "publication decision pending")
	inspection, _, err = fixture.execute("task", "show", "op-completion")
	require.NoError(t, err)
	is.Contains(inspection, "Workflow next action: await_decision")
	is.Equal(3, fixture.reviewPipelineRuns)
	is.Len(fixture.git.captured, 1)
	is.Equal(resumedTask, pendingTask)
	fixture.options.Dependencies.Terminal.InputIsTerminal = func(io.Reader) bool { return false }
	_, _, err = fixture.executeWithInput("a\n", "task", "run", "op-completion")
	require.ErrorIs(t, err, review.ErrInteractionRequired)
	unchanged, unchangedTask := fixture.loadFinalTask("op-completion")
	is.Equal(publication, unchanged)
	is.Equal(pendingTask, unchangedTask)
	is.Equal(3, fixture.reviewPipelineRuns)
	is.Len(fixture.git.captured, 1)

}

func TestIntegrationWorkflowTaskRunAgentFailureRecordsFailedAttempt(t *testing.T) {
	is := assert.New(t)
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-failure"))
	agentFailure := errors.New("agent failed")
	fixture.withFailingAgent(agentFailure)

	stdout, stderr, err := fixture.execute("task", "run", "op-failure")

	is.ErrorIs(err, agentFailure)
	is.Empty(stdout)
	is.Contains(stderr, "-- STEP: implement (agent) --")
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
		taskstate.EventWorkPrepared, taskstate.EventRunStarted, taskstate.EventRunFinished,
	)
	require.Len(t, finalState.Events, 3)
	is.Equal(taskstate.RunStatusFailed, finalState.Events[2].Status)
}

func TestIntegrationWorkflowTaskRunSuccessfulExitWithoutCompletionAllowsOrdinaryRetry(t *testing.T) {
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
		taskstate.EventWorkPrepared, taskstate.EventRunStarted, taskstate.EventRunFinished,
		taskstate.EventRunStarted, taskstate.EventRunFinished,
	)
}

func TestIntegrationWorkflowTaskRunAbsentProcessesInterruptPreviousAttemptBeforeRetry(t *testing.T) {
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
	interruption := recovered.Events[2]
	is.Equal(taskstate.EventRunInterrupted, interruption.Type)
	is.Equal(1, interruption.Attempt)
	is.Equal("task_run", interruption.InterruptionTrigger)
	is.Equal("supervisor_and_child_pids_absent", interruption.InterruptionReason)
	is.Equal(map[int]int{supervisorPID: 1, childPID: 1}, fixture.probedPIDs)
	is.Empty(recovered.Reviews)
	assertTaskNotPublished(t, recovered, finalTask)
}

func TestIntegrationWorkflowTaskRunBlockingReviewDispatchesTargetedRepair(t *testing.T) {
	is := assert.New(t)
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-follow-up"))
	implementation, repair := aCompletion(), aRepairCompletion()
	finding := aBlockingFinding()
	fixture.withCompletingAgent(implementation, repair)
	fixture.withRealKeptBlockerThenManualReviewPipeline(finding)

	_, stderr, err := fixture.executeWithInput("k\n", "task", "run", "op-follow-up")
	require.NoError(t, err, "task run; stderr: %s", stderr)

	is.Equal(2, fixture.reviewPipelineRuns, "implementation and repair reviews")
	loaded, finalTask := fixture.loadFinalTask("op-follow-up")
	finalTaskIDs := fixture.loadFinalTaskIDs()
	require.Len(t, loaded.Runs, 2)
	is.Equal(taskstate.RunStatusSucceeded, loaded.Runs[0].Status)
	is.Equal(taskstate.RunStatusSucceeded, loaded.Runs[1].Status)
	assertCompletionRecorded(t, implementation, loaded.Runs[0].Completion)
	assertCompletionRecorded(t, repair, loaded.Runs[1].Completion)
	require.NotNil(t, loaded.Work.Workflows[0].Publication)
	is.Equal(implementation.CommitSubject, loaded.Work.Workflows[0].Publication.CommitSubject)
	is.Equal(implementation.CommitBody, loaded.Work.Workflows[0].Publication.CommitBody)
	is.Equal(implementation.PRDescription, loaded.Work.Workflows[0].Publication.PRDescription)
	require.Len(t, fixture.git.captured, 2)
	is.Equal("fix: handle missing repositories\n\nReturn an empty result for unknown repositories.", fixture.git.captured[1].Message)
	is.Contains(fixture.agent.contexts[1], "Users can limit results to one repository.")
	is.Contains(fixture.agent.contexts[1], implementation.CommitBody)
	is.Equal(4244, loaded.Runs[1].Execution.ChildPID, "repair child PID")
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
