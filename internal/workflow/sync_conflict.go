package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
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
	if err := s.requireSyncConflictResolver(target.task.ID); err != nil {
		return SyncResult{}, err
	}
	repo := target.source.Repository
	syncOpts := syncConflictOptions(repo, taskTarget, destination, updatePolicy)
	operation, err := s.prepareSyncConflictCheckpoint(ctx, target, gitState, syncOpts)
	if err != nil {
		return SyncResult{}, err
	}

	branchSync, err := gitState.BeginTaskBranchConflictResolution(ctx, syncOpts)
	if err != nil {
		return SyncResult{}, fmt.Errorf("prepare conflict resolution for task %s: %w", target.task.ID, err)
	}
	if branchSync.Status != gitmeta.TaskBranchSyncConflicted {
		return s.completeNonConflictSync(ctx, target, gitState, taskTarget, syncOpts, branchSync, operation)
	}
	if err := recordSyncConflictFiles(repo.ID, target.task.ID, branchSync.ConflictFiles, operation); err != nil {
		return SyncResult{}, err
	}

	conflictOpts := syncConflictResolverOptions(target, taskTarget, destination, prURL, branchSync.ConflictFiles, operation)
	auditOpts, err := s.runSyncConflictResolver(ctx, repo.ID, target.task.ID, conflictOpts, operation)
	if err != nil {
		return SyncResult{}, s.recoverSyncConflictResolverFailure(ctx, target, gitState, operation, err)
	}

	completed, err := completeSyncConflict(ctx, target, gitState, syncOpts, branchSync.ConflictFiles, operation)
	if err != nil {
		var directErr syncConflictDirectError
		if errors.As(err, &directErr) {
			return SyncResult{}, directErr.err
		}
		return SyncResult{}, s.handleSyncConflictCompletionFailure(ctx, target, gitState, auditOpts, operation, err)
	}
	if completed.Status != gitmeta.TaskBranchSyncUpdated {
		return s.handleUnexpectedSyncConflictStatus(target, taskTarget, destination, auditOpts, completed)
	}
	if err := s.finishSyncConflictAudit(target, auditOpts, completed.Head); err != nil {
		return SyncResult{}, err
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
	recoveryGit, gitOK := gitState.(SyncConflictRecoveryGit)
	if !storeOK || !gitOK {
		return nil, nil
	}

	checkpoint, err := recoveryGit.InspectTaskBranchConflictCheckpoint(ctx, syncOpts)
	if err != nil {
		return nil, fmt.Errorf("checkpoint conflict resolution for task %s: %w", target.task.ID, err)
	}
	operation, err := store.BeginSyncConflictOperation(target.source.Repository.ID, target.task.ID, taskstate.SyncConflictOperation{
		ID:            fmt.Sprintf("sync-%d-%d", os.Getpid(), time.Now().UnixNano()),
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
	return &durableSyncConflictOperation{store: store, git: recoveryGit, operation: operation}, nil
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
	if operation == nil {
		return branchSyncResult(syncOpts.DefaultBranch, taskTarget.Branch, target.task.ID, branchSync)
	}

	needsPush, err := nonConflictSyncNeedsPush(ctx, syncOpts, branchSync, operation)
	if err != nil {
		return SyncResult{}, fmt.Errorf("inspect already-current sync branch for task %s: %w", target.task.ID, err)
	}
	if needsPush {
		if err := s.pushNonConflictSync(ctx, target, gitState, syncOpts, branchSync, operation); err != nil {
			return SyncResult{}, err
		}
	}
	if err := clearSyncConflictOperation(target.source.Repository.ID, target.task.ID, "non-conflict recovery checkpoint", operation); err != nil {
		return SyncResult{}, err
	}
	return branchSyncResult(syncOpts.DefaultBranch, taskTarget.Branch, target.task.ID, branchSync)
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
	completionGit, ok := gitState.(SyncConflictCompletionGit)
	if !ok {
		return fmt.Errorf("record non-conflict sync completion for task %s: Git adapter cannot push through durable boundary", target.task.ID)
	}
	repoID, taskID := target.source.Repository.ID, target.task.ID
	if err := operation.update(repoID, taskID, func(active *taskstate.SyncConflictOperation) {
		active.Phase = taskstate.SyncConflictPhaseLocalCompleted
		active.LocalHead = branchSync.Head
	}); err != nil {
		return err
	}
	if err := operation.update(repoID, taskID, func(active *taskstate.SyncConflictOperation) {
		active.Phase = taskstate.SyncConflictPhasePushIntent
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

func recordSyncConflictFiles(
	repoID string,
	taskID string,
	conflictFiles []string,
	operation *durableSyncConflictOperation,
) error {
	if operation == nil {
		return nil
	}
	if err := operation.update(repoID, taskID, func(active *taskstate.SyncConflictOperation) {
		active.Phase = taskstate.SyncConflictPhaseConflicted
		active.ConflictFiles = append([]string{}, conflictFiles...)
	}); err != nil {
		return fmt.Errorf("record conflict files for task %s: %w", taskID, err)
	}
	return nil
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
		Repository:    target.source.Repository,
		Task:          target.task.Clone(),
		Branch:        taskTarget.Branch,
		Worktree:      taskTarget.Worktree,
		DefaultBranch: destination,
		PRURL:         prURL,
		ConflictFiles: append([]string{}, conflictFiles...),
	}
	if operation == nil {
		return opts
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
	gitState SyncGit,
	syncOpts gitmeta.TaskBranchSyncOptions,
	conflictFiles []string,
	operation *durableSyncConflictOperation,
) (gitmeta.TaskBranchSyncResult, error) {
	if operation == nil {
		return gitState.CompleteTaskBranchConflictResolution(ctx, syncOpts, conflictFiles)
	}
	completionGit, ok := gitState.(SyncConflictCompletionGit)
	if !ok {
		return gitState.CompleteTaskBranchConflictResolution(ctx, syncOpts, conflictFiles)
	}
	return completeDurableSyncConflict(ctx, target, completionGit, syncOpts, conflictFiles, operation)
}

func completeDurableSyncConflict(
	ctx context.Context,
	target syncTarget,
	completionGit SyncConflictCompletionGit,
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
	if err := operation.update(repoID, taskID, func(active *taskstate.SyncConflictOperation) {
		active.Phase = taskstate.SyncConflictPhasePushIntent
	}); err != nil {
		return gitmeta.TaskBranchSyncResult{}, syncConflictDirectError{err: fmt.Errorf("record conflict push intent for task %s: %w", taskID, err)}
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

func (s SyncService) recoverSyncConflictResolverFailure(
	ctx context.Context,
	target syncTarget,
	gitState SyncGit,
	operation *durableSyncConflictOperation,
	cause error,
) error {
	if operation == nil {
		return cause
	}
	return errors.Join(cause, s.recoverObservedSyncConflictFailure(ctx, target, gitState, &operation.operation))
}

func (s SyncService) handleSyncConflictCompletionFailure(
	ctx context.Context,
	target syncTarget,
	gitState SyncGit,
	auditOpts taskstate.SyncConflictResolutionEventOptions,
	operation *durableSyncConflictOperation,
	cause error,
) error {
	if recordErr := s.recordSyncConflictResolutionFailure(target.source.Repository.ID, target.task.ID, auditOpts, cause); recordErr != nil {
		cause = errors.Join(cause, recordErr)
	}
	if operation != nil {
		cause = errors.Join(cause, s.recoverObservedSyncConflictFailure(ctx, target, gitState, &operation.operation))
	}
	return fmt.Errorf("complete resolved merge for task %s: %w", target.task.ID, cause)
}

func (s SyncService) handleUnexpectedSyncConflictStatus(
	target syncTarget,
	taskTarget tasktarget.Target,
	destination string,
	auditOpts taskstate.SyncConflictResolutionEventOptions,
	completed gitmeta.TaskBranchSyncResult,
) (SyncResult, error) {
	result, err := branchSyncResult(destination, taskTarget.Branch, target.task.ID, completed)
	if err != nil {
		if recordErr := s.recordSyncConflictResolutionFailure(target.source.Repository.ID, target.task.ID, auditOpts, err); recordErr != nil {
			err = errors.Join(err, recordErr)
		}
		return SyncResult{}, err
	}
	statusErr := fmt.Errorf("conflict resolution completed with branch sync status %q", completed.Status)
	if recordErr := s.recordSyncConflictResolutionFailure(target.source.Repository.ID, target.task.ID, auditOpts, statusErr); recordErr != nil {
		statusErr = errors.Join(statusErr, recordErr)
	}
	return result, statusErr
}

func (s SyncService) finishSyncConflictAudit(
	target syncTarget,
	auditOpts taskstate.SyncConflictResolutionEventOptions,
	commit string,
) error {
	auditOpts.Commit = strings.TrimSpace(commit)
	if _, err := s.RunStore.RecordSyncConflictResolutionFinished(target.source.Repository.ID, target.task.ID, auditOpts); err != nil {
		return fmt.Errorf("record conflict resolution finish for task %s: %w", target.task.ID, err)
	}
	return nil
}

func clearSyncConflictOperation(
	repoID string,
	taskID string,
	label string,
	operation *durableSyncConflictOperation,
) error {
	if operation == nil {
		return nil
	}
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
) (taskstate.SyncConflictResolutionEventOptions, error) {
	prepared, err := s.ConflictResolver.PrepareSyncConflictResolution(ctx, conflictOpts)
	if err != nil {
		return taskstate.SyncConflictResolutionEventOptions{}, fmt.Errorf(
			"prepare merge conflict agent for task %s: %w",
			taskID,
			err,
		)
	}
	prepared.Execution.SupervisorPID = os.Getpid()
	auditOpts := syncConflictResolutionEventOptions(conflictOpts, prepared.Execution, "")
	if err := recordSyncConflictResolverExecution(repoID, taskID, prepared.Execution, operation); err != nil {
		return taskstate.SyncConflictResolutionEventOptions{}, err
	}
	startedEvent, err := s.RunStore.RecordSyncConflictResolutionStarted(repoID, taskID, auditOpts)
	if err != nil {
		return taskstate.SyncConflictResolutionEventOptions{}, fmt.Errorf(
			"record conflict resolution start for task %s: %w",
			taskID,
			err,
		)
	}
	if startedEvent.Execution != nil {
		auditOpts.Execution = *startedEvent.Execution
	}

	err = prepared.Resolve(ctx)
	auditOpts.Usage = syncConflictResolverUsageOptions(prepared, auditOpts.Execution, err)
	if err != nil {
		if recordErr := s.recordSyncConflictResolutionFailure(repoID, taskID, auditOpts, err); recordErr != nil {
			err = errors.Join(err, recordErr)
		}
		return taskstate.SyncConflictResolutionEventOptions{}, fmt.Errorf(
			"resolve merge conflicts for task %s with agent: %w",
			taskID,
			err,
		)
	}
	return auditOpts, nil
}

func recordSyncConflictResolverExecution(
	repoID string,
	taskID string,
	execution taskstate.AgentExecution,
	operation *durableSyncConflictOperation,
) error {
	if operation == nil {
		return nil
	}
	if execution.StartedAt.IsZero() {
		execution.StartedAt = time.Now().UTC()
	}
	if err := operation.update(repoID, taskID, func(active *taskstate.SyncConflictOperation) {
		active.Phase = taskstate.SyncConflictPhaseResolving
		active.Execution = &execution
	}); err != nil {
		return fmt.Errorf("record conflict resolver execution for task %s: %w", taskID, err)
	}
	return nil
}

func syncConflictResolverUsageOptions(
	prepared PreparedSyncConflictResolution,
	execution taskstate.AgentExecution,
	runErr error,
) taskstate.RecordRunUsageOptions {
	if prepared.CaptureUsage == nil {
		return taskstate.RecordRunUsageOptions{}
	}
	return prepared.CaptureUsage(execution, runErr)
}

func (s SyncService) recordSyncConflictResolutionFailure(
	repoID,
	taskID string,
	opts taskstate.SyncConflictResolutionEventOptions,
	cause error,
) error {
	if _, err := s.RunStore.RecordSyncConflictResolutionFailed(repoID, taskID, opts, cause); err != nil {
		return fmt.Errorf("record conflict resolution failure for task %s: %w", taskID, err)
	}
	return nil
}

func syncConflictResolutionEventOptions(
	opts SyncConflictResolutionOptions,
	execution taskstate.AgentExecution,
	commit string,
) taskstate.SyncConflictResolutionEventOptions {
	return taskstate.SyncConflictResolutionEventOptions{
		Execution:     execution,
		Branch:        opts.Branch,
		DefaultBranch: opts.DefaultBranch,
		Worktree:      opts.Worktree,
		PRURL:         opts.PRURL,
		ConflictFiles: append([]string{}, opts.ConflictFiles...),
		Commit:        commit,
	}
}
