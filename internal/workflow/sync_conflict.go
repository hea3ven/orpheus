package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/work"
)

type durableSyncConflictOperation struct {
	store     SyncConflictRecoveryStore
	git       SyncConflictRecoveryGit
	operation taskstate.SyncConflictOperation
}

type syncConflictDirectError struct {
	err error
}

func (e syncConflictDirectError) Error() string {
	return e.err.Error()
}

func (e syncConflictDirectError) Unwrap() error {
	return e.err
}

func (s SyncService) resolveOpenPRBranchConflict(
	ctx context.Context,
	target syncTarget,
	gitState SyncGit,
	taskTarget tasktarget.Target,
	destination string,
	prURL string,
	updatePolicy SyncBranchUpdatePolicy,
) (SyncResult, error) {
	repo := target.source.Repository
	syncOpts := syncConflictOptions(repo, taskTarget, destination, updatePolicy)
	operation, err := s.prepareSyncConflictCheckpoint(ctx, target, gitState, syncOpts)
	if err != nil {
		return SyncResult{}, err
	}

	branchSync, err := gitState.BeginTaskBranchConflictResolution(ctx, syncOpts)
	if err != nil {
		cause := fmt.Errorf("prepare conflict resolution for task %s: %w", target.task.ID, err)
		return SyncResult{}, errors.Join(cause, s.recoverObservedSyncConflictFailure(ctx, target, gitState, cause))
	}
	if branchSync.Status != gitmeta.TaskBranchSyncConflicted {
		return s.completeNonConflictSync(ctx, target, gitState, taskTarget, syncOpts, branchSync, operation)
	}
	if err := s.requireSyncConflictResolver(target.task.ID); err != nil {
		return SyncResult{}, errors.Join(err, s.recoverObservedSyncConflictFailure(ctx, target, gitState, err))
	}

	conflictOpts := syncConflictResolverOptions(target, taskTarget, destination, prURL, branchSync.ConflictFiles, operation)
	err = s.runSyncConflictResolver(ctx, repo.ID, target.task.ID, conflictOpts, operation)
	if err != nil {
		return SyncResult{}, s.recoverSyncConflictResolverFailure(ctx, target, gitState, err)
	}

	completed, err := completeSyncConflict(ctx, target, gitState, syncOpts, branchSync.ConflictFiles, operation)
	if err != nil {
		var directErr syncConflictDirectError
		if errors.As(err, &directErr) {
			return SyncResult{}, directErr.err
		}
		return SyncResult{}, s.handleSyncConflictCompletionFailure(ctx, target, gitState, err)
	}
	if completed.Status != gitmeta.TaskBranchSyncUpdated {
		return s.handleUnexpectedSyncConflictStatus(target, taskTarget, destination, completed)
	}

	if err := clearSyncConflictOperation(repo.ID, target.task.ID, "successful conflict recovery state", operation); err != nil {
		return SyncResult{}, err
	}

	return resolvedConflictSyncResult(destination, taskTarget.Branch), nil
}

func resolvedConflictSyncResult(destination string, branch string) SyncResult {
	return SyncResult{
		Status: SyncStatusBranchUpdated,
		Reason: fmt.Sprintf(
			"resolved merge conflicts with the configured agent, merged %s into %s, and pushed the branch",
			destination,
			branch,
		),
	}
}

func (s SyncService) requireSyncConflictResolver(taskID string) error {
	if s.ConflictResolver == nil {
		return fmt.Errorf(
			"update open PR branch for task %s: merge conflicts require a configured conflict resolver",
			taskID,
		)
	}
	return nil
}

func syncConflictOptions(
	repo task.Repository,
	taskTarget tasktarget.Target,
	destination string,
	updatePolicy SyncBranchUpdatePolicy,
) gitmeta.TaskBranchSyncOptions {
	return gitmeta.TaskBranchSyncOptions{
		RepoPath:      repo.Path,
		DefaultBranch: destination,
		Branch:        taskTarget.Branch,
		Worktree:      taskTarget.Worktree,
		UpdatePolicy:  gitBranchUpdatePolicy(updatePolicy),
	}
}

func (s SyncService) prepareSyncConflictCheckpoint(
	ctx context.Context,
	target syncTarget,
	gitState SyncGit,
	syncOpts gitmeta.TaskBranchSyncOptions,
) (*durableSyncConflictOperation, error) {
	store, storeOK := s.RunStore.(SyncConflictRecoveryStore)
	if !storeOK {
		return nil, errors.New("branch synchronization requires durable workflow storage and native recovery observations")
	}

	checkpoint, err := gitState.InspectTaskBranchConflictCheckpoint(ctx, syncOpts)
	if err != nil {
		return nil, fmt.Errorf("checkpoint conflict resolution for task %s: %w", target.task.ID, err)
	}
	operation, err := store.BeginSyncConflictOperation(target.source.Repository.ID, target.task.ID, taskstate.SyncConflictOperation{
		Branch:        syncOpts.Branch,
		Worktree:      syncOpts.Worktree,
		DefaultBranch: syncOpts.DefaultBranch,
		Checkpoint: taskstate.SyncConflictCheckpoint{
			LocalHead:   checkpoint.LocalHead,
			RemoteHead:  checkpoint.RemoteHead,
			MergeSource: checkpoint.MergeSource,
		},
		Phase: taskstate.SyncConflictPhasePrepared,
	})
	if err != nil {
		return nil, fmt.Errorf("persist conflict resolution checkpoint for task %s: %w", target.task.ID, err)
	}
	return &durableSyncConflictOperation{store: store, git: gitState, operation: operation}, nil
}

func (s SyncService) completeNonConflictSync(
	ctx context.Context,
	target syncTarget,
	gitState SyncGit,
	taskTarget tasktarget.Target,
	syncOpts gitmeta.TaskBranchSyncOptions,
	branchSync gitmeta.TaskBranchSyncResult,
	operation *durableSyncConflictOperation,
) (SyncResult, error) {

	needsPush, err := nonConflictSyncNeedsPush(ctx, syncOpts, branchSync, operation)
	if err != nil {
		return SyncResult{}, err
	}
	if needsPush {
		if err := s.pushNonConflictSync(ctx, target, gitState, syncOpts, branchSync, operation); err != nil {
			return SyncResult{}, err
		}
	} else if err := settleUnchangedSync(ctx, target, syncOpts, branchSync, operation); err != nil {
		return SyncResult{}, err
	}

	if err := clearSyncConflictOperation(target.source.Repository.ID, target.task.ID, "settled branch synchronization", operation); err != nil {
		return SyncResult{}, err
	}
	return branchSyncResult(syncOpts.DefaultBranch, taskTarget.Branch, target.task.ID, branchSync)
}

func settleUnchangedSync(ctx context.Context, target syncTarget, syncOpts gitmeta.TaskBranchSyncOptions, branchSync gitmeta.TaskBranchSyncResult, operation *durableSyncConflictOperation) error {
	remote, err := operation.git.InspectRemoteTaskBranchHead(ctx, syncOpts)
	if err != nil {
		return err
	}
	if remote == branchSync.Head {
		if err := operation.update(target.source.Repository.ID, target.task.ID, func(active *taskstate.SyncConflictOperation) {
			active.LocalHead = branchSync.Head
			active.ObservedRemoteHead = remote
			active.Phase = taskstate.SyncConflictPhasePushed
		}); err != nil {
			return err
		}
	} else {
		checkpoint := gitmeta.TaskBranchConflictCheckpoint(operation.operation.Checkpoint)
		if err := operation.git.VerifyTaskBranchConflictRollback(ctx, syncOpts, checkpoint); err != nil {
			return err
		}
		if err := operation.store.ResolveSyncConflictOperation(target.source.Repository.ID, target.task.ID, operation.operation.ID, "unchanged_checkpoint_verified", "Conflict-only synchronization left the native checkpoint unchanged"); err != nil {
			return err
		}
	}
	return nil
}

func nonConflictSyncNeedsPush(
	ctx context.Context,
	syncOpts gitmeta.TaskBranchSyncOptions,
	branchSync gitmeta.TaskBranchSyncResult,
	operation *durableSyncConflictOperation,
) (bool, error) {
	if syncOpts.UpdatePolicy == gitmeta.TaskBranchUpdateConflictsOnly {
		return false, nil
	}
	if branchSync.Status == gitmeta.TaskBranchSyncUpdated {
		return true, nil
	}
	if branchSync.Status != gitmeta.TaskBranchSyncAlreadyCurrent {
		return false, nil
	}
	remoteHead, err := operation.git.InspectRemoteTaskBranchHead(ctx, syncOpts)
	if err != nil {
		return false, err
	}
	return remoteHead != branchSync.Head, nil
}

func (s SyncService) pushNonConflictSync(
	ctx context.Context,
	target syncTarget,
	gitState SyncGit,
	syncOpts gitmeta.TaskBranchSyncOptions,
	branchSync gitmeta.TaskBranchSyncResult,
	operation *durableSyncConflictOperation,
) error {
	completionGit := gitState
	repoID, taskID := target.source.Repository.ID, target.task.ID
	if err := operation.update(repoID, taskID, func(active *taskstate.SyncConflictOperation) {
		active.Phase = taskstate.SyncConflictPhaseLocalCompleted
		active.LocalHead = branchSync.Head
	}); err != nil {
		return err
	}
	if _, err := completionGit.PushCommittedTaskBranchConflictResolution(ctx, syncOpts); err != nil {
		return err
	}
	remoteHead, err := operation.git.InspectRemoteTaskBranchHead(ctx, syncOpts)
	if err != nil {
		return fmt.Errorf("inspect non-conflict sync push for task %s: %w", taskID, err)
	}
	if err := verifySyncConflictRemoteHead(remoteHead, branchSync.Head); err != nil {
		return fmt.Errorf("verify non-conflict sync push for task %s: %w", taskID, err)
	}
	return operation.update(repoID, taskID, func(active *taskstate.SyncConflictOperation) {
		active.Phase = taskstate.SyncConflictPhasePushed
		active.ObservedRemoteHead = remoteHead
	})
}

func syncConflictResolverOptions(
	target syncTarget,
	taskTarget tasktarget.Target,
	destination string,
	prURL string,
	conflictFiles []string,
	operation *durableSyncConflictOperation,
) SyncConflictResolutionOptions {
	opts := SyncConflictResolutionOptions{
		Address:       operation.operation.Address,
		Repository:    target.source.Repository,
		Task:          target.task.Clone(),
		Branch:        taskTarget.Branch,
		Worktree:      taskTarget.Worktree,
		DefaultBranch: destination,
		PRURL:         prURL,
		ConflictFiles: append([]string{}, conflictFiles...),
	}

	repoID, taskID := target.source.Repository.ID, target.task.ID
	opts.RecordChildPID = func(pid int) error {
		_, err := operation.store.UpdateSyncConflictOperation(repoID, taskID, operation.operation.ID, func(active *taskstate.SyncConflictOperation) error {
			if active.Execution == nil {
				return errors.New("resolver execution is not recorded")
			}
			active.Execution.ChildPID = pid
			return nil
		})
		return err
	}
	return opts
}

func completeSyncConflict(
	ctx context.Context,
	target syncTarget,
	completionGit SyncGit,
	syncOpts gitmeta.TaskBranchSyncOptions,
	conflictFiles []string,
	operation *durableSyncConflictOperation,
) (gitmeta.TaskBranchSyncResult, error) {
	completed, err := completionGit.CommitTaskBranchConflictResolution(ctx, syncOpts, conflictFiles)
	if err != nil {
		return gitmeta.TaskBranchSyncResult{}, err
	}

	repoID, taskID := target.source.Repository.ID, target.task.ID
	if err := operation.update(repoID, taskID, func(active *taskstate.SyncConflictOperation) {
		active.Phase = taskstate.SyncConflictPhaseLocalCompleted
		active.LocalHead = completed.Head
	}); err != nil {
		return gitmeta.TaskBranchSyncResult{}, syncConflictDirectError{err: fmt.Errorf("record local conflict merge completion for task %s: %w", taskID, err)}
	}

	completed, err = completionGit.PushCommittedTaskBranchConflictResolution(ctx, syncOpts)
	if err != nil {
		return gitmeta.TaskBranchSyncResult{}, err
	}
	remoteHead, err := operation.git.InspectRemoteTaskBranchHead(ctx, syncOpts)
	if err != nil {
		return gitmeta.TaskBranchSyncResult{}, syncConflictDirectError{err: fmt.Errorf("inspect pushed conflict branch for task %s: %w", taskID, err)}
	}
	if err := verifySyncConflictRemoteHead(remoteHead, completed.Head); err != nil {
		return gitmeta.TaskBranchSyncResult{}, syncConflictDirectError{err: fmt.Errorf("verify pushed conflict branch for task %s: %w", taskID, err)}
	}
	if err := operation.update(repoID, taskID, func(active *taskstate.SyncConflictOperation) {
		active.Phase = taskstate.SyncConflictPhasePushed
		active.ObservedRemoteHead = remoteHead
	}); err != nil {
		return gitmeta.TaskBranchSyncResult{}, syncConflictDirectError{err: fmt.Errorf("record pushed conflict branch for task %s: %w", taskID, err)}
	}
	return completed, nil
}

func verifySyncConflictRemoteHead(remoteHead string, localHead string) error {
	if remoteHead != localHead {
		return fmt.Errorf("remote head %s does not match local head %s", remoteHead, localHead)
	}
	return nil
}

func (s SyncService) recoverSyncConflictResolverFailure(ctx context.Context, target syncTarget, gitState SyncGit, cause error) error {
	return errors.Join(cause, s.recoverObservedSyncConflictFailure(ctx, target, gitState, cause))
}

func (s SyncService) handleSyncConflictCompletionFailure(ctx context.Context, target syncTarget, gitState SyncGit, cause error) error {
	recoveryErr := s.recoverObservedSyncConflictFailure(ctx, target, gitState, cause)
	return fmt.Errorf("complete resolved merge for task %s: %w", target.task.ID, errors.Join(cause, recoveryErr))
}

func (s SyncService) handleUnexpectedSyncConflictStatus(target syncTarget, taskTarget tasktarget.Target, destination string, completed gitmeta.TaskBranchSyncResult) (SyncResult, error) {
	result, err := branchSyncResult(destination, taskTarget.Branch, target.task.ID, completed)
	if err != nil {
		return SyncResult{}, err
	}
	return result, fmt.Errorf("conflict resolution completed with branch sync status %q", completed.Status)
}

func clearSyncConflictOperation(
	repoID string,
	taskID string,
	label string,
	operation *durableSyncConflictOperation,
) error {
	if err := operation.store.ClearSyncConflictOperation(repoID, taskID, operation.operation.ID); err != nil {
		return fmt.Errorf("clear %s for task %s: %w", label, taskID, err)
	}
	return nil
}

func (operation *durableSyncConflictOperation) update(
	repoID string,
	taskID string,
	update func(*taskstate.SyncConflictOperation),
) error {
	return operation.updateWithError(repoID, taskID, func(active *taskstate.SyncConflictOperation) error {
		update(active)
		return nil
	})
}

func (operation *durableSyncConflictOperation) updateWithError(
	repoID string,
	taskID string,
	update func(*taskstate.SyncConflictOperation) error,
) error {
	updated, err := operation.store.UpdateSyncConflictOperation(repoID, taskID, operation.operation.ID, update)
	if err != nil {
		return err
	}
	operation.operation = updated
	return nil
}

func (s SyncService) runSyncConflictResolver(
	ctx context.Context,
	repoID string,
	taskID string,
	conflictOpts SyncConflictResolutionOptions,
	operation *durableSyncConflictOperation,
) error {
	prepared, err := s.ConflictResolver.PrepareSyncConflictResolution(ctx, conflictOpts)
	if err != nil {
		return fmt.Errorf(
			"prepare merge conflict agent for task %s: %w",
			taskID,
			err,
		)
	}
	prepared.Execution.SupervisorPID = os.Getpid()
	prepared.Execution.Address = operation.operation.Address
	if prepared.Execution.StartedAt.IsZero() {
		prepared.Execution.StartedAt = time.Now().UTC()
	}
	if err := recordSyncConflictResolverExecution(repoID, taskID, prepared.Execution, conflictOpts.ConflictFiles, operation); err != nil {
		return err
	}

	result := prepared.Execute(ctx, *operation.operation.Execution)
	err = result.Err

	recordErr := recordSyncResolverResult(repoID, taskID, operation, result)
	if recordErr != nil {
		return errors.Join(err, recordErr)
	}
	if result.Exit != nil && result.Exit.Code != 0 && err == nil {
		err = fmt.Errorf("resolver exited with code %d", result.Exit.Code)
	}
	if err != nil {

		return fmt.Errorf(
			"resolve merge conflicts for task %s with agent: %w",
			taskID,
			err,
		)
	}
	return nil
}

func recordSyncResolverResult(repoID, taskID string, operation *durableSyncConflictOperation, result agent.ExecutionResult) error {
	if result.Started && result.Exit == nil {
		return errors.New("resolver has no safe observed process exit; leave its workflow effort unresolved")
	}
	if !result.Started && result.Err == nil {
		return errors.New("resolver reported neither a started process nor a startup failure")
	}

	processErr := operation.update(repoID, taskID, func(active *taskstate.SyncConflictOperation) {
		if !result.Started {
			active.Execution.Process = &work.StartFailedProcess{At: time.Now().UTC(), Reason: result.Err.Error()}
		} else {
			active.Execution.Process = &work.ExitedProcess{At: result.Exit.At, Code: &result.Exit.Code}
			active.Execution.ChildPID = result.Exit.PID
		}
	})
	usageErr := operation.store.RecordExecutionUsage(operation.operation.Address, result.Usage)
	return errors.Join(processErr, usageErr)

}

func recordSyncConflictResolverExecution(
	repoID string,
	taskID string,
	execution taskstate.AgentExecution,
	files []string,
	operation *durableSyncConflictOperation,
) error {
	if execution.StartedAt.IsZero() {
		execution.StartedAt = time.Now().UTC()
	}
	if err := operation.update(repoID, taskID, func(active *taskstate.SyncConflictOperation) {
		active.Phase = taskstate.SyncConflictPhaseResolving
		active.Execution = &execution
		active.ConflictFiles = cloneConflictFiles(files)
	}); err != nil {
		return fmt.Errorf("record conflict resolver execution for task %s: %w", taskID, err)
	}
	return nil
}

func cloneConflictFiles(files []string) []string { return append([]string(nil), files...) }
