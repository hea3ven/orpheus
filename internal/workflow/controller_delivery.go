package workflow

import (
	"context"
	"errors"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
)

// Finalize is the delivery-only entry point for task done. It uses the same
// phase handlers as Advance, but cannot start implementation or review work.
func (s Controller) Finalize(ctx context.Context, opts FinalizeOptions) (FinalizationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var expected *TaskRunRoute
	for {
		result, next, err := s.advanceDeliveryPhase(ctx, opts, expected)
		if err != nil || next == nil {
			return result, err
		}
		expected = next
	}
}

// advanceDeliveryPhase performs one selected phase under mutation protection.
// A publication handler observes delivery; the controller then appends External
// review or Finalization. Closure is executed only on a subsequent iteration.
func (s Controller) advanceDeliveryPhase(ctx context.Context, opts FinalizeOptions, expected *TaskRunRoute) (FinalizationResult, *TaskRunRoute, error) {
	delivery := s.Delivery
	if delivery.BackendFactory == nil {
		return FinalizationResult{}, nil, errors.New("task finalization backend factory is required")
	}
	store := delivery.Store
	if store == nil {
		store = workstate.NewStore(delivery.Paths)
	}
	boundary := delivery.Git
	if boundary == nil {
		boundary = LocalFinalizationGit{}
	}
	var result FinalizationResult
	var next *TaskRunRoute
	err := state.WithGlobalMutationLockLogger(ctx, delivery.Paths, finalizationLockOperation, delivery.Logger, func() error {
		source, item, backend, err := delivery.resolvePublicationTask(ctx, opts, store)
		if err != nil {
			return err
		}
		snapshot, err := store.Load(source.Repository.ID, item.ID)
		if err != nil {
			return err
		}
		view, err := taskstate.Project(snapshot)
		if err != nil {
			return err
		}
		if err := CheckExpectedRoute(expected, item, view); err != nil {
			return err
		}
		if w := snapshot.ActiveWorkflow(); w != nil {
			if pending, ok := w.LastPhase().(*work.PublicationPhase); ok && pending.Consent == nil {
				return ErrPublicationConsentRequired
			}
		}
		changed, err := s.refreshPublicationSnapshot(ctx, source, item, backend, &snapshot)
		if err != nil {
			return err
		}
		if changed {
			if _, err := store.Save(snapshot, snapshot.Version); err != nil {
				return err
			}
			return ErrPublicationConsentRequired
		}
		selected, err := selectCommittedPublication(&snapshot, item)
		if err != nil {
			return err
		}
		result, next, err = s.applyDeliveryPhase(ctx, store, boundary, source, item, backend, &snapshot, selected)
		return err
	})
	return result, next, err
}

func (s Controller) enterDeliverySuccessor(ctx context.Context, store WorkStore, backend FinalizationBackend, snapshot *work.State, selected committedPublication, result *FinalizationResult) error {
	var next work.Phase
	switch delivered := selected.publication.Delivery.(type) {
	case *work.PullRequestDelivery:
		result.PRURL = delivered.PullRequest.URL
		observed, err := s.Delivery.recordPublicationPR(ctx, backend, result.Task, result.PRURL)
		if err != nil {
			return err
		}
		result.Task = observed
		next = &work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: selected.phaseIndex}}
	case *work.DirectDelivery:
		next = &work.FinalizationPhase{Acceptance: &work.DirectAcceptance{Publication: work.PublicationRef{Phase: selected.phaseIndex}}}
	default:
		return errors.New("publication handler did not establish observed delivery")
	}
	selected.workflow.Phases = append(selected.workflow.Phases, next)
	version, err := store.Save(*snapshot, snapshot.Version)
	if err != nil {
		return err
	}
	snapshot.Version = version
	return nil
}

// publicationOwner follows containment references from selected work, never a
// task-wide latest publication or PR metadata.
func publicationOwner(w *work.Workflow) (int, error) {
	switch phase := w.LastPhase().(type) {
	case *work.PublicationPhase:
		return len(w.Phases) - 1, nil
	case *work.ExternalReviewPhase:
		return phase.Publication.Phase, nil
	case *work.FinalizationPhase:
		switch accepted := phase.Acceptance.(type) {
		case *work.DirectAcceptance:
			return accepted.Publication.Phase, nil
		case *work.PRAcceptance:
			return w.Phases[accepted.ExternalReview].(*work.ExternalReviewPhase).Publication.Phase, nil
		}
	}
	return 0, errors.New("active local work prevents publication")
}

func (s Controller) applyDeliveryPhase(ctx context.Context, store WorkStore, boundary FinalizationGit, source task.RepositorySource, item task.Task, backend FinalizationBackend, snapshot *work.State, selected committedPublication) (FinalizationResult, *TaskRunRoute, error) {
	delivery := s.Delivery
	result := FinalizationResult{Repository: source.Repository, Task: item, Branch: selected.prepared.TaskBranch}
	var next *TaskRunRoute
	var err error
	if err := s.reportSnapshot(item, *snapshot, true); err != nil {
		return result, nil, err
	}
	switch selected.workflow.LastPhase().(type) {
	case *work.PublicationPhase:
		if err := delivery.recordCommittedDelivery(ctx, store, boundary, source.Repository, item, snapshot, selected, &result); err != nil {
			return result, nil, err
		}
		if err := s.enterDeliverySuccessor(ctx, store, backend, snapshot, selected, &result); err != nil {
			return result, nil, err
		}
		if err := s.reportSnapshot(item, *snapshot, false); err != nil {
			return result, nil, err
		}
		if _, direct := selected.publication.Delivery.(*work.DirectDelivery); direct {
			next, err = deliverySuccessorRoute(result.Task, *snapshot)
			if err != nil {
				return result, nil, err
			}
		}

	case *work.FinalizationPhase:
		if err := delivery.closeDeliveredTask(ctx, store, snapshot, selected.workflow, backend, item); err != nil {
			return result, nil, err
		}
		observed, err := backend.Get(ctx, item.ID)
		if err != nil {
			return result, nil, err
		}
		result.Task = observed
		result.WorktreeCleanup = delivery.cleanupDeliveredWorktree(ctx, store, source.Repository, observed, backend, selected.prepared.Directory)
	case *work.ExternalReviewPhase:
		result.PRURL = selected.publication.Delivery.(*work.PullRequestDelivery).PullRequest.URL
		observed, err := delivery.recordPublicationPR(ctx, backend, item, result.PRURL)
		if err != nil {
			return result, nil, err
		}
		result.Task = observed
	}
	if pr, ok := selected.publication.Delivery.(*work.PullRequestDelivery); ok {
		result.PRURL = pr.PullRequest.URL
	}
	view, err := taskstate.Project(*snapshot)
	if err != nil {
		return result, nil, err
	}
	result.Finalization = taskstate.FinalizationFacts(view)
	return result, next, nil
}

func deliverySuccessorRoute(item task.Task, snapshot work.State) (*TaskRunRoute, error) {
	view, err := taskstate.Project(snapshot)
	if err != nil {
		return nil, err
	}
	route, err := SelectTaskRunRoute(item, view)
	return &route, err
}
