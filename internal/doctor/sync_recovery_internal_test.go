package doctor

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestClassifySyncConflictRecoveryLeavesUnresolvedStateUntouched(t *testing.T) {
	operation := taskstate.SyncConflictOperation{
		ID:            "sync-1",
		Branch:        "orpheus/op-1",
		Worktree:      "/fixture/op-1",
		DefaultBranch: "main",
		Phase:         taskstate.SyncConflictPhaseUnresolved,
	}
	taskState := taskstate.TaskState{
		TaskID:        "op-1",
		WorkDirectory: taskstate.WorkDirectory{Path: operation.Worktree},
		GitFacts:      taskstate.GitFacts{Branch: operation.Branch, Worktree: operation.Worktree},
	}
	before := operation

	diagnosis := classifySyncConflictRecovery(registry.Repo{}, taskState, operation, nil)

	if diagnosis.outcome != "unresolved" || diagnosis.reason != "operation was previously marked unresolved" {
		t.Fatalf("diagnosis = %#v, want unresolved default reason", diagnosis)
	}
	if !reflect.DeepEqual(operation, before) {
		t.Fatalf("operation = %#v, want unchanged %#v", operation, before)
	}
}

func TestClassifySyncConflictRecoveryRejectsOwnershipMismatch(t *testing.T) {
	operation := taskstate.SyncConflictOperation{
		Branch:   "orpheus/op-1",
		Worktree: "/fixture/op-1",
		Phase:    taskstate.SyncConflictPhasePrepared,
	}
	diagnosis := classifySyncConflictRecovery(registry.Repo{}, taskstate.TaskState{}, operation, nil)
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
	})
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
