package taskstate_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
)

func TestStorePersistsAndClearsSyncConflictRecoveryOperation(t *testing.T) {
	store := newTestStore(t, time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC))
	operation, err := store.BeginSyncConflictOperation("alpha", "op-1", taskstate.SyncConflictOperation{
		ID: "sync-1", Branch: "orpheus/op-1", Worktree: "/fixture/op-1", DefaultBranch: "main",
		Checkpoint: taskstate.SyncConflictCheckpoint{LocalHead: "local", RemoteHead: "remote", MergeSource: "refs/remotes/origin/main"},
		Phase:      taskstate.SyncConflictPhasePrepared,
	})
	if err != nil {
		t.Fatalf("begin operation: %v", err)
	}
	if operation.CreatedAt.IsZero() || operation.UpdatedAt.IsZero() {
		t.Fatalf("operation timestamps = %#v", operation)
	}

	_, err = store.UpdateSyncConflictOperation("alpha", "op-1", operation.ID, func(active *taskstate.SyncConflictOperation) error {
		active.Phase = taskstate.SyncConflictPhaseConflicted
		active.ConflictFiles = []string{"conflict.txt"}
		return nil
	})
	if err != nil {
		t.Fatalf("update operation: %v", err)
	}
	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.ActiveSyncConflict == nil || loaded.ActiveSyncConflict.Phase != taskstate.SyncConflictPhaseConflicted || loaded.ActiveSyncConflict.ConflictFiles[0] != "conflict.txt" {
		t.Fatalf("active operation = %#v", loaded.ActiveSyncConflict)
	}

	if err := store.ResolveSyncConflictOperation("alpha", "op-1", operation.ID, "rolled_back", "supervisor absent"); err != nil {
		t.Fatalf("resolve operation: %v", err)
	}
	loaded, err = store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if loaded.ActiveSyncConflict != nil {
		t.Fatalf("active operation = %#v, want nil", loaded.ActiveSyncConflict)
	}
	if len(loaded.Events) != 1 || loaded.Events[0].Type != taskstate.EventSyncConflictRolledBack {
		t.Fatalf("events = %#v", loaded.Events)
	}
	assertStoreYAMLContains(t, store, "alpha", "op-1", "sync_conflict_rolled_back")
}

func TestStoreKeepsUnresolvedSyncConflictOperationAsGuard(t *testing.T) {
	store := newTestStore(t, time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC))
	operation, err := store.BeginSyncConflictOperation("alpha", "op-1", taskstate.SyncConflictOperation{
		ID: "sync-1", Branch: "orpheus/op-1", Worktree: "/fixture/op-1", DefaultBranch: "main",
		Checkpoint: taskstate.SyncConflictCheckpoint{LocalHead: "local", RemoteHead: "remote", MergeSource: "refs/remotes/origin/main"}, Phase: taskstate.SyncConflictPhasePrepared,
	})
	if err != nil {
		t.Fatalf("begin operation: %v", err)
	}
	if err := store.MarkSyncConflictOperationUnresolved("alpha", "op-1", operation.ID, "remote task branch changed"); err != nil {
		t.Fatalf("mark unresolved: %v", err)
	}
	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.ActiveSyncConflict == nil || loaded.ActiveSyncConflict.Phase != taskstate.SyncConflictPhaseUnresolved || !strings.Contains(loaded.ActiveSyncConflict.Reason, "remote") {
		t.Fatalf("active operation = %#v", loaded.ActiveSyncConflict)
	}
	if _, err := store.BeginSyncConflictOperation("alpha", "op-1", operation); err == nil {
		t.Fatal("begin duplicate operation succeeded")
	}
}
