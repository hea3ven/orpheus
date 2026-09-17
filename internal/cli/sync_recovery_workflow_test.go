//go:build integration

package cli_test

import (
	"errors"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationTaskSyncUpdatesBranchAndRetriesPushWithoutAnotherMerge(t *testing.T) {
	f := newSyncFixture(t)
	f.syncGit.behind = true
	f.publication.pushError = errors.New("origin unavailable")

	stdout, stderr, err := f.runError("", "task", "sync", "op-sync")

	require.ErrorIs(t, err, f.publication.pushError)
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	head := f.candidate.head
	assert.NotEqual(t, head, f.publication.remote["orpheus/op-sync"])
	assert.Equal(t, 1, f.syncGit.commits)
	f.publication.pushError = nil
	stdout, stderr = f.run("", "task", "sync", "op-sync")
	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "origin was behind")
	assert.Equal(t, head, f.publication.remote["orpheus/op-sync"])
	f.run("", "task", "sync", "op-sync")
	assert.Equal(t, 1, f.syncGit.commits)
	assert.Len(t, f.candidate.pushes, 1)
	assert.Empty(t, f.agent.launches)
	assert.Empty(t, f.pr.created)
	assert.Empty(t, f.tasks.closed)
	for _, call := range f.syncGit.calls {
		assert.Equal(t, gitmeta.TaskBranchUpdateAlways, call.UpdatePolicy)
	}
}

func TestIntegrationTaskSyncAllLeavesConflictFreeBranchUnchanged(t *testing.T) {
	f := newSyncFixture(t)
	f.syncGit.behind = true
	head := f.candidate.head

	stdout, stderr := f.run("", "task", "sync", "--all")

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "conflict-only batch sync left it unchanged")
	assert.Equal(t, head, f.candidate.head)
	assert.Equal(t, head, f.publication.remote["orpheus/op-sync"])
	assert.Empty(t, f.candidate.pushes)
	assert.Zero(t, f.syncGit.commits)
	require.Len(t, f.syncGit.calls, 1)
	assert.Equal(t, gitmeta.TaskBranchUpdateConflictsOnly, f.syncGit.calls[0].UpdatePolicy)
	f.run("", "task", "sync", "op-sync")
	assert.NotEqual(t, head, f.candidate.head)
	assert.Equal(t, f.candidate.head, f.publication.remote["orpheus/op-sync"])
	assert.Equal(t, 1, f.syncGit.commits)
}

func TestIntegrationTaskSyncConflictFailureRollsBackAndRetries(t *testing.T) {
	for _, failure := range []string{"resolver", "push"} {
		t.Run(failure, func(t *testing.T) {
			f := newSyncFixture(t)
			cause := errors.New("repair unavailable")
			if failure == "resolver" {
				f.resolvingAgent(cause)
			} else {
				f.resolvingAgent(nil)
				f.publication.pushError = cause
			}
			head := f.candidate.head

			stdout, stderr, err := f.runError("", "task", "sync", "op-sync")

			require.ErrorIs(t, err, cause)
			assert.Empty(t, stdout)
			assert.Empty(t, stderr)
			assert.Equal(t, head, f.candidate.head)
			assert.Equal(t, head, f.publication.remote["orpheus/op-sync"])
			assert.False(t, f.syncGit.mergeActive)
			assert.Equal(t, 1, f.syncGit.rollbacks)
			state, item := f.loadFinalTask("op-sync")
			assert.Nil(t, state.ActiveSyncConflict)
			assert.Equal(t, taskmodel.StatusInProgress, item.Status)
			require.GreaterOrEqual(t, len(state.Events), 2)
			assert.Equal(t, taskstate.EventSyncConflictStarted, state.Events[0].Type)
			assert.Equal(t, taskstate.EventSyncConflictFailed, state.Events[1].Type)
			assert.Contains(t, state.Events[1].Error, cause.Error())
			f.publication.pushError = nil
			f.resolvingAgent(nil)

			stdout, stderr = f.run("", "task", "sync", "op-sync")

			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "resolved merge conflicts with the configured agent")
			assert.NotEqual(t, head, f.candidate.head)
			assert.Equal(t, f.candidate.head, f.publication.remote["orpheus/op-sync"])
			state, _ = f.loadFinalTask("op-sync")
			assert.Nil(t, state.ActiveSyncConflict)
			assert.Equal(t, taskstate.EventSyncConflictFinished, state.Events[len(state.Events)-1].Type)
			f.run("", "task", "sync", "op-sync")
			assert.Len(t, f.agent.launches, 2)
			assert.Len(t, f.candidate.pushes, 1)
			assert.Empty(t, f.pr.created)
			assert.Empty(t, f.tasks.closed)
		})
	}
}

func TestIntegrationTaskSyncRecoversAmbiguousPushWithoutRepeatingRepair(t *testing.T) {
	for _, moved := range []bool{false, true} {
		name := "published head confirmed"
		if moved {
			name = "remote moved requires manual recovery"
		}
		t.Run(name, func(t *testing.T) {
			f := newSyncFixture(t)
			f.resolvingAgent(nil)
			f.syncGit.remoteError = errors.New("remote inspection unavailable")

			_, _, err := f.runError("", "task", "sync", "op-sync")

			require.ErrorContains(t, err, "remote inspection unavailable")
			state, _ := f.loadFinalTask("op-sync")
			require.NotNil(t, state.ActiveSyncConflict)
			assert.Equal(t, taskstate.SyncConflictPhasePushIntent, state.ActiveSyncConflict.Phase)
			assert.Equal(t, f.candidate.head, state.ActiveSyncConflict.LocalHead)
			assert.Equal(t, f.candidate.head, f.publication.remote["orpheus/op-sync"])
			operationID := state.ActiveSyncConflict.ID
			// Simulate a stopped supervisor; the application must inspect the durable intent.
			_, err = f.taskStore.UpdateSyncConflictOperation("alpha", "op-sync", operationID, func(active *taskstate.SyncConflictOperation) error { active.Execution = nil; return nil })
			require.NoError(t, err)
			f.syncGit.remoteError = nil
			if moved {
				f.publication.remote["orpheus/op-sync"] = "someone-elses-commit"
			}

			stdout, stderr, err := f.runError("", "task", "sync", "op-sync")

			assert.Empty(t, stderr)
			state, _ = f.loadFinalTask("op-sync")
			if moved {
				require.ErrorContains(t, err, "remote task branch changed from the recorded checkpoint")
				assert.Empty(t, stdout)
				require.NotNil(t, state.ActiveSyncConflict)
				assert.Equal(t, taskstate.SyncConflictPhaseUnresolved, state.ActiveSyncConflict.Phase)
				assert.Equal(t, "someone-elses-commit", f.publication.remote["orpheus/op-sync"])
				_, _, err = f.runError("", "task", "sync", "op-sync")
				require.ErrorContains(t, err, "unresolved sync conflict recovery")
			} else {
				require.NoError(t, err)
				assert.Contains(t, stdout, "is still open for review")
				assert.Nil(t, state.ActiveSyncConflict)
				require.Len(t, state.Events, 2)
				assert.Equal(t, taskstate.EventSyncConflictFinished, state.Events[1].Type)
				assert.Equal(t, f.candidate.head, state.Events[1].Commit)
			}
			assert.Len(t, f.agent.launches, 1)
			assert.Equal(t, 1, f.syncGit.commits)
			assert.Len(t, f.candidate.pushes, 1)
			assert.Zero(t, f.syncGit.rollbacks)
		})
	}
}

func TestIntegrationTaskSyncMergedPRRetriesFailedClosure(t *testing.T) {
	f := newSyncFixture(t)
	f.pr.state = pullrequest.StateMerged
	branch, dir := f.expectedTarget("op-sync")
	itemBefore := f.backend.tasks["op-sync"].Clone()
	f.completion("op-sync", branch, dir, "Ready", "Published implementation", taskstate.RunStatusSucceeded)
	f.backend.tasks["op-sync"] = itemBefore
	f.mutations.closeError = errors.New("backend unavailable")
	beforeState, before := f.loadFinalTask("op-sync")

	_, _, err := f.runError("", "task", "sync", "op-sync")

	require.ErrorIs(t, err, f.mutations.closeError)
	failedState, failedTask := f.loadFinalTask("op-sync")
	assert.Equal(t, beforeState, failedState)
	assert.Equal(t, before, failedTask)
	assert.Empty(t, f.tasks.closed)
	f.mutations.closeError = nil
	f.run("", "task", "sync", "op-sync")
	state, item := f.loadFinalTask("op-sync")
	assert.Equal(t, taskmodel.StatusClosed, item.Status)
	require.Len(t, state.Events, len(beforeState.Events)+2)
	assertMergedAudit(t, state.Events[len(beforeState.Events)], f.pr.url)
	assert.Equal(t, taskstate.EventWorktreeRemoved, state.Events[len(state.Events)-1].Type)
	assert.NotContains(t, f.git.targets, before.OrpheusMetadata().Worktree)
	f.run("", "task", "sync", "op-sync")
	assert.Len(t, f.tasks.closed, 1)
	assert.Equal(t, 2, f.pr.statusReads)
}

func TestIntegrationPublicationRetriesPRLookupWithoutRepublishing(t *testing.T) {
	f := newFeatureFinalizationFixture(t, "op-retry", false)
	f.pr.findError = errors.New("PR lookup unavailable")

	_, _, err := f.runError("", "task", "done", "op-retry")

	require.ErrorIs(t, err, f.pr.findError)
	assert.Empty(t, f.pr.created)
	assert.Empty(t, f.mutations.prURLs)
	f.pr.findError = nil
	f.run("", "task", "done", "op-retry")
	f.assertPublishedPR("op-retry")
	assert.Len(t, f.pr.created, 1)
	assert.Len(t, f.candidate.commits, 1)
	assert.Len(t, f.candidate.pushes, 1)
}
