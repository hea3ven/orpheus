package delivery

import (
	"context"
	"errors"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

type Answer interface{ deliveryAnswer() }
type PauseAnswer struct{}
type ConsentAnswer struct{ Evidence string }
type FlowAnswer struct {
	Flow    publication.IntegrationFlow
	PRDraft *bool
}
type RestoreAnswer struct{ Checkout gitmeta.PublicationRecovery }
type ManualRepairAnswer struct{}

func (PauseAnswer) deliveryAnswer()        {}
func (ConsentAnswer) deliveryAnswer()      {}
func (FlowAnswer) deliveryAnswer()         {}
func (RestoreAnswer) deliveryAnswer()      {}
func (ManualRepairAnswer) deliveryAnswer() {}

// Pending reconstructs the typed unanswered decision from committed facts.
func Pending(current work.State) Request {
	w := current.ActiveWorkflow()
	if w == nil {
		return nil
	}
	p, ok := w.LastPhase().(*work.PublicationPhase)
	if !ok {
		return nil
	}
	id := phaseexec.InputID{Owner: work.AttemptRef{RepositoryID: current.RepositoryID, TaskID: current.TaskID, Workflow: len(current.Workflows) - 1, Phase: len(w.Phases) - 1}, Version: current.Version}
	if p.NeedsPublicationRecovery() {
		id.Kind = RecoveryInput
		return RecoveryRequest{ID: id}
	}
	if p.Consent == nil {
		return consentRequest(current, id.Owner, w, p)
	}
	return nil
}

func consentRequest(current work.State, owner work.AttemptRef, w *work.Workflow, p *work.PublicationPhase) ConsentRequest {
	request := ConsentRequest{ID: phaseexec.InputID{Owner: owner, Version: current.Version, Kind: ConsentInput}, Input: p.Input, Destination: p.Destination, Comparison: p.Comparison, Flow: publication.IntegrationFlowDirectMerge}
	if w.Publication != nil {
		request.Content = *w.Publication
	}
	if target, pr := p.Target.(*work.PullRequestTarget); pr {
		request.Flow = publication.IntegrationFlowPullRequest
		request.PRDraft = target.Draft
	}
	return request
}

func validateInput(current work.State, id phaseexec.InputID) error {
	pending := Pending(current)
	var actual phaseexec.InputID
	switch request := pending.(type) {
	case ConsentRequest:
		actual = request.ID
	case RecoveryRequest:
		actual = request.ID
	default:
		return phaseexec.ErrStaleInput
	}
	if actual.Version != id.Version || actual.Kind != id.Kind || !actual.Owner.Equal(id.Owner) {
		return phaseexec.ErrStaleInput
	}
	return nil
}

// Respond validates a phase-specific answer and returns its operation. No answer
// writes state or executes an external effect until the runner accepts it.
func (s *Service) Respond(current work.State, id phaseexec.InputID, answer Answer) (phaseexec.Operation, error) {
	if err := validateInput(current, id); err != nil {
		return phaseexec.Operation{}, err
	}
	valid := false
	switch answer.(type) {
	case PauseAnswer:
		valid = true
	case ConsentAnswer, FlowAnswer:
		valid = id.Kind == ConsentInput
	case RestoreAnswer, ManualRepairAnswer:
		valid = id.Kind == RecoveryInput
	}
	if !valid {
		return phaseexec.Operation{}, errors.New("response does not match pending publication interaction")
	}
	return operation("respond to publication interaction", id.Owner, func(snapshot *work.State, _ *work.Workflow, _ *work.PublicationPhase) (phaseexec.Prepared, error) {
		if err := validateInput(*snapshot, id); err != nil {
			return phaseexec.Prepared{}, err
		}
		if _, pause := answer.(PauseAnswer); pause {
			return phaseexec.Prepared{}, nil
		}
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			return s.applyAnswer(ctx, snapshot, id, answer)

		}}, nil
	}), nil
}

func (s *Service) applyAnswer(ctx context.Context, snapshot *work.State, id phaseexec.InputID, answer Answer) phaseexec.Result {
	switch response := answer.(type) {
	case ConsentAnswer:
		return s.consent(ctx, *snapshot, id.Owner, response)
	case FlowAnswer:
		return s.flow(ctx, snapshot, id.Owner, response)
	case RestoreAnswer:
		return s.restore(ctx, snapshot, id.Owner, response)
	case ManualRepairAnswer:
		return s.manualRepair(ctx, snapshot, id.Owner)
	}
	return phaseexec.Result{Err: errors.New("unsupported publication answer")}
}

func (s *Service) consent(ctx context.Context, snapshot work.State, owner work.AttemptRef, response ConsentAnswer) phaseexec.Result {
	if err := s.verifyPublicationInput(ctx, snapshot, owner); err != nil {
		return phaseexec.Result{Err: err}
	}
	if err := ctx.Err(); err != nil {
		return phaseexec.Result{Err: err}
	}
	at := time.Now().UTC()
	return phaseexec.Result{Update: change(owner, func(current *work.State, _ *work.Workflow, _ *work.PublicationPhase) (bool, error) {
		err := current.AuthorizePublication(owner.Workflow, work.PublicationRef{Phase: owner.Phase}, at, response.Evidence)
		return err == nil, err
	})}

}

func (s *Service) flow(ctx context.Context, snapshot *work.State, owner work.AttemptRef, response FlowAnswer) phaseexec.Result {
	changed, err := s.changePublicationChoices(ctx, snapshot, owner, response)
	if err != nil {
		return phaseexec.Result{Err: err}
	}
	if err := ctx.Err(); err != nil {
		return phaseexec.Result{Err: err}
	}
	if !changed {
		return phaseexec.Result{}
	}
	plan := snapshot.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
	return phaseexec.Result{Update: change(owner, func(_ *work.State, w *work.Workflow, _ *work.PublicationPhase) (bool, error) {
		w.Phases = append(w.Phases, plan)
		return true, nil
	})}

}

func (s *Service) restore(ctx context.Context, snapshot *work.State, owner work.AttemptRef, response RestoreAnswer) phaseexec.Result {
	w := snapshot.ActiveWorkflow()
	p := w.LastPhase().(*work.PublicationPhase)
	if err := s.validateRecovery(ctx, w, p); err != nil {
		return phaseexec.Result{Err: err}
	}
	req := recoveryRequest(w, p, s.Source.Repository)
	if err := s.boundary().RestorePublicationInput(ctx, req, response.Checkout); err != nil {
		return phaseexec.Result{Err: err}
	}
	if p.DirectSync != nil || p.Integration != nil {
		return s.repairedPublicHistory(ctx, snapshot, owner)
	}
	public, err := s.boundary().PackagePublicCommit(ctx, req)
	if err != nil {
		return phaseexec.Result{Err: err}
	}
	actual := work.CodeIdentity{Commit: public.Commit, Tree: public.Tree}
	var treeErr error
	if public.Tree != req.Input.Tree {
		treeErr = packageTreeError(work.CodeIdentity{Commit: req.Input.Commit, Tree: req.Input.Tree}, actual)
	}
	return phaseexec.Result{Err: treeErr, Update: change(owner, func(_ *work.State, _ *work.Workflow, p *work.PublicationPhase) (bool, error) {
		p.Operation.Public = &actual
		return true, nil
	})}

}

func (s *Service) manualRepair(ctx context.Context, snapshot *work.State, owner work.AttemptRef) phaseexec.Result {
	w := snapshot.ActiveWorkflow()
	p := w.LastPhase().(*work.PublicationPhase)
	if err := s.validateRecovery(ctx, w, p); err != nil {
		return phaseexec.Result{Err: err}
	}
	if p.DirectSync != nil || p.Integration != nil {
		return s.repairedPublicHistory(ctx, snapshot, owner)
	}
	plan := publicationContinuation(p)
	nextOwner := owner
	nextOwner.Phase++
	if err := s.observePublicationInput(ctx, w, nextOwner, &plan, nil); err != nil {
		return phaseexec.Result{Err: err}
	}
	return phaseexec.Result{Update: change(owner, func(_ *work.State, w *work.Workflow, _ *work.PublicationPhase) (bool, error) {
		w.Phases = append(w.Phases, &plan)
		return true, nil
	})}
}
