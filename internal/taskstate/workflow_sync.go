package taskstate

import (
	"errors"
	"reflect"
	"strconv"
	"strings"

	"github.com/hea3ven/orpheus/internal/work"
)

// SyncConflictOperation is a derived recovery view. The document stores only
// typed branch-sync facts under their containing external-review occurrence.
func (s Store) BeginSyncConflictOperation(repoID, taskID string, observed SyncConflictOperation) (SyncConflictOperation, error) {
	var ref work.AttemptRef
	view, err := s.updateWorkflow(repoID, taskID, func(snapshot *work.State) (bool, error) {
		w := snapshot.ActiveWorkflow()
		if w == nil {
			return false, errors.New("branch sync requires an active workflow")
		}
		external, ok := w.LastPhase().(*work.ExternalReviewPhase)
		if !ok {
			return false, errors.New("branch sync requires the current delivered external review")
		}
		prepared := w.Context()
		destination, err := w.ReviewDestination(w.Phases[external.Publication.Phase].(*work.PublicationPhase).Plan.Review)
		if err != nil {
			return false, err
		}
		if observed.Branch != prepared.TaskBranch || observed.Worktree != prepared.Directory || observed.DefaultBranch != destination.Branch {
			return false, errors.New("branch-sync observations differ from their owning workflow")
		}
		if len(external.BranchSyncs) > 0 {
			last := external.BranchSyncs[len(external.BranchSyncs)-1]
			if last.Push == nil && last.Rollback == nil {
				return false, errors.New("branch sync already has an unsettled effort")
			}
		}
		index := len(external.BranchSyncs)
		ref = work.AttemptRef{RepositoryID: repoID, TaskID: taskID, Workflow: len(snapshot.Workflows) - 1, Phase: len(w.Phases) - 1, BranchSync: &index}
		external.BranchSyncs = append(external.BranchSyncs, work.BranchSync{StartedAt: s.nowUTC(), Checkpoint: work.BranchSyncCheckpoint(observed.Checkpoint)})
		return true, nil
	})
	if err != nil {
		return SyncConflictOperation{}, err
	}
	return syncProjection(view, ref)
}

func syncReference(repoID, taskID, address string) (work.AttemptRef, error) {
	ref := work.AttemptRef{RepositoryID: repoID, TaskID: taskID}
	parts := strings.Split(address, "/")
	if len(parts) != 3 {
		return ref, errors.New("branch-sync recovery requires workflow/phase/sync indexes")
	}
	indexes := make([]int, 3)
	for i, raw := range parts {
		index, err := strconv.Atoi(raw)
		if err != nil || index < 0 || strconv.Itoa(index) != raw {
			return ref, errors.New("invalid branch-sync containment address")
		}
		indexes[i] = index
	}
	ref.Workflow, ref.Phase, ref.BranchSync = indexes[0], indexes[1], &indexes[2]
	return ref, nil
}

func branchSyncAt(snapshot *work.State, ref work.AttemptRef) (*work.Workflow, *work.BranchSync, error) {
	if ref.RepositoryID != snapshot.RepositoryID || ref.TaskID != snapshot.TaskID || ref.Workflow < 0 || ref.Workflow >= len(snapshot.Workflows) || ref.BranchSync == nil || ref.Step != nil {
		return nil, nil, errors.New("invalid branch-sync owner")
	}
	w := &snapshot.Workflows[ref.Workflow]
	if ref.Phase < 0 || ref.Phase >= len(w.Phases) {
		return nil, nil, errors.New("invalid external-review owner")
	}
	external, ok := w.Phases[ref.Phase].(*work.ExternalReviewPhase)
	if !ok || *ref.BranchSync < 0 || *ref.BranchSync >= len(external.BranchSyncs) {
		return nil, nil, errors.New("invalid branch-sync position")
	}
	return w, &external.BranchSyncs[*ref.BranchSync], nil
}

func currentBranchSync(snapshot *work.State, ref work.AttemptRef) (*work.Workflow, *work.BranchSync, error) {
	w, sync, err := branchSyncAt(snapshot, ref)
	if err != nil {
		return nil, nil, err
	}
	if snapshot.ActiveWorkflow() != w || ref.Workflow != len(snapshot.Workflows)-1 || ref.Phase != len(w.Phases)-1 || *ref.BranchSync != len(w.Phases[ref.Phase].(*work.ExternalReviewPhase).BranchSyncs)-1 {
		return nil, nil, errors.New("branch sync no longer owns the current effort")
	}
	return w, sync, nil
}

func syncProjection(view TaskState, ref work.AttemptRef) (SyncConflictOperation, error) {
	w, sync, err := branchSyncAt(view.Work, ref)
	if err != nil {
		return SyncConflictOperation{}, err
	}
	return projectBranchSync(&TaskState{CurrentWorkflow: ref.Workflow}, w, sync, ref), nil
}

// UpdateSyncConflictOperation translates real external observations from the
// service's recovery view. Phase labels and command intents are never persisted.
func (s Store) UpdateSyncConflictOperation(repoID, taskID, address string, update func(*SyncConflictOperation) error) (SyncConflictOperation, error) {
	ref, err := syncReference(repoID, taskID, address)
	if err != nil {
		return SyncConflictOperation{}, err
	}
	view, err := s.updateWorkflow(repoID, taskID, func(snapshot *work.State) (bool, error) {
		w, sync, err := currentBranchSync(snapshot, ref)
		if err != nil {
			return false, err
		}
		previous := projectBranchSync(&TaskState{CurrentWorkflow: ref.Workflow}, w, sync, ref)
		observed := previous
		if err := update(&observed); err != nil {
			return false, err
		}
		if observed.ID != previous.ID || observed.Branch != previous.Branch || observed.Worktree != previous.Worktree || observed.DefaultBranch != previous.DefaultBranch || observed.Checkpoint != previous.Checkpoint {
			return false, errors.New("branch-sync ownership and native checkpoint are immutable")
		}
		changed, err := s.observeSyncExecution(sync, observed)
		if err != nil {
			return false, err
		}

		if observed.LocalHead != "" && sync.LocalCommit == nil {
			sync.LocalCommit = &work.BranchSyncCommit{Head: observed.LocalHead, ObservedAt: s.nowUTC()}
			changed = true
		}
		if observed.Phase == SyncConflictPhasePushed && sync.Push == nil {
			if sync.LocalCommit == nil || observed.ObservedRemoteHead != sync.LocalCommit.Head {
				return false, errors.New("push requires independently observed exact local merge head")
			}
			sync.Push = &work.BranchSyncPush{Head: observed.ObservedRemoteHead, ObservedAt: s.nowUTC(), Evidence: "Origin task branch independently observed at the recorded local sync result"}
			changed = true
		}
		return changed, nil
	})
	if err != nil {
		return SyncConflictOperation{}, err
	}
	return syncProjection(view, ref)
}

func (s Store) observeSyncExecution(sync *work.BranchSync, observed SyncConflictOperation) (bool, error) {
	if observed.Execution == nil {
		if sync.ConflictResolution != nil {
			return false, errors.New("recorded conflict resolver cannot be erased")
		}
		return false, nil
	}
	if sync.ConflictResolution == nil {
		execution, err := observedExecution(*observed.Execution)
		if err != nil {
			return false, err
		}
		sync.ConflictResolution = &work.ConflictResolution{Files: cloneStrings(observed.ConflictFiles), Execution: execution}
		return true, nil
	}
	execution := &sync.ConflictResolution.Execution
	before := *execution
	execution.ChildPID = observed.Execution.ChildPID
	// Only an actual native process observation can settle the execution.
	if observed.Execution.Process != nil {
		execution.Process = observed.Execution.Process
		if exit, ok := execution.Process.(*work.ExitedProcess); ok {
			execution.Duration = exit.At.Sub(execution.StartedAt)
		}
	}
	applyObservedUsage(execution, executionUsageOptions(*observed.Execution), s.nowUTC())
	return !reflect.DeepEqual(before, *execution), nil
}

func executionUsageOptions(e AgentExecution) RecordRunUsageOptions {
	return RecordRunUsageOptions{Model: e.ObservedModel, Session: e.Session, Usage: e.Usage, UsageCost: e.UsageCost, UsageCapture: e.UsageCapture}
}

// Clear is a verification of settlement, not deletion of the containing history.
func (s Store) ClearSyncConflictOperation(repoID, taskID, address string) error {
	ref, err := syncReference(repoID, taskID, address)
	if err != nil {
		return err
	}
	view, err := s.Load(repoID, taskID)
	if err != nil {
		return err
	}
	_, sync, err := currentBranchSync(view.Work, ref)
	if err != nil {
		return err
	}
	if sync.Push == nil && sync.Rollback == nil {
		return errors.New("unsettled branch-sync history cannot be cleared")
	}
	return nil
}

// ResolveSyncConflictOperation consumes the caller's verified native rollback.
func (s Store) ResolveSyncConflictOperation(repoID, taskID, address, outcome, reason string) error {
	if strings.TrimSpace(outcome) == "" || strings.TrimSpace(reason) == "" {
		return errors.New("verified rollback requires outcome and reason")
	}
	ref, err := syncReference(repoID, taskID, address)
	if err != nil {
		return err
	}
	_, err = s.updateWorkflow(repoID, taskID, func(snapshot *work.State) (bool, error) {
		_, sync, err := currentBranchSync(snapshot, ref)
		if err != nil {
			return false, err
		}
		if sync.Rollback != nil {
			return false, nil
		}
		if sync.Push != nil {
			return false, errors.New("pushed branch sync cannot be rolled back")
		}
		now := s.nowUTC()
		if sync.ConflictResolution != nil {
			execution := &sync.ConflictResolution.Execution
			if _, running := execution.Process.(*work.RunningProcess); running {
				execution.Process = &work.InterruptedProcess{At: now, Reason: reason, Trigger: "sync_recovery"}
			}
		}
		sync.Rollback = &work.BranchSyncRollback{LocalHead: sync.Checkpoint.LocalHead, RemoteHead: sync.Checkpoint.RemoteHead, ObservedAt: now, Reason: reason, Evidence: "Caller independently verified native checkout and local/remote checkpoint restoration"}
		return true, nil
	})
	return err
}

// An unresolved effort remains the safety guard. Diagnostic prose/command
// intentions are not another persisted operation journal.
func (s Store) MarkSyncConflictOperationUnresolved(repoID, taskID, address, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("unresolved inspection requires a reason")
	}
	ref, err := syncReference(repoID, taskID, address)
	if err != nil {
		return err
	}
	view, err := s.Load(repoID, taskID)
	if err != nil {
		return err
	}
	_, sync, err := currentBranchSync(view.Work, ref)
	if err != nil {
		return err
	}
	if sync.Push != nil || sync.Rollback != nil {
		return errors.New("branch sync is already settled")
	}
	return nil
}
