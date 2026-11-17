package workflow

import (
	"context"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow/review"
)

// Workflow authorizes forward-requirement capture only for a selected Review,
// never for accepted publication plans. Review owns configuration and history.
func (s Controller) continueReview(base ReviewContext, snapshot taskstate.TaskState, route TaskRunRoute) (taskstate.TaskState, error) {
	if snapshot.Work == nil || (route.Action != TaskRunActionResumeReview && route.Action != TaskRunActionPreparePublication) {
		return snapshot, nil
	}
	runner := s.reviewRunner(base.RepoID(), base.TaskID())
	phase := review.Continuation{Paths: s.Paths, Repository: base.Source.Repository}
	current := *snapshot.Work
	for {
		selected, err := phase.Next(current, *route.Owner)
		if err != nil {
			return snapshot, err
		}
		if op, ok := selected.Operation(); ok {
			current, err = runner.ExecuteHeld(context.Background(), current, op)
			if err != nil {
				return snapshot, err
			}
			continue
		}
		return taskstate.Project(current)
	}
}
