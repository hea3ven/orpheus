package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
)

const finalizationLockOperation = "task finalization"

// FinalizationBackend is the backend capability set needed to finalize a task.
type FinalizationBackend interface {
	task.GitFactsMutator
	task.Getter
	task.Lister
	task.PRURLMutator
	task.CloseMutator
}

// FinalizationBackendFactory creates a finalization-capable backend for one repository.
type FinalizationBackendFactory func(task.RepositorySource) (FinalizationBackend, error)

// FinalizationRunStore persists and reads run/finalization facts.
type FinalizationRunStore interface {
	Load(repoID, taskID string) (taskstate.TaskState, error)
	SetFinalizationIntegrationFlow(repoID, taskID string, flow publication.IntegrationFlow) (taskstate.Finalization, error)
	SetFinalizationDestination(repoID, taskID, destination string) (taskstate.Finalization, error)
	RecordFinalizationPublicationStart(repoID, taskID string) (taskstate.Finalization, error)
	RecordFinalizationCommitIntent(repoID, taskID string, parent string, message string) (taskstate.Finalization, error)
	RecordFinalizationCommit(repoID, taskID string, commit string) (taskstate.Finalization, error)
	RecordFinalizationMerge(repoID, taskID string, commit string) (taskstate.Finalization, error)
	RecordFinalizationPush(repoID, taskID string, opts taskstate.FinalizationPushOptions) (taskstate.Finalization, error)
	RecordFinalizationClose(repoID, taskID string, opts taskstate.FinalizationCloseOptions) (taskstate.Finalization, error)
	RecordFinalizationFailure(repoID, taskID string, cause error) (taskstate.Event, error)
	RecordFeatureBranchPR(repoID, taskID string, opts taskstate.FeatureBranchPROptions) (taskstate.Event, error)
	RecordGitFacts(repoID, taskID, branch, worktree string) (taskstate.TaskState, error)
}

// FinalizationGit performs the Git operations used by task finalization.
type FinalizationGit interface {
	CurrentBranch(ctx context.Context, dir string) (string, error)
	HasWorkingTreeChanges(ctx context.Context, dir string) (bool, error)
	HeadCommit(ctx context.Context, dir string) (string, error)
	VerifyCommit(ctx context.Context, dir string, commit string, parent string, message string) error
	StageAll(ctx context.Context, dir string) error
	Commit(ctx context.Context, dir string, message string) (string, error)
	PushDefaultBranch(ctx context.Context, dir string, branch string) error
	PushTaskBranch(ctx context.Context, dir string, branch string) error
	VerifyRemoteDestination(ctx context.Context, repo task.Repository, destination string) error
	MergeTaskBranchIntoDestination(ctx context.Context, repo task.Repository, destination string, branch string) (string, error)
	ValidateRecordedDirectMerge(ctx context.Context, repo task.Repository, destination string, mergeCommit string) (alreadyPushed bool, err error)
	MaterializeTaskBranch(ctx context.Context, repo task.Repository, taskID string, branch string, paths state.Paths) (gitmeta.TaskWorktreeSetupResult, error)
	ValidateMaterializedTaskBranchRetry(ctx context.Context, repo task.Repository, taskID string, branch string, paths state.Paths) error
}

// LocalFinalizationGit delegates finalization Git operations to the local git binary.
type LocalFinalizationGit struct{}

// CurrentBranch returns the current local Git branch.
func (LocalFinalizationGit) CurrentBranch(ctx context.Context, dir string) (string, error) {
	return gitmeta.CurrentBranch(ctx, dir)
}

// HasWorkingTreeChanges reports whether the checkout has local changes.
func (LocalFinalizationGit) HasWorkingTreeChanges(ctx context.Context, dir string) (bool, error) {
	return gitmeta.HasWorkingTreeChanges(ctx, dir)
}

// HeadCommit returns the current HEAD SHA.
func (LocalFinalizationGit) HeadCommit(ctx context.Context, dir string) (string, error) {
	return gitmeta.HeadCommit(ctx, dir)
}

// VerifyCommit verifies a publication commit against its durable intent.
func (LocalFinalizationGit) VerifyCommit(ctx context.Context, dir string, commit string, parent string, message string) error {
	return gitmeta.VerifyCommit(ctx, dir, commit, parent, message)
}

// StageAll stages all finalization changes.
func (LocalFinalizationGit) StageAll(ctx context.Context, dir string) error {
	return gitmeta.StageAll(ctx, dir)
}

// Commit commits staged finalization changes.
func (LocalFinalizationGit) Commit(ctx context.Context, dir string, message string) (string, error) {
	return gitmeta.Commit(ctx, dir, message)
}

// PushDefaultBranch pushes the registered default branch.
func (LocalFinalizationGit) PushDefaultBranch(ctx context.Context, dir string, branch string) error {
	return gitmeta.PushDefaultBranch(ctx, dir, branch)
}

// PushTaskBranch pushes a feature branch.
func (LocalFinalizationGit) PushTaskBranch(ctx context.Context, dir string, branch string) error {
	return gitmeta.PushTaskBranch(ctx, dir, branch)
}

// VerifyRemoteDestination verifies that a named destination exists on origin.
func (LocalFinalizationGit) VerifyRemoteDestination(ctx context.Context, repo task.Repository, destination string) error {
	return gitmeta.VerifyRemoteBranch(ctx, repo.Path, destination)
}

// MergeTaskBranchIntoDestination merges an approved task branch without pushing it.
func (LocalFinalizationGit) MergeTaskBranchIntoDestination(ctx context.Context, repo task.Repository, destination string, branch string) (string, error) {
	return gitmeta.MergeTaskBranchIntoDestination(ctx, repo, destination, branch)
}

// ValidateRecordedDirectMerge confirms that the recorded merge is either the
// local destination tip ready to push or is already on origin.
func (LocalFinalizationGit) ValidateRecordedDirectMerge(
	ctx context.Context,
	repo task.Repository,
	destination string,
	mergeCommit string,
) (bool, error) {
	return gitmeta.ValidateRecordedDirectMergeIntoDestination(ctx, repo, destination, mergeCommit)
}

// MaterializeTaskBranch moves reviewed repository-root changes to the
// deterministic task branch immediately before feature-branch publication.
func (LocalFinalizationGit) MaterializeTaskBranch(ctx context.Context, repo task.Repository, taskID string, branch string, paths state.Paths) (gitmeta.TaskWorktreeSetupResult, error) {
	return gitmeta.MaterializeRepoRootTaskBranch(ctx, gitmeta.TaskWorktreeOptions{
		RepoID: repo.ID, RepoName: repo.Name, RepoPath: repo.Path,
		DefaultBranch: repo.DefaultBranch, TaskID: taskID, Branch: branch, Paths: paths, AllowDirty: true,
	})
}

// ValidateMaterializedTaskBranchRetry validates an interrupted repository-root
// materialization before finalization repairs its persisted Git facts.
func (LocalFinalizationGit) ValidateMaterializedTaskBranchRetry(ctx context.Context, repo task.Repository, taskID string, branch string, paths state.Paths) error {
	return gitmeta.ValidateMaterializedRepoRootTaskBranchRetry(ctx, gitmeta.TaskWorktreeOptions{
		RepoID: repo.ID, RepoName: repo.Name, RepoPath: repo.Path,
		DefaultBranch: repo.DefaultBranch, TaskID: taskID, Branch: branch, Paths: paths, AllowDirty: true,
	})
}

// FinalizationService finalizes reviewed main/solo task work.
type FinalizationService struct {
	Paths          state.Paths
	Sources        []task.RepositorySource
	BackendFactory FinalizationBackendFactory
	RunStore       FinalizationRunStore
	Git            FinalizationGit
	PRProvider     pullrequest.Provider
	Logger         *slog.Logger
}

// FinalizeOptions are the CLI-provided finalization controls.
type FinalizeOptions struct {
	TaskID                string
	CWD                   string
	Summary               string
	Description           string
	AllowRunningCompleted bool
	RequirePassedReview   bool
}

// FinalizationResult reports the finalized task and recorded facts.
type FinalizationResult struct {
	Repository   task.Repository
	Task         task.Task
	Finalization taskstate.Finalization
	Branch       string
	PRURL        string
	PRRecovered  bool
}

// RunningCompletionConfirmation describes a stale running run that can be
// finalized only after explicit operator confirmation.
type RunningCompletionConfirmation struct {
	TaskID  string
	Attempt int
	Summary string
}

// RunningCompletionConfirmationError reports that finalization is otherwise
// ready, but the latest completed run is still recorded as running.
type RunningCompletionConfirmationError struct {
	Confirmation RunningCompletionConfirmation
}

func (e *RunningCompletionConfirmationError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf(
		"latest run attempt %d for task %s is %q with a completion block; explicit interactive confirmation is required",
		e.Confirmation.Attempt,
		e.Confirmation.TaskID,
		taskstate.RunStatusRunning,
	)
}

// RunningCompletionConfirmationFromError extracts confirmation details from an error.
func RunningCompletionConfirmationFromError(err error) (RunningCompletionConfirmation, bool) {
	var confirmationErr *RunningCompletionConfirmationError
	if errors.As(err, &confirmationErr) && confirmationErr != nil {
		return confirmationErr.Confirmation, true
	}
	return RunningCompletionConfirmation{}, false
}

type finalizationTarget struct {
	source  task.RepositorySource
	backend FinalizationBackend
	task    task.Task
}

type finalizationDiagnosticTarget struct {
	repoID string
	taskID string
	branch string
}

func (t *finalizationDiagnosticTarget) recordTarget(target finalizationTarget) {
	if t == nil {
		return
	}
	t.repoID = target.source.Repository.ID
	t.taskID = target.task.ID
	metadata := target.task.OrpheusMetadata()
	if metadata.HasBranch {
		t.branch = strings.TrimSpace(metadata.Branch)
	}
}

func (t *finalizationDiagnosticTarget) recordBranch(branch string) {
	if t == nil {
		return
	}
	t.branch = strings.TrimSpace(branch)
}

type finalizationContext struct {
	state        taskstate.TaskState
	target       taskstate.GitFacts
	latest       taskstate.RunAttempt
	publication  taskstate.RunAttempt
	latestReview taskstate.ReviewAttempt
	hasReview    bool
	finalization taskstate.Finalization
}

func expectedFinalizationTargets(
	repo task.Repository,
	taskItem task.Task,
	finalizeCtx finalizationContext,
	paths state.Paths,
) (tasktarget.ExpectedTargets, error) {
	if isLegacyMainSoloFinalizationTarget(repo, finalizeCtx) {
		return tasktarget.ExpectedTargetsForLegacyMainSolo(repo)
	}
	return tasktarget.ExpectedTargetsForTaskOrRecordedBranch(repo, taskItem, finalizeCtx.target.Branch, paths)
}

func isLegacyMainSoloFinalizationTarget(repo task.Repository, finalizeCtx finalizationContext) bool {
	if _, modern := taskstate.WorkDirectoryFor(finalizeCtx.state); modern {
		return false
	}
	return tasktarget.ClassifyRunTarget(repo, finalizeCtx.target.Branch, finalizeCtx.target.Worktree) == tasktarget.TargetMainSolo
}

// Finalize commits reviewed repo-root changes, pushes the default branch, and
// closes the backend task after the push has succeeded.
func (s FinalizationService) Finalize(ctx context.Context, opts FinalizeOptions) (FinalizationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = gitmeta.ContextWithLogger(ctx, s.Logger)
	span := logging.Start(ctx, s.Logger, "task finalization workflow",
		slog.String("component", "workflow"),
		slog.String("operation", "task_finalization"),
		slog.String("task_id", opts.TaskID),
	)
	var result FinalizationResult
	var finalErr error
	diagnosticTarget := finalizationDiagnosticTarget{taskID: strings.TrimSpace(opts.TaskID)}
	defer func() {
		span.FinishError(ctx, finalErr, finalizationFinishAttrs(result, diagnosticTarget)...)
	}()

	if s.BackendFactory == nil {
		finalErr = errors.New("task finalization backend factory is required")
		return FinalizationResult{}, finalErr
	}
	if s.RunStore == nil {
		finalErr = errors.New("task finalization run store is required")
		return FinalizationResult{}, finalErr
	}
	gitState := s.Git
	if gitState == nil {
		gitState = LocalFinalizationGit{}
	}

	finalErr = state.WithGlobalMutationLockLogger(ctx, s.Paths, finalizationLockOperation, s.Logger, func() error {
		finalized, err := s.finalizeLocked(ctx, opts, gitState, &diagnosticTarget)
		if err != nil {
			return err
		}
		result = finalized
		return nil
	})
	if finalErr != nil {
		return FinalizationResult{}, finalErr
	}
	return result, nil
}

func finalizationFinishAttrs(
	result FinalizationResult,
	target finalizationDiagnosticTarget,
) []slog.Attr {
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

	attrs := make([]slog.Attr, 0, 4)
	if repoID != "" {
		attrs = append(attrs, slog.String("repo_id", repoID))
	}
	if taskID != "" {
		attrs = append(attrs, slog.String("task_id", taskID))
	}
	if branch != "" {
		attrs = append(attrs, slog.String("branch", branch))
	}
	if result.PRURL != "" {
		attrs = append(attrs, slog.Bool("has_pr", true))
	}
	return attrs
}

func (s FinalizationService) finalizeLocked(
	ctx context.Context,
	opts FinalizeOptions,
	gitState FinalizationGit,
	diagnosticTarget *finalizationDiagnosticTarget,
) (FinalizationResult, error) {
	target, err := s.resolveTarget(ctx, opts, gitState)
	if err != nil {
		return FinalizationResult{}, err
	}
	diagnosticTarget.recordTarget(target)
	repo := target.source.Repository

	finalizeCtx, err := s.loadFinalizationContext(repo, target.task)
	if err != nil {
		return FinalizationResult{}, err
	}
	// Resolve the branch before finalization writes any durable publication
	// facts. Recorded feature branches bypass current configuration so retries
	// remain recoverable after a template change. Legacy main/solo state has no
	// task branch to resolve and retains its recorded direct-finalization flow.
	if _, err := expectedFinalizationTargets(repo, target.task, finalizeCtx, s.Paths); err != nil {
		return FinalizationResult{}, err
	}
	if opts.RequirePassedReview {
		if err := validateLatestReviewPassed(target.task.ID, finalizeCtx); err != nil {
			return FinalizationResult{}, err
		}
	}
	finalizeCtx, err = s.lockIntegrationFlow(repo, target.task, finalizeCtx)
	if err != nil {
		return FinalizationResult{}, err
	}
	finalizeCtx, err = s.lockIntegrationDestination(ctx, gitState, repo, target.task, finalizeCtx)
	if err != nil {
		return FinalizationResult{}, err
	}
	result, err := s.finalizeAfterReviewGate(ctx, opts, target, finalizeCtx, gitState, diagnosticTarget)
	if err != nil && opts.RequirePassedReview {
		recordErr := s.recordFinalizationFailure(repo.ID, target.task.ID, err)
		if recordErr != nil {
			return FinalizationResult{}, fmt.Errorf("%w; additionally failed to record finalization failure: %w", err, recordErr)
		}
	}
	return result, err
}

func (s FinalizationService) lockIntegrationFlow(repo task.Repository, taskItem task.Task, ctx finalizationContext) (finalizationContext, error) {
	flow := ctx.finalization.IntegrationFlow
	if strings.TrimSpace(string(flow)) == "" {
		config, err := publication.LoadConfig(s.Paths)
		if err != nil {
			return finalizationContext{}, err
		}
		flow = publication.ResolveIntegrationFlow("", publication.IntegrationFlow(repo.IntegrationFlow), config.IntegrationFlow)
	}
	finalization, err := s.RunStore.SetFinalizationIntegrationFlow(repo.ID, taskItem.ID, flow)
	if err != nil {
		return finalizationContext{}, fmt.Errorf("lock publication integration flow: %w", err)
	}
	ctx.finalization = finalization
	ctx.state.Finalization = &finalization
	return ctx, nil
}

func (s FinalizationService) lockIntegrationDestination(
	ctx context.Context,
	gitState FinalizationGit,
	repo task.Repository,
	taskItem task.Task,
	ctxFacts finalizationContext,
) (finalizationContext, error) {
	destination, err := integrationDestination(repo, ctxFacts.finalization)
	if err != nil {
		return finalizationContext{}, err
	}
	if destination != strings.TrimSpace(repo.DefaultBranch) {
		if err := gitState.VerifyRemoteDestination(ctx, repo, destination); err != nil {
			return finalizationContext{}, fmt.Errorf("verify selected integration destination before publication: %w", err)
		}
	}
	// Older partial publication state has no destination fact. Preserve its
	// compatible default-branch behavior rather than changing identity after
	// mutations; all new publication records a destination before starting.
	if finalizationHasPublicationMutationWithoutDestination(ctxFacts.finalization) {
		return ctxFacts, nil
	}
	finalization, err := s.RunStore.SetFinalizationDestination(repo.ID, taskItem.ID, destination)
	if err != nil {
		return finalizationContext{}, fmt.Errorf("lock publication destination: %w", err)
	}
	ctxFacts.finalization = finalization
	ctxFacts.state.Finalization = &finalization
	return ctxFacts, nil
}

func finalizationHasPublicationMutationWithoutDestination(finalization taskstate.Finalization) bool {
	return strings.TrimSpace(finalization.DestinationBranch) == "" &&
		(finalization.PublicationStartedAt != nil || finalization.CommittedAt != nil ||
			strings.TrimSpace(finalization.Commit) != "" || finalization.PendingCommit != nil ||
			strings.TrimSpace(finalization.MergeCommit) != "" || finalization.PushedAt != nil || finalization.ClosedAt != nil)
}

func integrationDestination(repo task.Repository, finalization taskstate.Finalization) (string, error) {
	destination := strings.TrimSpace(finalization.DestinationBranch)
	if destination == "" {
		destination = strings.TrimSpace(repo.DefaultBranch)
	}
	if destination == "" {
		return "", fmt.Errorf("repo %q has no registered default branch or selected integration destination", repo.ID)
	}
	return destination, nil
}

func validateFeatureBranchIntegrationDestination(
	repo task.Repository,
	finalization taskstate.Finalization,
	publicationBranch string,
) (string, error) {
	destination, err := integrationDestination(repo, finalization)
	if err != nil {
		return "", err
	}
	if destination == publicationBranch {
		return "", fmt.Errorf(
			"selected integration destination %q is the task publication branch; select the registered default branch or a different existing remote branch before publishing",
			destination,
		)
	}
	return destination, nil
}

func (s FinalizationService) finalizeAfterReviewGate(
	ctx context.Context,
	opts FinalizeOptions,
	target finalizationTarget,
	finalizeCtx finalizationContext,
	gitState FinalizationGit,
	diagnosticTarget *finalizationDiagnosticTarget,
) (FinalizationResult, error) {
	repo := target.source.Repository
	targets, err := expectedFinalizationTargets(repo, target.task, finalizeCtx, s.Paths)
	if err != nil {
		return FinalizationResult{}, err
	}
	// A crash or persistence failure can leave checkout, backend metadata, and
	// local Git facts at different points of branch materialization. The actual
	// deterministic checkout is authoritative for this narrowly-scoped retry;
	// reconcile both fact stores before requiring their normal strict mirror.
	target, finalizeCtx, err = s.reconcileMaterializedRepoRootTaskBranch(ctx, target, finalizeCtx, gitState, targets)
	if err != nil {
		return FinalizationResult{}, err
	}
	metadataTarget, err := tasktarget.ClassifyMetadataTarget(target.task.OrpheusMetadata(), targets)
	if err != nil {
		return FinalizationResult{}, fmt.Errorf("task %s metadata target is invalid: %w", target.task.ID, err)
	}
	taskTarget, err := tasktarget.ClassifyGitFacts(finalizeCtx.target, targets)
	if err != nil {
		return FinalizationResult{}, fmt.Errorf("task %s has inconsistent taskstate target: %w", target.task.ID, err)
	}
	if metadataTarget.Branch != taskTarget.Branch || metadataTarget.Worktree != taskTarget.Worktree {
		return FinalizationResult{}, fmt.Errorf(
			"task %s metadata target %q/%q does not mirror taskstate target %q/%q",
			target.task.ID,
			metadataTarget.Branch,
			metadataTarget.Worktree,
			taskTarget.Branch,
			taskTarget.Worktree,
		)
	}

	diagnosticTarget.recordBranch(taskTarget.Branch)
	if isFeatureBranchTarget(taskTarget.Kind) {
		return s.publishApprovedTaskBranch(ctx, target, finalizeCtx, taskTarget, gitState)
	}
	if taskTarget.Kind == tasktarget.TargetMainSolo {
		if _, modern := taskstate.WorkDirectoryFor(finalizeCtx.state); modern {
			return s.materializeAndPublishRepoRoot(ctx, target, finalizeCtx, gitState, diagnosticTarget, targets.RepoRootTeam.Branch)
		}
		// Existing main/solo state predates work-directory selection and retains
		// its recorded direct-finalization semantics for safe reconciliation.
		return s.finalizeDefaultBranch(ctx, opts, target, finalizeCtx, taskTarget, gitState)
	}
	return FinalizationResult{}, fmt.Errorf("task %s has unsupported work directory", target.task.ID)
}

// materializeAndPublishRepoRoot converts a completed repository-root/default
// branch checkout into its deterministic task branch only after review. This
// preserves the operator's initial work-directory choice while ensuring every
// publication follows the pull-request flow.
func (s FinalizationService) reconcileMaterializedRepoRootTaskBranch(
	ctx context.Context,
	target finalizationTarget,
	finalizeCtx finalizationContext,
	gitState FinalizationGit,
	targets tasktarget.ExpectedTargets,
) (finalizationTarget, finalizationContext, error) {
	repo := target.source.Repository
	directory, modern := taskstate.WorkDirectoryFor(finalizeCtx.state)
	if !modern || directory.Path != targets.MainSolo.Worktree {
		return target, finalizeCtx, nil
	}
	currentBranch, err := gitState.CurrentBranch(ctx, directory.Path)
	if err != nil {
		return target, finalizeCtx, fmt.Errorf("inspect repository-root task branch materialization: %w", err)
	}
	if currentBranch != targets.RepoRootTeam.Branch {
		return target, finalizeCtx, nil
	}

	finalization, err := s.validateMaterializedRepoRootTaskBranch(
		ctx,
		gitState,
		repo,
		target.task.ID,
		targets.RepoRootTeam.Branch,
		directory.Path,
		finalizeCtx.finalization,
	)
	if err != nil {
		return target, finalizeCtx, err
	}
	finalizeCtx.finalization = finalization

	// Repair both stores idempotently, including the cases where either write
	// succeeded before the process stopped. In particular, do not rewrite Git
	// facts after the PR URL was persisted: the backend intentionally rejects
	// post-PR mutations, and identical facts need no update.
	metadata := target.task.OrpheusMetadata()
	if metadata.Branch != targets.RepoRootTeam.Branch || metadata.Worktree != directory.Path {
		if err := target.backend.UpdateGitFacts(ctx, target.task.ID, targets.RepoRootTeam.Branch, directory.Path); err != nil {
			return target, finalizeCtx, fmt.Errorf("reconcile materialized task branch in backend: %w", err)
		}
	}
	if finalizeCtx.target.Branch != targets.RepoRootTeam.Branch || finalizeCtx.target.Worktree != directory.Path {
		if _, err := s.RunStore.RecordGitFacts(repo.ID, target.task.ID, targets.RepoRootTeam.Branch, directory.Path); err != nil {
			return target, finalizeCtx, fmt.Errorf("reconcile materialized task branch in task state: %w", err)
		}
	}

	finalizeCtx.target = taskstate.GitFacts{Branch: targets.RepoRootTeam.Branch, Worktree: directory.Path}
	finalizeCtx.state.GitFacts = finalizeCtx.target
	publicationTask := target.task.Clone()
	if publicationTask.Metadata == nil {
		publicationTask.Metadata = task.Metadata{}
	}
	publicationTask.Metadata[task.MetadataBranch] = targets.RepoRootTeam.Branch
	publicationTask.Metadata[task.MetadataWorktree] = directory.Path
	target.task = publicationTask
	return target, finalizeCtx, nil
}

// validateMaterializedRepoRootTaskBranch accepts only a checkout at the
// reviewed default ref, a recorded publication commit, or a commit that matches
// a durable publication intent.
func (s FinalizationService) validateMaterializedRepoRootTaskBranch(
	ctx context.Context,
	gitState FinalizationGit,
	repo task.Repository,
	taskID string,
	branch string,
	worktree string,
	finalization taskstate.Finalization,
) (taskstate.Finalization, error) {
	if strings.TrimSpace(finalization.Commit) != "" {
		head, err := gitState.HeadCommit(ctx, worktree)
		if err != nil {
			return taskstate.Finalization{}, fmt.Errorf("verify recorded publication commit during materialized branch recovery: %w", err)
		}
		if head != finalization.Commit {
			return taskstate.Finalization{}, fmt.Errorf(
				"recorded publication commit is %s, but current HEAD is %s; refusing to reconcile materialized task branch",
				finalization.Commit,
				head,
			)
		}
		return finalization, nil
	}
	if finalization.PendingCommit != nil {
		var err error
		finalization, err = s.recoverPendingFeatureBranchFinalizationCommit(
			ctx,
			gitState,
			repo.ID,
			taskID,
			worktree,
			finalization,
		)
		if err != nil {
			return taskstate.Finalization{}, fmt.Errorf("recover pending publication commit during materialized branch recovery: %w", err)
		}
		if strings.TrimSpace(finalization.Commit) != "" {
			return finalization, nil
		}
	}
	if err := gitState.ValidateMaterializedTaskBranchRetry(ctx, repo, taskID, branch, s.Paths); err != nil {
		return taskstate.Finalization{}, fmt.Errorf("validate materialized task branch recovery: %w", err)
	}
	return finalization, nil
}

func (s FinalizationService) materializeAndPublishRepoRoot(
	ctx context.Context,
	target finalizationTarget,
	finalizeCtx finalizationContext,
	gitState FinalizationGit,
	diagnosticTarget *finalizationDiagnosticTarget,
	publicationBranch string,
) (FinalizationResult, error) {
	repo := target.source.Repository
	if err := validateDefaultBranchFinalizationReady(repo, target.task, finalizeCtx, false); err != nil {
		return FinalizationResult{}, err
	}
	if _, err := validateFeatureBranchIntegrationDestination(repo, finalizeCtx.finalization, publicationBranch); err != nil {
		return FinalizationResult{}, err
	}
	if err := ensureTaskBranchUnowned(ctx, target.backend, target.task.ID, publicationBranch); err != nil {
		return FinalizationResult{}, err
	}
	finalization, err := s.RunStore.RecordFinalizationPublicationStart(repo.ID, target.task.ID)
	if err != nil {
		return FinalizationResult{}, fmt.Errorf("record publication start before materializing task branch: %w", err)
	}
	finalizeCtx.finalization = finalization
	finalizeCtx.state.Finalization = &finalization

	setup, err := gitState.MaterializeTaskBranch(ctx, repo, target.task.ID, publicationBranch, s.Paths)
	if err != nil {
		return FinalizationResult{}, fmt.Errorf("materialize repository-root task branch: %w", err)
	}
	if err := target.backend.UpdateGitFacts(ctx, target.task.ID, setup.Branch, setup.WorktreePath); err != nil {
		return FinalizationResult{}, fmt.Errorf("record materialized task branch in backend: %w", err)
	}
	if _, err := s.RunStore.RecordGitFacts(repo.ID, target.task.ID, setup.Branch, setup.WorktreePath); err != nil {
		return FinalizationResult{}, fmt.Errorf("record materialized task branch: %w", err)
	}
	publicationTarget := tasktarget.Target{
		Kind: tasktarget.TargetRepoRootTeam, Branch: setup.Branch, Worktree: setup.WorktreePath,
	}
	// Publication validation consumes the current Git facts; the fixed work
	// directory remains unchanged in persisted state.
	finalizeCtx.target = taskstate.GitFacts{Branch: setup.Branch, Worktree: setup.WorktreePath}
	publicationTask := target.task.Clone()
	if publicationTask.Metadata == nil {
		publicationTask.Metadata = task.Metadata{}
	}
	publicationTask.Metadata[task.MetadataBranch] = setup.Branch
	publicationTask.Metadata[task.MetadataWorktree] = setup.WorktreePath
	target.task = publicationTask
	diagnosticTarget.recordBranch(publicationTarget.Branch)
	return s.publishApprovedTaskBranch(ctx, target, finalizeCtx, publicationTarget, gitState)
}

func (s FinalizationService) recordFinalizationFailure(repoID string, taskID string, cause error) error {
	_, err := s.RunStore.RecordFinalizationFailure(repoID, taskID, cause)
	return err
}

func (s FinalizationService) finalizeDefaultBranch(
	ctx context.Context,
	opts FinalizeOptions,
	target finalizationTarget,
	finalizeCtx finalizationContext,
	taskTarget tasktarget.Target,
	gitState FinalizationGit,
) (FinalizationResult, error) {
	repo := target.source.Repository
	if taskTarget.Kind != tasktarget.TargetMainSolo {
		return FinalizationResult{}, fmt.Errorf("task %s target %q cannot be finalized by task done", target.task.ID, taskTarget.Kind)
	}

	pendingConfirmation, err := defaultBranchPendingConfirmation(repo, target.task, finalizeCtx, opts.AllowRunningCompleted)
	if err != nil {
		return FinalizationResult{}, err
	}
	if err := ensureDefaultBranchCheckout(ctx, gitState, repo); err != nil {
		return FinalizationResult{}, err
	}
	hasChanges, err := gitState.HasWorkingTreeChanges(ctx, repo.Path)
	if err != nil {
		return FinalizationResult{}, fmt.Errorf("inspect repo-root changes: %w", err)
	}

	finalization, err := s.ensureDefaultBranchFinalizationCommit(
		ctx,
		opts,
		target,
		finalizeCtx,
		gitState,
		hasChanges,
		pendingConfirmation,
	)
	if err != nil {
		return FinalizationResult{}, err
	}
	if pendingConfirmation != nil {
		return FinalizationResult{}, pendingConfirmation
	}

	finalization, err = s.ensureDefaultBranchPushed(ctx, gitState, repo, target.task.ID, finalization)
	if err != nil {
		return FinalizationResult{}, err
	}
	finalization, err = s.ensureDefaultBranchClosed(ctx, target, finalization)
	if err != nil {
		return FinalizationResult{}, err
	}

	return FinalizationResult{
		Repository:   repo,
		Task:         target.task.Clone(),
		Finalization: finalization,
		Branch:       repo.DefaultBranch,
	}, nil
}

func defaultBranchPendingConfirmation(
	repo task.Repository,
	taskItem task.Task,
	finalizeCtx finalizationContext,
	allowRunningCompleted bool,
) (*RunningCompletionConfirmationError, error) {
	var pendingConfirmation *RunningCompletionConfirmationError
	err := validateDefaultBranchFinalizationReady(repo, taskItem, finalizeCtx, allowRunningCompleted)
	if err == nil {
		return nil, nil
	}
	if !errors.As(err, &pendingConfirmation) || pendingConfirmation == nil {
		return nil, err
	}
	return pendingConfirmation, nil
}

func ensureDefaultBranchCheckout(ctx context.Context, gitState FinalizationGit, repo task.Repository) error {
	currentBranch, err := gitState.CurrentBranch(ctx, repo.Path)
	if err != nil {
		return fmt.Errorf("inspect current Git branch: %w", err)
	}
	if currentBranch != repo.DefaultBranch {
		return fmt.Errorf(
			"repo root %q is on branch %q, expected registered default branch %q",
			repo.Path,
			currentBranch,
			repo.DefaultBranch,
		)
	}
	return nil
}

func (s FinalizationService) ensureDefaultBranchFinalizationCommit(
	ctx context.Context,
	opts FinalizeOptions,
	target finalizationTarget,
	finalizeCtx finalizationContext,
	gitState FinalizationGit,
	hasChanges bool,
	pendingConfirmation *RunningCompletionConfirmationError,
) (taskstate.Finalization, error) {
	repo := target.source.Repository
	finalization := finalizeCtx.finalization
	if strings.TrimSpace(finalization.Commit) != "" {
		err := verifyRecordedDefaultBranchCommit(ctx, gitState, repo.Path, target.task.ID, finalization, hasChanges)
		return finalization, err
	}

	summary, description, err := finalizationMessageParts(finalizeCtx.publication.Completion, opts)
	if err != nil {
		return taskstate.Finalization{}, err
	}
	title, err := publication.RenderTitle(repo.TitleTemplate, summary, target.task.ExternalRef)
	if err != nil {
		return taskstate.Finalization{}, err
	}
	return s.createDefaultBranchFinalizationCommit(
		ctx,
		gitState,
		repo,
		target.task.ID,
		title+"\n\n"+description,
		hasChanges,
		pendingConfirmation,
	)
}

func (s FinalizationService) ensureDefaultBranchPushed(
	ctx context.Context,
	gitState FinalizationGit,
	repo task.Repository,
	taskID string,
	finalization taskstate.Finalization,
) (taskstate.Finalization, error) {
	if finalization.PushedAt != nil {
		return finalization, nil
	}
	if err := gitState.PushDefaultBranch(ctx, repo.Path, repo.DefaultBranch); err != nil {
		return taskstate.Finalization{}, err
	}
	finalization, err := s.RunStore.RecordFinalizationPush(repo.ID, taskID, taskstate.FinalizationPushOptions{
		Branch:     repo.DefaultBranch,
		PushTarget: taskstate.PushTargetMain,
	})
	if err != nil {
		return taskstate.Finalization{}, fmt.Errorf("record finalization push: %w", err)
	}
	return finalization, nil
}

// ensureDirectMergeDestinationPushed verifies the durable merge checkpoint
// before publishing the selected destination. If origin already contains it, a
// prior push succeeded and only recording that outcome remains.
func (s FinalizationService) ensureDirectMergeDestinationPushed(
	ctx context.Context,
	gitState FinalizationGit,
	repo task.Repository,
	taskID string,
	destination string,
	finalization taskstate.Finalization,
) (taskstate.Finalization, error) {
	if finalization.PushedAt != nil {
		return finalization, nil
	}
	alreadyPushed, err := gitState.ValidateRecordedDirectMerge(ctx, repo, destination, finalization.MergeCommit)
	if err != nil {
		return taskstate.Finalization{}, err
	}
	if !alreadyPushed {
		if err := gitState.PushDefaultBranch(ctx, repo.Path, destination); err != nil {
			return taskstate.Finalization{}, err
		}
	}
	finalization, err = s.RunStore.RecordFinalizationPush(repo.ID, taskID, taskstate.FinalizationPushOptions{
		Branch:     destination,
		PushTarget: taskstate.PushTargetMain,
	})
	if err != nil {
		return taskstate.Finalization{}, fmt.Errorf("record finalization push: %w", err)
	}
	return finalization, nil
}

func (s FinalizationService) ensureDefaultBranchClosed(
	ctx context.Context,
	target finalizationTarget,
	finalization taskstate.Finalization,
) (taskstate.Finalization, error) {
	if finalization.ClosedAt != nil {
		return finalization, nil
	}
	repo := target.source.Repository
	if target.task.Status != task.StatusClosed {
		if err := target.backend.Close(ctx, target.task.ID); err != nil {
			return taskstate.Finalization{}, err
		}
	}
	finalization, err := s.RunStore.RecordFinalizationClose(repo.ID, target.task.ID, taskstate.FinalizationCloseOptions{
		Reason: taskstate.CloseReasonDefaultBranchPublished,
	})
	if err != nil {
		return taskstate.Finalization{}, fmt.Errorf("record finalization close: %w", err)
	}
	return finalization, nil
}

func verifyRecordedDefaultBranchCommit(
	ctx context.Context,
	gitState FinalizationGit,
	repoPath string,
	taskID string,
	finalization taskstate.Finalization,
	hasChanges bool,
) error {
	if hasChanges {
		return fmt.Errorf(
			"task %s already has finalization commit %s recorded, but repo root %q has new uncommitted changes; "+
				"M4 will not create a second finalization commit, so stash, commit manually outside Orpheus, or remove the extra changes before retrying",
			taskID,
			finalization.Commit,
			repoPath,
		)
	}
	head, err := gitState.HeadCommit(ctx, repoPath)
	if err != nil {
		return fmt.Errorf("verify recorded finalization commit: %w", err)
	}
	if head != finalization.Commit {
		return fmt.Errorf(
			"recorded finalization commit is %s, but current HEAD is %s; M4 will not infer or repair manually committed states",
			finalization.Commit,
			head,
		)
	}
	return nil
}

func (s FinalizationService) createDefaultBranchFinalizationCommit(
	ctx context.Context,
	gitState FinalizationGit,
	repo task.Repository,
	taskID string,
	message string,
	hasChanges bool,
	pendingConfirmation *RunningCompletionConfirmationError,
) (taskstate.Finalization, error) {
	if !hasChanges {
		return taskstate.Finalization{}, fmt.Errorf(
			"repo root %q has no changes to commit and task %s has no recorded finalization commit; "+
				"review or adjust the repo-root changes before running task done, or pass the task id after repairing state manually",
			repo.Path,
			taskID,
		)
	}
	if pendingConfirmation != nil {
		return taskstate.Finalization{}, pendingConfirmation
	}
	if err := gitState.StageAll(ctx, repo.Path); err != nil {
		return taskstate.Finalization{}, err
	}
	commit, err := gitState.Commit(ctx, repo.Path, message)
	if err != nil {
		return taskstate.Finalization{}, err
	}
	finalization, err := s.RunStore.RecordFinalizationCommit(repo.ID, taskID, commit)
	if err != nil {
		return taskstate.Finalization{}, fmt.Errorf("record finalization commit: %w", err)
	}
	return finalization, nil
}

func (s FinalizationService) recoverPendingFeatureBranchFinalizationCommit(
	ctx context.Context,
	gitState FinalizationGit,
	repoID string,
	taskID string,
	worktree string,
	finalization taskstate.Finalization,
) (taskstate.Finalization, error) {
	intent := finalization.PendingCommit
	if intent == nil {
		return finalization, nil
	}
	head, err := gitState.HeadCommit(ctx, worktree)
	if err != nil {
		return taskstate.Finalization{}, fmt.Errorf("inspect pending publication commit: %w", err)
	}
	if head == intent.Parent {
		return finalization, nil
	}
	if err := gitState.VerifyCommit(ctx, worktree, head, intent.Parent, intent.Message); err != nil {
		return taskstate.Finalization{}, fmt.Errorf("verify pending publication commit: %w", err)
	}
	finalization, err = s.RunStore.RecordFinalizationCommit(repoID, taskID, head)
	if err != nil {
		return taskstate.Finalization{}, fmt.Errorf("record recovered publication commit: %w", err)
	}
	return finalization, nil
}

func verifyRecordedFeatureBranchCommit(
	ctx context.Context,
	gitState FinalizationGit,
	worktree string,
	taskID string,
	finalization taskstate.Finalization,
	hasChanges bool,
) error {
	if hasChanges {
		return fmt.Errorf(
			"task %s already has finalization commit %s recorded, but task worktree %q has new uncommitted changes; "+
				"task done will not create a second publication commit, so stash, commit manually outside Orpheus, or remove the extra changes before retrying",
			taskID,
			finalization.Commit,
			worktree,
		)
	}
	head, err := gitState.HeadCommit(ctx, worktree)
	if err != nil {
		return fmt.Errorf("verify recorded publication commit: %w", err)
	}
	if head != finalization.Commit {
		return fmt.Errorf(
			"recorded publication commit is %s, but current HEAD is %s; task done will not infer or repair manually committed states",
			finalization.Commit,
			head,
		)
	}
	return nil
}

func (s FinalizationService) createFeatureBranchFinalizationCommit(
	ctx context.Context,
	gitState FinalizationGit,
	repoID string,
	taskID string,
	worktree string,
	message string,
	hasChanges bool,
	finalization taskstate.Finalization,
) (taskstate.Finalization, error) {
	if !hasChanges {
		return taskstate.Finalization{}, fmt.Errorf(
			"task worktree %q has no reviewed local changes to commit for task %s; "+
				"review or adjust the feature-branch changes before running task done",
			worktree,
			taskID,
		)
	}
	if finalization.PendingCommit == nil {
		parent, err := gitState.HeadCommit(ctx, worktree)
		if err != nil {
			return taskstate.Finalization{}, fmt.Errorf("inspect publication commit parent: %w", err)
		}
		finalization, err = s.RunStore.RecordFinalizationCommitIntent(repoID, taskID, parent, message)
		if err != nil {
			return taskstate.Finalization{}, fmt.Errorf("record publication commit intent: %w", err)
		}
	}
	if finalization.PendingCommit == nil {
		return taskstate.Finalization{}, errors.New("record publication commit intent: finalization state has no pending commit")
	}
	if err := gitState.StageAll(ctx, worktree); err != nil {
		return taskstate.Finalization{}, err
	}
	commit, err := gitState.Commit(ctx, worktree, finalization.PendingCommit.Message)
	if err != nil {
		return taskstate.Finalization{}, err
	}
	finalization, err = s.RunStore.RecordFinalizationCommit(repoID, taskID, commit)
	if err != nil {
		return taskstate.Finalization{}, fmt.Errorf("record publication commit: %w", err)
	}
	return finalization, nil
}

func (s FinalizationService) publishApprovedTaskBranch(
	ctx context.Context,
	target finalizationTarget,
	finalizeCtx finalizationContext,
	taskTarget tasktarget.Target,
	gitState FinalizationGit,
) (FinalizationResult, error) {
	if finalizeCtx.finalization.IntegrationFlow == publication.IntegrationFlowDirectMerge {
		return s.directMergeFeatureBranch(ctx, target, finalizeCtx, taskTarget, gitState)
	}
	return s.publishFeatureBranch(ctx, target, finalizeCtx, taskTarget, gitState)
}

func (s FinalizationService) publishFeatureBranch(
	ctx context.Context,
	target finalizationTarget,
	finalizeCtx finalizationContext,
	taskTarget tasktarget.Target,
	gitState FinalizationGit,
) (FinalizationResult, error) {
	repo := target.source.Repository
	if err := validateFeatureBranchPublicationReady(repo, target.task, finalizeCtx, taskTarget); err != nil {
		return FinalizationResult{}, err
	}
	if _, err := validateFeatureBranchIntegrationDestination(repo, finalizeCtx.finalization, taskTarget.Branch); err != nil {
		return FinalizationResult{}, err
	}
	finalization, err := s.recordFeatureBranchPublicationStart(repo.ID, target.task.ID, finalizeCtx.finalization)
	if err != nil {
		return FinalizationResult{}, err
	}
	finalizeCtx.finalization = finalization
	if s.PRProvider == nil {
		return FinalizationResult{}, errors.New("task done PR provider is required")
	}
	if err := ensureFeatureBranchCheckout(ctx, gitState, taskTarget); err != nil {
		return FinalizationResult{}, err
	}

	message, err := featureBranchPublicationMessage(repo, target.task, finalizeCtx.publication)
	if err != nil {
		return FinalizationResult{}, err
	}

	hasChanges, err := gitState.HasWorkingTreeChanges(ctx, taskTarget.Worktree)
	if err != nil {
		return FinalizationResult{}, fmt.Errorf("inspect task worktree changes: %w", err)
	}

	finalization, err = s.ensureFeatureBranchFinalizationCommit(
		ctx,
		gitState,
		repo.ID,
		target.task.ID,
		taskTarget.Worktree,
		message,
		hasChanges,
		finalizeCtx.finalization,
	)
	if err != nil {
		return FinalizationResult{}, err
	}

	finalization, err = s.ensureFeatureBranchPushed(ctx, gitState, repo.ID, target.task.ID, taskTarget, finalization)
	if err != nil {
		return FinalizationResult{}, err
	}

	prURL, prRecovered, err := s.ensureFeatureBranchPRRecorded(ctx, target, finalizeCtx, taskTarget)
	if err != nil {
		return FinalizationResult{}, err
	}

	return featureBranchFinalizationResult(repo, target.task, finalization, taskTarget.Branch, prURL, prRecovered), nil
}

func (s FinalizationService) recordFeatureBranchPublicationStart(
	repoID string,
	taskID string,
	finalization taskstate.Finalization,
) (taskstate.Finalization, error) {
	if finalizationHasPublicationMutationWithoutDestination(finalization) {
		return finalization, nil
	}
	started, err := s.RunStore.RecordFinalizationPublicationStart(repoID, taskID)
	if err != nil {
		return taskstate.Finalization{}, fmt.Errorf("record publication start before publication: %w", err)
	}
	return started, nil
}

func (s FinalizationService) directMergeFeatureBranch(
	ctx context.Context,
	target finalizationTarget,
	finalizeCtx finalizationContext,
	taskTarget tasktarget.Target,
	gitState FinalizationGit,
) (FinalizationResult, error) {
	repo := target.source.Repository
	if err := validateFeatureBranchPublicationReady(repo, target.task, finalizeCtx, taskTarget); err != nil {
		return FinalizationResult{}, err
	}
	destination, err := validateFeatureBranchIntegrationDestination(repo, finalizeCtx.finalization, taskTarget.Branch)
	if err != nil {
		return FinalizationResult{}, err
	}
	finalization, err := s.recordFeatureBranchPublicationStart(repo.ID, target.task.ID, finalizeCtx.finalization)
	if err != nil {
		return FinalizationResult{}, err
	}
	// A repository-root direct merge leaves that fixed work directory on the
	// default branch. If the task commit was already recorded, do not require it
	// to switch back merely to retry recording/pushing the completed merge.
	currentBranch, err := gitState.CurrentBranch(ctx, taskTarget.Worktree)
	if err != nil {
		return FinalizationResult{}, fmt.Errorf("inspect task branch for direct merge: %w", err)
	}
	mergedRepoRootRetry := taskTarget.Worktree == repo.Path && currentBranch == destination && finalization.Commit != ""
	if !mergedRepoRootRetry {
		if err := ensureFeatureBranchCheckout(ctx, gitState, taskTarget); err != nil {
			return FinalizationResult{}, err
		}
		message, err := featureBranchPublicationMessage(repo, target.task, finalizeCtx.publication)
		if err != nil {
			return FinalizationResult{}, err
		}
		hasChanges, err := gitState.HasWorkingTreeChanges(ctx, taskTarget.Worktree)
		if err != nil {
			return FinalizationResult{}, fmt.Errorf("inspect task worktree changes: %w", err)
		}
		finalization, err = s.ensureFeatureBranchFinalizationCommit(ctx, gitState, repo.ID, target.task.ID, taskTarget.Worktree, message, hasChanges, finalization)
		if err != nil {
			return FinalizationResult{}, err
		}
	}
	if finalization.MergeCommit == "" {
		mergeCommit, err := gitState.MergeTaskBranchIntoDestination(ctx, repo, destination, taskTarget.Branch)
		if err != nil {
			return FinalizationResult{}, err
		}
		finalization, err = s.RunStore.RecordFinalizationMerge(repo.ID, target.task.ID, mergeCommit)
		if err != nil {
			return FinalizationResult{}, fmt.Errorf("record direct merge: %w", err)
		}
	}
	finalization, err = s.ensureDirectMergeDestinationPushed(ctx, gitState, repo, target.task.ID, destination, finalization)
	if err != nil {
		return FinalizationResult{}, err
	}
	finalization, err = s.ensureDefaultBranchClosed(ctx, target, finalization)
	if err != nil {
		return FinalizationResult{}, err
	}
	return FinalizationResult{Repository: repo, Task: target.task.Clone(), Finalization: finalization, Branch: destination}, nil
}

func (s FinalizationService) ensureFeatureBranchPRRecorded(
	ctx context.Context,
	target finalizationTarget,
	finalizeCtx finalizationContext,
	taskTarget tasktarget.Target,
) (string, bool, error) {
	repo := target.source.Repository
	prURL, prRecovered, err := s.findOrCreateFeatureBranchPR(ctx, repo, target.task, finalizeCtx, taskTarget)
	if err != nil {
		return "", false, err
	}
	metadata := target.task.OrpheusMetadata()
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
		if metadata.PRURL != prURL {
			return "", false, fmt.Errorf(
				"task %s has %s %q, but recovered pull request is %q",
				target.task.ID,
				task.MetadataPRURL,
				metadata.PRURL,
				prURL,
			)
		}
	} else if err := target.backend.SetPRURL(ctx, target.task.ID, prURL); err != nil {
		return "", false, err
	}
	if err := s.recordFeatureBranchPR(repo.ID, target.task.ID, prURL, taskTarget.Branch, prRecovered); err != nil {
		return "", false, fmt.Errorf("record feature branch PR: %w", err)
	}
	return prURL, prRecovered, nil
}

func featureBranchPublicationMessage(
	repo task.Repository,
	taskItem task.Task,
	latest taskstate.RunAttempt,
) (string, error) {
	summary, description, err := finalizationMessageParts(latest.Completion, FinalizeOptions{})
	if err != nil {
		return "", err
	}
	title, err := publication.RenderTitle(repo.TitleTemplate, summary, taskItem.ExternalRef)
	if err != nil {
		return "", err
	}
	return title + "\n\n" + description, nil
}

func (s FinalizationService) recordFeatureBranchPR(
	repoID string,
	taskID string,
	prURL string,
	branch string,
	prRecovered bool,
) error {
	_, err := s.RunStore.RecordFeatureBranchPR(repoID, taskID, taskstate.FeatureBranchPROptions{
		PRURL:        prURL,
		Branch:       branch,
		WasRecovered: prRecovered,
	})
	return err
}

func featureBranchFinalizationResult(
	repo task.Repository,
	taskItem task.Task,
	finalization taskstate.Finalization,
	branch string,
	prURL string,
	prRecovered bool,
) FinalizationResult {
	return FinalizationResult{
		Repository:   repo,
		Task:         taskWithPRURL(taskItem, prURL),
		Finalization: finalization,
		Branch:       branch,
		PRURL:        prURL,
		PRRecovered:  prRecovered,
	}
}

func ensureFeatureBranchCheckout(ctx context.Context, gitState FinalizationGit, target tasktarget.Target) error {
	currentBranch, err := gitState.CurrentBranch(ctx, target.Worktree)
	if err != nil {
		return fmt.Errorf("inspect current Git branch: %w", err)
	}
	if currentBranch != target.Branch {
		return fmt.Errorf(
			"task worktree %q is on branch %q, expected task branch %q",
			target.Worktree,
			currentBranch,
			target.Branch,
		)
	}
	return nil
}

func (s FinalizationService) ensureFeatureBranchFinalizationCommit(
	ctx context.Context,
	gitState FinalizationGit,
	repoID string,
	taskID string,
	worktree string,
	message string,
	hasChanges bool,
	finalization taskstate.Finalization,
) (taskstate.Finalization, error) {
	if strings.TrimSpace(finalization.Commit) != "" {
		err := verifyRecordedFeatureBranchCommit(ctx, gitState, worktree, taskID, finalization, hasChanges)
		return finalization, err
	}
	if finalization.PendingCommit != nil {
		var err error
		finalization, err = s.recoverPendingFeatureBranchFinalizationCommit(
			ctx,
			gitState,
			repoID,
			taskID,
			worktree,
			finalization,
		)
		if err != nil {
			return taskstate.Finalization{}, err
		}
		if strings.TrimSpace(finalization.Commit) != "" {
			return finalization, nil
		}
	}
	return s.createFeatureBranchFinalizationCommit(ctx, gitState, repoID, taskID, worktree, message, hasChanges, finalization)
}

func (s FinalizationService) ensureFeatureBranchPushed(
	ctx context.Context,
	gitState FinalizationGit,
	repoID string,
	taskID string,
	target tasktarget.Target,
	finalization taskstate.Finalization,
) (taskstate.Finalization, error) {
	if finalization.PushedAt != nil {
		return finalization, nil
	}
	if err := gitState.PushTaskBranch(ctx, target.Worktree, target.Branch); err != nil {
		return taskstate.Finalization{}, err
	}
	finalization, err := s.RunStore.RecordFinalizationPush(repoID, taskID, taskstate.FinalizationPushOptions{
		Branch:     target.Branch,
		PushTarget: taskstate.PushTargetBranch,
	})
	if err != nil {
		return taskstate.Finalization{}, fmt.Errorf("record publication push: %w", err)
	}
	return finalization, nil
}

func (s FinalizationService) findOrCreateFeatureBranchPR(
	ctx context.Context,
	repo task.Repository,
	taskItem task.Task,
	finalizeCtx finalizationContext,
	target tasktarget.Target,
) (string, bool, error) {
	baseBranch, err := integrationDestination(repo, finalizeCtx.finalization)
	if err != nil {
		return "", false, err
	}
	diagnostics := pullrequest.DiagnosticContext{
		RepoID: repo.ID,
		TaskID: taskItem.ID,
		Branch: target.Branch,
	}
	found, ok, err := s.PRProvider.FindOpenByBranch(ctx, pullrequest.FindOpenByBranchRequest{
		RepositoryPath: repo.Path,
		HeadBranch:     target.Branch,
		BaseBranch:     baseBranch,
		Diagnostics:    diagnostics,
	})
	if err != nil {
		return "", false, err
	}
	if ok {
		return strings.TrimSpace(found.URL), true, nil
	}

	publicationOptions, err := ResolvePublicationOptions(s.Paths, repo)
	if err != nil {
		return "", false, err
	}
	content, err := BuildPublicationPullRequestContentFromStateWithOptions(publicationOptions, taskItem, finalizeCtx.state)
	if err != nil {
		return "", false, err
	}
	created, err := s.PRProvider.Create(ctx, pullrequest.CreateRequest{
		RepositoryPath: repo.Path,
		HeadBranch:     target.Branch,
		BaseBranch:     baseBranch,
		Title:          content.Title,
		Body:           content.Body,
		Diagnostics:    diagnostics,
	})
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(created.URL), false, nil
}

func taskWithPRURL(taskItem task.Task, prURL string) task.Task {
	updated := taskItem.Clone()
	if updated.Metadata == nil {
		updated.Metadata = task.Metadata{}
	}
	updated.Metadata[task.MetadataPRURL] = prURL
	return updated
}

func (s FinalizationService) resolveTarget(
	ctx context.Context,
	opts FinalizeOptions,
	gitState FinalizationGit,
) (finalizationTarget, error) {
	taskID := strings.TrimSpace(opts.TaskID)
	if taskID == "" {
		return s.inferTarget(ctx, opts, gitState)
	}

	resolved, err := task.ResolveTaskSource(s.Sources, taskID)
	if err != nil {
		return finalizationTarget{}, err
	}
	backend, err := s.BackendFactory(resolved.Source)
	if err != nil {
		return finalizationTarget{}, fmt.Errorf(
			"task done %s: create backend for repo %s (%s; prefix %s): %w",
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
			return finalizationTarget{}, fmt.Errorf(
				"task done %s: task was not found in repo %s (%s; prefix %s): %w",
				resolved.TaskID,
				resolved.Source.Repository.ID,
				resolved.Source.Repository.Name,
				resolved.Source.Repository.TaskIDPrefix,
				err,
			)
		}
		return finalizationTarget{}, fmt.Errorf(
			"task done %s: query repo %s (%s; prefix %s): %w",
			resolved.TaskID,
			resolved.Source.Repository.ID,
			resolved.Source.Repository.Name,
			resolved.Source.Repository.TaskIDPrefix,
			err,
		)
	}
	return finalizationTarget{source: resolved.Source, backend: backend, task: taskItem}, nil
}

func (s FinalizationService) inferTarget(
	ctx context.Context,
	opts FinalizeOptions,
	gitState FinalizationGit,
) (finalizationTarget, error) {
	normalizedCWD, err := currentDirectory(opts.CWD)
	if err != nil {
		return finalizationTarget{}, err
	}
	currentBranch, err := gitState.CurrentBranch(ctx, normalizedCWD)
	if err != nil {
		return finalizationTarget{}, fmt.Errorf("inspect current Git branch while inferring task: %w", err)
	}

	source, err := s.inferSourceFromCWD(normalizedCWD)
	if err != nil {
		return finalizationTarget{}, err
	}

	backend, candidates, err := s.loadInferenceCandidates(ctx, source, currentBranch, normalizedCWD)
	if err != nil {
		return finalizationTarget{}, err
	}
	switch len(candidates) {
	case 1:
		return finalizationTarget{source: source, backend: backend, task: candidates[0]}, nil
	case 0:
		return finalizationTarget{}, fmt.Errorf(
			"cannot infer task to finalize from current directory %q on branch %q: no non-closed ready task owns the current branch; pass <task-id>",
			normalizedCWD,
			currentBranch,
		)
	default:
		return finalizationTarget{}, fmt.Errorf(
			"cannot infer task to finalize from current directory %q on branch %q: multiple non-closed ready tasks own the current branch (%s); pass <task-id>",
			normalizedCWD,
			currentBranch,
			strings.Join(taskIDs(candidates), ", "),
		)
	}
}

func (s FinalizationService) loadInferenceCandidates(
	ctx context.Context,
	source task.RepositorySource,
	currentBranch string,
	workingDirectory string,
) (FinalizationBackend, []task.Task, error) {
	backend, err := s.BackendFactory(source)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"task done: create backend for repo %s (%s; prefix %s): %w",
			source.Repository.ID,
			source.Repository.Name,
			source.Repository.TaskIDPrefix,
			err,
		)
	}
	tasks, err := backend.List(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"task done: query repo %s (%s; prefix %s) while inferring task: %w",
			source.Repository.ID,
			source.Repository.Name,
			source.Repository.TaskIDPrefix,
			err,
		)
	}
	candidates, err := s.inferableCurrentBranchReadyTasks(
		source.Repository,
		tasks,
		currentBranch,
		workingDirectory,
	)
	if err != nil {
		return nil, nil, err
	}
	return backend, candidates, nil
}

func (s FinalizationService) inferSourceFromCWD(normalizedCWD string) (task.RepositorySource, error) {
	matches := make([]task.RepositorySource, 0, 1)
	for _, source := range s.Sources {
		repoPath, err := cleanAbsPath("registered repo root", source.Repository.Path)
		if err != nil {
			return task.RepositorySource{}, err
		}
		if repoPath == normalizedCWD {
			matches = append(matches, source)
			continue
		}
		worktreeParent, err := s.Paths.DataPath(filepath.Join("repos", source.Repository.ID, "worktrees"))
		if err != nil {
			return task.RepositorySource{}, fmt.Errorf("resolve task worktree parent for repo %s: %w", source.Repository.ID, err)
		}
		if filepath.Dir(normalizedCWD) == filepath.Clean(worktreeParent) {
			matches = append(matches, source)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return task.RepositorySource{}, fmt.Errorf(
			"cannot infer task to finalize from current directory %q: cwd must be exactly a registered repo root or deterministic task worktree; pass <task-id>",
			normalizedCWD,
		)
	default:
		return task.RepositorySource{}, fmt.Errorf(
			"cannot infer task to finalize from current directory %q: multiple registered repos use this root; pass <task-id>",
			normalizedCWD,
		)
	}
}

func (s FinalizationService) inferableCurrentBranchReadyTasks(
	repo task.Repository,
	tasks []task.Task,
	currentBranch string,
	workingDirectory string,
) ([]task.Task, error) {
	candidates := make([]task.Task, 0, 1)
	for _, taskItem := range tasks {
		if taskItem.Status == task.StatusClosed {
			continue
		}
		state, err := s.RunStore.Load(repo.ID, taskItem.ID)
		if err != nil {
			return nil, fmt.Errorf("load task state for %s/%s: %w", repo.ID, taskItem.ID, err)
		}
		taskTarget, ok := taskstate.GitFactsFor(state)
		if !ok || strings.TrimSpace(taskTarget.Branch) != currentBranch {
			continue
		}
		taskWorktree, err := cleanAbsPath("taskstate target worktree", taskTarget.Worktree)
		if err != nil || taskWorktree != workingDirectory {
			continue
		}
		finalizeCtx := finalizationContext{state: state, target: taskTarget}
		targets, err := expectedFinalizationTargets(repo, taskItem, finalizeCtx, s.Paths)
		if err != nil {
			return nil, err
		}
		target, err := tasktarget.ClassifyGitFacts(taskTarget, targets)
		if err != nil {
			continue
		}
		ok, err = s.isInferableCurrentBranchReady(
			repo,
			taskItem,
			currentBranch,
			workingDirectory,
			target,
			state,
		)
		if err != nil {
			return nil, err
		}
		if ok {
			candidates = append(candidates, taskItem.Clone())
		}
	}
	return candidates, nil
}

func (s FinalizationService) isInferableCurrentBranchReady(
	repo task.Repository,
	taskItem task.Task,
	currentBranch string,
	workingDirectory string,
	target tasktarget.Target,
	state taskstate.TaskState,
) (bool, error) {
	if taskItem.Status == task.StatusClosed {
		return false, nil
	}
	if target.Branch != currentBranch {
		return false, nil
	}
	if target.Worktree != workingDirectory {
		return false, nil
	}
	latest, ok := taskstate.LatestRun(state)
	if !ok {
		return false, nil
	}
	taskTarget, ok := taskstate.GitFactsFor(state)
	if !ok {
		return false, nil
	}
	ctx := finalizationContext{
		target:       taskTarget,
		latest:       latest,
		publication:  latest,
		finalization: taskstate.FinalizationFacts(state),
	}
	switch target.Kind {
	case tasktarget.TargetMainSolo:
		return isInferableDefaultBranchFinalizationReady(repo, taskItem, ctx), nil
	case tasktarget.TargetRepoRootTeam:
		return isInferableFeatureBranchPublicationReady(repo, taskItem, ctx, target), nil
	case tasktarget.TargetWorktreeTeam:
		return isInferableFeatureBranchPublicationReady(repo, taskItem, ctx, target), nil
	default:
		return false, nil
	}
}

func isInferableDefaultBranchFinalizationReady(
	repo task.Repository,
	taskItem task.Task,
	ctx finalizationContext,
) bool {
	if _, err := finalizationDefaultBranch(repo); err != nil {
		return false
	}
	if taskItem.Status != task.StatusInProgress {
		return false
	}
	metadata := taskItem.OrpheusMetadata()
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
		return false
	}
	if ctx.latest.Completion == nil {
		return false
	}
	return completedHandoffStatus(ctx.latest.Status) || ctx.latest.Status == taskstate.RunStatusRunning
}

func isInferableFeatureBranchPublicationReady(
	repo task.Repository,
	taskItem task.Task,
	ctx finalizationContext,
	target tasktarget.Target,
) bool {
	if _, err := finalizationDefaultBranch(repo); err != nil {
		return false
	}
	if !isFeatureBranchTarget(target.Kind) {
		return false
	}
	if taskItem.Status != task.StatusInProgress {
		return false
	}
	metadata := taskItem.OrpheusMetadata()
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
		return false
	}
	if ctx.latest.Completion == nil {
		return false
	}
	return completedHandoffStatus(ctx.latest.Status)
}

func (s FinalizationService) loadFinalizationContext(repo task.Repository, taskItem task.Task) (finalizationContext, error) {
	state, err := s.RunStore.Load(repo.ID, taskItem.ID)
	if err != nil {
		return finalizationContext{}, fmt.Errorf("load task state for %s/%s: %w", repo.ID, taskItem.ID, err)
	}
	latest, ok := taskstate.LatestRun(state)
	if !ok {
		return finalizationContext{}, fmt.Errorf("task %s has no Orpheus run attempts; run `orpheus task run %s` first", taskItem.ID, taskItem.ID)
	}
	taskTarget, ok := taskstate.GitFactsFor(state)
	if !ok {
		return finalizationContext{}, fmt.Errorf("task %s has no taskstate target; run `orpheus task run %s` first", taskItem.ID, taskItem.ID)
	}
	publicationRun, err := publicationRun(state)
	if err != nil {
		return finalizationContext{}, fmt.Errorf("select publication completion for task %s: %w", taskItem.ID, err)
	}
	latestReview, hasReview := taskstate.LatestReview(state)
	return finalizationContext{
		state:        state,
		target:       taskTarget,
		latest:       latest,
		publication:  publicationRun,
		latestReview: latestReview,
		hasReview:    hasReview,
		finalization: taskstate.FinalizationFacts(state),
	}, nil
}

func validateLatestReviewPassed(taskID string, ctx finalizationContext) error {
	if !ctx.hasReview {
		return fmt.Errorf("task %s has no local review attempt; run `orpheus task run %s` before `orpheus task done %s`", taskID, taskID, taskID)
	}
	if ctx.latestReview.Status != taskstate.ReviewStatusPassed {
		return fmt.Errorf(
			"latest review attempt %d for task %s is %q, expected %q; run `orpheus task run %s`",
			ctx.latestReview.Attempt,
			taskID,
			ctx.latestReview.Status,
			taskstate.ReviewStatusPassed,
			taskID,
		)
	}
	return nil
}

func validateDefaultBranchFinalizationReady(
	repo task.Repository,
	taskItem task.Task,
	ctx finalizationContext,
	allowRunningCompleted bool,
) error {
	defaultBranch, err := finalizationDefaultBranch(repo)
	if err != nil {
		return err
	}
	repoRoot, err := cleanAbsPath("registered repo root", repo.Path)
	if err != nil {
		return err
	}
	if err := validateDefaultBranchTaskStatus(taskItem, ctx.finalization); err != nil {
		return err
	}
	if err := validateDefaultBranchTaskMetadata(repoRoot, defaultBranch, taskItem); err != nil {
		return err
	}
	if err := validateDefaultBranchLatestRun(repoRoot, defaultBranch, taskItem, ctx.target, ctx.latest); err != nil {
		return err
	}
	return validateDefaultBranchLatestStatus(taskItem, ctx.latest, allowRunningCompleted)
}

func validateFeatureBranchPublicationReady(
	repo task.Repository,
	taskItem task.Task,
	ctx finalizationContext,
	target tasktarget.Target,
) error {
	if _, err := finalizationDefaultBranch(repo); err != nil {
		return err
	}
	if err := validateFeatureBranchTarget(target, taskItem.ID); err != nil {
		return err
	}
	if err := validateFeatureBranchTaskStatus(taskItem, ctx.finalization); err != nil {
		return err
	}
	if err := validateFeatureBranchTaskMetadata(taskItem, ctx.state, target); err != nil {
		return err
	}
	return validateFeatureBranchLatestRun(
		taskItem,
		ctx.target,
		ctx.latest,
		target,
		featureBranchPRRecordIsMissing(ctx.state, taskItem.OrpheusMetadata().PRURL, target.Branch),
	)
}

func finalizationDefaultBranch(repo task.Repository) (string, error) {
	if strings.TrimSpace(repo.ID) == "" {
		return "", errors.New("repo id is required")
	}
	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	if defaultBranch == "" {
		return "", fmt.Errorf("repo %q has no registered default branch", repo.ID)
	}
	return defaultBranch, nil
}

func validateDefaultBranchTaskStatus(taskItem task.Task, finalization taskstate.Finalization) error {
	switch taskItem.Status {
	case task.StatusInProgress:
		return nil
	case task.StatusClosed:
		if strings.TrimSpace(finalization.Commit) != "" {
			return nil
		}
		return fmt.Errorf("task %s is closed and has no recorded finalization commit; refusing to infer manual finalization", taskItem.ID)
	default:
		return fmt.Errorf("task %s is %s, expected in_progress for main/solo finalization", taskItem.ID, formatStatusForFinalization(taskItem.Status))
	}
}

func validateDefaultBranchTaskMetadata(repoRoot string, defaultBranch string, taskItem task.Task) error {
	metadata := taskItem.OrpheusMetadata()
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
		return fmt.Errorf("task %s has %s set; task done only finalizes main/solo local-ready tasks without PR URLs", taskItem.ID, task.MetadataPRURL)
	}
	if !metadata.HasBranch || strings.TrimSpace(metadata.Branch) != defaultBranch {
		return fmt.Errorf(
			"task %s metadata %s is %q, expected registered default branch %q",
			taskItem.ID,
			task.MetadataBranch,
			metadata.Branch,
			defaultBranch,
		)
	}
	return validateDefaultBranchWorktreeMetadata(repoRoot, taskItem.ID, metadata)
}

func validateDefaultBranchWorktreeMetadata(repoRoot string, taskID string, metadata task.OrpheusMetadata) error {
	metadataWorktree, err := cleanAbsPath(task.MetadataWorktree, metadata.Worktree)
	if metadata.HasWorktree && err != nil {
		return fmt.Errorf("task %s metadata %s is invalid: %w", taskID, task.MetadataWorktree, err)
	}
	if !metadata.HasWorktree || metadataWorktree != repoRoot {
		return fmt.Errorf(
			"task %s metadata %s is %q, expected registered repo root %q",
			taskID,
			task.MetadataWorktree,
			metadata.Worktree,
			repoRoot,
		)
	}
	return nil
}

func validateDefaultBranchLatestRun(
	repoRoot string,
	defaultBranch string,
	taskItem task.Task,
	taskTarget taskstate.GitFacts,
	latest taskstate.RunAttempt,
) error {
	if latest.Completion == nil {
		return fmt.Errorf("latest run attempt %d for task %s has no main-mode completion block; run `orpheus agent done` first", latest.Attempt, taskItem.ID)
	}
	if strings.TrimSpace(taskTarget.Branch) != defaultBranch {
		return fmt.Errorf(
			"task %s taskstate target branch is %q, expected registered default branch %q",
			taskItem.ID,
			taskTarget.Branch,
			defaultBranch,
		)
	}
	if err := validateTaskTargetWorktree(repoRoot, "registered repo root", taskItem.ID, taskTarget); err != nil {
		return err
	}

	classificationRun := latest
	if !completedHandoffStatus(latest.Status) {
		classificationRun.Status = taskstate.RunStatusSucceeded
	}
	localTarget := tasktarget.Target{Kind: tasktarget.TargetMainSolo, Branch: defaultBranch, Worktree: repoRoot}
	if _, ok := ClassifyExpectedLocalReviewReady(
		tasktarget.ExpectedTargets{MainSolo: localTarget},
		taskItem,
		taskTarget,
		&classificationRun,
	); !ok {
		return fmt.Errorf("latest run attempt %d for task %s is not a main/solo local-ready completion", latest.Attempt, taskItem.ID)
	}
	return nil
}

func validateTaskTargetWorktree(
	expected string,
	expectedLabel string,
	taskID string,
	taskTarget taskstate.GitFacts,
) error {
	targetWorktree, err := cleanAbsPath("taskstate target worktree", taskTarget.Worktree)
	if err != nil {
		return err
	}
	if targetWorktree != expected {
		return fmt.Errorf(
			"task %s taskstate target worktree is %q, expected %s %q",
			taskID,
			taskTarget.Worktree,
			expectedLabel,
			expected,
		)
	}
	return nil
}

func validateDefaultBranchLatestStatus(
	taskItem task.Task,
	latest taskstate.RunAttempt,
	allowRunningCompleted bool,
) error {
	if completedHandoffStatus(latest.Status) {
		return nil
	}
	if latest.Status == taskstate.RunStatusRunning {
		if allowRunningCompleted {
			return nil
		}
		return &RunningCompletionConfirmationError{
			Confirmation: RunningCompletionConfirmation{
				TaskID:  taskItem.ID,
				Attempt: latest.Attempt,
				Summary: strings.TrimSpace(latest.Completion.Summary),
			},
		}
	}
	return fmt.Errorf(
		"latest run attempt %d for task %s is %q, expected %q with a main-mode completion block",
		latest.Attempt,
		taskItem.ID,
		latest.Status,
		taskstate.RunStatusSucceeded,
	)
}

func validateFeatureBranchTarget(target tasktarget.Target, taskID string) error {
	if !isFeatureBranchTarget(target.Kind) {
		return fmt.Errorf("task %s is not a feature-branch publication target", taskID)
	}
	return nil
}

func validateFeatureBranchTaskStatus(taskItem task.Task, finalization taskstate.Finalization) error {
	if taskItem.Status == task.StatusClosed {
		if finalization.IntegrationFlow == publication.IntegrationFlowDirectMerge &&
			strings.TrimSpace(finalization.MergeCommit) != "" && finalization.PushedAt != nil {
			return nil
		}
		return fmt.Errorf("task %s is closed; feature-branch publication requires an open backend task", taskItem.ID)
	}
	if taskItem.Status != task.StatusInProgress {
		return fmt.Errorf("task %s is %s, expected in_progress for feature-branch publication", taskItem.ID, formatStatusForFinalization(taskItem.Status))
	}
	return nil
}

func validateFeatureBranchTaskMetadata(taskItem task.Task, state taskstate.TaskState, target tasktarget.Target) error {
	metadata := taskItem.OrpheusMetadata()
	if !metadata.HasPRURL || strings.TrimSpace(metadata.PRURL) == "" {
		return nil
	}
	if featureBranchPRRecordIsMissing(state, metadata.PRURL, target.Branch) {
		return nil
	}
	return fmt.Errorf("task %s already has %s set; use task sync to poll PR review state", taskItem.ID, task.MetadataPRURL)
}

func featureBranchPRRecordIsMissing(state taskstate.TaskState, prURL string, branch string) bool {
	if strings.TrimSpace(prURL) == "" {
		return false
	}
	finalization := taskstate.FinalizationFacts(state)
	if strings.TrimSpace(finalization.Commit) == "" || finalization.PushedAt == nil {
		return false
	}
	for _, event := range state.Events {
		if (event.Type == taskstate.EventPRCreated || event.Type == taskstate.EventPRRecovered) &&
			event.PRURL == prURL && event.Branch == branch {
			return false
		}
	}
	return true
}

func validateFeatureBranchLatestRun(
	taskItem task.Task,
	taskTarget taskstate.GitFacts,
	latest taskstate.RunAttempt,
	target tasktarget.Target,
	allowMissingPRRecord bool,
) error {
	if latest.Completion == nil {
		return fmt.Errorf("latest run attempt %d for task %s has no completion block; run `orpheus agent done` first", latest.Attempt, taskItem.ID)
	}
	if !completedHandoffStatus(latest.Status) {
		return fmt.Errorf(
			"latest run attempt %d for task %s is %q, expected %q with a completion block",
			latest.Attempt,
			taskItem.ID,
			latest.Status,
			taskstate.RunStatusSucceeded,
		)
	}
	if strings.TrimSpace(taskTarget.Branch) != target.Branch {
		return fmt.Errorf(
			"task %s taskstate target branch is %q, expected task branch %q",
			taskItem.ID,
			taskTarget.Branch,
			target.Branch,
		)
	}
	if err := validateTaskTargetWorktree(target.Worktree, "task worktree", taskItem.ID, taskTarget); err != nil {
		return err
	}
	if allowMissingPRRecord {
		return nil
	}
	if _, ok := ClassifyExpectedPRReviewReady(
		expectedTargetsForFeatureBranchTarget(target),
		taskItem,
		taskTarget,
		&latest,
	); !ok {
		return fmt.Errorf("latest run attempt %d for task %s is not a PR-ready feature-branch completion", latest.Attempt, taskItem.ID)
	}
	return nil
}

func isFeatureBranchTarget(kind tasktarget.TargetKind) bool {
	return kind == tasktarget.TargetWorktreeTeam || kind == tasktarget.TargetRepoRootTeam
}

func expectedTargetsForFeatureBranchTarget(target tasktarget.Target) tasktarget.ExpectedTargets {
	if target.Kind == tasktarget.TargetRepoRootTeam {
		return tasktarget.ExpectedTargets{RepoRootTeam: target}
	}
	return tasktarget.ExpectedTargets{WorktreeTeam: target}
}

func finalizationMessageParts(completion *taskstate.Completion, opts FinalizeOptions) (string, string, error) {
	if completion == nil {
		return "", "", errors.New("completion is required")
	}
	summary := strings.TrimSpace(opts.Summary)
	if summary == "" {
		summary = strings.TrimSpace(completion.Summary)
	}
	description := strings.TrimSpace(opts.Description)
	if description == "" {
		description = strings.TrimSpace(completion.Description)
	}
	if summary == "" {
		return "", "", errors.New("finalization summary is required")
	}
	if description == "" {
		return "", "", errors.New("finalization description is required")
	}
	return summary, description, nil
}

func currentDirectory(cwd string) (string, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current directory: %w", err)
		}
	}
	return cleanAbsPath("current directory", cwd)
}

func taskIDs(tasks []task.Task) []string {
	ids := make([]string, 0, len(tasks))
	for _, taskItem := range tasks {
		ids = append(ids, taskItem.ID)
	}
	return ids
}

func formatStatusForFinalization(status task.Status) string {
	statusText := strings.TrimSpace(string(status))
	if statusText == "" {
		return "unknown"
	}
	return statusText
}
