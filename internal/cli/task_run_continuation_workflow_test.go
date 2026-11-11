//go:build integration

package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	workmodel "github.com/hea3ven/orpheus/internal/work"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskRunUnfinishedRepoRootContinuesThroughCompletion(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure error
		status  taskstate.RunStatus
		handoff bool
	}{
		{name: "successful exit", status: taskstate.RunStatusSucceeded},
		{name: "failed exit with handoff", handoff: true, failure: errors.New("agent failed"), status: taskstate.RunStatusFailed},
		{name: "failed exit", failure: errors.New("agent failed"), status: taskstate.RunStatusFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTaskWorkflowFixture(t, anOpenTask("op-continue"))
			fixture.withAgentExitingWithoutCompletion(1)
			if test.handoff {
				handoff := aCompletion()
				fixture.agent.outcomes[0].completion = &handoff
			}

			fixture.agent.outcomes[0].err = test.failure
			fixture.agent.outcomes[0].mutateCandidate = func() {
				fixture.git.repoRootDirty = true
				fixture.git.hasCandidateChanges = true
			}
			_, stderr, err := fixture.execute("task", "run", "--repo-root", "op-continue")
			if test.failure != nil {
				require.ErrorIs(t, err, test.failure)
			} else {
				require.NoError(t, err, "stderr: %s", stderr)
			}

			before, _ := fixture.loadFinalTask("op-continue")
			require.Len(t, before.Runs, 1)
			assert.Equal(t, test.status, before.Runs[0].Status)
			if test.handoff {
				assertCompletionRecorded(t, aCompletion(), before.Runs[0].Completion)
			} else {
				assert.Nil(t, before.Runs[0].Completion)
			}

			completion := aCompletion()
			fixture.withCompletingAgent(completion)
			fixture.withRealReviewPipeline()

			stdout, stderr, err := fixture.execute("task", "run", "op-continue")

			require.NoError(t, err, "stderr: %s", stderr)
			assert.Contains(t, stdout, "Recorded completion for op-continue")
			final, item := fixture.loadFinalTask("op-continue")
			require.Len(t, final.Runs, 2)
			assert.Equal(t, before.Runs[0], final.Runs[0])
			assert.True(t, final.Runs[0].Execution.Interactive)
			assert.True(t, final.Runs[1].Execution.Interactive)
			assert.Equal(t, 2, final.Runs[1].Attempt)
			assert.Equal(t, taskstate.RunStatusSucceeded, final.Runs[1].Status)
			assertCompletionRecorded(t, completion, final.Runs[1].Completion)
			assertPersistedTaskTarget(t, final, item, "orpheus/op-continue", taskWorkflowRepoRoot)
			assert.Equal(t, taskmodel.StatusInProgress, item.Status)
			assertWaitingForManualReview(t, final)
			assertTaskNotPublished(t, final, item)
			require.Len(t, fixture.agent.launches, 2)
			for _, launch := range fixture.agent.launches {
				assert.Equal(t, taskWorkflowRepoRoot, launch.dir)
				assert.Equal(t, "orpheus/op-continue", launch.environment["ORPHEUS_BRANCH"])
			}
			assert.Empty(t, fixture.git.worktreeSetups)
			assert.True(t, fixture.git.repoRootDirty)
		})
	}
}

func TestIntegrationWorkflowTaskRunContinuesPersistedSuccessfulHandoffWithoutAnotherImplementer(t *testing.T) {
	for _, repoRoot := range []bool{false, true} {
		t.Run(fmt.Sprintf("repo_root_%t", repoRoot), func(t *testing.T) {
			fixture := newTaskWorkflowFixture(t, anInProgressTask("op-continue"))
			if repoRoot {
				fixture.seedRepoRootAttempt("op-continue")
			} else {
				fixture.seedRunningAttempt("op-continue", 100, 101)
			}
			completion := aCompletion()
			_, err := seedFixtureHandoff(fixture.taskStore, "alpha", "op-continue", 1, taskstate.CompleteRunOptions{CommitSubject: completion.CommitSubject, CommitBody: completion.CommitBody, PRDescription: completion.PRDescription, TechnicalExplanation: completion.TechnicalExplanation})
			require.NoError(t, err)
			_, err = seedFixtureExit(fixture.taskStore, "alpha", "op-continue", 1, 0)
			require.NoError(t, err)
			_, err = seedFixtureUsage(fixture.taskStore, "alpha", "op-continue", 1, aCapturedPiUsage())
			require.NoError(t, err)
			before, _ := fixture.loadFinalTask("op-continue")
			fixture.withRealReviewPipeline()
			inspection, _, err := fixture.execute("task", "show", "workflow", "op-continue", "--json")
			require.NoError(t, err)
			var view workflow.View
			require.NoError(t, json.Unmarshal([]byte(inspection), &view))
			assert.Equal(t, workflow.TaskRunActionFinishImplementation, view.Next.Action)
			assert.Nil(t, view.Next.Recovery)
			assert.Empty(t, fixture.git.captured)

			fixture.git.hasCandidateChanges = true
			captureError := errors.New("capture interrupted after retention")
			fixture.git.captureLeavesChanges, fixture.git.captureError = true, captureError
			_, _, err = fixture.execute("task", "run", "op-continue")
			require.ErrorIs(t, err, captureError)
			interrupted, _ := fixture.loadFinalTask("op-continue")
			assert.Empty(t, interrupted.Runs[0].Completion.Commit)
			assert.Empty(t, interrupted.Reviews)
			require.Len(t, fixture.git.captured, 1)
			assert.True(t, fixture.git.hasCandidateChanges)

			fixture.git.captureError = nil
			_, _, err = fixture.execute("task", "run", "op-continue")
			require.ErrorContains(t, err, "uncommitted changes")
			recovered, _ := fixture.loadFinalTask("op-continue")
			assert.Equal(t, "output-1", recovered.Runs[0].Completion.Commit, "retained recovery persists despite later checkout changes")
			assert.Empty(t, recovered.Reviews)
			assert.True(t, fixture.git.hasCandidateChanges)
			require.Len(t, fixture.git.captured, 1, "recovery must not commit leftovers")
			assert.Empty(t, fixture.agent.launches)
			fixture.git.hasCandidateChanges = false
			_, stderr, err := fixture.execute("task", "run", "op-continue")

			require.NoError(t, err, "stderr: %s", stderr)
			final, item := fixture.loadFinalTask("op-continue")
			require.Len(t, final.Runs, 1)
			assert.Equal(t, before.Runs[0].Execution, final.Runs[0].Execution)
			assertCompletionRecorded(t, completion, final.Runs[0].Completion)
			assert.Equal(t, before.Runs[0].Completion.CompletedAt, final.Runs[0].Completion.CompletedAt)
			assert.NotEmpty(t, final.Runs[0].Completion.Commit)
			assert.Len(t, fixture.git.captured, 1)
			assert.Empty(t, fixture.agent.launches)
			assertWaitingForManualReview(t, final)
			assertTaskNotPublished(t, final, item)
		})
	}
}

func TestIntegrationWorkflowTaskRunInterruptedRepoRootContinuesThroughCompletion(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anInProgressTask("op-continue"))
	fixture.seedRepoRootAttempt("op-continue")
	fixture.git.repoRootDirty = true
	fixture.withAbsentProcesses(100, 101)

	_, err := seedFixtureHandoff(fixture.taskStore, "alpha", "op-continue", 1, taskstate.CompleteRunOptions{CommitSubject: "feat: interrupted handoff", CommitBody: "Retain rationale", PRDescription: "Still needs a successful process"})
	require.NoError(t, err)
	before, _ := fixture.loadFinalTask("op-continue")
	inspection, _, err := fixture.execute("task", "show", "workflow", "op-continue", "--json")
	require.NoError(t, err)
	var view workflow.View
	require.NoError(t, json.Unmarshal([]byte(inspection), &view))
	require.NotNil(t, view.Next.Recovery)
	assert.Equal(t, workflow.AttachedExecutionRecoverable, view.Next.Recovery.Condition)
	assert.Contains(t, view.Next.Advice, "orpheus task run op-continue")
	unchanged, _ := fixture.loadFinalTask("op-continue")
	assert.Equal(t, before, unchanged)
	completion := aCompletion()
	fixture.withCompletingAgent(completion)
	fixture.withRealReviewPipeline()

	_, stderr, err := fixture.execute("task", "run", "op-continue")

	require.NoError(t, err, "stderr: %s", stderr)
	assert.Contains(t, stderr, "reconciled interrupted implementation attempt")
	final, item := fixture.loadFinalTask("op-continue")
	require.Len(t, final.Runs, 2)
	assert.Equal(t, taskstate.RunStatusInterrupted, final.Runs[0].Status)
	assert.Equal(t, before.Runs[0].Completion, final.Runs[0].Completion)
	assert.Empty(t, final.Runs[0].Completion.Commit)
	var interruption *taskstate.Event
	for i := range final.Events {
		if final.Events[i].Type == taskstate.EventRunInterrupted {
			interruption = &final.Events[i]
		}
	}
	require.NotNil(t, interruption)
	assert.Equal(t, before.Runs[0].Address, interruption.Address)
	assert.Equal(t, "supervisor_and_child_pids_absent", interruption.InterruptionReason)
	assert.Equal(t, taskstate.RunStatusSucceeded, final.Runs[1].Status)
	assertCompletionRecorded(t, completion, final.Runs[1].Completion)
	assertPersistedTaskTarget(t, final, item, "orpheus/op-continue", taskWorkflowRepoRoot)
	assertWaitingForManualReview(t, final)
	assertTaskNotPublished(t, final, item)
	assert.Equal(t, taskWorkflowRepoRoot, fixture.onlyAgentLaunch().dir)
	assert.True(t, fixture.git.repoRootDirty)
}

func TestIntegrationWorkflowTaskRunDirtyRepoRootRequiresRunHistory(t *testing.T) {
	for _, recordedTarget := range []bool{false, true} {
		name := "first run"
		if recordedTarget {
			name = "metadata without run history"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newTaskWorkflowFixture(t, anOpenTask("op-continue"))
			fixture.configureImplementer("recorder", agent.Profile{Command: "unused-agent"})
			fixture.git.repoRootDirty = true
			if recordedTarget {
				fixture.withBackendTarget("op-continue", "orpheus/op-continue", taskWorkflowRepoRoot)
			}
			before, beforeTask := fixture.loadFinalTask("op-continue")

			_, _, err := fixture.execute("task", "run", "--repo-root", "op-continue")

			require.ErrorContains(t, err, "uncommitted changes")
			final, item := fixture.loadFinalTask("op-continue")
			assert.Equal(t, before, final)
			assert.Equal(t, beforeTask, item)
			assert.Empty(t, fixture.agent.launches)
			assert.Empty(t, fixture.backend.markCalls)
			assert.True(t, fixture.git.repoRootDirty)
		})
	}
}

func TestIntegrationWorkflowTaskRunRepoRootContinuationRejectsConflictingTargets(t *testing.T) {
	for _, test := range []struct {
		name      string
		change    func(*taskWorkflowFixture)
		errorText string
	}{
		{name: "branch mismatch", change: func(f *taskWorkflowFixture) {
			f.withBackendTarget("op-continue", "other", taskWorkflowRepoRoot)
		}, errorText: "repository-root metadata"},
		{name: "directory mismatch", change: func(f *taskWorkflowFixture) {
			f.withBackendTarget("op-continue", "orpheus/op-continue", "/fixture/other")
		}, errorText: "not tied to"},
		{name: "missing metadata", change: func(f *taskWorkflowFixture) {
			item := f.backend.tasks["op-continue"]
			item.Metadata = nil
			f.backend.tasks[item.ID] = item
		}, errorText: "not tied to"},
		{name: "another owner", change: func(f *taskWorkflowFixture) {
			f.backend.tasks["op-other"] = anInProgressTask("op-other")
			f.withBackendTarget("op-other", "orpheus/op-other", taskWorkflowRepoRoot)
		}, errorText: "already has non-closed task op-other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTaskWorkflowFixture(t, anInProgressTask("op-continue"))
			fixture.seedRepoRootAttempt("op-continue")
			_, err := seedFixtureExit(fixture.taskStore, "alpha", "op-continue", 1, 1)
			require.NoError(t, err)
			fixture.configureImplementer("recorder", agent.Profile{Command: "unused-agent"})
			fixture.git.repoRootDirty = true
			test.change(fixture)
			before, beforeTask := fixture.loadFinalTask("op-continue")
			setups := len(fixture.git.setups)

			_, _, err = fixture.execute("task", "run", "op-continue")

			require.ErrorContains(t, err, test.errorText)
			final, item := fixture.loadFinalTask("op-continue")
			assert.Equal(t, before, final)
			assert.Equal(t, beforeTask, item)
			assert.Len(t, fixture.git.setups, setups)
			assert.Empty(t, fixture.agent.launches)
			assert.Empty(t, fixture.backend.markCalls)
		})
	}
}

func TestIntegrationWorkflowTaskRunRepoRootContinuationBlocksUnresolvedProcesses(t *testing.T) {
	for _, repoRoot := range []bool{false, true} {
		for _, test := range []struct {
			name       string
			supervisor agentexec.ProcessLiveness
			child      agentexec.ProcessLiveness
		}{
			{name: "live supervisor", supervisor: agentexec.ProcessLive},
			{name: "unverifiable supervisor", supervisor: agentexec.ProcessUnknown},
			{name: "live child", supervisor: agentexec.ProcessAbsent, child: agentexec.ProcessLive},
			{name: "unverifiable child", supervisor: agentexec.ProcessAbsent, child: agentexec.ProcessUnknown},
		} {
			t.Run(fmt.Sprintf("repo_root_%t/%s", repoRoot, test.name), func(t *testing.T) {
				fixture := newTaskWorkflowFixture(t, anInProgressTask("op-continue"))
				if repoRoot {
					fixture.seedRepoRootAttempt("op-continue")
					fixture.git.repoRootDirty = true
				} else {
					fixture.seedRunningAttempt("op-continue", 100, 101)
				}
				fixture.options.Dependencies.ProcessProbe = func(pid int) (agentexec.ProcessLiveness, error) {
					if pid == 100 {
						return test.supervisor, nil
					}
					return test.child, nil
				}

				_, err := seedFixtureHandoff(fixture.taskStore, "alpha", "op-continue", 1, taskstate.CompleteRunOptions{CommitSubject: "feat: handoff before exit", CommitBody: "Preserve this handoff", PRDescription: "Not evidence of process success"})
				require.NoError(t, err)
				before, beforeTask := fixture.loadFinalTask("op-continue")
				setups := len(fixture.git.setups)
				inspection, _, err := fixture.execute("task", "show", "workflow", "op-continue", "--json")
				require.NoError(t, err)
				var view workflow.View
				require.NoError(t, json.Unmarshal([]byte(inspection), &view))
				require.NotNil(t, view.Next.Recovery)
				expectedCondition := workflow.AttachedExecutionLive
				if test.supervisor == agentexec.ProcessUnknown || test.child == agentexec.ProcessUnknown {
					expectedCondition = workflow.AttachedExecutionUnverifiable
				}
				assert.Equal(t, expectedCondition, view.Next.Recovery.Condition)
				assert.NotEmpty(t, view.Next.Recovery.Reason)
				inspection, _, err = fixture.execute("task", "show", "workflow", "op-continue")
				require.NoError(t, err)
				afterInspect, afterTask := fixture.loadFinalTask("op-continue")
				assert.Equal(t, before, afterInspect, "read-only inspection must not reconcile or capture")
				assert.Equal(t, beforeTask, afterTask)

				stdout, stderr, err := fixture.execute("task", "run", "op-continue")

				require.NoError(t, err)
				assert.Contains(t, stderr, "IMPLEMENTATION")
				if expectedCondition == workflow.AttachedExecutionLive {
					assert.Contains(t, stdout, "implementation attempt 1 is still active")
					assert.Contains(t, inspection, "wait for it to finish")
				} else {
					assert.Contains(t, stdout, "implementation attempt 1 is unverifiable")
					assert.Contains(t, stdout, "orpheus doctor")
					assert.Contains(t, inspection, "is unverifiable")
					assert.NotContains(t, stdout+inspection, "wait for it to finish")
					assert.NotContains(t, stdout+inspection, "is active")
				}
				assert.Empty(t, fixture.git.captured)
				final, item := fixture.loadFinalTask("op-continue")
				assert.Equal(t, before, final)
				assert.Equal(t, beforeTask, item)
				assert.Len(t, fixture.git.setups, setups)
				assert.Empty(t, fixture.agent.launches)
				assert.Empty(t, fixture.backend.markCalls)
			})
		}
	}
}

func (f *taskWorkflowFixture) seedRepoRootAttempt(taskID string) {
	f.t.Helper()
	f.withBackendTarget(taskID, "orpheus/op-continue", taskWorkflowRepoRoot)
	setup, err := f.git.SetupRepoRootTaskBranch(context.Background(), gitmeta.TaskWorktreeOptions{
		RepoID: "alpha", RepoPath: taskWorkflowRepoRoot, DefaultBranch: "main", Branch: "orpheus/op-continue",
	})
	require.NoError(f.t, err)
	_, err = f.git.PrepareBaseline(context.Background(), taskWorkflowRepoRoot, "orpheus/op-continue", "refs/orpheus/fixture-baseline", nil)
	require.NoError(f.t, err)
	_, err = f.taskStore.StartRun("alpha", taskID, taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: taskWorkflowRepoRoot, TaskBranch: "orpheus/op-continue", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: setup.StartingCommit, Tree: "initial-tree"}},
		Agent: "recorder", Branch: "orpheus/op-continue", Worktree: taskWorkflowRepoRoot,
		WorkDirectory: taskWorkflowRepoRoot, SupervisorPID: 100,
		BaselineCommit: setup.StartingCommit,
	})
	require.NoError(f.t, err)
	_, err = seedFixtureChildPID(f.taskStore, "alpha", taskID, 1, 101)
	require.NoError(f.t, err)
}
