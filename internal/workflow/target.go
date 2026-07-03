package workflow

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

// TargetKind identifies an Orpheus task workflow target.
type TargetKind string

const (
	// TargetUnknown means a branch/worktree pair does not match a supported target.
	TargetUnknown TargetKind = ""

	// TargetWorktreeTeam means work runs in Orpheus' deterministic task worktree and becomes PR-ready.
	TargetWorktreeTeam TargetKind = "worktree"

	// TargetRepoRootTeam means work runs in the registered repo root on a task branch and becomes PR-ready.
	TargetRepoRootTeam TargetKind = "repo-root"

	// TargetMainSolo means work runs in the registered repo root and becomes local-review-ready.
	TargetMainSolo TargetKind = "main"
)

// DisplayName returns the agent/operator-facing target name.
func (k TargetKind) DisplayName() string {
	switch k {
	case TargetWorktreeTeam:
		return "worktree/team"
	case TargetRepoRootTeam:
		return "repo-root/team"
	case TargetMainSolo:
		return "main/solo"
	default:
		return string(k)
	}
}

// ReviewLifecycle identifies the review step unlocked by a successful completion.
type ReviewLifecycle string

const (
	// ReviewLifecycleUnknown means no supported review lifecycle was classified.
	ReviewLifecycleUnknown ReviewLifecycle = ""

	// ReviewLifecyclePRReady means a worktree/team completion is ready for PR creation/review.
	ReviewLifecyclePRReady ReviewLifecycle = "pr-ready"

	// ReviewLifecycleLocalReady means a main/solo completion is ready for local human review.
	ReviewLifecycleLocalReady ReviewLifecycle = "local-ready"
)

// Target describes a concrete workflow branch/worktree pair.
type Target struct {
	Kind     TargetKind
	Branch   string
	Worktree string
}

// ExpectedTargets describes the supported execution targets for one task.
type ExpectedTargets struct {
	MainSolo     Target
	WorktreeTeam Target
	RepoRootTeam Target
}

// CompletionClassification describes the target and review lifecycle for a completed run.
type CompletionClassification struct {
	Target    Target
	Lifecycle ReviewLifecycle
}

// ExpectedTargetsForTask returns the strict execution targets used when dispatching or validating an active run.
func ExpectedTargetsForTask(repo task.Repository, taskID string, paths state.Paths) (ExpectedTargets, error) {
	repoTarget, err := gitmeta.ExpectedRepoRoot(gitmeta.RepoRootOptions{
		RepoID:        repo.ID,
		RepoName:      repo.Name,
		RepoPath:      repo.Path,
		DefaultBranch: repo.DefaultBranch,
	})
	if err != nil {
		return ExpectedTargets{}, fmt.Errorf("resolve registered repo-root target: %w", err)
	}
	worktreeTarget, err := gitmeta.ExpectedTaskWorktree(gitmeta.TaskWorktreeOptions{
		RepoID:        repo.ID,
		RepoName:      repo.Name,
		RepoPath:      repo.Path,
		DefaultBranch: repo.DefaultBranch,
		TaskID:        taskID,
		Paths:         paths,
	})
	if err != nil {
		return ExpectedTargets{}, fmt.Errorf("resolve deterministic task worktree target: %w", err)
	}
	repoRootTaskTarget, err := gitmeta.ExpectedRepoRootTaskBranch(gitmeta.TaskWorktreeOptions{
		RepoID:        repo.ID,
		RepoName:      repo.Name,
		RepoPath:      repo.Path,
		DefaultBranch: repo.DefaultBranch,
		TaskID:        taskID,
		Paths:         paths,
	})
	if err != nil {
		return ExpectedTargets{}, fmt.Errorf("resolve repo-root task branch target: %w", err)
	}

	return ExpectedTargets{
		MainSolo: Target{
			Kind:     TargetMainSolo,
			Branch:   repoTarget.Branch,
			Worktree: filepath.Clean(repoTarget.WorktreePath),
		},
		WorktreeTeam: Target{
			Kind:     TargetWorktreeTeam,
			Branch:   worktreeTarget.Branch,
			Worktree: filepath.Clean(worktreeTarget.WorktreePath),
		},
		RepoRootTeam: Target{
			Kind:     TargetRepoRootTeam,
			Branch:   repoRootTaskTarget.Branch,
			Worktree: filepath.Clean(repoRootTaskTarget.WorktreePath),
		},
	}, nil
}

// ClassifyMetadataTarget matches Orpheus task metadata against exact expected workflow targets.
func ClassifyMetadataTarget(metadata task.OrpheusMetadata, targets ExpectedTargets) (Target, error) {
	if !metadata.HasBranch || strings.TrimSpace(metadata.Branch) == "" {
		return Target{}, fmt.Errorf("%s is missing", task.MetadataBranch)
	}
	if !metadata.HasWorktree || strings.TrimSpace(metadata.Worktree) == "" {
		return Target{}, fmt.Errorf("%s is missing", task.MetadataWorktree)
	}

	metadataWorktree, err := cleanAbsPath(task.MetadataWorktree, metadata.Worktree)
	if err != nil {
		return Target{}, err
	}
	metadataBranch := strings.TrimSpace(metadata.Branch)

	matchesMain := metadataBranch == targets.MainSolo.Branch && metadataWorktree == targets.MainSolo.Worktree
	matchesWorktree := metadataBranch == targets.WorktreeTeam.Branch &&
		metadataWorktree == targets.WorktreeTeam.Worktree
	matchesRepoRootTeam := metadataBranch == targets.RepoRootTeam.Branch &&
		metadataWorktree == targets.RepoRootTeam.Worktree
	matchCount := 0
	for _, matches := range []bool{matchesMain, matchesWorktree, matchesRepoRootTeam} {
		if matches {
			matchCount++
		}
	}
	switch {
	case matchCount > 1:
		return Target{}, fmt.Errorf(
			"%s and %s match multiple supported execution targets",
			task.MetadataBranch,
			task.MetadataWorktree,
		)
	case matchesMain:
		return targets.MainSolo, nil
	case matchesWorktree:
		return targets.WorktreeTeam, nil
	case matchesRepoRootTeam:
		return targets.RepoRootTeam, nil
	default:
		return Target{}, fmt.Errorf(
			"%s=%q and %s=%q do not match repo-root default target (%s=%q, %s=%q), worktree target (%s=%q, %s=%q), or repo-root task branch target (%s=%q, %s=%q)",
			task.MetadataBranch,
			metadata.Branch,
			task.MetadataWorktree,
			metadata.Worktree,
			task.MetadataBranch,
			targets.MainSolo.Branch,
			task.MetadataWorktree,
			targets.MainSolo.Worktree,
			task.MetadataBranch,
			targets.WorktreeTeam.Branch,
			task.MetadataWorktree,
			targets.WorktreeTeam.Worktree,
			task.MetadataBranch,
			targets.RepoRootTeam.Branch,
			task.MetadataWorktree,
			targets.RepoRootTeam.Worktree,
		)
	}
}

// ClassifyRunTarget classifies a branch/worktree pair using repository-level target rules.
func ClassifyRunTarget(repo task.Repository, branch string, worktree string) TargetKind {
	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	repoRoot := cleanPath(repo.Path)
	branch = strings.TrimSpace(branch)
	worktree = cleanPath(worktree)

	if branch == "" || worktree == "" || defaultBranch == "" || repoRoot == "" {
		return TargetUnknown
	}
	if branch == defaultBranch && worktree == repoRoot {
		return TargetMainSolo
	}
	if branch != defaultBranch && worktree == repoRoot {
		return TargetRepoRootTeam
	}
	if branch != defaultBranch && worktree != repoRoot {
		return TargetWorktreeTeam
	}
	return TargetUnknown
}

// ClassifyCompletionTarget classifies a successful Orpheus completion into its review lifecycle.
func ClassifyCompletionTarget(
	repo task.Repository,
	taskItem task.Task,
	taskTarget taskstate.TaskTarget,
	latestRun *taskstate.RunAttempt,
) (CompletionClassification, bool) {
	if latestRun == nil || latestRun.Status != taskstate.RunStatusSucceeded || latestRun.Completion == nil {
		return CompletionClassification{}, false
	}

	metadata := taskItem.OrpheusMetadata()
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
		return CompletionClassification{}, false
	}
	if !metadata.HasBranch || !metadata.HasWorktree {
		return CompletionClassification{}, false
	}

	branch := strings.TrimSpace(taskTarget.Branch)
	worktree := cleanPath(taskTarget.Worktree)
	if branch == "" || worktree == "" {
		return CompletionClassification{}, false
	}
	if strings.TrimSpace(metadata.Branch) != branch || cleanPath(metadata.Worktree) != worktree {
		return CompletionClassification{}, false
	}

	targetKind := ClassifyRunTarget(repo, branch, worktree)
	switch targetKind {
	case TargetMainSolo:
		return CompletionClassification{
			Target:    Target{Kind: TargetMainSolo, Branch: branch, Worktree: worktree},
			Lifecycle: ReviewLifecycleLocalReady,
		}, true
	case TargetWorktreeTeam:
		return CompletionClassification{
			Target:    Target{Kind: TargetWorktreeTeam, Branch: branch, Worktree: worktree},
			Lifecycle: ReviewLifecyclePRReady,
		}, true
	case TargetRepoRootTeam:
		return CompletionClassification{
			Target:    Target{Kind: TargetRepoRootTeam, Branch: branch, Worktree: worktree},
			Lifecycle: ReviewLifecyclePRReady,
		}, true
	default:
		return CompletionClassification{}, false
	}
}

// ClassifyExpectedCompletionTarget classifies a completed run only when task metadata
// and task-level target facts match one of the exact expected workflow targets.
func ClassifyExpectedCompletionTarget(
	targets ExpectedTargets,
	taskItem task.Task,
	taskTarget taskstate.TaskTarget,
	latestRun *taskstate.RunAttempt,
) (CompletionClassification, bool) {
	if latestRun == nil || latestRun.Status != taskstate.RunStatusSucceeded || latestRun.Completion == nil {
		return CompletionClassification{}, false
	}

	metadata := taskItem.OrpheusMetadata()
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
		return CompletionClassification{}, false
	}

	metadataTarget, err := ClassifyMetadataTarget(metadata, targets)
	if err != nil {
		return CompletionClassification{}, false
	}
	target, err := ClassifyTaskStateTarget(taskTarget, targets)
	if err != nil {
		return CompletionClassification{}, false
	}
	if metadataTarget.Branch != target.Branch || metadataTarget.Worktree != target.Worktree {
		return CompletionClassification{}, false
	}

	switch target.Kind {
	case TargetMainSolo:
		return CompletionClassification{
			Target:    target,
			Lifecycle: ReviewLifecycleLocalReady,
		}, true
	case TargetWorktreeTeam:
		return CompletionClassification{
			Target:    target,
			Lifecycle: ReviewLifecyclePRReady,
		}, true
	case TargetRepoRootTeam:
		return CompletionClassification{
			Target:    target,
			Lifecycle: ReviewLifecyclePRReady,
		}, true
	default:
		return CompletionClassification{}, false
	}
}

// ClassifyLocalReviewReady reports whether a task has a main/solo local-ready completion.
func ClassifyLocalReviewReady(
	repo task.Repository,
	taskItem task.Task,
	taskTarget taskstate.TaskTarget,
	latestRun *taskstate.RunAttempt,
) (CompletionClassification, bool) {
	classification, ok := ClassifyCompletionTarget(repo, taskItem, taskTarget, latestRun)
	return classification, ok && classification.Target.Kind == TargetMainSolo
}

// ClassifyPRReviewReady reports whether a task has a worktree/team PR-ready completion.
func ClassifyPRReviewReady(
	repo task.Repository,
	taskItem task.Task,
	taskTarget taskstate.TaskTarget,
	latestRun *taskstate.RunAttempt,
) (CompletionClassification, bool) {
	classification, ok := ClassifyCompletionTarget(repo, taskItem, taskTarget, latestRun)
	return classification, ok && isPRReviewTarget(classification.Target.Kind)
}

// ClassifyExpectedLocalReviewReady reports whether a task has a strict main/solo local-ready completion.
func ClassifyExpectedLocalReviewReady(
	targets ExpectedTargets,
	taskItem task.Task,
	taskTarget taskstate.TaskTarget,
	latestRun *taskstate.RunAttempt,
) (CompletionClassification, bool) {
	classification, ok := ClassifyExpectedCompletionTarget(targets, taskItem, taskTarget, latestRun)
	return classification, ok && classification.Target.Kind == TargetMainSolo
}

// ClassifyExpectedPRReviewReady reports whether a task has a strict worktree/team PR-ready completion.
func ClassifyExpectedPRReviewReady(
	targets ExpectedTargets,
	taskItem task.Task,
	taskTarget taskstate.TaskTarget,
	latestRun *taskstate.RunAttempt,
) (CompletionClassification, bool) {
	classification, ok := ClassifyExpectedCompletionTarget(targets, taskItem, taskTarget, latestRun)
	return classification, ok && isPRReviewTarget(classification.Target.Kind)
}

func isPRReviewTarget(kind TargetKind) bool {
	return kind == TargetWorktreeTeam || kind == TargetRepoRootTeam
}

// ClassifyTaskStateTarget matches the canonical taskstate target against exact expected workflow targets.
func ClassifyTaskStateTarget(taskTarget taskstate.TaskTarget, targets ExpectedTargets) (Target, error) {
	branch := strings.TrimSpace(taskTarget.Branch)
	if branch == "" {
		return Target{}, errors.New("taskstate target branch is missing")
	}
	worktree, err := cleanAbsPath("taskstate target worktree", taskTarget.Worktree)
	if err != nil {
		return Target{}, err
	}

	switch {
	case branch == targets.MainSolo.Branch && worktree == targets.MainSolo.Worktree:
		return targets.MainSolo, nil
	case branch == targets.WorktreeTeam.Branch && worktree == targets.WorktreeTeam.Worktree:
		return targets.WorktreeTeam, nil
	case branch == targets.RepoRootTeam.Branch && worktree == targets.RepoRootTeam.Worktree:
		return targets.RepoRootTeam, nil
	default:
		return Target{}, fmt.Errorf(
			"taskstate target branch/worktree %q/%q does not match an expected workflow target",
			taskTarget.Branch,
			taskTarget.Worktree,
		)
	}
}

func cleanAbsPath(label string, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s must be absolute, got %q", label, path)
	}
	return filepath.Clean(path), nil
}

func cleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}
