package tasktarget

import (
	"context"
	"fmt"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/task"
	"strings"
)

// CheckBranchOwnership prevents two task records from attaching to the
// same rendered branch. It runs before Git, backend metadata, or taskstate is
// mutated, so a collision cannot turn into an unrelated retry target.
func CheckBranchOwnership(ctx context.Context, backend task.Lister, currentTaskID string, branch string) error {
	tasks, err := backend.List(ctx)
	if err != nil {
		return fmt.Errorf("inspect task branch ownership: %w", err)
	}
	for _, taskItem := range tasks {
		if strings.TrimSpace(taskItem.ID) == currentTaskID {
			continue
		}
		metadata := taskItem.OrpheusMetadata()
		if metadata.HasBranch && strings.TrimSpace(metadata.Branch) == branch {
			return fmt.Errorf(
				"task branch %q is already recorded for task %s; refusing to reuse it for task %s",
				branch,
				taskItem.ID,
				currentTaskID,
			)
		}
	}
	return nil
}

func CheckRepoRootAvailable(
	ctx context.Context,
	backend task.Lister,
	repo task.Repository,
	currentTaskID string,
	expected gitmeta.TaskWorktreeSetupResult,
) error {
	tasks, err := backend.List(ctx)
	if err != nil {
		return fmt.Errorf("inspect repo-root/default-branch ownership: %w", err)
	}

	for _, taskItem := range tasks {
		if strings.TrimSpace(taskItem.ID) == currentTaskID || taskItem.Status == task.StatusClosed {
			continue
		}
		metadata := taskItem.OrpheusMetadata()
		if !metadataMatches(metadata, expected) &&
			cleanPath(metadata.Worktree) != cleanPath(expected.WorktreePath) {
			continue
		}
		return fmt.Errorf(
			"repo %s (%s) already has non-closed task %s owning repo-root metadata (%s=%q, %s=%q); finish local review or clear that metadata before running another task from the repo root",
			repo.ID,
			repo.Name,
			taskItem.ID,
			task.MetadataBranch,
			metadata.Branch,
			task.MetadataWorktree,
			metadata.Worktree,
		)
	}
	return nil
}

func metadataMatches(metadata task.OrpheusMetadata, expected gitmeta.TaskWorktreeSetupResult) bool {
	return metadata.HasBranch && strings.TrimSpace(metadata.Branch) == expected.Branch && metadata.HasWorktree && cleanPath(metadata.Worktree) == cleanPath(expected.WorktreePath)
}
