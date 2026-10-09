//go:build integration

package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskRunUnfinishedRepoRootContinuesThroughCompletion(t *testing.T) {
	for _, test := range []struct {
		name    string
		failure error
		status  taskstate.RunStatus
	}{
		{name: "successful exit", status: taskstate.RunStatusSucceeded},
		{name: "failed exit", failure: errors.New("agent failed"), status: taskstate.RunStatusFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTaskWorkflowFixture(t, anOpenTask("op-continue"))
			fixture.withAgentExitingWithoutCompletion(1)
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
			completion := aCompletion()
			fixture.withCompletingAgent(completion)
			fixture.withRealReviewPipeline()

			stdout, stderr, err := fixture.execute("task", "run", "op-continue")

			require.NoError(t, err, "stderr: %s", stderr)
			assert.Contains(t, stdout, "Recorded completion for op-continue")
			final, item := fixture.loadFinalTask("op-continue")
			require.Len(t, final.Runs, 2)
			assert.Equal(t, before.Runs[0], final.Runs[0])
			assert.Nil(t, final.Runs[0].Completion)
			assert.True(t, final.Runs[0].Execution.Interactive)
			assert.True(t, final.Runs[1].Execution.Interactive)
			assert.Equal(t, 2, final.Runs[1].Attempt)
			assert.Equal(t, taskstate.RunStatusSucceeded, final.Runs[1].Status)
			assertCompletionRecorded(t, completion, final.Runs[1].Completion)
			assertPersistedTaskTarget(t, final, item, "main", taskWorkflowRepoRoot)
			assert.Equal(t, taskmodel.StatusInProgress, item.Status)
			assertWaitingForManualReview(t, final)
			assertTaskNotPublished(t, final, item)
			require.Len(t, fixture.agent.launches, 2)
			for _, launch := range fixture.agent.launches {
				assert.Equal(t, taskWorkflowRepoRoot, launch.dir)
				assert.Equal(t, "main", launch.environment["ORPHEUS_BRANCH"])
			}
			assert.Empty(t, fixture.git.worktreeSetups)
			assert.True(t, fixture.git.repoRootDirty)
		})
	}
}

func TestIntegrationWorkflowTaskRunInterruptedRepoRootContinuesThroughCompletion(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anInProgressTask("op-continue"))
	fixture.seedRepoRootAttempt("op-continue")
	fixture.git.repoRootDirty = true
	fixture.withAbsentProcesses(100, 101)
	completion := aCompletion()
	fixture.withCompletingAgent(completion)
	fixture.withRealReviewPipeline()

	_, stderr, err := fixture.execute("task", "run", "op-continue")

	require.NoError(t, err, "stderr: %s", stderr)
	assert.Contains(t, stderr, "reconciled interrupted implementation attempt")
	final, item := fixture.loadFinalTask("op-continue")
	require.Len(t, final.Runs, 2)
	assert.Equal(t, taskstate.RunStatusInterrupted, final.Runs[0].Status)
	assert.Nil(t, final.Runs[0].Completion)
	assert.Equal(t, taskstate.EventRunInterrupted, final.Events[1].Type)
	assert.Equal(t, "supervisor_and_child_pids_absent", final.Events[1].InterruptionReason)
	assert.Equal(t, taskstate.RunStatusSucceeded, final.Runs[1].Status)
	assertCompletionRecorded(t, completion, final.Runs[1].Completion)
	assertPersistedTaskTarget(t, final, item, "main", taskWorkflowRepoRoot)
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
				fixture.withBackendTarget("op-continue", "main", taskWorkflowRepoRoot)
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
			f.withBackendTarget("op-continue", "main", "/fixture/other")
		}, errorText: "not tied to"},
		{name: "missing metadata", change: func(f *taskWorkflowFixture) {
			item := f.backend.tasks["op-continue"]
			item.Metadata = nil
			f.backend.tasks[item.ID] = item
		}, errorText: "not tied to"},
		{name: "another owner", change: func(f *taskWorkflowFixture) {
			f.backend.tasks["op-other"] = anInProgressTask("op-other")
			f.withBackendTarget("op-other", "main", taskWorkflowRepoRoot)
		}, errorText: "already has non-closed task op-other"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newTaskWorkflowFixture(t, anInProgressTask("op-continue"))
			fixture.seedRepoRootAttempt("op-continue")
			_, err := fixture.taskStore.FinishRun("alpha", "op-continue", 1, taskstate.RunStatusFailed)
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
		t.Run(test.name, func(t *testing.T) {
			fixture := newTaskWorkflowFixture(t, anInProgressTask("op-continue"))
			fixture.seedRepoRootAttempt("op-continue")
			fixture.git.repoRootDirty = true
			fixture.options.Dependencies.ProcessProbe = func(pid int) (agentexec.ProcessLiveness, error) {
				if pid == 100 {
					return test.supervisor, nil
				}
				return test.child, nil
			}
			before, beforeTask := fixture.loadFinalTask("op-continue")
			setups := len(fixture.git.setups)

			stdout, _, err := fixture.execute("task", "run", "op-continue")

			require.NoError(t, err)
			assert.Contains(t, stdout, "implementation attempt 1 is active")
			final, item := fixture.loadFinalTask("op-continue")
			assert.Equal(t, before, final)
			assert.Equal(t, beforeTask, item)
			assert.Len(t, fixture.git.setups, setups)
			assert.Empty(t, fixture.agent.launches)
			assert.Empty(t, fixture.backend.markCalls)
		})
	}
}

func (f *taskWorkflowFixture) seedRepoRootAttempt(taskID string) {
	f.t.Helper()
	f.withBackendTarget(taskID, "main", taskWorkflowRepoRoot)
	_, err := f.git.SetupRepoRoot(context.Background(), gitmeta.RepoRootOptions{
		RepoID: "alpha", RepoPath: taskWorkflowRepoRoot, DefaultBranch: "main",
	})
	require.NoError(f.t, err)
	_, err = f.taskStore.StartRun("alpha", taskID, taskstate.StartRunOptions{
		Agent: "recorder", Branch: "main", Worktree: taskWorkflowRepoRoot,
		WorkDirectory: taskWorkflowRepoRoot, SupervisorPID: 100,
	})
	require.NoError(f.t, err)
	_, err = f.taskStore.RecordRunChildPID("alpha", taskID, 1, 101)
	require.NoError(f.t, err)
}
