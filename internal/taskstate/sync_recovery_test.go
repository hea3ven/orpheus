package taskstate_test

import (
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreRetainsTypedSyncHistoryAcrossIndependentLocalAndRemoteBoundaries(t *testing.T) {
	history := projectionHistory(t)
	history.Workflows[0].Phases = history.Workflows[0].Phases[:7]
	original := history.Workflows[0].Phases[6].(*work.ExternalReviewPhase).BranchSyncs[0]
	store, _ := newPolicyStore(t, history)
	operation, err := store.BeginSyncConflictOperation("repo", "task", taskstate.SyncConflictOperation{Branch: "task-branch", Worktree: "/fixture/task", DefaultBranch: "main", Checkpoint: taskstate.SyncConflictCheckpoint{LocalHead: "S1", RemoteHead: "S1", MergeSource: "D3"}})
	require.NoError(t, err)
	assert.Equal(t, "0/6/1", operation.ID)
	require.NotNil(t, operation.Address.BranchSync)
	assert.Equal(t, 1, *operation.Address.BranchSync)
	_, err = store.UpdateSyncConflictOperation("repo", "task", operation.ID, func(observed *taskstate.SyncConflictOperation) error {
		observed.ConflictFiles = []string{"conflict.go"}
		observed.Execution = &taskstate.AgentExecution{Address: operation.Address, Status: taskstate.RunStatusRunning, Agent: "resolver", Harness: "pi", Command: "fixture-resolver", SupervisorPID: 123, StartedAt: operation.CreatedAt, Process: &work.RunningProcess{}}
		return nil
	})
	require.NoError(t, err)
	_, err = store.UpdateSyncConflictOperation("repo", "task", operation.ID, func(observed *taskstate.SyncConflictOperation) error { observed.Execution.ChildPID = 456; return nil })
	require.NoError(t, err)
	loaded, err := store.Load("repo", "task")
	require.NoError(t, err)
	require.NoError(t, loaded.Work.CheckActiveAttempt(operation.Address))
	require.Error(t, store.ClearSyncConflictOperation("repo", "task", operation.ID), "neither running nor local-only work is removable history")
	zero := 0
	_, err = store.UpdateSyncConflictOperation("repo", "task", operation.ID, func(observed *taskstate.SyncConflictOperation) error {
		observed.Execution.Process = &work.ExitedProcess{Code: &zero, At: operation.CreatedAt.Add(time.Second)}
		return nil
	})
	require.NoError(t, err)
	_, err = store.UpdateSyncConflictOperation("repo", "task", operation.ID, func(observed *taskstate.SyncConflictOperation) error { observed.LocalHead = "S2"; return nil })
	require.NoError(t, err)
	loaded, err = store.Load("repo", "task")
	require.NoError(t, err)
	require.NotNil(t, loaded.ActiveSyncConflict, "local merge is not remote delivery")
	before := loaded
	_, err = store.UpdateSyncConflictOperation("repo", "task", operation.ID, func(observed *taskstate.SyncConflictOperation) error {
		observed.ObservedRemoteHead = "foreign"
		observed.Phase = taskstate.SyncConflictPhasePushed
		return nil
	})
	require.Error(t, err)
	rejected, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, before, rejected)
	_, err = store.UpdateSyncConflictOperation("repo", "task", operation.ID, func(observed *taskstate.SyncConflictOperation) error {
		observed.ObservedRemoteHead = "S2"
		observed.Phase = taskstate.SyncConflictPhasePushed
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, store.ClearSyncConflictOperation("repo", "task", operation.ID))
	settled, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Nil(t, settled.ActiveSyncConflict)
	syncs := settled.Work.Workflows[0].Phases[6].(*work.ExternalReviewPhase).BranchSyncs
	require.Len(t, syncs, 2)
	assert.Equal(t, original, syncs[0])
	assert.Equal(t, "S2", syncs[1].LocalCommit.Head)
	assert.Equal(t, "S2", syncs[1].Push.Head)
	assert.Equal(t, "P2", taskstate.FinalizationFacts(settled).Commit)
	assert.Equal(t, "feat: feature intent", settled.Work.Workflows[0].FeatureCompletion().Summary)
	assert.Error(t, settled.Work.CheckActiveAttempt(operation.Address), "successful exit ends child callback ownership")
}

func TestStoreKeepsUnresolvedSyncAsGuardAndAppendsVerifiedRollbackWithoutErasingIt(t *testing.T) {
	history := projectionHistory(t)
	history.Workflows[0].Phases = history.Workflows[0].Phases[:7]
	store, _ := newPolicyStore(t, history)
	observed := taskstate.SyncConflictOperation{Branch: "task-branch", Worktree: "/fixture/task", DefaultBranch: "main", Checkpoint: taskstate.SyncConflictCheckpoint{LocalHead: "S1", RemoteHead: "S1", MergeSource: "D3"}}
	operation, err := store.BeginSyncConflictOperation("repo", "task", observed)
	require.NoError(t, err)
	before, err := store.Load("repo", "task")
	require.NoError(t, err)
	require.NoError(t, store.MarkSyncConflictOperationUnresolved("repo", "task", operation.ID, "remote inspection unavailable"))
	_, err = store.BeginSyncConflictOperation("repo", "task", observed)
	require.Error(t, err)
	require.Error(t, store.ClearSyncConflictOperation("repo", "task", operation.ID))
	for _, address := range []string{"sync-generated-id", "0/6", "0/6/2", "0/5/0", "0/6/-1", "0/6/+1"} {
		_, err := store.UpdateSyncConflictOperation("repo", "task", address, func(*taskstate.SyncConflictOperation) error { return nil })
		require.Error(t, err)
	}
	after, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, before, after, "failed inspection and stale addresses cannot rewrite history or fabricate settlement")
	require.NoError(t, store.ResolveSyncConflictOperation("repo", "task", operation.ID, "verified_rollback", "Caller verified original native checkpoint after recovery"))
	rolledBack, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Nil(t, rolledBack.ActiveSyncConflict)
	syncs := rolledBack.Work.Workflows[0].Phases[6].(*work.ExternalReviewPhase).BranchSyncs
	require.Len(t, syncs, 2)
	require.NotNil(t, syncs[1].Rollback)
	assert.Equal(t, "S1", syncs[1].Rollback.LocalHead)
	assert.Equal(t, "S1", syncs[1].Rollback.RemoteHead)
	assert.Nil(t, syncs[1].Push)
	assert.Nil(t, syncs[1].ConflictResolution, "recovery without a launched resolver cannot fabricate an execution")
}
