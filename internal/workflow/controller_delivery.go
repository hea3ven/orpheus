package workflow

import (
	"context"
	"errors"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/delivery"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workstate"
)

// Finalize is delivery-only: it cannot select Implementation or Review.
func (s Controller) Finalize(ctx context.Context, opts FinalizeOptions) (FinalizationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return s.advanceDeliveryPhase(ctx, opts, nil)
}

func (s Controller) deliveryRunner(repoID, taskID string) phaseexec.Runner {
	store := s.Delivery.Store
	if store == nil {
		store = workstate.NewStore(s.Paths)
	}
	return phaseexec.Runner{Store: store, Paths: s.Paths, RepositoryID: repoID, TaskID: taskID, Logger: s.Logger}
}

func (s Controller) deliveryPhase(base ReviewContext, backend delivery.Backend, runner phaseexec.Runner) *delivery.Service {
	return &delivery.Service{Paths: runner.Paths, Source: base.Source, Task: base.Task, Backend: backend, Git: s.Delivery.Git, DirectGit: s.Delivery.DirectGit, ConflictResolver: s.Delivery.ConflictResolver, ProcessProbe: s.ProcessProbe, PRProvider: s.Delivery.PRProvider, Commit: runner.Commit}
}

func (s Controller) advanceDeliveryPhase(ctx context.Context, opts FinalizeOptions, expected *TaskRunRoute) (FinalizationResult, error) {
	base, backend, current, runner, err := s.loadDeliveryInvocation(ctx, opts, expected)
	if err != nil {
		return FinalizationResult{}, err
	}
	result := FinalizationResult{Repository: base.Source.Repository, Task: base.Task}
	phase := s.deliveryPhase(base, backend, runner)
	phase.Progress = func(snapshot work.State) error { return s.reportSnapshot(base.Task, snapshot, true) }
	current, outcome, err := driveDelivery(ctx, runner, phase, current, delivery.Intent{AuthorizedOnly: expected == nil})
	if err != nil {
		return result, err
	}
	switch delivered := outcome.(type) {
	case delivery.PRDelivered:
		result.PRURL, result.PRRecovered, result.Task = delivered.PRURL, delivered.Recovered, delivered.Task
	case delivery.DirectDelivered:
		result.Task = delivered.Task
	default:
		return result, errors.New("delivery returned no semantic outcome")
	}
	current, err = s.enterDeliverySuccessor(ctx, runner, current, outcome)
	if err != nil {
		return result, err
	}
	view, err := taskstate.Project(current)
	if err != nil {
		return result, err
	}
	result.Finalization = taskstate.FinalizationFacts(view)
	w := &current.Workflows[len(current.Workflows)-1]
	result.Branch = w.Context().TaskBranch
	index, err := delivery.Owner(w)
	if err != nil {
		return result, err
	}
	result.InputCommit = w.Phases[index].(*work.PublicationPhase).Input.Code.Commit
	return result, s.reportSnapshot(result.Task, current, false)
}

func (s Controller) loadDeliveryInvocation(ctx context.Context, opts FinalizeOptions, expected *TaskRunRoute) (ReviewContext, delivery.Backend, work.State, phaseexec.Runner, error) {
	if s.Delivery.BackendFactory == nil {
		return ReviewContext{}, nil, work.State{}, phaseexec.Runner{}, errors.New("task finalization backend factory is required")
	}
	store := s.Delivery.Store
	if store == nil {
		store = workstate.NewStore(s.Delivery.Paths)
	}
	var base ReviewContext
	var backend delivery.Backend
	var current work.State
	err := state.WithGlobalMutationLockLogger(ctx, s.Delivery.Paths, finalizationLockOperation, s.Delivery.Logger, func() error {
		source, item, resolved, err := s.Delivery.resolvePublicationTask(ctx, opts, store)
		if err != nil {
			return err
		}
		base = ReviewContext{Source: source, Task: item}
		backend = resolved
		current, err = store.Load(source.Repository.ID, item.ID)
		if err != nil {
			return err
		}
		view, err := taskstate.Project(current)
		if err != nil {
			return err
		}
		return CheckExpectedRoute(expected, item, view)
	})
	if err != nil {
		return ReviewContext{}, nil, work.State{}, phaseexec.Runner{}, err
	}
	runner := phaseexec.Runner{Store: store, Paths: s.Delivery.Paths, RepositoryID: current.RepositoryID, TaskID: current.TaskID, Logger: s.Delivery.Logger}
	return base, backend, current, runner, nil
}

// publicationInputPending preserves each command's existing stopping behavior.
type publicationInputPending struct{ request delivery.Request }

func (p publicationInputPending) Error() string {
	if _, recovery := p.request.(delivery.RecoveryRequest); recovery {
		return "publication packaging needs an interactive restore-or-manual-repair decision; continue task run"
	}
	return delivery.ErrConsentRequired.Error()
}
func (p publicationInputPending) Unwrap() error {
	if _, consent := p.request.(delivery.ConsentRequest); consent {
		return delivery.ErrConsentRequired
	}
	return nil
}

func driveDelivery(ctx context.Context, runner phaseexec.Runner, phase *delivery.Service, current work.State, intent delivery.Intent) (work.State, delivery.Outcome, error) {
	for {
		selected, err := phase.Next(current, intent)
		if err != nil {
			return current, nil, err
		}
		if op, ok := selected.Operation(); ok {
			current, err = runner.Execute(ctx, current, op)
			if err != nil {
				return current, nil, err
			}
			continue
		}
		if request, ok := selected.Input(); ok {
			return current, nil, publicationInputPending{request: request}
		}
		outcome, _ := selected.Outcome()
		return current, outcome, nil
	}
}

func (s Controller) enterDeliverySuccessor(ctx context.Context, runner phaseexec.Runner, current work.State, outcome delivery.Outcome) (work.State, error) {
	var owner work.AttemptRef
	var next work.Phase
	switch result := outcome.(type) {
	case delivery.PRDelivered:
		owner = result.Publication
		next = &work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: owner.Phase}}
	case delivery.DirectDelivered:
		owner = result.Publication
		next = &work.FinalizationPhase{}
	}
	// Delivery retries from a successor do not append another phase.
	if _, publishing := (&current.Workflows[len(current.Workflows)-1]).LastPhase().(*work.PublicationPhase); !publishing {
		return current, nil
	}
	return runner.Execute(ctx, current, phaseexec.Operation{Name: "advance delivered publication", Prepare: func(snapshot *work.State) (phaseexec.Prepared, error) {
		w := snapshot.ActiveWorkflow()
		if w == nil || owner.Workflow != len(snapshot.Workflows)-1 || owner.Phase != len(w.Phases)-1 {
			return phaseexec.Prepared{}, phaseexec.ErrStaleInput
		}
		w.Phases = append(w.Phases, next)
		return phaseexec.Prepared{Changed: true}, nil
	}})
}

func (s Controller) preparePublication(ctx context.Context, current ReviewAttemptContext) error {
	backend, err := s.BackendFactory(current.Source)
	if err != nil {
		return err
	}
	runner := s.deliveryRunner(current.RepoID(), current.TaskID())
	snapshot, err := runner.Load(ctx)
	if err != nil {
		return err
	}
	phase := s.deliveryPhase(current.ReviewContext, backend, runner)
	for {
		selected, err := phase.Next(snapshot, delivery.Intent{Review: &current.Review.Address})
		if err != nil {
			return err
		}
		if op, ok := selected.Operation(); ok {
			snapshot, err = runner.Execute(ctx, snapshot, op)
			if err != nil {
				return err
			}
			continue
		}
		return nil
	}
}

func (s Controller) respondDelivery(ctx context.Context, id InteractionID, response any) (View, error) {
	base, err := s.resolveReviewTask(ctx, id.Owner.TaskID)
	if err != nil {
		return View{}, err
	}
	if base.RepoID() != id.Owner.RepositoryID {
		return View{}, phaseexec.ErrStaleInput
	}
	backend, err := s.BackendFactory(base.Source)
	if err != nil {
		return View{}, err
	}
	runner := s.deliveryRunner(base.RepoID(), base.TaskID())
	current, err := runner.Load(ctx)
	if err != nil {
		return View{}, err
	}
	view, err := interactionView(base.Task, current)
	if err != nil {
		return View{}, err
	}
	if err := validateInteractionID(view.Pending, id); err != nil {
		return View{}, err
	}
	answer, ok := response.(delivery.Answer)
	if _, pause := response.(PauseResponse); pause {
		answer, ok = delivery.PauseAnswer{}, true
	}
	if !ok {
		return View{}, errors.New("response does not match pending publication interaction")
	}
	phase := s.deliveryPhase(base, backend, runner)
	op, err := phase.Respond(current, phaseexec.InputID{Owner: id.Owner, Version: id.Version, Kind: string(id.Kind)}, answer)
	if err != nil {
		return View{}, err
	}
	saved, err := runner.Execute(ctx, current, op)
	if err != nil {
		return View{}, err
	}
	return interactionView(base.Task, saved)
}

// InspectPublicationRecovery keeps the current read-only frontend entry point.
func (s Controller) InspectPublicationRecovery(ctx context.Context, id InteractionID) (gitmeta.PublicationRecovery, error) {
	var result gitmeta.PublicationRecovery
	err := state.WithGlobalMutationLockLogger(ctx, s.Paths, "inspect publication recovery", s.Logger, func() error {
		base, err := s.resolveReviewTask(ctx, id.Owner.TaskID)
		if err != nil {
			return err
		}
		if base.RepoID() != id.Owner.RepositoryID {
			return phaseexec.ErrStaleInput
		}
		backend, err := s.BackendFactory(base.Source)
		if err != nil {
			return err
		}
		runner := s.deliveryRunner(base.RepoID(), base.TaskID())
		current, err := runner.Load(ctx)
		if err != nil {
			return err
		}
		view, err := interactionView(base.Task, current)
		if err != nil {
			return err
		}
		if err := validateInteractionID(view.Pending, id); err != nil {
			return err
		}
		result, err = s.deliveryPhase(base, backend, runner).InspectRecovery(ctx, current, phaseexec.InputID{Owner: id.Owner, Version: id.Version, Kind: string(id.Kind)})
		return err
	})
	return result, err
}
