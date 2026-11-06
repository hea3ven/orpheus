package taskstate

import (
	"errors"

	"github.com/hea3ven/orpheus/internal/work"
)

// PreparePublication retains passed-review intent without manufacturing consent.
// Delivery choices belong to Publication, not a manual review assessment.
func (s Store) PreparePublication(ref work.AttemptRef, plan work.PublicationPlan) (work.AttemptRef, error) {
	var owner work.AttemptRef
	_, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		w, _, err := currentReview(snapshot, ref)
		if err != nil {
			return false, err
		}
		outcome, err := w.ReviewOutcome(work.ReviewRef{Phase: ref.Phase})
		if err != nil {
			return false, err
		}
		if outcome != work.ReviewPassed && outcome != work.ReviewPassedWithWaivers {
			return false, errors.New("publication requires the complete passed review")
		}
		if plan.Review.Phase != ref.Phase {
			return false, errors.New("publication plan must identify its passed review")
		}
		owner = work.AttemptRef{RepositoryID: ref.RepositoryID, TaskID: ref.TaskID, Workflow: ref.Workflow, Phase: len(w.Phases)}
		w.Phases = append(w.Phases, &work.PublicationPhase{Plan: plan})
		return true, nil
	})
	return owner, err
}
