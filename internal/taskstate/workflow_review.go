package taskstate

import (
	"errors"

	"github.com/hea3ven/orpheus/internal/work"
)

func currentReview(snapshot *work.State, ref work.AttemptRef) (*work.Workflow, *work.ReviewPhase, error) {
	w := snapshot.ActiveWorkflow()
	if w == nil || ref.RepositoryID != snapshot.RepositoryID || ref.TaskID != snapshot.TaskID || ref.Workflow != len(snapshot.Workflows)-1 || ref.Phase < 0 || ref.Phase != len(w.Phases)-1 || ref.BranchSync != nil {
		return nil, nil, errors.New("review address does not own the current workflow phase")
	}
	p, ok := w.LastPhase().(*work.ReviewPhase)
	if !ok {
		return nil, nil, errors.New("current phase is not review")
	}
	if ref.Step != nil && (*ref.Step < 0 || *ref.Step != len(p.Steps)-1) {
		return nil, nil, errors.New("review callback came from a superseded step")
	}
	return w, p, nil
}

// RecordRepairBudgetStop records the real invocation's consumed repair budget.
// A later explicit task run may use a fresh budget without rewriting this stop.
func (s Store) RecordRepairBudgetStop(ref work.AttemptRef, used, maximum int) (ReviewAttempt, error) {
	view, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		_, p, err := currentReview(snapshot, ref)
		if err != nil {
			return false, err
		}
		p.RepairBudgetStops = append(p.RepairBudgetStops, work.RepairBudgetStop{At: s.nowUTC(), Used: used, Maximum: maximum})
		return true, nil
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, ref)
}

// CurrentReviewAt checks an invocation's parent address without changing facts.
// Resuming a prompt is not another persisted running-status transition.
func (s Store) CurrentReviewAt(ref work.AttemptRef) (ReviewAttempt, error) {
	view, err := s.Load(ref.RepositoryID, ref.TaskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	_, phase, err := currentReview(view.Work, ref)
	if err != nil {
		return ReviewAttempt{}, err
	}
	if ref.Step != nil && (*ref.Step < 0 || *ref.Step != len(phase.Steps)-1) {
		return ReviewAttempt{}, errors.New("review step has been superseded")
	}
	return viewReviewAt(view, ref)
}
