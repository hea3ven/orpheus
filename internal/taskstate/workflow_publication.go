package taskstate

import (
	"errors"

	"github.com/hea3ven/orpheus/internal/work"
)

// PreparePublication retains passed-review intent without manufacturing consent.
// Manual publication choices become durable only with their actual approval.
func (s Store) PreparePublication(ref work.AttemptRef, target work.PublicationTarget, content string) (work.AttemptRef, error) {
	var owner work.AttemptRef
	_, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		w, phase, err := currentReview(snapshot, ref)
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
		contract := w.ReviewContracts[phase.Contract]
		// Only the latest attempt of each manual slot is effective. No tentative
		// choices from a blocked, aborted or superseded assessment become intent.
		for slot := range contract.Slots {
			for index := len(phase.Steps) - 1; index >= 0; index-- {
				manual, ok := phase.Steps[index].(*work.ManualReviewStep)
				if !ok || manual.Slot != slot {
					continue
				}
				if approval, ok := manual.Decision.(*work.ApprovedReview); ok && approval.Publication != nil {
					target = approval.Publication.Target
				}
				break
			}
		}
		owner = work.AttemptRef{RepositoryID: ref.RepositoryID, TaskID: ref.TaskID, Workflow: ref.Workflow, Phase: len(w.Phases)}
		w.Phases = append(w.Phases, &work.PublicationPhase{Plan: work.PublicationPlan{Review: work.ReviewRef{Phase: ref.Phase}, Target: target, Content: content}})
		return true, nil
	})
	return owner, err
}
