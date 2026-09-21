//go:build integration

package cli_test

import (
	"context"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
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
	startFinishedDoctorRun(t, store, "op-pushed", branch, "/fixture/repo")
	operation, err := store.BeginSyncConflictOperation("alpha", "op-pushed", taskstate.SyncConflictOperation{
		ID:            "sync-pushed",
		Branch:        branch,
		Worktree:      "/fixture/repo",
		DefaultBranch: "main",
		Checkpoint: taskstate.SyncConflictCheckpoint{
			LocalHead: "initial", RemoteHead: "initial", MergeSource: "initial",
		},
		Phase:     taskstate.SyncConflictPhasePushIntent,
		LocalHead: completedHead,
	})
	if err != nil {
		t.Fatalf("begin sync conflict operation: %v", err)
	}
	result, stderr := fixture.mustExecute("doctor")
	assert.Empty(t, stderr)
	assertDoctorSyncOutcome(t, result, "pushed")
	loaded, err := store.Load("alpha", "op-pushed")
	if err != nil {
		t.Fatalf("load dry-run state: %v", err)
	}
	if loaded.ActiveSyncConflict == nil || loaded.ActiveSyncConflict.Phase != operation.Phase || loaded.ActiveSyncConflict.ObservedRemoteHead != "" {
		t.Fatalf("dry-run operation = %#v, want unchanged push intent", loaded.ActiveSyncConflict)
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
	assertDoctorSyncEvent(t, loaded.Events, taskstate.EventSyncConflictFinished, completedHead)
}

func testDoctorRollsBackSyncConflict(t *testing.T) {
	t.Helper()
	git := &doctorRecoveryGit{remote: "task", head: "task", conflicted: true}
	const branch = "orpheus/op-rollback"
	const taskHead = "task"
	const mainHead = "default"

	fixture := newDoctorSyncWorkflow(t, git)
	store := taskstate.NewStore(fixture.paths)
	startFinishedDoctorRun(t, store, "op-rollback", branch, "/fixture/repo")
	_, err := store.BeginSyncConflictOperation("alpha", "op-rollback", taskstate.SyncConflictOperation{
		ID:            "sync-rollback",
		Branch:        branch,
		Worktree:      "/fixture/repo",
		DefaultBranch: "main",
		Checkpoint: taskstate.SyncConflictCheckpoint{
			LocalHead: taskHead, RemoteHead: taskHead, MergeSource: mainHead,
		},
		Phase:         taskstate.SyncConflictPhaseConflicted,
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
	assertDoctorSyncEvent(t, loaded.Events, taskstate.EventSyncConflictRolledBack, "")
}

func newDoctorSyncWorkflow(t *testing.T, git *doctorRecoveryGit) *workflowFixture {
	t.Helper()
	fixture := newCommandWorkflow(t)
	fixture.options.Dependencies.DoctorEffects.SyncGit = git
	require.NoError(t, fixture.registryStore.Save(registry.Registry{Repos: []registry.Repo{{ID: "alpha", Name: "Alpha", Path: "/fixture/repo", DefaultBranch: "main", BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "op"}}}))
	withWorkflowSources(t, fixture, map[string]taskSourceResult{"/fixture/repo": {tasks: []taskmodel.Task{{ID: "op-pushed", Status: taskmodel.StatusInProgress, IssueType: taskmodel.IssueTypeTask}, {ID: "op-rollback", Status: taskmodel.StatusInProgress, IssueType: taskmodel.IssueTypeTask}}}})
	return fixture
}

func startFinishedDoctorRun(t *testing.T, store taskstate.Store, taskID, branch, worktree string) {
	t.Helper()
	run, err := store.StartRun("alpha", taskID, taskstate.StartRunOptions{
		Agent: "implementer", WorkDirectory: worktree, Branch: branch, Worktree: worktree,
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := store.FinishRun("alpha", taskID, run.Attempt, taskstate.RunStatusSucceeded); err != nil {
		t.Fatalf("finish run: %v", err)
	}
}

func assertDoctorSyncOutcome(t *testing.T, output, want string) {
	t.Helper()
	assert.Contains(t, output, "Sync conflict recovery")
	assert.Contains(t, output, want)
	assert.Equal(t, 1, strings.Count(output, "op-pushed")+strings.Count(output, "op-rollback"), "one recovery operation must be reported")
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
