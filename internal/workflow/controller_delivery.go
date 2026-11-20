package workflow

import (
	"context"
	"errors"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/delivery"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

func (s DeliveryService) runner(repoID, taskID string) phaseexec.Runner {
	return phaseexec.Runner{Store: s.Store, Paths: s.Paths, RepositoryID: repoID, TaskID: taskID, Logger: s.Logger}
}

func (s DeliveryService) deliveryPhase(base ReviewContext, backend delivery.Backend, runner phaseexec.Runner) *delivery.Service {
	return &delivery.Service{Paths: runner.Paths, Source: base.Source, Task: base.Task, Backend: backend, Git: s.Git, DirectGit: s.DirectGit, ConflictResolver: s.ConflictResolver, ProcessProbe: s.ProcessProbe, PRProvider: s.PRProvider, Commit: runner.Commit}
}

// advanceDeliveryPhase drives Publication, returning no result when operator input is required.
func (s DeliveryService) advanceDeliveryPhase(ctx context.Context, base ReviewContext, expected *TaskRunRoute) (*PublicationResult, error) {
	backend, err := s.BackendFactory(base.Source)
	if err != nil {
		return nil, err
	}
	runner := s.runner(base.RepoID(), base.TaskID())
	current, err := loadPublication(ctx, runner, base.Task, expected)
	if err != nil {
		return nil, err
	}
	phase := s.deliveryPhase(base, backend, runner)
	phase.Progress = func(snapshot work.State) error { return s.reportSnapshot(base.Task, snapshot, true) }
	current, outcome, err := driveDelivery(ctx, runner, phase, current)
	if err != nil || outcome == nil {
		return nil, err
	}
	return s.finishDelivery(ctx, runner, current, outcome)
}

func loadPublication(ctx context.Context, runner phaseexec.Runner, item task.Task, expected *TaskRunRoute) (work.State, error) {
	var current work.State
	err := state.WithGlobalMutationLockLogger(ctx, runner.Paths, "load publication", runner.Logger, func() error {
		var err error
		current, err = runner.Load(ctx)
		if err != nil {
			return err
		}
		view, err := taskstate.Project(current)
		if err != nil {
			return err
		}
		return CheckExpectedRoute(expected, item, view)
	})
	return current, err
}

func driveDelivery(ctx context.Context, runner phaseexec.Runner, phase *delivery.Service, current work.State) (work.State, delivery.Outcome, error) {
	for {
		selected, err := phase.Next(current, delivery.Intent{})
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
		if _, pending := selected.Input(); pending {
			return current, nil, nil
		}
		outcome, ok := selected.Outcome()
		if !ok {
			return current, nil, errors.New("publication returned no operation, input or outcome")
		}
		return current, outcome, nil
	}
}

func (s DeliveryService) finishDelivery(ctx context.Context, runner phaseexec.Runner, current work.State, outcome delivery.Outcome) (*PublicationResult, error) {
	var owner work.AttemptRef
	var next work.Phase
	var item task.Task
	var prURL string
	switch delivered := outcome.(type) {
	case delivery.PRDelivered:
		owner, item, prURL = delivered.Publication, delivered.Task, delivered.PRURL
		next = &work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: owner.Phase}}
	case delivery.DirectDelivered:
		owner, item = delivered.Publication, delivered.Task
		next = &work.FinalizationPhase{}
	default:
		return nil, errors.New("delivery returned no semantic outcome")
	}
	current, err := runner.Execute(ctx, current, phaseexec.Operation{Name: "advance delivered publication", Prepare: func(snapshot *work.State) (phaseexec.Prepared, error) {
		w := snapshot.ActiveWorkflow()
		if w == nil || owner.Workflow != len(snapshot.Workflows)-1 || owner.Phase != len(w.Phases)-1 {
			return phaseexec.Prepared{}, phaseexec.ErrStaleInput
		}
		w.Phases = append(w.Phases, next)
		return phaseexec.Prepared{Changed: true}, nil
	}})
	if err != nil {
		return nil, err
	}
	w := &current.Workflows[owner.Workflow]
	p := w.Phases[owner.Phase].(*work.PublicationPhase)
	branch := w.Context().TaskBranch
	if prURL == "" {
		branch = p.Destination.Branch
	}
	return &PublicationResult{TaskID: item.ID, InputCommit: p.Input.Code.Commit, Branch: branch, PRURL: prURL}, s.reportSnapshot(item, current, false)
}

func (s Controller) preparePublication(ctx context.Context, current ReviewAttemptContext) error {
	backend, err := s.Delivery.BackendFactory(current.Source)
	if err != nil {
		return err
	}
	runner := s.Delivery.runner(current.RepoID(), current.TaskID())
	snapshot, err := runner.Load(ctx)
	if err != nil {
		return err
	}
	phase := s.Delivery.deliveryPhase(current.ReviewContext, backend, runner)
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
	backend, err := s.Delivery.BackendFactory(base.Source)
	if err != nil {
		return View{}, err
	}
	runner := s.Delivery.runner(base.RepoID(), base.TaskID())
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
	phase := s.Delivery.deliveryPhase(base, backend, runner)
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
		backend, err := s.Delivery.BackendFactory(base.Source)
		if err != nil {
			return err
		}
		runner := s.Delivery.runner(base.RepoID(), base.TaskID())
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
		result, err = s.Delivery.deliveryPhase(base, backend, runner).InspectRecovery(ctx, current, phaseexec.InputID{Owner: id.Owner, Version: id.Version, Kind: string(id.Kind)})
		return err
	})
	return result, err
}
