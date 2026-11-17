package delivery

import (
	"context"
	"errors"
	"fmt"

	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

func (s *Service) initialize(current work.State, review work.AttemptRef) phaseexec.Operation {
	wi, pi := review.Workflow, review.Phase
	return phaseexec.Operation{Name: "reserve publication", Prepare: func(snapshot *work.State) (phaseexec.Prepared, error) {
		if review.RepositoryID != snapshot.RepositoryID || review.TaskID != snapshot.TaskID || review.Step != nil || review.BranchSync != nil {
			return phaseexec.Prepared{}, phaseexec.ErrStaleInput
		}
		view, err := admitPublication(*snapshot, wi, pi)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		w := snapshot.ActiveWorkflow()
		owner := work.AttemptRef{RepositoryID: snapshot.RepositoryID, TaskID: snapshot.TaskID, Workflow: wi, Phase: pi + 1}
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			plan, err := s.initialPublication(ctx, w, owner, view)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			s.refreshed = true
			return phaseexec.Result{Update: func(next *work.State) (bool, error) {
				active := next.ActiveWorkflow()
				if active == nil || len(next.Workflows)-1 != wi || len(active.Phases)-1 != pi {
					return false, phaseexec.ErrStaleInput
				}
				active.Phases = append(active.Phases, plan)
				return true, nil
			}}
		}}, nil
	}}
}

func admitPublication(snapshot work.State, wi, pi int) (taskstate.TaskState, error) {
	w := snapshot.ActiveWorkflow()
	if w == nil || len(snapshot.Workflows)-1 != wi || len(w.Phases)-1 != pi {
		return taskstate.TaskState{}, phaseexec.ErrStaleInput
	}
	if _, ok := w.LastPhase().(*work.ReviewPhase); !ok {
		return taskstate.TaskState{}, phaseexec.ErrStaleInput
	}
	result, err := w.ReviewOutcome(work.ReviewRef{Phase: pi})
	if err != nil {
		return taskstate.TaskState{}, err
	}
	if result != work.ReviewPassed && result != work.ReviewPassedWithWaivers {
		return taskstate.TaskState{}, errors.New("publication requires the complete passed review")
	}
	view, err := taskstate.Project(snapshot)
	if err != nil {
		return taskstate.TaskState{}, err
	}
	if _, err := taskstate.CompletionRunsForReview(view); err != nil {
		return taskstate.TaskState{}, err
	}
	for _, phase := range w.Phases {
		if p, ok := phase.(*work.PublicationPhase); ok {
			if _, delivered := p.Delivery.(*work.PullRequestDelivery); delivered {
				return taskstate.TaskState{}, errors.New("initial PR publication is complete; later work must continue on public history")
			}
		}
	}
	return view, nil
}

func (s *Service) initialPublication(ctx context.Context, w *work.Workflow, owner work.AttemptRef, view taskstate.TaskState) (*work.PublicationPhase, error) {
	config, err := publication.LoadConfig(s.Paths)
	if err != nil {
		return nil, err
	}
	flow := taskstate.FinalizationFacts(view).IntegrationFlow
	if flow == "" {
		flow = publication.ResolveIntegrationFlow("", publication.IntegrationFlow(s.Source.Repository.IntegrationFlow), config.IntegrationFlow)
	}
	var target work.PublicationTarget = &work.DirectMergeTarget{}
	if flow == publication.IntegrationFlowPullRequest {
		target = &work.PullRequestTarget{Draft: publication.ResolvePRDraft(s.Source.Repository.PRDraft, config.PRDraft)}
	}
	plan := &work.PublicationPhase{Review: work.ReviewRef{Phase: owner.Phase - 1}, Target: target}
	if err := s.observePublicationInput(ctx, w, owner, plan, nil); err != nil {
		return nil, err
	}
	return plan, nil
}

func (s *Service) refresh(owner work.AttemptRef) phaseexec.Operation {
	return operation("refresh publication input", owner, func(snapshot *work.State, _ *work.Workflow, p *work.PublicationPhase) (phaseexec.Prepared, error) {
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			changed, err := s.refreshPublicationSnapshot(ctx, s.Source, s.Task, s.Backend, snapshot)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			s.refreshed = true
			if !changed {
				return phaseexec.Result{}
			}
			w := snapshot.ActiveWorkflow()
			if len(w.Phases)-1 > owner.Phase {
				plan := w.LastPhase().(*work.PublicationPhase)
				return phaseexec.Result{Update: change(owner, func(_ *work.State, active *work.Workflow, _ *work.PublicationPhase) (bool, error) {
					active.Phases = append(active.Phases, plan)
					return true, nil
				})}
			}
			if p.Integration != nil && p.Integration.InvalidatedAt != nil {
				at := *p.Integration.InvalidatedAt
				return phaseexec.Result{Update: change(owner, func(_ *work.State, _ *work.Workflow, active *work.PublicationPhase) (bool, error) {
					active.Integration.InvalidatedAt = &at
					return true, nil
				})}
			}
			return phaseexec.Result{Err: fmt.Errorf("publication refresh produced no owned change")}
		}}, nil
	})
}
