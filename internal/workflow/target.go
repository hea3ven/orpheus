package workflow

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
)

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

// CompletionClassification describes the target and review lifecycle for a completed run.
type CompletionClassification struct {
	Target    tasktarget.Target
	Lifecycle ReviewLifecycle
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

	targetKind := tasktarget.ClassifyRunTarget(repo, branch, worktree)
	switch targetKind {
	case tasktarget.TargetMainSolo:
		return CompletionClassification{
			Target: tasktarget.Target{
				Kind:     tasktarget.TargetMainSolo,
				Branch:   branch,
				Worktree: worktree,
			},
			Lifecycle: ReviewLifecycleLocalReady,
		}, true
	case tasktarget.TargetWorktreeTeam:
		return CompletionClassification{
			Target: tasktarget.Target{
				Kind:     tasktarget.TargetWorktreeTeam,
				Branch:   branch,
				Worktree: worktree,
			},
			Lifecycle: ReviewLifecyclePRReady,
		}, true
	case tasktarget.TargetRepoRootTeam:
		return CompletionClassification{
			Target: tasktarget.Target{
				Kind:     tasktarget.TargetRepoRootTeam,
				Branch:   branch,
				Worktree: worktree,
			},
			Lifecycle: ReviewLifecyclePRReady,
		}, true
	default:
		return CompletionClassification{}, false
	}
}

// ClassifyExpectedCompletionTarget classifies a completed run only when task metadata
// and task-level target facts match one of the exact expected workflow targets.
func ClassifyExpectedCompletionTarget(
	targets tasktarget.ExpectedTargets,
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

	metadataTarget, err := tasktarget.ClassifyMetadataTarget(metadata, targets)
	if err != nil {
		return CompletionClassification{}, false
	}
	target, err := tasktarget.ClassifyTaskStateTarget(taskTarget, targets)
	if err != nil {
		return CompletionClassification{}, false
	}
	if metadataTarget.Branch != target.Branch || metadataTarget.Worktree != target.Worktree {
		return CompletionClassification{}, false
	}

	switch target.Kind {
	case tasktarget.TargetMainSolo:
		return CompletionClassification{
			Target:    target,
			Lifecycle: ReviewLifecycleLocalReady,
		}, true
	case tasktarget.TargetWorktreeTeam:
		return CompletionClassification{
			Target:    target,
			Lifecycle: ReviewLifecyclePRReady,
		}, true
	case tasktarget.TargetRepoRootTeam:
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
	return classification, ok && classification.Target.Kind == tasktarget.TargetMainSolo
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
	targets tasktarget.ExpectedTargets,
	taskItem task.Task,
	taskTarget taskstate.TaskTarget,
	latestRun *taskstate.RunAttempt,
) (CompletionClassification, bool) {
	classification, ok := ClassifyExpectedCompletionTarget(targets, taskItem, taskTarget, latestRun)
	return classification, ok && classification.Target.Kind == tasktarget.TargetMainSolo
}

// ClassifyExpectedPRReviewReady reports whether a task has a strict worktree/team PR-ready completion.
func ClassifyExpectedPRReviewReady(
	targets tasktarget.ExpectedTargets,
	taskItem task.Task,
	taskTarget taskstate.TaskTarget,
	latestRun *taskstate.RunAttempt,
) (CompletionClassification, bool) {
	classification, ok := ClassifyExpectedCompletionTarget(targets, taskItem, taskTarget, latestRun)
	return classification, ok && isPRReviewTarget(classification.Target.Kind)
}

func isPRReviewTarget(kind tasktarget.TargetKind) bool {
	return kind == tasktarget.TargetWorktreeTeam || kind == tasktarget.TargetRepoRootTeam
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
