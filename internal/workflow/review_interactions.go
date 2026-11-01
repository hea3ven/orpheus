package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

// responseInteraction rechecks the owner after an attached prompt has finished
// recording notes/findings. Respond then uses CAS to refuse concurrent edits.
func (s ReviewLifecycleService) responseInteraction(ctx context.Context, owner work.AttemptRef) (Interaction, error) {
	controller := Controller{ReviewLifecycleService: s}
	view, err := controller.Inspect(ctx, owner.TaskID)
	if err != nil {
		return Interaction{}, err
	}
	if view.Pending == nil || !view.Pending.ID.Owner.Equal(owner) {
		return Interaction{}, ErrStaleInteraction
	}
	return *view.Pending, nil
}

func (s ReviewLifecycleService) recordManualResponse(ctx context.Context, owner work.AttemptRef, result review.ManualResult) error {
	pending, err := s.responseInteraction(ctx, owner)
	if err != nil {
		return errors.Join(review.ErrInvalidExecution, err)
	}
	action := DecisionApprove
	switch result.Status {
	case taskstate.ReviewStatusBlocked:
		action = DecisionRequestChanges
	case taskstate.ReviewStatusAborted:
		action = DecisionAbort
	case "", taskstate.ReviewStatusPassed:
	default:
		return fmt.Errorf("%w: invalid manual response", review.ErrInvalidExecution)
	}
	_, err = (Controller{ReviewLifecycleService: s}).Respond(ctx, pending.ID, ManualResponse{Action: action, Publication: result.Publication})
	if err != nil {
		return errors.Join(review.ErrInvalidExecution, err)
	}
	return nil
}

func (s ReviewLifecycleService) recordFindingResponses(ctx context.Context, owner work.AttemptRef, decisions []review.AutomatedBlockerDecision) error {
	pending, err := s.responseInteraction(ctx, owner)
	if err != nil {
		return errors.Join(review.ErrInvalidExecution, err)
	}
	latest, err := s.RunStore.CurrentReviewAt(owner)
	if err != nil {
		return err
	}
	response := FindingsResponse{}
	for _, decision := range decisions {
		if decision.FindingIndex < 0 || decision.FindingIndex >= len(latest.Findings) || latest.Findings[decision.FindingIndex].Address == nil {
			return fmt.Errorf("%w: response does not identify a retained finding", review.ErrInvalidExecution)
		}
		response.Decisions = append(response.Decisions, FindingDecision{Ref: *latest.Findings[decision.FindingIndex].Address, Action: DecisionAction(decision.Action), Reason: decision.Reason})
	}
	_, err = (Controller{ReviewLifecycleService: s}).Respond(ctx, pending.ID, response)
	if err != nil {
		return errors.Join(review.ErrInvalidExecution, err)
	}
	return nil
}
