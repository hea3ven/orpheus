package externalreview

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func syncFixture(t *testing.T) (phaseexec.Runner, work.State) {
	t.Helper()
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	require.NoError(t, err)
	history, err := testutil.DeliveredPRWork("repo", "task", "/fixture/task", "task-branch", "https://github.test/org/repo/pull/1")
	require.NoError(t, err)
	at := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	history.Workflows[0].Phases[4].(*work.ExternalReviewPhase).BranchSyncs = []work.BranchSync{{StartedAt: at, Checkpoint: work.BranchSyncCheckpoint{LocalHead: "fixture-output", RemoteHead: "fixture-output", MergeSource: "D2"}, LocalCommit: &work.BranchSyncCommit{Head: "S1", ObservedAt: at.Add(time.Second)}, Push: &work.BranchSyncPush{ObservedAt: at.Add(2 * time.Second)}}}
	runner := phaseexec.Runner{Store: workstate.NewStore(paths), Paths: paths, RepositoryID: "repo", TaskID: "task"}
	version, err := runner.Store.Save(history, 0)
	require.NoError(t, err)
	history.Version = version
	return runner, history
}

func syncOptions() gitmeta.TaskBranchSyncOptions {
	return gitmeta.TaskBranchSyncOptions{Branch: "task-branch", Worktree: "/fixture/task", DefaultBranch: "main"}
}
func newCheckpoint() gitmeta.TaskBranchConflictCheckpoint {
	return gitmeta.TaskBranchConflictCheckpoint{LocalHead: "S1", RemoteHead: "S1", MergeSource: "D3"}
}

func TestSyncHistoryRetainsIndependentLocalAndRemoteBoundaries(t *testing.T) {
	runner, history := syncFixture(t)
	original := history.Workflows[0].Phases[4].(*work.ExternalReviewPhase).BranchSyncs[0]
	current, err := runner.Commit(context.Background(), CheckpointChange(syncOptions(), newCheckpoint()))
	require.NoError(t, err)
	ref, sync := activeSync(current)
	require.NotNil(t, sync)
	require.NotNil(t, ref.BranchSync)
	assert.Equal(t, 1, *ref.BranchSync)
	started := sync.StartedAt
	_, err = runner.Commit(context.Background(), branchChange(ref, func(sync *work.BranchSync) error {
		sync.ConflictResolution = &work.ConflictResolution{Files: []string{"conflict.go"}, Execution: work.AgentExecution{Profile: "resolver", Harness: "pi", Command: "fixture-resolver", SupervisorPID: 123, StartedAt: started, Process: &work.RunningProcess{}}}
		return nil
	}))
	require.NoError(t, err)
	current, err = runner.Commit(context.Background(), branchChange(ref, func(sync *work.BranchSync) error { sync.ConflictResolution.Execution.ChildPID = 456; return nil }))
	require.NoError(t, err)
	require.NoError(t, current.CheckActiveAttempt(ref))
	_, active := activeSync(current)
	require.NotNil(t, active, "a running resolver is unsettled history")
	zero := 0
	_, err = runner.Commit(context.Background(), branchChange(ref, func(sync *work.BranchSync) error {
		sync.ConflictResolution.Execution.Process = &work.ExitedProcess{Code: &zero, At: time.Now().UTC()}
		return nil
	}))
	require.NoError(t, err)
	current, err = runner.Commit(context.Background(), LocalCommitChange(ref, "S2"))
	require.NoError(t, err)
	_, active = activeSync(current)
	require.NotNil(t, active, "a local merge is not remote delivery")
	before := current
	rejected, err := runner.Commit(context.Background(), PushChange(ref, "foreign"))
	require.Error(t, err)
	assert.Equal(t, before, rejected, "invalid observations must not leak a candidate")
	current, err = runner.Commit(context.Background(), PushChange(ref, "S2"))
	require.NoError(t, err)
	_, active = activeSync(current)
	assert.Nil(t, active)
	syncs := current.Workflows[0].Phases[4].(*work.ExternalReviewPhase).BranchSyncs
	require.Len(t, syncs, 2)
	assert.Equal(t, original, syncs[0])
	assert.Equal(t, "S2", syncs[1].LocalCommit.Head)
	require.NotNil(t, syncs[1].Push)
	assert.False(t, syncs[1].Push.ObservedAt.Before(syncs[1].LocalCommit.ObservedAt))
	view, err := taskstate.Project(current)
	require.NoError(t, err)
	assert.Equal(t, "fixture-output", taskstate.FinalizationFacts(view).Commit)
	assert.Equal(t, "feat: fixture intent", current.Workflows[0].FeatureCompletion().CommitSubject)
	assert.Error(t, current.CheckActiveAttempt(ref), "successful exit ends child callback ownership")
}

func TestUnresolvedSyncGuardsUntilVerifiedRollbackWithoutErasingHistory(t *testing.T) {
	runner, _ := syncFixture(t)
	before, err := runner.Commit(context.Background(), CheckpointChange(syncOptions(), newCheckpoint()))
	require.NoError(t, err)
	ref, _ := activeSync(before)
	_, err = runner.Commit(context.Background(), CheckpointChange(syncOptions(), newCheckpoint()))
	require.Error(t, err)
	for _, wrong := range []work.AttemptRef{
		{RepositoryID: "other", TaskID: "task", Workflow: 0, Phase: 4, BranchSync: ref.BranchSync},
		{RepositoryID: "repo", TaskID: "task", Workflow: 0, Phase: 3, BranchSync: ref.BranchSync},
		{RepositoryID: "repo", TaskID: "task", Workflow: 0, Phase: 4},
		{RepositoryID: "repo", TaskID: "task", Workflow: 0, Phase: 4, Step: ref.BranchSync},
	} {
		_, err := runner.Commit(context.Background(), LocalCommitChange(wrong, "foreign"))
		require.Error(t, err)
	}
	after, err := runner.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, before, after, "stale addresses cannot rewrite history or fabricate settlement")
	rolledBack, err := runner.Commit(context.Background(), RollbackChange(ref, "Caller verified original native checkpoint after recovery"))
	require.NoError(t, err)
	_, active := activeSync(rolledBack)
	assert.Nil(t, active)
	syncs := rolledBack.Workflows[0].Phases[4].(*work.ExternalReviewPhase).BranchSyncs
	require.Len(t, syncs, 2)
	require.NotNil(t, syncs[1].Rollback)
	assert.Equal(t, "S1", syncs[1].Checkpoint.LocalHead)
	assert.Equal(t, "S1", syncs[1].Checkpoint.RemoteHead)
	assert.Nil(t, syncs[1].Push)
	assert.Nil(t, syncs[1].ConflictResolution, "recovery without a launched resolver cannot fabricate an execution")
}
