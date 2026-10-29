package doctor

import (
	"context"
	"errors"
	"github.com/hea3ven/orpheus/internal/agent"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
	"time"
)

func TestClassifySyncConflictRecoveryRejectsOwnershipMismatch(t *testing.T) {
	operation := taskstate.SyncConflictOperation{
		Branch:   "orpheus/op-1",
		Worktree: "/fixture/op-1",
		Phase:    taskstate.SyncConflictPhasePrepared,
	}
	diagnosis := classifySyncConflictRecovery(registry.Repo{}, taskstate.TaskState{}, operation, nil, workflow.LocalSyncGit{})
	if diagnosis.outcome != "ownership_mismatch" {
		t.Fatalf("outcome = %q, want ownership_mismatch", diagnosis.outcome)
	}
}

func doctorSyncFixture(t *testing.T, localCompleted bool) (state.Paths, taskstate.Store, taskstate.SyncConflictOperation) {
	t.Helper()
	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	require.NoError(t, err)
	history, err := testutil.DeliveredPRWork("alpha", "op-1", "/fixture/op-1", "orpheus/op-1", "https://github.test/org/repo/pull/1")
	require.NoError(t, err)
	started := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	sync := work.BranchSync{StartedAt: started, Checkpoint: work.BranchSyncCheckpoint{LocalHead: "before", RemoteHead: "before", MergeSource: "main-head"}}
	if localCompleted {
		sync.LocalCommit = &work.BranchSyncCommit{Head: "completed", ObservedAt: started.Add(time.Second)}
	}
	history.Workflows[0].Phases[4].(*work.ExternalReviewPhase).BranchSyncs = []work.BranchSync{sync}
	_, err = workstate.NewStore(paths).Save(history, 0)
	require.NoError(t, err)
	store := taskstate.NewStore(paths)
	view, err := store.Load("alpha", "op-1")
	require.NoError(t, err)
	require.NotNil(t, view.ActiveSyncConflict)
	return paths, store, *view.ActiveSyncConflict
}

type unavailableDoctorSyncGit struct {
	workflow.SyncConflictRecoveryGit
}

func (unavailableDoctorSyncGit) InspectRemoteTaskBranchHead(context.Context, gitmeta.TaskBranchSyncOptions) (string, error) {
	return "", errors.New("fixture remote inspection unavailable")
}

func TestRunLeavesUnverifiableSyncHistoryUntouched(t *testing.T) {
	paths, store, _ := doctorSyncFixture(t, false)
	before, err := store.Load("alpha", "op-1")
	require.NoError(t, err)
	for _, fix := range []bool{false, true} {
		result, err := Run(Options{Paths: paths, Registry: registry.Registry{Repos: []registry.Repo{{ID: "alpha", Path: "/fixture/alpha"}}}, Fix: fix, Effects: Effects{SyncGit: unavailableDoctorSyncGit{}, WorktreeExists: func(string) bool { return false }, CaptureUsage: func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
			return taskstate.RecordRunUsageOptions{UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureUnknown, Reason: "No fixture usage supplied"}}
		}}})
		require.NoError(t, err)
		require.Len(t, result.SyncConflictRows, 1)
		assert.Equal(t, "unverifiable", result.SyncConflictRows[0].Outcome)
		assert.Contains(t, result.SyncConflictRows[0].Reason, "fixture remote inspection unavailable")
		after, err := store.Load("alpha", "op-1")
		require.NoError(t, err)
		assert.Equal(t, before, after, "unavailable native observations cannot authorize a rollback or erase the guard")
	}
}

func TestRepairSyncConflictRecoveryCompletesObservedPushWithoutInventingResolverExecution(t *testing.T) {
	_, store, operation := doctorSyncFixture(t, true)
	diagnosis := repairSyncConflictRecovery(store, "alpha", "op-1", operation, syncConflictRecoveryDiagnosis{outcome: "pushed", reason: "remote task branch matches the recorded local completion", remoteHead: "completed"}, unavailableDoctorSyncGit{})
	assert.Equal(t, "pushed", diagnosis.outcome, diagnosis.reason)
	loaded, err := store.Load("alpha", "op-1")
	require.NoError(t, err)
	assert.Nil(t, loaded.ActiveSyncConflict)
	syncs := loaded.Work.Workflows[0].Phases[4].(*work.ExternalReviewPhase).BranchSyncs
	require.Len(t, syncs, 1)
	assert.Equal(t, "completed", syncs[0].LocalCommit.Head)
	require.NotNil(t, syncs[0].Push)
	assert.Equal(t, "completed", syncs[0].Push.Head)
	assert.Nil(t, syncs[0].ConflictResolution)
	for _, event := range loaded.Events {
		assert.NotEqual(t, taskstate.EventSyncConflictFinished, event.Type, "non-agent synchronization must not create an agent audit execution")
	}
}
