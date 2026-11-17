package workflow

import (
	"context"
	"errors"
	"github.com/hea3ven/orpheus/internal/work"

	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/review"
)

// The facade routes typed answers. Review owns input/configuration/revision
// validation and mutation; the runner performs the single accepted save.
func (s Controller) respondReview(ctx context.Context, id InteractionID, response any) (View, error) {

	base, current, err := s.inspectReviewResponse(ctx, id)
	if err != nil {
		return View{}, err
	}
	runner := s.deliveryRunner(base.RepoID(), base.TaskID())

	answer, ok := response.(review.Answer)
	if _, pause := response.(PauseResponse); pause {
		answer, ok = review.PauseAnswer{}, true
	}
	if !ok {
		return View{}, errors.New("response does not match pending review interaction")
	}
	responses := review.Assessments{Paths: s.Paths, Repository: base.Source.Repository, Git: s.CandidateGit}
	op, err := responses.Respond(current, phaseexec.InputID{Owner: id.Owner, Version: id.Version, Kind: string(id.Kind)}, answer)
	if err != nil {
		return View{}, err
	}
	saved, err := runner.Execute(ctx, current, op)
	if err != nil {
		return View{}, err
	}
	return interactionView(base.Task, saved)
}

// inspectReviewResponse is the existing source/owner/version gate shared by
// attached conversations and the public response facade. It performs no save.
func (s Controller) inspectReviewResponse(ctx context.Context, id InteractionID) (ReviewContext, work.State, error) {
	base, err := s.resolveReviewTask(ctx, id.Owner.TaskID)
	if err != nil {
		return ReviewContext{}, work.State{}, err
	}
	if base.RepoID() != id.Owner.RepositoryID {
		return ReviewContext{}, work.State{}, phaseexec.ErrStaleInput
	}
	runner := s.deliveryRunner(base.RepoID(), base.TaskID())
	current, err := runner.Load(ctx)
	if err != nil {
		return ReviewContext{}, work.State{}, err
	}
	view, err := interactionView(base.Task, current)
	if err != nil {
		return ReviewContext{}, work.State{}, err
	}
	if err := validateInteractionID(view.Pending, id); err != nil {
		return ReviewContext{}, work.State{}, err
	}
	return base, current, nil
}
