package taskstate

import (
	"errors"

	"github.com/hea3ven/orpheus/internal/work"
)

func (s Store) RecordWorktreeCleanup(repoID, taskID string, opts WorktreeCleanupOptions) (Event, error) {
	var event Event
	_, err := s.updateWorkflow(repoID, taskID, func(snapshot *work.State) (bool, error) {
		if len(snapshot.Workflows) == 0 {
			return false, errors.New("cleanup requires observed workflow closure")
		}
		wi := len(snapshot.Workflows) - 1
		w := &snapshot.Workflows[wi]
		final, ok := w.LastPhase().(*work.FinalizationPhase)
		if !ok || final.Closure == nil {
			return false, errors.New("cleanup requires observed source closure")
		}
		if opts.Worktree != w.Context().Directory {
			return false, errors.New("cleanup path differs from the prepared workflow")
		}
		for _, record := range final.Cleanup {
			if removed, ok := record.(*work.WorktreeRemoved); ok && removed.Path == opts.Worktree {
				event = Event{Type: EventWorktreeRemoved, At: removed.At, Worktree: removed.Path}
				return false, nil
			}
		}
		now := s.nowUTC()
		final.Cleanup = append(final.Cleanup, &work.WorktreeRemoved{Path: opts.Worktree, At: now, Evidence: "Native non-forced worktree removal completed"})
		event = Event{Address: work.AttemptRef{RepositoryID: repoID, TaskID: taskID, Workflow: wi, Phase: len(w.Phases) - 1}, Type: EventWorktreeRemoved, At: now, Worktree: opts.Worktree}
		return true, nil
	})
	return event, err
}
