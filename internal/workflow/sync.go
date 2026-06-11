package workflow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

const syncLockOperation = "task sync"

// SyncBackendFactory creates a sync-capable backend for one repository.
type SyncBackendFactory func(task.RepositorySource) (task.SyncBackend, error)

// SyncRunStore reads task execution facts needed by sync.
type SyncRunStore interface {
	Load(repoID, taskID string) (taskstate.TaskState, error)
}

// SyncGit performs the Git operation used by task sync.
type SyncGit interface {
	PushTaskBranch(ctx context.Context, dir string, branch string) error
}

// LocalSyncGit delegates sync Git operations to the local git binary.
type LocalSyncGit struct{}

// PushTaskBranch pushes branch to origin.
func (LocalSyncGit) PushTaskBranch(ctx context.Context, dir string, branch string) error {
	return gitmeta.PushTaskBranch(ctx, dir, branch)
}

// SyncService pushes PR-ready task branches.
type SyncService struct {
	Paths          state.Paths
	Sources        []task.RepositorySource
	BackendFactory SyncBackendFactory
	RunStore       SyncRunStore
	Git            SyncGit
	PRProvider     pullrequest.Provider
}

// SyncOptions are the CLI-provided sync controls.
type SyncOptions struct {
	TaskID string
}

// SyncStatus describes the outcome of a single-task sync.
type SyncStatus string

const (
	// SyncStatusPRCreated means the task branch was pushed and a new PR was created.
	SyncStatusPRCreated SyncStatus = "pr_created"

	// SyncStatusPRRecovered means the task branch was pushed and an existing PR was recovered.
	SyncStatusPRRecovered SyncStatus = "pr_recovered"

	// SyncStatusAlreadyInReview means the task already had a local PR URL recorded.
	SyncStatusAlreadyInReview SyncStatus = "already_in_review"

	// SyncStatusSkipped means the task was resolvable but not branch-push eligible.
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

type syncTarget struct {
	source  task.RepositorySource
	backend task.SyncBackend
	task    task.Task
}

// Sync resolves one task, skips non-eligible states, and pushes eligible task branches.
func (s SyncService) Sync(ctx context.Context, opts SyncOptions) (SyncResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.BackendFactory == nil {
		return SyncResult{}, errors.New("task sync backend factory is required")
	}
	if s.RunStore == nil {
		return SyncResult{}, errors.New("task sync run store is required")
	}
	if s.PRProvider == nil {
		return SyncResult{}, errors.New("task sync PR provider is required")
	}
	gitState := s.Git
	if gitState == nil {
		gitState = LocalSyncGit{}
	}

	var result SyncResult
	err := state.WithGlobalMutationLock(s.Paths, syncLockOperation, func() error {
		synced, err := s.syncLocked(ctx, opts, gitState)
		if err != nil {
			return err
		}
		result = synced
		return nil
	})
	if err != nil {
		return SyncResult{}, err
	}
	return result, nil
}

func (s SyncService) syncLocked(ctx context.Context, opts SyncOptions, gitState SyncGit) (SyncResult, error) {
	target, err := s.resolveTarget(ctx, opts)
	if err != nil {
		return SyncResult{}, err
	}

	if result, ok := s.alreadyInReview(target); ok {
		return result, nil
	}

	taskState, err := s.RunStore.Load(target.source.Repository.ID, target.task.ID)
	if err != nil {
		return SyncResult{}, fmt.Errorf("load task state for %s/%s: %w", target.source.Repository.ID, target.task.ID, err)
	}
	latest, ok := taskstate.LatestRun(taskState)
	if !ok {
		return s.skip(target, taskstate.RunAttempt{}, "task has no Orpheus run attempts"), nil
	}

	targets, err := ExpectedTargetsForTask(target.source.Repository, target.task.ID, s.Paths)
	if err != nil {
		return SyncResult{}, err
	}
	eligible, reason, err := syncEligibility(target.source.Repository, target.task, latest, targets)
	if err != nil {
		return SyncResult{}, err
	}
	if !eligible {
		return s.skip(target, latest, reason), nil
	}

	branch := strings.TrimSpace(target.task.OrpheusMetadata().Branch)
	if err := gitState.PushTaskBranch(ctx, target.source.Repository.Path, branch); err != nil {
		return SyncResult{}, err
	}

	baseBranch := strings.TrimSpace(target.source.Repository.DefaultBranch)
	found, ok, err := s.PRProvider.FindOpenByBranch(ctx, pullrequest.FindOpenByBranchRequest{
		RepositoryPath: target.source.Repository.Path,
		HeadBranch:     branch,
		BaseBranch:     baseBranch,
	})
	if err != nil {
		return SyncResult{}, err
	}
	if ok {
		prURL := strings.TrimSpace(found.URL)
		if err := target.backend.SetPRURL(ctx, target.task.ID, prURL); err != nil {
			return SyncResult{}, err
		}
		return s.synced(target, latest, SyncStatusPRRecovered, branch, prURL), nil
	}

	content, err := BuildSyncPullRequestContent(target.task, latest)
	if err != nil {
		return SyncResult{}, err
	}
	created, err := s.PRProvider.Create(ctx, pullrequest.CreateRequest{
		RepositoryPath: target.source.Repository.Path,
		HeadBranch:     branch,
		BaseBranch:     baseBranch,
		Title:          content.Title,
		Body:           content.Body,
	})
	if err != nil {
		return SyncResult{}, err
	}
	prURL := strings.TrimSpace(created.URL)
	if err := target.backend.SetPRURL(ctx, target.task.ID, prURL); err != nil {
		return SyncResult{}, err
	}
	return s.synced(target, latest, SyncStatusPRCreated, branch, prURL), nil
}

func (s SyncService) synced(
	target syncTarget,
	latest taskstate.RunAttempt,
	status SyncStatus,
	branch string,
	prURL string,
) SyncResult {
	updated := target.task.Clone()
	if updated.Metadata == nil {
		updated.Metadata = task.Metadata{}
	}
	updated.Metadata[task.MetadataPRURL] = prURL
	return SyncResult{
		Repository: target.source.Repository,
		Task:       updated,
		LatestRun:  latest,
		Status:     status,
		Branch:     branch,
		Worktree:   strings.TrimSpace(target.task.OrpheusMetadata().Worktree),
		PRURL:      prURL,
	}
}

func (s SyncService) alreadyInReview(target syncTarget) (SyncResult, bool) {
	metadata := target.task.OrpheusMetadata()
	prURL := strings.TrimSpace(metadata.PRURL)
	if !metadata.HasPRURL || prURL == "" {
		return SyncResult{}, false
	}
	return SyncResult{
		Repository: target.source.Repository,
		Task:       target.task.Clone(),
		Status:     SyncStatusAlreadyInReview,
		Reason:     task.MetadataPRURL + " is already set",
		Branch:     strings.TrimSpace(metadata.Branch),
		Worktree:   strings.TrimSpace(target.task.OrpheusMetadata().Worktree),
		PRURL:      prURL,
	}, true
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

func syncEligibility(
	repo task.Repository,
	taskItem task.Task,
	latest taskstate.RunAttempt,
	targets ExpectedTargets,
) (bool, string, error) {
	metadata := taskItem.OrpheusMetadata()
	prURLSet := metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != ""
	if taskItem.Status == task.StatusClosed {
		return false, "task is closed", nil
	}
	if prURLSet {
		return false, task.MetadataPRURL + " is already set", nil
	}

	switch latest.Status {
	case taskstate.RunStatusRunning:
		return false, fmt.Sprintf("latest run attempt %d is still running", latest.Attempt), nil
	case taskstate.RunStatusFailed:
		return false, fmt.Sprintf("latest run attempt %d failed", latest.Attempt), nil
	case taskstate.RunStatusSucceeded:
	default:
		return false, fmt.Sprintf("latest run attempt %d has unsupported status %q", latest.Attempt, latest.Status), nil
	}

	if latest.Completion == nil {
		return false, fmt.Sprintf("latest run attempt %d succeeded without a completion block", latest.Attempt), nil
	}

	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	repoRoot, err := cleanAbsPath("registered repo root", repo.Path)
	if err != nil {
		return false, "", err
	}
	if defaultBranch == "" {
		return false, "", fmt.Errorf("repo %q has no registered default branch", repo.ID)
	}
	latestBranch := strings.TrimSpace(latest.Branch)
	runWorktree, err := cleanAbsPath("latest run worktree", latest.Worktree)
	if err != nil {
		return false, "", err
	}
	runTarget := ClassifyRunTarget(repo, latest.Branch, latest.Worktree)
	if runTarget == TargetMainSolo {
		return false, "latest run is a main/solo local-review-ready run; use `orpheus task done`", nil
	}
	if latestBranch == defaultBranch {
		return false, "latest run is on the registered default branch, not a worktree/team task branch", nil
	}
	if runWorktree == repoRoot {
		return false, "latest run worktree is the registered repo root, not a worktree/team task worktree", nil
	}
	if strings.TrimSpace(latest.Completion.CommitError) != "" {
		return false, fmt.Sprintf("completion commit failed: %s", strings.TrimSpace(latest.Completion.CommitError)), nil
	}
	if strings.TrimSpace(latest.Completion.Commit) == "" {
		return false, "completion commit is missing", nil
	}
	if !metadata.HasBranch || strings.TrimSpace(metadata.Branch) == "" {
		return false, "", fmt.Errorf("task %s metadata %s is missing", taskItem.ID, task.MetadataBranch)
	}
	if !metadata.HasWorktree || strings.TrimSpace(metadata.Worktree) == "" {
		return false, "", fmt.Errorf("task %s metadata %s is missing", taskItem.ID, task.MetadataWorktree)
	}
	metadataWorktree, err := cleanAbsPath(task.MetadataWorktree, metadata.Worktree)
	if err != nil {
		return false, "", fmt.Errorf("task %s metadata %s is invalid: %w", taskItem.ID, task.MetadataWorktree, err)
	}

	metadataBranch := strings.TrimSpace(metadata.Branch)
	if strings.HasPrefix(metadataBranch, "-") {
		return false, "", fmt.Errorf("task %s metadata %s is unsafe Git branch %q", taskItem.ID, task.MetadataBranch, metadataBranch)
	}
	if metadataBranch != strings.TrimSpace(latest.Branch) {
		return false, "", fmt.Errorf(
			"task %s metadata %s is %q, expected latest run branch %q",
			taskItem.ID,
			task.MetadataBranch,
			metadata.Branch,
			latest.Branch,
		)
	}
	if metadataWorktree != runWorktree {
		return false, "", fmt.Errorf(
			"task %s metadata %s is %q, expected latest run worktree %q",
			taskItem.ID,
			task.MetadataWorktree,
			filepath.Clean(metadata.Worktree),
			runWorktree,
		)
	}
	// MVP limitation: task sync pushes the named task branch but does not yet verify
	// that the recorded completion commit is still the branch HEAD.
	if _, ok := ClassifyExpectedPRReviewReady(targets, taskItem, &latest); !ok {
		return false, "latest run is not a worktree/team PR-ready completion", nil
	}

	return true, "", nil
}

// PullRequestContent is the generated title/body for a task sync PR.
type PullRequestContent struct {
	Title string
	Body  string
}

// BuildSyncPullRequestContent returns PR text from the completion handoff.
func BuildSyncPullRequestContent(taskItem task.Task, latest taskstate.RunAttempt) (PullRequestContent, error) {
	if strings.TrimSpace(taskItem.ID) == "" {
		return PullRequestContent{}, errors.New("task id is required")
	}
	if latest.Completion == nil {
		return PullRequestContent{}, errors.New("completion is required")
	}
	title := singleLine(latest.Completion.Summary)
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

func singleLine(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}
