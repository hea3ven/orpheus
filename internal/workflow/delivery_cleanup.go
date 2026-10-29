package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

type publicationCleanupRecorder func(string, string, taskstate.WorktreeCleanupOptions) (taskstate.Event, error)

func (record publicationCleanupRecorder) RecordWorktreeCleanup(repoID, taskID string, opts taskstate.WorktreeCleanupOptions) (taskstate.Event, error) {
	return record(repoID, taskID, opts)
}

func (s FinalizationService) cleanupDeliveredWorktree(ctx context.Context, store WorkStore, repo task.Repository, item task.Task, backend task.Getter, directory string) *WorktreeCleanupResult {
	if directory == repo.Path {
		return nil
	}
	unsafe := func(reason string) *WorktreeCleanupResult {
		return &WorktreeCleanupResult{Outcome: WorktreeCleanupUnsafe, Worktree: directory, Reason: reason}
	}
	snapshot, err := store.Load(repo.ID, item.ID)
	if err != nil {
		return unsafe("reload workflow after closure: " + err.Error())
	}
	view, err := taskstate.Project(snapshot)
	if err != nil {
		return unsafe("validate workflow before cleanup: " + err.Error())
	}
	observed, err := backend.Get(ctx, item.ID)
	if err != nil {
		return unsafe("confirm source closure before cleanup: " + err.Error())
	}
	if observed.Status != task.StatusClosed {
		return unsafe("task source did not confirm closure before cleanup")
	}
	owner := work.AttemptRef{RepositoryID: repo.ID, TaskID: item.ID, Workflow: len(snapshot.Workflows) - 1, Phase: len(snapshot.Workflows[len(snapshot.Workflows)-1].Phases) - 1}
	recorder := publicationCleanupRecorder(func(_, _ string, opts taskstate.WorktreeCleanupOptions) (taskstate.Event, error) {
		at := time.Now().UTC()
		err := appendPublicationCleanup(store, owner, &work.WorktreeRemoved{Path: opts.Worktree, At: at, Evidence: "Native non-forced worktree removal completed"})
		return taskstate.Event{Address: owner, Type: taskstate.EventWorktreeRemoved, At: at, Worktree: opts.Worktree}, err
	})
	result := CleanClosedTaskWorktree(ctx, ClosedTaskWorktreeCleanupOptions{Paths: s.Paths, Repository: repo, Task: observed, TaskState: view, Fix: true, Git: s.CleanupGit, Recorder: recorder})
	if result.Outcome == WorktreeCleanupUnsafe || result.Outcome == WorktreeCleanupFailed {
		if err := appendPublicationCleanup(store, owner, &work.WorktreeRetained{Path: directory, At: time.Now().UTC(), Reason: result.Reason, Evidence: "Closed-task cleanup observed refusal or failure"}); err != nil {
			result.Reason = joinCleanupReason(result.Reason, "could not record retention observation: "+err.Error())
		}
	}
	return &result
}

func appendPublicationCleanup(store WorkStore, owner work.AttemptRef, observation work.CleanupObservation) error {
	snapshot, err := store.Load(owner.RepositoryID, owner.TaskID)
	if err != nil {
		return err
	}
	if owner.Workflow < 0 || owner.Workflow != len(snapshot.Workflows)-1 {
		return errors.New("cleanup no longer owns the current workflow")
	}
	w := &snapshot.Workflows[owner.Workflow]
	if owner.Phase != len(w.Phases)-1 {
		return errors.New("cleanup no longer owns the closure occurrence")
	}
	final, ok := w.LastPhase().(*work.FinalizationPhase)
	if !ok || final.Closure == nil {
		return errors.New("cleanup requires observed source closure")
	}
	final.Cleanup = append(final.Cleanup, observation)
	_, err = store.Save(snapshot, snapshot.Version)
	return err
}
