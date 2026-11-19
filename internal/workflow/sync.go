package workflow

import (
	"context"
	"errors"
	"fmt"
	"github.com/hea3ven/orpheus/internal/workflow/externalreview"
	"github.com/hea3ven/orpheus/internal/workflow/finalization"
	"log/slog"
	"strings"

	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

const syncLockOperation = "task sync"

// SyncBackendFactory creates a sync-capable backend for one repository.
type SyncBackendFactory func(task.RepositorySource) (task.SyncBackend, error)

// SyncRunStore reads service projections of workflow history.
type SyncRunStore interface {
	Load(repoID, taskID string) (taskstate.TaskState, error)
}

// SyncService reconciles backend task state from recorded pull request state.
type SyncService struct {
	Store            phaseexec.Store
	Progress         func(Progress) error
	Paths            state.Paths
	Sources          []task.RepositorySource
	BackendFactory   SyncBackendFactory
	ScanFactory      task.BackendFactory
	RunStore         SyncRunStore
	Git              externalreview.Git
	CleanupGit       finalization.ClosedTaskWorktreeGit
	ConflictResolver phaseexec.ConflictResolver
	ProcessProbe     phaseexec.ProcessProbe
	PRProvider       pullrequest.Provider
	Logger           *slog.Logger
}

// SyncBranchUpdatePolicy controls when sync changes an open PR branch.
type SyncBranchUpdatePolicy string

const (
	// SyncBranchUpdateAlways preserves the merge-and-push behavior of task-ID sync.
	SyncBranchUpdateAlways SyncBranchUpdatePolicy = "always"

	// SyncBranchUpdateConflictsOnly changes the branch only to resolve conflicts.
	SyncBranchUpdateConflictsOnly SyncBranchUpdatePolicy = "conflicts_only"
)

// SyncOptions are the CLI-provided sync controls.
type SyncOptions struct {
	TaskID             string
	BranchUpdatePolicy SyncBranchUpdatePolicy
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

	// SyncStatusFinalized means direct delivery was finalized without a PR.
	SyncStatusFinalized SyncStatus = "finalized"

	// SyncStatusSkipped means the task was resolvable but had no PR state to reconcile.
	SyncStatusSkipped SyncStatus = "skipped"
)

// SyncResult reports the resolved task and sync outcome.
type SyncResult struct {
	Repository      task.Repository
	Task            task.Task
	LatestRun       taskstate.RunAttempt
	Status          SyncStatus
	Reason          string
	Branch          string
	Worktree        string
	PRURL           string
	WorktreeCleanup *finalization.WorktreeCleanupResult
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

// Sync resolves one task and applies its open-PR branch update policy.
func (s SyncService) Sync(ctx context.Context, opts SyncOptions) (SyncResult, error) {
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

	policy, err := resolveSyncBranchUpdatePolicy(opts.BranchUpdatePolicy)
	if err != nil {
		finalErr = err
		return SyncResult{}, err
	}
	opts.BranchUpdatePolicy = policy
	finalErr = state.WithGlobalMutationLockLogger(ctx, s.Paths, syncLockOperation, s.Logger, func() error {
		synced, err := s.syncLocked(ctx, opts, &diagnosticTarget)
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

// SyncAll reconciles published PRs, direct deliveries, and pending cleanup.
func (s SyncService) SyncAll(ctx context.Context) (SyncAllResult, error) {
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

	finalErr = state.WithGlobalMutationLockLogger(ctx, s.Paths, syncLockOperation, s.Logger, func() error {
		candidates, failures := s.scanSyncAllCandidates(ctx)
		result.Failures = append(result.Failures, failures...)
		for _, failure := range failures {
			s.logSyncAllOutcome(ctx, failure, SyncResult{})
		}

		for _, candidate := range candidates {
			synced, err := s.syncLocked(ctx, SyncOptions{
				TaskID:             candidate.taskID,
				BranchUpdatePolicy: SyncBranchUpdateConflictsOnly,
			}, nil)
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

func resolveSyncBranchUpdatePolicy(policy SyncBranchUpdatePolicy) (SyncBranchUpdatePolicy, error) {
	if policy == "" {
		return SyncBranchUpdateAlways, nil
	}
	switch policy {
	case SyncBranchUpdateAlways, SyncBranchUpdateConflictsOnly:
		return policy, nil
	default:
		return "", fmt.Errorf("unsupported sync branch update policy %q", policy)
	}
}

func gitBranchUpdatePolicy(policy SyncBranchUpdatePolicy) gitmeta.TaskBranchUpdatePolicy {
	if policy == SyncBranchUpdateConflictsOnly {
		return gitmeta.TaskBranchUpdateConflictsOnly
	}
	return gitmeta.TaskBranchUpdateAlways
}

func (s SyncService) scanSyncAllCandidates(ctx context.Context) ([]syncAllCandidate, []SyncAllFailure) {
	candidates := make([]syncAllCandidate, 0)
	failures := make([]SyncAllFailure, 0)
	for _, source := range s.Sources {
		backend, err := s.ScanFactory(source)
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
		snapshot, err := s.RunStore.Load(source.Repository.ID, taskItem.ID)
		if err != nil {
			failures = append(failures, SyncAllFailure{Repository: source.Repository, TaskID: taskItem.ID, Operation: "load_workflow", Err: err})
			continue
		}
		pending := snapshot.Work != nil && finalization.Pending(*snapshot.Work, source.Repository.Path)
		if snapshot.Work != nil && len(snapshot.Work.Workflows) > 0 {
			_, external := snapshot.Work.Workflows[len(snapshot.Work.Workflows)-1].LastPhase().(*work.ExternalReviewPhase)
			pending = pending || (external && taskItem.Status != task.StatusClosed)
		}
		if pending || (taskItem.Status != task.StatusClosed && metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "") {
			candidates = append(candidates, syncAllCandidate{source: source, taskID: taskItem.ID})
		}
	}
	return candidates, failures
}

func isSyncAllRunnableTask(taskItem task.Task) bool {
	if strings.TrimSpace(taskItem.ID) == "" {
		return false
	}
	return taskItem.IssueType != task.IssueTypeEpic && taskItem.Status != task.StatusCancelled
}

func (s SyncService) syncLocked(
	ctx context.Context,
	opts SyncOptions,
	diagnosticTarget *syncDiagnosticTarget,
) (SyncResult, error) {
	target, err := s.resolveTarget(ctx, opts)
	if err != nil {
		return SyncResult{}, err
	}
	diagnosticTarget.recordTarget(target)
	if target.task.Status == task.StatusCancelled {
		return s.skip(target, taskstate.RunAttempt{}, "task is cancelled"), nil
	}

	snapshot, err := s.Store.Load(target.source.Repository.ID, target.task.ID)
	if err != nil {
		return SyncResult{}, err
	}
	prURL := prURLFromTask(target.task)
	if len(snapshot.Workflows) > 0 {
		w := &snapshot.Workflows[len(snapshot.Workflows)-1]
		switch phase := w.LastPhase().(type) {
		case *work.FinalizationPhase:
			return s.finalizeTask(ctx, target, &snapshot)
		case *work.PublicationPhase:
			if _, direct := phase.Delivery.(*work.DirectDelivery); direct {
				w.Phases = append(w.Phases, &work.FinalizationPhase{})
				if err := saveSyncSnapshot(s.Store, &snapshot); err != nil {
					return SyncResult{}, err
				}
				return s.finalizeTask(ctx, target, &snapshot)
			}
		case *work.ExternalReviewPhase:
			prURL = w.Phases[phase.Publication.Phase].(*work.PublicationPhase).Delivery.(*work.PullRequestDelivery).PullRequest.URL
			if phase.Outcome != nil {
				return s.consumePROutcome(ctx, target, &snapshot)
			}
		default:
			return s.skip(target, taskstate.RunAttempt{}, "local workflow progression is selected; external review is not active"), nil
		}
	}

	if target.task.Status == task.StatusClosed && len(snapshot.Workflows) == 0 {
		return s.skip(target, taskstate.RunAttempt{}, "task is closed"), nil
	}

	if prURL == "" {
		return s.skip(target, taskstate.RunAttempt{}, task.MetadataPRURL+" is not set"), nil
	}
	return s.advanceExternalReview(ctx, target, opts.BranchUpdatePolicy, snapshot)

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

func (s SyncService) notify(target syncTarget, prState pullrequest.State) error {
	if s.Progress == nil {
		return nil
	}
	snapshot, err := s.RunStore.Load(target.source.Repository.ID, target.task.ID)
	if err != nil {
		return err
	}
	view, err := InspectTask(target.task, snapshot)
	if err != nil {
		return err
	}
	if view.Publication != nil && prState != "" {
		view.Publication.PRState = string(prState)
		if prState == pullrequest.StateClosed {
			view.Publication.PRState = "closed without merge"
		}
	}
	return s.Progress(Progress{View: view, Resumed: view.Phase == PhaseExternalReview})
}
