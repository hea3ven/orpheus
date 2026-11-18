package externalreview

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

func (s *Service) recoveryOperation(ref work.AttemptRef) phaseexec.Operation {
	return phaseexec.Operation{Name: "reconcile branch synchronization", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		sync, err := syncAt(current, ref)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		view, err := taskstate.Project(*current)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		operation := view.ActiveSyncConflict
		if operation == nil {
			return phaseexec.Prepared{}, errors.New("branch synchronization has no recovery observation")
		}
		metadata := s.Task.OrpheusMetadata()
		if strings.TrimSpace(metadata.Branch) != operation.Branch || strings.TrimSpace(metadata.Worktree) != operation.Worktree {
			return phaseexec.Prepared{}, s.unresolved("task target no longer matches the recorded conflict operation")
		}
		inspection := executionInspection(operation.Execution)
		if inspection.Condition == phaseexec.AttachedExecutionLive {
			return phaseexec.Prepared{}, fmt.Errorf("task %s sync conflict resolver is still live: %s", s.Task.ID, inspection.Reason)
		}
		if inspection.Condition == phaseexec.AttachedExecutionUnverifiable {
			return phaseexec.Prepared{}, s.unresolved(inspection.Reason)
		}
		w, _, publication, err := externalAt(current)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		destination, err := w.ReviewDestination(publication.Review)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		opts := gitmeta.TaskBranchSyncOptions{RepoPath: s.Repository.Path, Branch: operation.Branch, Worktree: operation.Worktree, DefaultBranch: destination.Branch}
		checkpoint := sync.Checkpoint
		local := ""
		if sync.LocalCommit != nil {
			local = sync.LocalCommit.Head
		}
		var execution *work.AgentExecution
		if sync.ConflictResolution != nil {
			observed := sync.ConflictResolution.Execution
			execution = &observed
		}
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			result := s.reconcileCheckpoint(ctx, ref, opts, checkpoint, execution, local, inspection.Reason)
			if result.Err == nil {
				s.reconciled = true
			}
			return result
		}}, nil
	}}
}

func (s *Service) unresolved(reason string) error {
	return fmt.Errorf("task %s has unresolved sync conflict recovery: %s", s.Task.ID, reason)
}

func (s *Service) recoverFailure(ctx context.Context, ref work.AttemptRef, opts gitmeta.TaskBranchSyncOptions, checkpoint work.BranchSyncCheckpoint, execution *work.AgentExecution, local string, cause error) phaseexec.Result {
	// Result/PID writes may have succeeded independently. In particular, a
	// failed terminal save must not turn a still-supervised resolver into an
	// abandoned checkout eligible for rollback.
	if execution != nil {
		if err := s.checkResolverRecovery(ctx); err != nil {
			return phaseexec.Result{Err: errors.Join(cause, err)}
		}
	}
	result := s.reconcileCheckpoint(ctx, ref, opts, checkpoint, execution, local, cause.Error())
	result.Err = errors.Join(cause, result.Err)
	return result
}

func (s *Service) checkResolverRecovery(ctx context.Context) error {
	current, err := s.Checkpoint(context.WithoutCancel(ctx), nil)
	if err != nil {
		return err
	}
	view, err := taskstate.Project(current)
	if err != nil {
		return err
	}
	if view.ActiveSyncConflict == nil {
		return nil
	}
	inspection := executionInspection(view.ActiveSyncConflict.Execution)
	if inspection.Condition == phaseexec.AttachedExecutionLive {
		return fmt.Errorf("task %s sync conflict resolver is still live: %s", s.Task.ID, inspection.Reason)
	}
	if inspection.Condition == phaseexec.AttachedExecutionUnverifiable {
		return s.unresolved(inspection.Reason)
	}
	return nil
}

func (s *Service) reconcileCheckpoint(ctx context.Context, ref work.AttemptRef, opts gitmeta.TaskBranchSyncOptions, checkpoint work.BranchSyncCheckpoint, execution *work.AgentExecution, local, reason string) phaseexec.Result {
	remote, err := s.Git.InspectRemoteTaskBranchHead(ctx, opts)
	if err != nil {
		return phaseexec.Result{Err: s.unresolved("remote inspection failed: " + err.Error())}
	}
	if local != "" && remote == local {
		return phaseexec.Result{Update: pushed(ref, remote, false)}
	}
	if remote != checkpoint.RemoteHead {
		return phaseexec.Result{Err: s.unresolved("remote task branch changed from the recorded checkpoint")}
	}
	native := gitmeta.TaskBranchConflictCheckpoint(checkpoint)
	if err := s.Git.VerifyTaskBranchConflictRollback(ctx, opts, native); err == nil {
		return phaseexec.Result{Update: RollbackChange(ref, reason)}
	}
	if err := s.Git.InspectTaskBranchConflictRollbackEligibility(ctx, opts, native, local); err != nil {
		return phaseexec.Result{Err: s.unresolved("local recovery state is incompatible: " + err.Error())}
	}
	if execution == nil && local != "" {
		observed, err := s.Git.InspectTaskBranchConflictCheckpoint(ctx, opts)
		if err != nil || observed.LocalHead != local {
			return phaseexec.Result{Err: s.unresolved("completed local merge no longer matches its recorded head")}
		}
		if _, err := s.Git.PushCommittedTaskBranchConflictResolution(ctx, opts); err != nil {
			return phaseexec.Result{Err: err}
		}
		remote, err := s.Git.InspectRemoteTaskBranchHead(ctx, opts)
		if err != nil {
			return phaseexec.Result{Err: err}
		}
		if remote != local {
			return phaseexec.Result{Err: fmt.Errorf("remote head %s does not match local head %s", remote, local)}
		}
		return phaseexec.Result{Update: pushed(ref, remote, false)}
	}
	if err := s.Git.RollbackTaskBranchConflictResolution(ctx, opts, native); err != nil {
		return phaseexec.Result{Err: s.unresolved("safe rollback failed: " + err.Error())}
	}
	if err := s.Git.VerifyTaskBranchConflictRollback(ctx, opts, native); err != nil {
		return phaseexec.Result{Err: s.unresolved("rollback could not be independently verified: " + err.Error())}
	}
	return phaseexec.Result{Update: RollbackChange(ref, reason)}
}

// executionInspection keeps actual process supervision distinct from native Git recovery.
func executionInspection(execution *taskstate.AgentExecution) phaseexec.AttachedExecutionInspection {
	if execution == nil {
		return phaseexec.AttachedExecutionInspection{Condition: phaseexec.AttachedExecutionRecoverable, Reason: "resolver_not_started"}
	}
	if execution.Status != taskstate.RunStatusRunning {
		return phaseexec.AttachedExecutionInspection{Condition: phaseexec.AttachedExecutionRecoverable, Reason: "resolver_process_outcome_observed"}
	}
	return phaseexec.InspectAttachedExecution(*execution, nil)
}
