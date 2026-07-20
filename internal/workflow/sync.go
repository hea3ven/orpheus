package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
)

const syncLockOperation = "task sync"

// SyncBackendFactory creates a sync-capable backend for one repository.
type SyncBackendFactory func(task.RepositorySource) (task.SyncBackend, error)

// SyncScanBackendFactory creates a read backend for batch sync candidate scanning.
type SyncScanBackendFactory func(task.RepositorySource) (task.ReadBackend, error)

// SyncRunStore records local audit events produced by sync reconciliation.
type SyncRunStore interface {
	RecordTaskClosed(repoID, taskID string, opts taskstate.TaskClosedOptions) (taskstate.Event, error)
	RecordSyncConflictResolutionStarted(
		repoID,
		taskID string,
		opts taskstate.SyncConflictResolutionEventOptions,
	) (taskstate.Event, error)
	RecordSyncConflictResolutionFinished(
		repoID,
		taskID string,
		opts taskstate.SyncConflictResolutionEventOptions,
	) (taskstate.Event, error)
	RecordSyncConflictResolutionFailed(
		repoID,
		taskID string,
		opts taskstate.SyncConflictResolutionEventOptions,
		cause error,
	) (taskstate.Event, error)
}

// SyncGit performs the Git operations used by task sync branch updates.
type SyncGit interface {
	SyncTaskBranchWithDefault(
		ctx context.Context,
		opts gitmeta.TaskBranchSyncOptions,
	) (gitmeta.TaskBranchSyncResult, error)
	BeginTaskBranchConflictResolution(
		ctx context.Context,
		opts gitmeta.TaskBranchSyncOptions,
	) (gitmeta.TaskBranchSyncResult, error)
	CompleteTaskBranchConflictResolution(
		ctx context.Context,
		opts gitmeta.TaskBranchSyncOptions,
		conflictFiles []string,
	) (gitmeta.TaskBranchSyncResult, error)
}

// LocalSyncGit delegates sync Git operations to the local git binary.
type LocalSyncGit struct{}

// SyncTaskBranchWithDefault merges origin/default into a task branch and pushes it.
func (LocalSyncGit) SyncTaskBranchWithDefault(
	ctx context.Context,
	opts gitmeta.TaskBranchSyncOptions,
) (gitmeta.TaskBranchSyncResult, error) {
	return gitmeta.SyncTaskBranchWithDefault(ctx, opts)
}

// BeginTaskBranchConflictResolution leaves merge conflicts for a resolver.
func (LocalSyncGit) BeginTaskBranchConflictResolution(
	ctx context.Context,
	opts gitmeta.TaskBranchSyncOptions,
) (gitmeta.TaskBranchSyncResult, error) {
	return gitmeta.BeginTaskBranchConflictResolution(ctx, opts)
}

// CompleteTaskBranchConflictResolution commits and pushes a resolved merge.
func (LocalSyncGit) CompleteTaskBranchConflictResolution(
	ctx context.Context,
	opts gitmeta.TaskBranchSyncOptions,
	conflictFiles []string,
) (gitmeta.TaskBranchSyncResult, error) {
	return gitmeta.CompleteTaskBranchConflictResolution(ctx, opts, conflictFiles)
}

// SyncConflictResolver resolves merge conflicts left in a PR branch worktree.
type SyncConflictResolver interface {
	PrepareSyncConflictResolution(
		ctx context.Context,
		opts SyncConflictResolutionOptions,
	) (PreparedSyncConflictResolution, error)
}

// PreparedSyncConflictResolution describes a selected conflict-repair agent launch.
type PreparedSyncConflictResolution struct {
	Execution    taskstate.AgentExecution
	Resolve      func(context.Context) error
	CaptureUsage func(taskstate.AgentExecution, error) taskstate.RecordRunUsageOptions
}

// SyncConflictResolutionOptions describes one conflicted open-PR branch repair.
type SyncConflictResolutionOptions struct {
	Repository    task.Repository
	Task          task.Task
	Branch        string
	Worktree      string
	DefaultBranch string
	PRURL         string
	ConflictFiles []string
}

// SyncService reconciles backend task state from recorded pull request state.
type SyncService struct {
	Paths            state.Paths
	Sources          []task.RepositorySource
	BackendFactory   SyncBackendFactory
	ScanFactory      SyncScanBackendFactory
	RunStore         SyncRunStore
	Git              SyncGit
	ConflictResolver SyncConflictResolver
	PRProvider       pullrequest.Provider
	Logger           *slog.Logger
}

// SyncOptions are the CLI-provided sync controls.
type SyncOptions struct {
	TaskID string
}

// SyncStatus describes the outcome of a single-task sync.
type SyncStatus string

const (
	// SyncStatusAlreadyInReview means the task's recorded PR is still open.
	SyncStatusAlreadyInReview SyncStatus = "already_in_review"

	// SyncStatusBranchUpdated means an open PR branch was updated from the default branch.
	SyncStatusBranchUpdated SyncStatus = "branch_updated"

	// SyncStatusPRMerged means the task's recorded PR is merged.
	SyncStatusPRMerged SyncStatus = "pr_merged"

	// SyncStatusSkipped means the task was resolvable but had no PR state to reconcile.
	SyncStatusSkipped SyncStatus = "skipped"
)

// SyncResult reports the resolved task and sync outcome.
type SyncResult struct {
	Repository task.Repository
	Task       task.Task
	LatestRun  taskstate.RunAttempt
	Status     SyncStatus
	Reason     string
	Branch     string
	Worktree   string
	PRURL      string
}

// SyncAllFailure is a per-repository or per-task batch sync failure.
type SyncAllFailure struct {
	Repository task.Repository
	TaskID     string
	Operation  string
	Err        error
}

// SyncAllResult reports grouped outcomes from a best-effort batch sync.
type SyncAllResult struct {
	Results  []SyncResult
	Failures []SyncAllFailure
}

// HasFailures reports whether any repository or candidate failed.
func (r SyncAllResult) HasFailures() bool {
	return len(r.Failures) > 0
}

type syncTarget struct {
	source  task.RepositorySource
	backend task.SyncBackend
	task    task.Task
}

type syncDiagnosticTarget struct {
	repoID string
	taskID string
	branch string
	hasPR  bool
}

func (t *syncDiagnosticTarget) recordTarget(target syncTarget) {
	if t == nil {
		return
	}
	t.repoID = target.source.Repository.ID
	t.taskID = target.task.ID
	metadata := target.task.OrpheusMetadata()
	if metadata.HasBranch {
		t.branch = strings.TrimSpace(metadata.Branch)
	}
	t.hasPR = metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != ""
}

type syncAllCandidate struct {
	source task.RepositorySource
	taskID string
}

// Sync resolves one task, skips non-eligible states, and pushes eligible task branches.
func (s SyncService) Sync(ctx context.Context, opts SyncOptions) (SyncResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = gitmeta.ContextWithLogger(ctx, s.Logger)
	span := logging.Start(ctx, s.Logger, "task sync workflow",
		slog.String("component", "workflow"),
		slog.String("operation", "task_sync"),
		slog.String("task_id", opts.TaskID),
	)
	var result SyncResult
	var finalErr error
	diagnosticTarget := syncDiagnosticTarget{taskID: strings.TrimSpace(opts.TaskID)}
	defer func() {
		span.FinishError(ctx, finalErr, syncFinishAttrs(result, diagnosticTarget)...)
	}()

	if err := s.validate(); err != nil {
		finalErr = err
		return SyncResult{}, err
	}
	gitState := s.Git
	if gitState == nil {
		gitState = LocalSyncGit{}
	}

	finalErr = state.WithGlobalMutationLockLogger(ctx, s.Paths, syncLockOperation, s.Logger, func() error {
		synced, err := s.syncLocked(ctx, opts, gitState, &diagnosticTarget)
		if err != nil {
			return err
		}
		result = synced
		return nil
	})
	if finalErr != nil {
		return SyncResult{}, finalErr
	}
	return result, nil
}

func syncFinishAttrs(result SyncResult, target syncDiagnosticTarget) []slog.Attr {
	repoID := result.Repository.ID
	if repoID == "" {
		repoID = target.repoID
	}
	taskID := result.Task.ID
	if taskID == "" {
		taskID = target.taskID
	}
	branch := result.Branch
	if branch == "" {
		branch = target.branch
	}
	hasPR := result.PRURL != "" || target.hasPR

	attrs := make([]slog.Attr, 0, 5)
	if repoID != "" {
		attrs = append(attrs, slog.String("repo_id", repoID))
	}
	if taskID != "" {
		attrs = append(attrs, slog.String("task_id", taskID))
	}
	if result.Status != SyncStatus("") {
		attrs = append(attrs, slog.String("sync_status", string(result.Status)))
	}
	if branch != "" {
		attrs = append(attrs, slog.String("branch", branch))
	}
	if hasPR {
		attrs = append(attrs, slog.Bool("has_pr", true))
	}
	return attrs
}

// SyncAll scans all registered repositories and syncs tasks already at a PR boundary.
func (s SyncService) SyncAll(ctx context.Context) (SyncAllResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = gitmeta.ContextWithLogger(ctx, s.Logger)
	span := logging.Start(ctx, s.Logger, "task sync all workflow",
		slog.String("component", "workflow"),
		slog.String("operation", "task_sync_all"),
	)
	var result SyncAllResult
	var finalErr error
	defer func() {
		span.FinishError(ctx, finalErr,
			slog.Int("result_count", len(result.Results)),
			slog.Int("failure_count", len(result.Failures)),
		)
	}()

	if err := s.validate(); err != nil {
		finalErr = err
		return SyncAllResult{}, err
	}
	gitState := s.Git
	if gitState == nil {
		gitState = LocalSyncGit{}
	}

	finalErr = state.WithGlobalMutationLockLogger(ctx, s.Paths, syncLockOperation, s.Logger, func() error {
		candidates, failures := s.scanSyncAllCandidates(ctx)
		result.Failures = append(result.Failures, failures...)
		for _, failure := range failures {
			s.logSyncAllOutcome(ctx, failure, SyncResult{})
		}

		for _, candidate := range candidates {
			synced, err := s.syncLocked(ctx, SyncOptions{TaskID: candidate.taskID}, gitState, nil)
			if err != nil {
				failure := SyncAllFailure{
					Repository: candidate.source.Repository,
					TaskID:     candidate.taskID,
					Operation:  "sync",
					Err:        err,
				}
				result.Failures = append(result.Failures, failure)
				s.logSyncAllOutcome(ctx, failure, SyncResult{})
				continue
			}
			result.Results = append(result.Results, synced)
			s.logSyncAllOutcome(ctx, SyncAllFailure{}, synced)
		}
		return nil
	})
	if finalErr != nil {
		return SyncAllResult{}, finalErr
	}
	return result, nil
}

func (s SyncService) logSyncAllOutcome(ctx context.Context, failure SyncAllFailure, result SyncResult) {
	if s.Logger == nil || !s.Logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	attrs := []slog.Attr{slog.String("component", "workflow"), slog.String("operation", "task_sync_all_item")}
	status := logging.StatusSuccess
	if failure.Err != nil {
		status = logging.StatusFailure
		attrs = append(attrs,
			slog.String("repo_id", failure.Repository.ID),
			slog.String("task_id", failure.TaskID),
			slog.String("item_operation", failure.Operation),
		)
	} else {
		attrs = append(attrs,
			slog.String("repo_id", result.Repository.ID),
			slog.String("task_id", result.Task.ID),
			slog.String("sync_status", string(result.Status)),
			slog.String("branch", result.Branch),
		)
	}
	s.Logger.LogAttrs(ctx, slog.LevelDebug, "task sync all item finished", append(attrs, slog.String("status", status))...)
}

func (s SyncService) validate() error {
	if s.BackendFactory == nil {
		return errors.New("task sync backend factory is required")
	}
	if s.RunStore == nil {
		return errors.New("task sync run store is required")
	}
	if s.PRProvider == nil {
		return errors.New("task sync PR provider is required")
	}
	return nil
}

func (s SyncService) scanSyncAllCandidates(ctx context.Context) ([]syncAllCandidate, []SyncAllFailure) {
	candidates := make([]syncAllCandidate, 0)
	failures := make([]SyncAllFailure, 0)
	for _, source := range s.Sources {
		backend, err := s.syncScanBackend(source)
		if err != nil {
			failures = append(failures, SyncAllFailure{
				Repository: source.Repository,
				Operation:  "create_scan_backend",
				Err:        err,
			})
			continue
		}

		tasks, err := backend.List(ctx)
		if err != nil {
			failures = append(failures, SyncAllFailure{
				Repository: source.Repository,
				Operation:  "scan_tasks",
				Err:        err,
			})
			continue
		}

		repoCandidates, repoFailures := s.syncAllCandidatesForTasks(source, tasks)
		candidates = append(candidates, repoCandidates...)
		failures = append(failures, repoFailures...)
	}
	return candidates, failures
}

func (s SyncService) syncScanBackend(source task.RepositorySource) (task.ReadBackend, error) {
	if s.ScanFactory != nil {
		return s.ScanFactory(source)
	}

	backend, err := s.BackendFactory(source)
	if err != nil {
		return nil, err
	}
	readBackend, ok := backend.(task.ReadBackend)
	if !ok {
		return nil, errors.New("task sync scan backend must support list")
	}
	return readBackend, nil
}

func (s SyncService) syncAllCandidatesForTasks(
	source task.RepositorySource,
	tasks []task.Task,
) ([]syncAllCandidate, []SyncAllFailure) {
	candidates := make([]syncAllCandidate, 0)
	failures := make([]SyncAllFailure, 0)
	for _, taskItem := range tasks {
		if !isSyncAllRunnableTask(taskItem) {
			continue
		}

		metadata := taskItem.OrpheusMetadata()
		if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
			candidates = append(candidates, syncAllCandidate{source: source, taskID: taskItem.ID})
		}
	}
	return candidates, failures
}

func isSyncAllRunnableTask(taskItem task.Task) bool {
	if strings.TrimSpace(taskItem.ID) == "" || taskItem.Status == task.StatusClosed {
		return false
	}
	return taskItem.IssueType != task.IssueTypeEpic
}

func (s SyncService) syncLocked(
	ctx context.Context,
	opts SyncOptions,
	gitState SyncGit,
	diagnosticTarget *syncDiagnosticTarget,
) (SyncResult, error) {
	target, err := s.resolveTarget(ctx, opts)
	if err != nil {
		return SyncResult{}, err
	}
	diagnosticTarget.recordTarget(target)

	if target.task.Status == task.StatusClosed {
		return s.skip(target, taskstate.RunAttempt{}, "task is closed"), nil
	}

	if result, ok, err := s.pollExistingPR(ctx, target, gitState); ok || err != nil {
		if err != nil {
			return SyncResult{}, err
		}
		return result, nil
	}

	return s.skip(target, taskstate.RunAttempt{}, task.MetadataPRURL+" is not set"), nil
}

func (s SyncService) pollExistingPR(ctx context.Context, target syncTarget, gitState SyncGit) (SyncResult, bool, error) {
	metadata := target.task.OrpheusMetadata()
	prURL := strings.TrimSpace(metadata.PRURL)
	if !metadata.HasPRURL || prURL == "" {
		return SyncResult{}, false, nil
	}

	span := logging.Start(ctx, s.Logger, "pull request poll",
		slog.String("component", "workflow"),
		slog.String("operation", "poll_pr"),
		slog.String("repo_id", target.source.Repository.ID),
		slog.String("task_id", target.task.ID),
	)
	status, err := s.PRProvider.StatusByURL(ctx, pullrequest.StatusByURLRequest{
		URL: prURL,
		Diagnostics: pullrequest.DiagnosticContext{
			RepoID: target.source.Repository.ID,
			TaskID: target.task.ID,
			Branch: strings.TrimSpace(metadata.Branch),
			HasPR:  true,
		},
	})
	if err != nil {
		span.FinishError(ctx, err)
		return SyncResult{}, true, err
	}
	span.Finish(ctx, logging.StatusSuccess, slog.String("pr_state", string(status.State)))
	observedURL := strings.TrimSpace(status.URL)
	if observedURL == "" {
		observedURL = prURL
	}

	result := SyncResult{
		Repository: target.source.Repository,
		Task:       target.task.Clone(),
		Branch:     strings.TrimSpace(metadata.Branch),
		Worktree:   strings.TrimSpace(target.task.OrpheusMetadata().Worktree),
		PRURL:      observedURL,
	}

	return s.handleExistingPRStatus(ctx, target, gitState, status, result, observedURL)
}

func (s SyncService) handleExistingPRStatus(
	ctx context.Context,
	target syncTarget,
	gitState SyncGit,
	status pullrequest.PullRequestStatus,
	result SyncResult,
	observedURL string,
) (SyncResult, bool, error) {
	switch status.State {
	case pullrequest.StateOpen:
		return s.handleOpenPR(ctx, target, gitState, result)
	case pullrequest.StateMerged:
		result.Status = SyncStatusPRMerged
		result.Reason = "PR is merged; backend task was closed"
		if err := target.backend.Close(ctx, target.task.ID); err != nil {
			return SyncResult{}, true, fmt.Errorf("close backend task %s after merged PR %s: %w", target.task.ID, observedURL, err)
		}
		if _, err := s.RunStore.RecordTaskClosed(
			target.source.Repository.ID,
			target.task.ID,
			taskstate.TaskClosedOptions{
				Reason:          taskstate.CloseReasonPRMerged,
				PRURL:           observedURL,
				ObservedPRState: string(pullrequest.StateMerged),
			},
		); err != nil {
			return SyncResult{}, true, fmt.Errorf(
				"backend task %s was closed after merged PR %s but local task-state audit event failed: %w",
				target.task.ID,
				observedURL,
				err,
			)
		}
		result.Task.Status = task.StatusClosed
		return result, true, nil
	case pullrequest.StateClosed:
		return SyncResult{}, true, fmt.Errorf("task %s PR %s is closed without merge; no backend state was changed", target.task.ID, observedURL)
	default:
		return SyncResult{}, true, fmt.Errorf("task %s PR %s has unsupported provider state %q", target.task.ID, observedURL, status.State)
	}
}

func (s SyncService) handleOpenPR(
	ctx context.Context,
	target syncTarget,
	gitState SyncGit,
	result SyncResult,
) (SyncResult, bool, error) {
	result.Status = SyncStatusAlreadyInReview
	result.Reason = "PR is still open for review"
	updated, err := s.syncOpenPRBranch(ctx, target, gitState)
	if err != nil {
		return SyncResult{}, true, err
	}
	if updated.Status == SyncStatus("") {
		result.Reason = "branch update skipped: " + updated.Reason
		return result, true, nil
	}
	result.Status = updated.Status
	result.Reason = updated.Reason
	return result, true, nil
}

func (s SyncService) syncOpenPRBranch(ctx context.Context, target syncTarget, gitState SyncGit) (SyncResult, error) {
	repo := target.source.Repository
	targets, err := tasktarget.ExpectedTargetsForTask(repo, target.task.ID, s.Paths)
	if err != nil {
		return SyncResult{}, fmt.Errorf("resolve sync targets for task %s: %w", target.task.ID, err)
	}
	taskTarget, err := tasktarget.ClassifyMetadataTarget(target.task.OrpheusMetadata(), targets)
	if err != nil {
		return SyncResult{
			Reason: fmt.Sprintf("task metadata target is incomplete or unsupported: %v", err),
		}, nil
	}
	if !isFeatureBranchTarget(taskTarget.Kind) {
		return SyncResult{
			Reason: fmt.Sprintf("task target %s is not an Orpheus-managed PR branch", taskTarget.Kind.DisplayName()),
		}, nil
	}

	span := logging.Start(ctx, s.Logger, "sync task branch",
		slog.String("component", "workflow"),
		slog.String("operation", "sync_task_branch"),
		slog.String("repo_id", repo.ID),
		slog.String("task_id", target.task.ID),
		slog.String("branch", taskTarget.Branch),
		slog.String("cwd", taskTarget.Worktree),
	)
	branchSync, err := gitState.SyncTaskBranchWithDefault(ctx, gitmeta.TaskBranchSyncOptions{
		RepoPath:      repo.Path,
		DefaultBranch: repo.DefaultBranch,
		Branch:        taskTarget.Branch,
		Worktree:      taskTarget.Worktree,
	})
	if err != nil {
		if errors.Is(err, gitmeta.ErrMergeConflict) {
			span.Finish(ctx, "merge_conflict")
			return s.resolveOpenPRBranchConflict(ctx, target, gitState, taskTarget, prURLFromTask(target.task))
		}
		wrapped := fmt.Errorf("update open PR branch for task %s: %w", target.task.ID, err)
		span.FinishError(ctx, wrapped)
		return SyncResult{}, wrapped
	}
	span.Finish(ctx, logging.StatusSuccess, slog.String("sync_status", string(branchSync.Status)))

	return branchSyncResult(repo.DefaultBranch, taskTarget.Branch, target.task.ID, branchSync)
}

//nolint:funlen // The conflict-repair orchestration is clearer kept as one ordered workflow.
func (s SyncService) resolveOpenPRBranchConflict(
	ctx context.Context,
	target syncTarget,
	gitState SyncGit,
	taskTarget tasktarget.Target,
	prURL string,
) (SyncResult, error) {
	if s.ConflictResolver == nil {
		return SyncResult{}, fmt.Errorf(
			"update open PR branch for task %s: merge conflicts require a configured conflict resolver",
			target.task.ID,
		)
	}

	repo := target.source.Repository
	syncOpts := gitmeta.TaskBranchSyncOptions{
		RepoPath:      repo.Path,
		DefaultBranch: repo.DefaultBranch,
		Branch:        taskTarget.Branch,
		Worktree:      taskTarget.Worktree,
	}
	branchSync, err := gitState.BeginTaskBranchConflictResolution(ctx, syncOpts)
	if err != nil {
		return SyncResult{}, fmt.Errorf("prepare conflict resolution for task %s: %w", target.task.ID, err)
	}
	if branchSync.Status != gitmeta.TaskBranchSyncConflicted {
		return branchSyncResult(repo.DefaultBranch, taskTarget.Branch, target.task.ID, branchSync)
	}

	conflictOpts := SyncConflictResolutionOptions{
		Repository:    repo,
		Task:          target.task.Clone(),
		Branch:        taskTarget.Branch,
		Worktree:      taskTarget.Worktree,
		DefaultBranch: repo.DefaultBranch,
		PRURL:         prURL,
		ConflictFiles: append([]string{}, branchSync.ConflictFiles...),
	}
	auditOpts, err := s.runSyncConflictResolver(ctx, repo.ID, target.task.ID, conflictOpts)
	if err != nil {
		return SyncResult{}, err
	}

	completed, err := gitState.CompleteTaskBranchConflictResolution(ctx, syncOpts, branchSync.ConflictFiles)
	if err != nil {
		if recordErr := s.recordSyncConflictResolutionFailure(repo.ID, target.task.ID, auditOpts, err); recordErr != nil {
			err = errors.Join(err, recordErr)
		}
		return SyncResult{}, fmt.Errorf("complete resolved merge for task %s: %w", target.task.ID, err)
	}
	if completed.Status != gitmeta.TaskBranchSyncUpdated {
		result, resultErr := branchSyncResult(repo.DefaultBranch, taskTarget.Branch, target.task.ID, completed)
		if resultErr != nil {
			if recordErr := s.recordSyncConflictResolutionFailure(repo.ID, target.task.ID, auditOpts, resultErr); recordErr != nil {
				resultErr = errors.Join(resultErr, recordErr)
			}
			return SyncResult{}, resultErr
		}
		statusErr := fmt.Errorf("conflict resolution completed with branch sync status %q", completed.Status)
		if recordErr := s.recordSyncConflictResolutionFailure(repo.ID, target.task.ID, auditOpts, statusErr); recordErr != nil {
			statusErr = errors.Join(statusErr, recordErr)
		}
		return result, statusErr
	}
	finishedAuditOpts := auditOpts
	finishedAuditOpts.Commit = strings.TrimSpace(completed.Head)
	if _, err := s.RunStore.RecordSyncConflictResolutionFinished(repo.ID, target.task.ID, finishedAuditOpts); err != nil {
		return SyncResult{}, fmt.Errorf("record conflict resolution finish for task %s: %w", target.task.ID, err)
	}
	return SyncResult{
		Status: SyncStatusBranchUpdated,
		Reason: fmt.Sprintf(
			"resolved merge conflicts with the configured agent, merged %s into %s, and pushed the branch",
			repo.DefaultBranch,
			taskTarget.Branch,
		),
	}, nil
}

func (s SyncService) runSyncConflictResolver(
	ctx context.Context,
	repoID string,
	taskID string,
	conflictOpts SyncConflictResolutionOptions,
) (taskstate.SyncConflictResolutionEventOptions, error) {
	prepared, err := s.ConflictResolver.PrepareSyncConflictResolution(ctx, conflictOpts)
	if err != nil {
		return taskstate.SyncConflictResolutionEventOptions{}, fmt.Errorf(
			"prepare merge conflict agent for task %s: %w",
			taskID,
			err,
		)
	}
	auditOpts := syncConflictResolutionEventOptions(conflictOpts, prepared.Execution, "")
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

func branchSyncResult(
	defaultBranch string,
	branch string,
	taskID string,
	branchSync gitmeta.TaskBranchSyncResult,
) (SyncResult, error) {
	switch branchSync.Status {
	case gitmeta.TaskBranchSyncAlreadyCurrent:
		return SyncResult{
			Status: SyncStatusAlreadyInReview,
			Reason: fmt.Sprintf(
				"branch %s already includes %s",
				branch,
				defaultBranch,
			),
		}, nil
	case gitmeta.TaskBranchSyncPushed:
		return SyncResult{
			Status: SyncStatusBranchUpdated,
			Reason: fmt.Sprintf(
				"pushed %s because the local branch already includes %s but origin was behind",
				branch,
				defaultBranch,
			),
		}, nil
	case gitmeta.TaskBranchSyncUpdated:
		return SyncResult{
			Status: SyncStatusBranchUpdated,
			Reason: fmt.Sprintf(
				"merged %s into %s and pushed the branch",
				defaultBranch,
				branch,
			),
		}, nil
	default:
		return SyncResult{}, fmt.Errorf("update open PR branch for task %s: unsupported branch sync status %q", taskID, branchSync.Status)
	}
}

func prURLFromTask(taskItem task.Task) string {
	metadata := taskItem.OrpheusMetadata()
	if !metadata.HasPRURL {
		return ""
	}
	return strings.TrimSpace(metadata.PRURL)
}

func (s SyncService) resolveTarget(ctx context.Context, opts SyncOptions) (syncTarget, error) {
	resolved, err := task.ResolveTaskSource(s.Sources, opts.TaskID)
	if err != nil {
		return syncTarget{}, err
	}
	backend, err := s.BackendFactory(resolved.Source)
	if err != nil {
		return syncTarget{}, fmt.Errorf(
			"task sync %s: create backend for repo %s (%s; prefix %s): %w",
			resolved.TaskID,
			resolved.Source.Repository.ID,
			resolved.Source.Repository.Name,
			resolved.Source.Repository.TaskIDPrefix,
			err,
		)
	}
	taskItem, err := backend.Get(ctx, resolved.TaskID)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			return syncTarget{}, fmt.Errorf(
				"task sync %s: task was not found in repo %s (%s; prefix %s): %w",
				resolved.TaskID,
				resolved.Source.Repository.ID,
				resolved.Source.Repository.Name,
				resolved.Source.Repository.TaskIDPrefix,
				err,
			)
		}
		return syncTarget{}, fmt.Errorf(
			"task sync %s: query repo %s (%s; prefix %s): %w",
			resolved.TaskID,
			resolved.Source.Repository.ID,
			resolved.Source.Repository.Name,
			resolved.Source.Repository.TaskIDPrefix,
			err,
		)
	}
	return syncTarget{source: resolved.Source, backend: backend, task: taskItem}, nil
}

func (s SyncService) skip(target syncTarget, latest taskstate.RunAttempt, reason string) SyncResult {
	metadata := target.task.OrpheusMetadata()
	return SyncResult{
		Repository: target.source.Repository,
		Task:       target.task.Clone(),
		LatestRun:  latest,
		Status:     SyncStatusSkipped,
		Reason:     reason,
		Branch:     strings.TrimSpace(metadata.Branch),
		Worktree:   strings.TrimSpace(metadata.Worktree),
	}
}

// PullRequestContent is the generated title/body for a pull request.
type PullRequestContent struct {
	Title string
	Body  string
}

// PublicationOptions controls generated pull-request publication content.
type PublicationOptions struct {
	TitleTemplate        string
	IncludeReviewProcess bool
}

// ResolvePublicationOptions applies global and repository publication policy.
func ResolvePublicationOptions(paths state.Paths, repo task.Repository) (PublicationOptions, error) {
	reviewConfig, err := review.LoadConfig(paths)
	if err != nil {
		return PublicationOptions{}, err
	}
	includeReviewProcess := reviewConfig.IncludePRReviewProcess
	if repo.IncludePRReviewProcess != nil {
		includeReviewProcess = *repo.IncludePRReviewProcess
	}
	return PublicationOptions{
		TitleTemplate:        repo.TitleTemplate,
		IncludeReviewProcess: includeReviewProcess,
	}, nil
}

// BuildSyncPullRequestContent returns default PR text from the completion handoff.
func BuildSyncPullRequestContent(taskItem task.Task, latest taskstate.RunAttempt) (PullRequestContent, error) {
	return BuildPublicationPullRequestContent("", taskItem, latest)
}

// BuildPublicationPullRequestContent returns PR text using an optional title template.
func BuildPublicationPullRequestContent(titleTemplate string, taskItem task.Task, latest taskstate.RunAttempt) (PullRequestContent, error) {
	if strings.TrimSpace(taskItem.ID) == "" {
		return PullRequestContent{}, errors.New("task id is required")
	}
	if latest.Completion == nil {
		return PullRequestContent{}, errors.New("completion is required")
	}
	renderedTitle, err := publication.RenderTitle(titleTemplate, latest.Completion.Summary, taskItem.ExternalRef)
	if err != nil {
		return PullRequestContent{}, err
	}
	title := singleLine(renderedTitle)
	if title == "" {
		return PullRequestContent{}, errors.New("completion summary is required")
	}
	body := latest.Completion.DetailedDescription
	if strings.TrimSpace(body) == "" {
		return PullRequestContent{}, errors.New("completion detailed description is required")
	}
	return PullRequestContent{
		Title: title,
		Body:  body,
	}, nil
}

// BuildPublicationPullRequestContentFromState returns PR text from the
// canonical implementation completion plus any recorded review process.
func BuildPublicationPullRequestContentFromState(
	titleTemplate string,
	taskItem task.Task,
	state taskstate.TaskState,
) (PullRequestContent, error) {
	return BuildPublicationPullRequestContentFromStateWithOptions(PublicationOptions{
		TitleTemplate:        titleTemplate,
		IncludeReviewProcess: true,
	}, taskItem, state)
}

// BuildPublicationPullRequestContentFromStateWithOptions returns PR text from
// the canonical implementation completion and optional review-process history.
func BuildPublicationPullRequestContentFromStateWithOptions(
	options PublicationOptions,
	taskItem task.Task,
	state taskstate.TaskState,
) (PullRequestContent, error) {
	run, err := publicationRun(state)
	if err != nil {
		return PullRequestContent{}, err
	}
	content, err := BuildPublicationPullRequestContent(options.TitleTemplate, taskItem, run)
	if err != nil {
		return PullRequestContent{}, err
	}
	if options.IncludeReviewProcess {
		content.Body = appendReviewProcess(content.Body, state)
	}
	return content, nil
}

func publicationRun(state taskstate.TaskState) (taskstate.RunAttempt, error) {
	var selected taskstate.RunAttempt
	for _, run := range state.Runs {
		if run.Completion == nil || run.ReviewFollowUp != nil {
			continue
		}
		if selected.Attempt == 0 || run.Attempt > selected.Attempt {
			selected = run
		}
	}
	if selected.Attempt == 0 {
		return taskstate.RunAttempt{}, errors.New("original implementation completion is required")
	}
	return selected, nil
}

func appendReviewProcess(body string, state taskstate.TaskState) string {
	if len(state.Reviews) == 0 {
		return body
	}

	var builder strings.Builder
	builder.WriteString(strings.TrimRight(body, "\n"))
	builder.WriteString("\n\n## Review process\n")
	for _, review := range state.Reviews {
		appendReviewAttempt(&builder, review, state.Runs)
	}
	return builder.String()
}

func appendReviewAttempt(builder *strings.Builder, review taskstate.ReviewAttempt, runs []taskstate.RunAttempt) {
	builder.WriteString("\n### Review attempt ")
	builder.WriteString(strconv.Itoa(review.Attempt))
	builder.WriteString(" — ")
	builder.WriteString(reviewProcessStatus(review.Status))
	builder.WriteString("\n\n")

	for _, stepName := range reviewStepNames(review) {
		builder.WriteString("- ")
		builder.WriteString(reviewStepIcon(review, stepName))
		builder.WriteString(" `")
		builder.WriteString(stepName)
		builder.WriteString("`\n")
		for _, finding := range review.Findings {
			if findingStepName(finding, review.Step) == stepName {
				appendReviewFinding(builder, finding)
			}
		}
	}
	appendFixRuns(builder, review, runs)
}

func reviewProcessStatus(status taskstate.ReviewStatus) string {
	statusText := strings.TrimSpace(string(status))
	if statusText == "" {
		return "unknown"
	}
	return statusText
}

func reviewStepNames(review taskstate.ReviewAttempt) []string {
	names := make([]string, 0, len(review.Steps)+1)
	seen := map[string]bool{}
	for _, step := range review.Steps {
		name := strings.TrimSpace(step.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	for _, finding := range review.Findings {
		name := findingStepName(finding, review.Step)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) == 0 {
		name := strings.TrimSpace(review.Step)
		if name == "" {
			name = "review"
		}
		names = append(names, name)
	}
	return names
}

func findingStepName(finding taskstate.ReviewFinding, fallback string) string {
	name := strings.TrimSpace(finding.Step)
	if name != "" {
		return name
	}
	return strings.TrimSpace(fallback)
}

func reviewStepIcon(review taskstate.ReviewAttempt, stepName string) string {
	for _, finding := range review.Findings {
		if findingStepName(finding, review.Step) != stepName {
			continue
		}
		if taskstate.IsOpenBlockingReviewFinding(finding) {
			return "❌"
		}
	}
	if review.Status == taskstate.ReviewStatusFailed {
		return "⚠️"
	}
	return "✅"
}

func appendReviewFinding(builder *strings.Builder, finding taskstate.ReviewFinding) {
	title := singleLine(finding.Title)
	if title == "" {
		title = "Finding"
	}
	builder.WriteString("  - **")
	builder.WriteString(reviewFindingLabel(finding))
	builder.WriteString(":** ")
	builder.WriteString(title)
	builder.WriteString("\n")

	switch taskstate.ResolveReviewFinding(finding) {
	case taskstate.ReviewFindingResolutionWaived:
		builder.WriteString("    - Waived.\n")
		return
	case taskstate.ReviewFindingResolutionDowngraded:
		builder.WriteString("    - Downgraded to advisory.\n")
		return
	case taskstate.ReviewFindingResolutionTargetedByRun:
		appendBlockingFindingResolution(builder, finding)
		return
	}

	if taskstate.IsOpenBlockingReviewFinding(finding) {
		appendBlockingFindingResolution(builder, finding)
		return
	}

	if finding.Type == taskstate.FindingTypeSeparateTask {
		createdTaskID := strings.TrimSpace(finding.CreatedTaskID)
		if createdTaskID != "" {
			builder.WriteString("    - Created task: ")
			builder.WriteString(createdTaskID)
			builder.WriteString("\n")
		}
	}
}

func appendBlockingFindingResolution(builder *strings.Builder, finding taskstate.ReviewFinding) {
	switch taskstate.ResolveReviewFinding(finding) {
	case taskstate.ReviewFindingResolutionTargetedByRun:
		builder.WriteString("    - Fixed by run attempt ")
		builder.WriteString(strconv.Itoa(finding.TargetedByRunAttempt))
		builder.WriteString("\n")
		return
	case taskstate.ReviewFindingResolutionOpen:
		builder.WriteString("    - No targeted fix run recorded.\n")
	}
}

func reviewFindingLabel(finding taskstate.ReviewFinding) string {
	switch taskstate.ResolveReviewFinding(finding) {
	case taskstate.ReviewFindingResolutionWaived:
		return "Blocking (waived)"
	case taskstate.ReviewFindingResolutionDowngraded:
		return "Advisory (downgraded)"
	}

	switch finding.Type {
	case taskstate.FindingTypeBlocking:
		return "Blocking"
	case taskstate.FindingTypeAdvisory:
		return "Advisory"
	case taskstate.FindingTypeSeparateTask:
		return "Separate task"
	default:
		return "Finding"
	}
}

func appendFixRuns(builder *strings.Builder, review taskstate.ReviewAttempt, runs []taskstate.RunAttempt) {
	for _, run := range reviewFixRuns(review, runs) {
		if run.Completion == nil {
			continue
		}
		builder.WriteString("\n  **Fix run attempt ")
		builder.WriteString(strconv.Itoa(run.Attempt))
		builder.WriteString("**\n")
		builder.WriteString("  - Summary: `")
		builder.WriteString(singleLine(run.Completion.Summary))
		builder.WriteString("`\n")
		builder.WriteString("  - Description: ")
		builder.WriteString(strings.TrimSpace(run.Completion.Description))
		builder.WriteString("\n")
	}
}

func reviewFixRuns(review taskstate.ReviewAttempt, runs []taskstate.RunAttempt) []taskstate.RunAttempt {
	attempts := make([]int, 0)
	seen := map[int]bool{}
	for _, finding := range review.Findings {
		attempt := finding.TargetedByRunAttempt
		if attempt <= 0 || seen[attempt] {
			continue
		}
		seen[attempt] = true
		attempts = append(attempts, attempt)
	}

	result := make([]taskstate.RunAttempt, 0, len(attempts))
	for _, attempt := range attempts {
		for _, run := range runs {
			if run.Attempt == attempt {
				result = append(result, run)
				break
			}
		}
	}
	return result
}

func singleLine(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}
