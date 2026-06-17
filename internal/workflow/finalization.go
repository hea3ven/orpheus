package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

const finalizationLockOperation = "task finalization"

// FinalizationBackend is the backend capability set needed to finalize a task.
type FinalizationBackend interface {
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
	RecordFinalizationCommit(repoID, taskID string, commit string) (taskstate.Finalization, error)
	RecordFinalizationPush(repoID, taskID string) (taskstate.Finalization, error)
	RecordFinalizationClose(repoID, taskID string) (taskstate.Finalization, error)
}

// FinalizationGit performs the Git operations used by task finalization.
type FinalizationGit interface {
	CurrentBranch(ctx context.Context, dir string) (string, error)
	HasWorkingTreeChanges(ctx context.Context, dir string) (bool, error)
	HeadCommit(ctx context.Context, dir string) (string, error)
	StageAll(ctx context.Context, dir string) error
	Commit(ctx context.Context, dir string, message string) (string, error)
	PushDefaultBranch(ctx context.Context, dir string, branch string) error
	PushTaskBranch(ctx context.Context, dir string, branch string) error
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

// FinalizationService finalizes reviewed main/solo task work.
type FinalizationService struct {
	Paths          state.Paths
	Sources        []task.RepositorySource
	BackendFactory FinalizationBackendFactory
	RunStore       FinalizationRunStore
	Git            FinalizationGit
	PRProvider     pullrequest.Provider
}

// FinalizeOptions are the CLI-provided finalization controls.
type FinalizeOptions struct {
	TaskID                string
	CWD                   string
	Summary               string
	Description           string
	AllowRunningCompleted bool
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

type finalizationContext struct {
	latest       taskstate.RunAttempt
	finalization taskstate.Finalization
}

// Finalize commits reviewed repo-root changes, pushes the default branch, and
// closes the backend task after the push has succeeded.
func (s FinalizationService) Finalize(ctx context.Context, opts FinalizeOptions) (FinalizationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.BackendFactory == nil {
		return FinalizationResult{}, errors.New("task finalization backend factory is required")
	}
	if s.RunStore == nil {
		return FinalizationResult{}, errors.New("task finalization run store is required")
	}
	gitState := s.Git
	if gitState == nil {
		gitState = LocalFinalizationGit{}
	}

	var result FinalizationResult
	err := state.WithGlobalMutationLock(s.Paths, finalizationLockOperation, func() error {
		finalized, err := s.finalizeLocked(ctx, opts, gitState)
		if err != nil {
			return err
		}
		result = finalized
		return nil
	})
	if err != nil {
		return FinalizationResult{}, err
	}
	return result, nil
}

func (s FinalizationService) finalizeLocked(
	ctx context.Context,
	opts FinalizeOptions,
	gitState FinalizationGit,
) (FinalizationResult, error) {
	target, err := s.resolveTarget(ctx, opts)
	if err != nil {
		return FinalizationResult{}, err
	}
	repo := target.source.Repository

	finalizeCtx, err := s.loadFinalizationContext(repo, target.task)
	if err != nil {
		return FinalizationResult{}, err
	}
	targets, err := ExpectedTargetsForTask(repo, target.task.ID, s.Paths)
	if err != nil {
		return FinalizationResult{}, err
	}
	metadataTarget, err := ClassifyMetadataTarget(target.task.OrpheusMetadata(), targets)
	if err != nil {
		return FinalizationResult{}, err
	}

	if metadataTarget.Kind == TargetWorktreeTeam {
		return s.publishFeatureBranch(ctx, target, finalizeCtx, metadataTarget, gitState)
	}

	return s.finalizeDefaultBranch(ctx, opts, target, finalizeCtx, metadataTarget, gitState)
}

func (s FinalizationService) finalizeDefaultBranch(
	ctx context.Context,
	opts FinalizeOptions,
	target finalizationTarget,
	finalizeCtx finalizationContext,
	metadataTarget Target,
	gitState FinalizationGit,
) (FinalizationResult, error) {
	repo := target.source.Repository
	if metadataTarget.Kind != TargetMainSolo {
		return FinalizationResult{}, fmt.Errorf("task %s target %q cannot be finalized by task done", target.task.ID, metadataTarget.Kind)
	}
	var pendingConfirmation *RunningCompletionConfirmationError
	if err := validateDefaultBranchFinalizationReady(repo, target.task, finalizeCtx, opts.AllowRunningCompleted); err != nil {
		if errors.As(err, &pendingConfirmation) {
			if pendingConfirmation == nil {
				return FinalizationResult{}, err
			}
		} else {
			return FinalizationResult{}, err
		}
	}

	currentBranch, err := gitState.CurrentBranch(ctx, repo.Path)
	if err != nil {
		return FinalizationResult{}, fmt.Errorf("inspect current Git branch: %w", err)
	}
	if currentBranch != repo.DefaultBranch {
		return FinalizationResult{}, fmt.Errorf(
			"repo root %q is on branch %q, expected registered default branch %q",
			repo.Path,
			currentBranch,
			repo.DefaultBranch,
		)
	}

	summary, description, err := finalizationMessageParts(finalizeCtx.latest.Completion, opts)
	if err != nil {
		return FinalizationResult{}, err
	}
	message := summary + "\n\n" + description

	hasChanges, err := gitState.HasWorkingTreeChanges(ctx, repo.Path)
	if err != nil {
		return FinalizationResult{}, fmt.Errorf("inspect repo-root changes: %w", err)
	}

	finalization := finalizeCtx.finalization
	if strings.TrimSpace(finalization.Commit) != "" {
		if hasChanges {
			return FinalizationResult{}, fmt.Errorf(
				"task %s already has finalization commit %s recorded, but repo root %q has new uncommitted changes; "+
					"M4 will not create a second finalization commit, so stash, commit manually outside Orpheus, or remove the extra changes before retrying",
				target.task.ID,
				finalization.Commit,
				repo.Path,
			)
		}
		head, err := gitState.HeadCommit(ctx, repo.Path)
		if err != nil {
			return FinalizationResult{}, fmt.Errorf("verify recorded finalization commit: %w", err)
		}
		if head != finalization.Commit {
			return FinalizationResult{}, fmt.Errorf(
				"recorded finalization commit is %s, but current HEAD is %s; M4 will not infer or repair manually committed states",
				finalization.Commit,
				head,
			)
		}
	} else {
		if !hasChanges {
			return FinalizationResult{}, fmt.Errorf(
				"repo root %q has no changes to commit and task %s has no recorded finalization commit; "+
					"review or adjust the repo-root changes before running task done, or pass the task id after repairing state manually",
				repo.Path,
				target.task.ID,
			)
		}
		if pendingConfirmation != nil {
			return FinalizationResult{}, pendingConfirmation
		}
		if err := gitState.StageAll(ctx, repo.Path); err != nil {
			return FinalizationResult{}, err
		}
		commit, err := gitState.Commit(ctx, repo.Path, message)
		if err != nil {
			return FinalizationResult{}, err
		}
		finalization, err = s.RunStore.RecordFinalizationCommit(repo.ID, target.task.ID, commit)
		if err != nil {
			return FinalizationResult{}, fmt.Errorf("record finalization commit: %w", err)
		}
	}

	if pendingConfirmation != nil {
		return FinalizationResult{}, pendingConfirmation
	}

	if finalization.PushedAt == nil {
		if err := gitState.PushDefaultBranch(ctx, repo.Path, repo.DefaultBranch); err != nil {
			return FinalizationResult{}, err
		}
		finalization, err = s.RunStore.RecordFinalizationPush(repo.ID, target.task.ID)
		if err != nil {
			return FinalizationResult{}, fmt.Errorf("record finalization push: %w", err)
		}
	}

	if finalization.ClosedAt == nil {
		if target.task.Status != task.StatusClosed {
			if err := target.backend.Close(ctx, target.task.ID); err != nil {
				return FinalizationResult{}, err
			}
		}
		finalization, err = s.RunStore.RecordFinalizationClose(repo.ID, target.task.ID)
		if err != nil {
			return FinalizationResult{}, fmt.Errorf("record finalization close: %w", err)
		}
	}

	return FinalizationResult{
		Repository:   repo,
		Task:         target.task.Clone(),
		Finalization: finalization,
		Branch:       repo.DefaultBranch,
	}, nil
}

func (s FinalizationService) publishFeatureBranch(
	ctx context.Context,
	target finalizationTarget,
	finalizeCtx finalizationContext,
	metadataTarget Target,
	gitState FinalizationGit,
) (FinalizationResult, error) {
	repo := target.source.Repository
	if err := validateFeatureBranchPublicationReady(repo, target.task, finalizeCtx, metadataTarget); err != nil {
		return FinalizationResult{}, err
	}
	if s.PRProvider == nil {
		return FinalizationResult{}, errors.New("task done PR provider is required")
	}

	currentBranch, err := gitState.CurrentBranch(ctx, metadataTarget.Worktree)
	if err != nil {
		return FinalizationResult{}, fmt.Errorf("inspect current Git branch: %w", err)
	}
	if currentBranch != metadataTarget.Branch {
		return FinalizationResult{}, fmt.Errorf(
			"task worktree %q is on branch %q, expected task branch %q",
			metadataTarget.Worktree,
			currentBranch,
			metadataTarget.Branch,
		)
	}

	summary, description, err := finalizationMessageParts(finalizeCtx.latest.Completion, FinalizeOptions{})
	if err != nil {
		return FinalizationResult{}, err
	}
	message := summary + "\n\n" + description

	hasChanges, err := gitState.HasWorkingTreeChanges(ctx, metadataTarget.Worktree)
	if err != nil {
		return FinalizationResult{}, fmt.Errorf("inspect task worktree changes: %w", err)
	}

	finalization := finalizeCtx.finalization
	if strings.TrimSpace(finalization.Commit) != "" {
		if hasChanges {
			return FinalizationResult{}, fmt.Errorf(
				"task %s already has finalization commit %s recorded, but task worktree %q has new uncommitted changes; "+
					"task done will not create a second publication commit, so stash, commit manually outside Orpheus, or remove the extra changes before retrying",
				target.task.ID,
				finalization.Commit,
				metadataTarget.Worktree,
			)
		}
		head, err := gitState.HeadCommit(ctx, metadataTarget.Worktree)
		if err != nil {
			return FinalizationResult{}, fmt.Errorf("verify recorded publication commit: %w", err)
		}
		if head != finalization.Commit {
			return FinalizationResult{}, fmt.Errorf(
				"recorded publication commit is %s, but current HEAD is %s; task done will not infer or repair manually committed states",
				finalization.Commit,
				head,
			)
		}
	} else {
		if !hasChanges {
			return FinalizationResult{}, fmt.Errorf(
				"task worktree %q has no reviewed local changes to commit for task %s; "+
					"review or adjust the feature-branch changes before running task done",
				metadataTarget.Worktree,
				target.task.ID,
			)
		}
		if err := gitState.StageAll(ctx, metadataTarget.Worktree); err != nil {
			return FinalizationResult{}, err
		}
		commit, err := gitState.Commit(ctx, metadataTarget.Worktree, message)
		if err != nil {
			return FinalizationResult{}, err
		}
		finalization, err = s.RunStore.RecordFinalizationCommit(repo.ID, target.task.ID, commit)
		if err != nil {
			return FinalizationResult{}, fmt.Errorf("record publication commit: %w", err)
		}
	}

	if finalization.PushedAt == nil {
		if err := gitState.PushTaskBranch(ctx, metadataTarget.Worktree, metadataTarget.Branch); err != nil {
			return FinalizationResult{}, err
		}
		finalization, err = s.RunStore.RecordFinalizationPush(repo.ID, target.task.ID)
		if err != nil {
			return FinalizationResult{}, fmt.Errorf("record publication push: %w", err)
		}
	}

	baseBranch := strings.TrimSpace(repo.DefaultBranch)
	found, ok, err := s.PRProvider.FindOpenByBranch(ctx, pullrequest.FindOpenByBranchRequest{
		RepositoryPath: repo.Path,
		HeadBranch:     metadataTarget.Branch,
		BaseBranch:     baseBranch,
	})
	if err != nil {
		return FinalizationResult{}, err
	}
	prRecovered := ok
	prURL := ""
	if ok {
		prURL = strings.TrimSpace(found.URL)
	} else {
		content, err := BuildSyncPullRequestContent(target.task, finalizeCtx.latest)
		if err != nil {
			return FinalizationResult{}, err
		}
		created, err := s.PRProvider.Create(ctx, pullrequest.CreateRequest{
			RepositoryPath: repo.Path,
			HeadBranch:     metadataTarget.Branch,
			BaseBranch:     baseBranch,
			Title:          content.Title,
			Body:           content.Body,
		})
		if err != nil {
			return FinalizationResult{}, err
		}
		prURL = strings.TrimSpace(created.URL)
	}
	if err := target.backend.SetPRURL(ctx, target.task.ID, prURL); err != nil {
		return FinalizationResult{}, err
	}

	updated := target.task.Clone()
	if updated.Metadata == nil {
		updated.Metadata = task.Metadata{}
	}
	updated.Metadata[task.MetadataPRURL] = prURL
	return FinalizationResult{
		Repository:   repo,
		Task:         updated,
		Finalization: finalization,
		Branch:       metadataTarget.Branch,
		PRURL:        prURL,
		PRRecovered:  prRecovered,
	}, nil
}

func (s FinalizationService) resolveTarget(ctx context.Context, opts FinalizeOptions) (finalizationTarget, error) {
	taskID := strings.TrimSpace(opts.TaskID)
	if taskID == "" {
		return s.inferTarget(ctx, opts)
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

func (s FinalizationService) inferTarget(ctx context.Context, opts FinalizeOptions) (finalizationTarget, error) {
	normalizedCWD, err := currentDirectory(opts.CWD)
	if err != nil {
		return finalizationTarget{}, err
	}

	matches := make([]task.RepositorySource, 0, 1)
	for _, source := range s.Sources {
		repoPath, err := cleanAbsPath("registered repo root", source.Repository.Path)
		if err != nil {
			return finalizationTarget{}, err
		}
		if repoPath == normalizedCWD {
			matches = append(matches, source)
		}
	}
	if len(matches) == 0 {
		return finalizationTarget{}, fmt.Errorf(
			"cannot infer task to finalize from current directory %q: cwd must be exactly a registered repo root; pass <task-id>",
			normalizedCWD,
		)
	}
	if len(matches) > 1 {
		return finalizationTarget{}, fmt.Errorf(
			"cannot infer task to finalize from current directory %q: multiple registered repos use this root; pass <task-id>",
			normalizedCWD,
		)
	}

	source := matches[0]
	backend, err := s.BackendFactory(source)
	if err != nil {
		return finalizationTarget{}, fmt.Errorf(
			"task done: create backend for repo %s (%s; prefix %s): %w",
			source.Repository.ID,
			source.Repository.Name,
			source.Repository.TaskIDPrefix,
			err,
		)
	}
	tasks, err := backend.List(ctx)
	if err != nil {
		return finalizationTarget{}, fmt.Errorf(
			"task done: query repo %s (%s; prefix %s) while inferring task: %w",
			source.Repository.ID,
			source.Repository.Name,
			source.Repository.TaskIDPrefix,
			err,
		)
	}

	candidates := make([]task.Task, 0, 1)
	for _, taskItem := range tasks {
		ok, err := s.isInferableMainLocalReady(source.Repository, taskItem, false)
		if err != nil {
			return finalizationTarget{}, err
		}
		if ok {
			candidates = append(candidates, taskItem.Clone())
			continue
		}
		if !opts.AllowRunningCompleted {
			ok, err = s.isInferableMainLocalReady(source.Repository, taskItem, true)
			if err != nil {
				return finalizationTarget{}, err
			}
			if ok {
				candidates = append(candidates, taskItem.Clone())
			}
			continue
		}
		ok, err = s.isInferableMainLocalReady(source.Repository, taskItem, true)
		if err != nil {
			return finalizationTarget{}, err
		}
		if ok {
			candidates = append(candidates, taskItem.Clone())
		}
	}
	switch len(candidates) {
	case 1:
		return finalizationTarget{source: source, backend: backend, task: candidates[0]}, nil
	case 0:
		return finalizationTarget{}, fmt.Errorf(
			"cannot infer task to finalize from repo root %q: no non-closed main/solo local-ready task owns the registered root/default branch; pass <task-id>",
			normalizedCWD,
		)
	default:
		return finalizationTarget{}, fmt.Errorf(
			"cannot infer task to finalize from repo root %q: multiple non-closed main/solo local-ready tasks own the registered root/default branch (%s); pass <task-id>",
			normalizedCWD,
			strings.Join(taskIDs(candidates), ", "),
		)
	}
}

func (s FinalizationService) isInferableMainLocalReady(repo task.Repository, taskItem task.Task, allowRunningCompleted bool) (bool, error) {
	if taskItem.Status == task.StatusClosed {
		return false, nil
	}
	state, err := s.RunStore.Load(repo.ID, taskItem.ID)
	if err != nil {
		return false, fmt.Errorf("load task state for %s/%s: %w", repo.ID, taskItem.ID, err)
	}
	latest, ok := taskstate.LatestRun(state)
	if !ok {
		return false, nil
	}
	ctx := finalizationContext{
		latest:       latest,
		finalization: taskstate.FinalizationFacts(state),
	}
	targets, err := ExpectedTargetsForTask(repo, taskItem.ID, s.Paths)
	if err != nil {
		return false, err
	}
	target, err := ClassifyMetadataTarget(taskItem.OrpheusMetadata(), targets)
	if err != nil || target.Kind != TargetMainSolo {
		return false, nil
	}
	if err := validateDefaultBranchFinalizationReady(repo, taskItem, ctx, allowRunningCompleted); err != nil {
		return false, nil
	}
	return true, nil
}

func (s FinalizationService) loadFinalizationContext(repo task.Repository, taskItem task.Task) (finalizationContext, error) {
	state, err := s.RunStore.Load(repo.ID, taskItem.ID)
	if err != nil {
		return finalizationContext{}, fmt.Errorf("load task state for %s/%s: %w", repo.ID, taskItem.ID, err)
	}
	latest, ok := taskstate.LatestRun(state)
	if !ok {
		return finalizationContext{}, fmt.Errorf("task %s has no Orpheus run attempts; run `orpheus task run --main %s` first", taskItem.ID, taskItem.ID)
	}
	return finalizationContext{
		latest:       latest,
		finalization: taskstate.FinalizationFacts(state),
	}, nil
}

func validateDefaultBranchFinalizationReady(
	repo task.Repository,
	taskItem task.Task,
	ctx finalizationContext,
	allowRunningCompleted bool,
) error {
	if strings.TrimSpace(repo.ID) == "" {
		return errors.New("repo id is required")
	}
	repoRoot, err := cleanAbsPath("registered repo root", repo.Path)
	if err != nil {
		return err
	}
	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	if defaultBranch == "" {
		return fmt.Errorf("repo %q has no registered default branch", repo.ID)
	}

	switch taskItem.Status {
	case task.StatusInProgress:
	case task.StatusClosed:
		if strings.TrimSpace(ctx.finalization.Commit) == "" {
			return fmt.Errorf("task %s is closed and has no recorded finalization commit; refusing to infer manual finalization", taskItem.ID)
		}
	default:
		return fmt.Errorf("task %s is %s, expected in_progress for main/solo finalization", taskItem.ID, formatStatusForFinalization(taskItem.Status))
	}

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
	metadataWorktree, err := cleanAbsPath(task.MetadataWorktree, metadata.Worktree)
	if !metadata.HasWorktree || err != nil || metadataWorktree != repoRoot {
		if err != nil && metadata.HasWorktree {
			return fmt.Errorf("task %s metadata %s is invalid: %w", taskItem.ID, task.MetadataWorktree, err)
		}
		return fmt.Errorf(
			"task %s metadata %s is %q, expected registered repo root %q",
			taskItem.ID,
			task.MetadataWorktree,
			metadata.Worktree,
			repoRoot,
		)
	}

	latest := ctx.latest
	if latest.Completion == nil {
		return fmt.Errorf("latest run attempt %d for task %s has no main-mode completion block; run `orpheus agent done` first", latest.Attempt, taskItem.ID)
	}
	if strings.TrimSpace(latest.Branch) != defaultBranch {
		return fmt.Errorf(
			"latest run attempt %d for task %s branch is %q, expected registered default branch %q",
			latest.Attempt,
			taskItem.ID,
			latest.Branch,
			defaultBranch,
		)
	}
	runWorktree, err := cleanAbsPath("latest run worktree", latest.Worktree)
	if err != nil {
		return err
	}
	if runWorktree != repoRoot {
		return fmt.Errorf(
			"latest run attempt %d for task %s worktree is %q, expected registered repo root %q",
			latest.Attempt,
			taskItem.ID,
			latest.Worktree,
			repoRoot,
		)
	}
	classificationRun := latest
	if latest.Status != taskstate.RunStatusSucceeded {
		classificationRun.Status = taskstate.RunStatusSucceeded
	}
	localTarget := Target{Kind: TargetMainSolo, Branch: defaultBranch, Worktree: repoRoot}
	if _, ok := ClassifyExpectedLocalReviewReady(ExpectedTargets{MainSolo: localTarget}, taskItem, &classificationRun); !ok {
		return fmt.Errorf("latest run attempt %d for task %s is not a main/solo local-ready completion", latest.Attempt, taskItem.ID)
	}
	if latest.Status != taskstate.RunStatusSucceeded {
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
	return nil
}

func validateFeatureBranchPublicationReady(
	repo task.Repository,
	taskItem task.Task,
	ctx finalizationContext,
	target Target,
) error {
	if strings.TrimSpace(repo.ID) == "" {
		return errors.New("repo id is required")
	}
	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	if defaultBranch == "" {
		return fmt.Errorf("repo %q has no registered default branch", repo.ID)
	}
	if target.Kind != TargetWorktreeTeam {
		return fmt.Errorf("task %s is not a feature-branch publication target", taskItem.ID)
	}
	if taskItem.Status == task.StatusClosed {
		return fmt.Errorf("task %s is closed; feature-branch publication requires an open backend task", taskItem.ID)
	}
	if taskItem.Status != task.StatusInProgress {
		return fmt.Errorf("task %s is %s, expected in_progress for feature-branch publication", taskItem.ID, formatStatusForFinalization(taskItem.Status))
	}

	metadata := taskItem.OrpheusMetadata()
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
		return fmt.Errorf("task %s already has %s set; use task sync to poll PR review state", taskItem.ID, task.MetadataPRURL)
	}

	latest := ctx.latest
	if latest.Completion == nil {
		return fmt.Errorf("latest run attempt %d for task %s has no completion block; run `orpheus agent done` first", latest.Attempt, taskItem.ID)
	}
	if latest.Status != taskstate.RunStatusSucceeded {
		return fmt.Errorf(
			"latest run attempt %d for task %s is %q, expected %q with a completion block",
			latest.Attempt,
			taskItem.ID,
			latest.Status,
			taskstate.RunStatusSucceeded,
		)
	}
	if strings.TrimSpace(latest.Branch) != target.Branch {
		return fmt.Errorf(
			"latest run attempt %d for task %s branch is %q, expected task branch %q",
			latest.Attempt,
			taskItem.ID,
			latest.Branch,
			target.Branch,
		)
	}
	runWorktree, err := cleanAbsPath("latest run worktree", latest.Worktree)
	if err != nil {
		return err
	}
	if runWorktree != target.Worktree {
		return fmt.Errorf(
			"latest run attempt %d for task %s worktree is %q, expected task worktree %q",
			latest.Attempt,
			taskItem.ID,
			latest.Worktree,
			target.Worktree,
		)
	}
	if _, ok := ClassifyExpectedPRReviewReady(ExpectedTargets{WorktreeTeam: target}, taskItem, &latest); !ok {
		return fmt.Errorf("latest run attempt %d for task %s is not a worktree/team PR-ready completion", latest.Attempt, taskItem.ID)
	}
	return nil
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
