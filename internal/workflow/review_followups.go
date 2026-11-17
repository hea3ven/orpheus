package workflow

import (
	"context"
	"fmt"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/review"
)

// advanceReviewFollowUps bridges the existing specialized frontend to Review's
// operations. Only a completed Review outcome allows workflow to enter Publication.
func (s Controller) advanceReviewFollowUps(ctx context.Context, current ReviewAttemptContext) (review.FollowUpOutcome, error) {
	runner := s.reviewRunner(current.RepoID(), current.TaskID())
	saved, err := runner.Load(ctx)
	if err != nil {
		return review.FollowUpOutcome{}, err
	}
	phase := review.FollowUps{
		Backend: func() (task.CreateMutator, error) { return s.BackendFactory(current.Source) },
		Created: func(candidate review.SeparateTaskCandidate, created task.Task) error {
			return s.Frontend.SeparateTaskCreated(current, candidate, created)
		},
	}
	for {
		selected, err := phase.Next(saved, current.Review.Address)
		if err != nil {
			return review.FollowUpOutcome{}, err
		}
		if outcome, ok := selected.Outcome(); ok {
			return outcome, nil
		}
		op, hasOperation := selected.Operation()
		if request, ok := selected.Input(); ok {
			id, answer, err := s.reviewFollowUpAnswer(current, request)
			if err != nil {
				return review.FollowUpOutcome{}, err
			}
			op, err = phase.Respond(saved, id, answer)
			if err != nil {
				return review.FollowUpOutcome{}, err
			}
			hasOperation = true
		}
		if !hasOperation {
			return review.FollowUpOutcome{}, fmt.Errorf("review follow-up selection has no alternative")
		}
		saved, err = runner.Execute(ctx, saved, op)
		if err != nil {
			return review.FollowUpOutcome{}, err
		}
	}
}

func (s Controller) reviewFollowUpAnswer(current ReviewAttemptContext, request review.FollowUpRequest) (phaseexec.InputID, review.FollowUpAnswer, error) {
	switch input := request.(type) {
	case review.FollowUpSelection:
		selected, err := s.Frontend.SelectSeparateTaskCandidates(current, input.Candidates)
		if err != nil {
			return input.ID, nil, fmt.Errorf("%w: task run %s: %w", review.ErrPresentationUnavailable, current.TaskID(), err)
		}
		return input.ID, review.SelectFollowUps{Candidates: selected}, nil
	case review.FollowUpCreationFailure:
		proceed, err := s.Frontend.ContinueAfterFollowUpCreationFailure(current, input.Candidate, input.Err)
		if err != nil {
			return input.ID, nil, fmt.Errorf("%w: %w", review.ErrPresentationUnavailable, err)
		}
		return input.ID, review.ContinueAfterCreationFailure{Proceed: proceed}, nil
	default:
		return phaseexec.InputID{}, nil, fmt.Errorf("unsupported review follow-up input %T", request)
	}
}
