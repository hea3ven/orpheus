package doctor

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/workflow"
)

func TestRunLeavesPersistedUnresolvedSyncConflictUntouched(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatal(err)
	}
	store := taskstate.NewStore(paths)
	run, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "recorder", WorkDirectory: "/fixture/op-1", Branch: "orpheus/op-1", Worktree: "/fixture/op-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun("alpha", "op-1", run.Attempt, taskstate.RunStatusSucceeded); err != nil {
		t.Fatal(err)
	}
	_, err = store.BeginSyncConflictOperation("alpha", "op-1", taskstate.SyncConflictOperation{
		ID: "sync-1", Branch: "orpheus/op-1", Worktree: "/fixture/op-1", DefaultBranch: "main", Phase: taskstate.SyncConflictPhaseUnresolved,
		Checkpoint: taskstate.SyncConflictCheckpoint{LocalHead: "before", RemoteHead: "before", MergeSource: "main-head"},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatal(err)
	}

	for _, fix := range []bool{false, true} {
		result, err := Run(Options{Paths: paths, Registry: registry.Registry{Repos: []registry.Repo{{ID: "alpha", Path: "/fixture/alpha"}}}, Fix: fix})
		if err != nil {
			t.Fatalf("doctor fix=%v: %v", fix, err)
		}
		if len(result.SyncConflictRows) != 1 || result.SyncConflictRows[0].Outcome != "unresolved" || result.SyncConflictRows[0].Reason != "operation was previously marked unresolved" {
			t.Fatalf("sync diagnosis = %#v", result.SyncConflictRows)
		}
		after, err := store.Load("alpha", "op-1")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("doctor fix=%v changed durable unresolved state: got %#v, want %#v", fix, after, before)
		}
	}
}

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

func TestRepairSyncConflictRecoveryCompletesPushedState(t *testing.T) {
	paths, err := state.NewPaths(
		filepath.Join(testutil.CanonicalTempDir(t), "config"),
		filepath.Join(testutil.CanonicalTempDir(t), "data"),
	)
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := taskstate.NewStore(paths)
	operation, err := store.BeginSyncConflictOperation("alpha", "op-1", taskstate.SyncConflictOperation{
		ID:            "sync-1",
		Branch:        "orpheus/op-1",
		Worktree:      "/fixture/op-1",
		DefaultBranch: "main",
		Checkpoint: taskstate.SyncConflictCheckpoint{
			LocalHead: "before", RemoteHead: "before", MergeSource: "main-head",
		},
		Phase:     taskstate.SyncConflictPhasePushIntent,
		LocalHead: "completed",
	})
	if err != nil {
		t.Fatalf("begin sync conflict operation: %v", err)
	}

	diagnosis := repairSyncConflictRecovery(store, "alpha", "op-1", operation, syncConflictRecoveryDiagnosis{
		outcome: "pushed", reason: "remote task branch matches the recorded local completion", remoteHead: operation.LocalHead,
	}, workflow.LocalSyncGit{})
	if diagnosis.outcome != "pushed" {
		t.Fatalf("outcome = %q, want pushed", diagnosis.outcome)
	}
	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load repaired state: %v", err)
	}
	if loaded.ActiveSyncConflict != nil {
		t.Fatalf("active operation = %#v, want nil", loaded.ActiveSyncConflict)
	}
	if len(loaded.Events) != 1 || loaded.Events[0].Type != taskstate.EventSyncConflictFinished || loaded.Events[0].Commit != operation.LocalHead {
		t.Fatalf("events = %#v, want one finished event for %s", loaded.Events, operation.LocalHead)
	}
}
