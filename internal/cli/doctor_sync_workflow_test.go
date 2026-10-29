//go:build integration

package cli_test

import (
	"context"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	workmodel "github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/hea3ven/orpheus/internal/workstate"
)

func TestIntegrationWorkflowDoctorRecoversSyncConflictState(t *testing.T) {
	t.Run("completes pushed state", testDoctorCompletesPushedSyncConflict)
	t.Run("rolls back local conflict state", testDoctorRollsBackSyncConflict)
}

func testDoctorCompletesPushedSyncConflict(t *testing.T) {
	t.Helper()
	git := &doctorRecoveryGit{remote: "completed", head: "completed"}
	const branch = "orpheus/op-pushed"
	const completedHead = "completed"

	fixture := newDoctorSyncWorkflow(t, git)
	store := taskstate.NewStore(fixture.paths)
	saveReaderHistory(t, fixture, readerDeliveredHistory(t, "alpha", "op-pushed", "/fixture/repo", branch, workflowTime("2026-07-07T10:00:00Z")))
	operation, err := store.BeginSyncConflictOperation("alpha", "op-pushed", taskstate.SyncConflictOperation{
		Branch:        branch,
		Worktree:      "/fixture/repo",
		DefaultBranch: "main",
		Checkpoint: taskstate.SyncConflictCheckpoint{
			LocalHead: "initial", RemoteHead: "initial", MergeSource: "initial",
		},
		LocalHead: completedHead,
	})
	if err != nil {
		t.Fatalf("begin sync conflict operation: %v", err)
	}
	operation, err = store.UpdateSyncConflictOperation("alpha", "op-pushed", operation.ID, func(observed *taskstate.SyncConflictOperation) error { observed.LocalHead = completedHead; return nil })
	require.NoError(t, err)
	result, stderr := fixture.mustExecute("doctor")
	assert.Empty(t, stderr)
	assertDoctorSyncOutcome(t, result, "pushed")
	loaded, err := store.Load("alpha", "op-pushed")
	if err != nil {
		t.Fatalf("load dry-run state: %v", err)
	}
	if loaded.ActiveSyncConflict == nil || loaded.ActiveSyncConflict.Phase != operation.Phase || loaded.ActiveSyncConflict.ObservedRemoteHead != "" {
		t.Fatalf("dry-run operation = %#v, want unchanged local completion", loaded.ActiveSyncConflict)
	}

	result, stderr = fixture.mustExecute("doctor", "--fix")
	assert.Empty(t, stderr)
	assertDoctorSyncOutcome(t, result, "pushed")
	loaded, err = store.Load("alpha", "op-pushed")
	if err != nil {
		t.Fatalf("load fixed state: %v", err)
	}
	if loaded.ActiveSyncConflict != nil {
		t.Fatalf("active operation = %#v, want nil", loaded.ActiveSyncConflict)
	}
	raw, err := workstate.NewStore(fixture.paths).Load("alpha", "op-pushed")
	require.NoError(t, err)
	synced := raw.Workflows[0].Phases[4].(*workmodel.ExternalReviewPhase).BranchSyncs[0]
	require.NotNil(t, synced.Push)
	assert.Equal(t, completedHead, synced.Push.Head)
	assert.Nil(t, synced.ConflictResolution, "an ordinary synchronization cannot invent a resolver execution")
}

func testDoctorRollsBackSyncConflict(t *testing.T) {
	t.Helper()
	git := &doctorRecoveryGit{remote: "task", head: "task", conflicted: true}
	const branch = "orpheus/op-rollback"
	const taskHead = "task"
	const mainHead = "default"

	fixture := newDoctorSyncWorkflow(t, git)
	store := taskstate.NewStore(fixture.paths)
	saveReaderHistory(t, fixture, readerDeliveredHistory(t, "alpha", "op-rollback", "/fixture/repo", branch, workflowTime("2026-07-07T10:00:00Z")))
	_, err := store.BeginSyncConflictOperation("alpha", "op-rollback", taskstate.SyncConflictOperation{
		Branch:        branch,
		Worktree:      "/fixture/repo",
		DefaultBranch: "main",
		Checkpoint: taskstate.SyncConflictCheckpoint{
			LocalHead: taskHead, RemoteHead: taskHead, MergeSource: mainHead,
		},
		ConflictFiles: []string{"conflict.txt"},
	})
	if err != nil {
		t.Fatalf("begin sync conflict operation: %v", err)
	}
	result, stderr := fixture.mustExecute("doctor")
	assert.Empty(t, stderr)
	assertDoctorSyncOutcome(t, result, "rollbackable")
	if !git.conflicted || git.rollbacks != 0 {
		t.Fatal("dry run changed conflict")
	}

	loaded, err := store.Load("alpha", "op-rollback")
	if err != nil {
		t.Fatalf("load dry-run state: %v", err)
	}
	if loaded.ActiveSyncConflict == nil {
		t.Fatal("dry run cleared active sync conflict")
	}

	result, stderr = fixture.mustExecute("doctor", "--fix")
	assert.Empty(t, stderr)
	assertDoctorSyncOutcome(t, result, "rolled_back")
	if git.head != taskHead || git.conflicted || git.rollbacks != 1 {
		t.Fatalf("rollback = %#v", git)
	}

	loaded, err = store.Load("alpha", "op-rollback")
	if err != nil {
		t.Fatalf("load fixed state: %v", err)
	}
	if loaded.ActiveSyncConflict != nil {
		t.Fatalf("active operation = %#v, want nil", loaded.ActiveSyncConflict)
	}
	assertDoctorSyncEvent(t, loaded.Events, taskstate.EventBranchSyncRolledBack, taskHead)
}

func newDoctorSyncWorkflow(t *testing.T, git *doctorRecoveryGit) *workflowFixture {
	t.Helper()
	fixture := newCommandWorkflow(t)
	fixture.options.Dependencies.DoctorEffects.SyncGit = git
	require.NoError(t, fixture.registryStore.Save(registry.Registry{Repos: []registry.Repo{{ID: "alpha", Name: "Alpha", Path: "/fixture/repo", DefaultBranch: "main", BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "op"}}}))
	withWorkflowSources(t, fixture, map[string]taskSourceResult{"/fixture/repo": {tasks: []taskmodel.Task{{ID: "op-pushed", Status: taskmodel.StatusInProgress, IssueType: taskmodel.IssueTypeTask}, {ID: "op-rollback", Status: taskmodel.StatusInProgress, IssueType: taskmodel.IssueTypeTask}}}})
	return fixture
}

func assertDoctorSyncOutcome(t *testing.T, output, want string) {
	t.Helper()
	assert.Contains(t, output, "Sync conflict recovery")
	assert.Contains(t, output, want)
	recovery := strings.Split(strings.Split(output, "Sync conflict recovery")[1], "\n\n")[0]
	assert.Equal(t, 1, strings.Count(recovery, "op-pushed")+strings.Count(recovery, "op-rollback"), "one actual recovery operation must be reported independently of implementation/reviewer telemetry")
}

func assertDoctorSyncEvent(t *testing.T, events []taskstate.Event, eventType taskstate.EventType, commit string) {
	t.Helper()
	for _, event := range events {
		if event.Type == eventType && event.Commit == commit {
			return
		}
	}
	t.Fatalf("events = %#v, want %s with commit %q", events, eventType, commit)
}

type doctorRecoveryGit struct {
	workflow.SyncConflictRecoveryGit
	remote, head string
	conflicted   bool
	rollbacks    int
}

func (g *doctorRecoveryGit) InspectRemoteTaskBranchHead(context.Context, gitmeta.TaskBranchSyncOptions) (string, error) {
	return g.remote, nil
}
func (*doctorRecoveryGit) InspectTaskBranchConflictRollbackEligibility(context.Context, gitmeta.TaskBranchSyncOptions, gitmeta.TaskBranchConflictCheckpoint, string) error {
	return nil
}
func (g *doctorRecoveryGit) RollbackTaskBranchConflictResolution(_ context.Context, _ gitmeta.TaskBranchSyncOptions, checkpoint gitmeta.TaskBranchConflictCheckpoint) error {
	g.head = checkpoint.LocalHead
	g.conflicted = false
	g.rollbacks++
	return nil
}
