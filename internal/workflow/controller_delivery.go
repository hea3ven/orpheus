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

// Finalize advances only a selected publication or finalization with exact-plan
// consent. It cannot capture implementation output or authorize delivery.
func (c Controller) Finalize(ctx context.Context, opts FinalizeOptions) (FinalizationResult, error) {
	s := c.Delivery
	if ctx == nil {
		ctx = context.Background()
	}
	if s.BackendFactory == nil {
		return FinalizationResult{}, errors.New("task finalization backend factory is required")
	}

	store := s.Store
	if store == nil {
		store = workstate.NewStore(s.Paths)
	}
	boundary := s.Git
	if boundary == nil {
		boundary = LocalFinalizationGit{}
	}
	var result FinalizationResult
	err := state.WithGlobalMutationLockLogger(ctx, s.Paths, finalizationLockOperation, s.Logger, func() error {
		source, item, backend, err := s.resolvePublicationTask(ctx, opts, store)
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
		if err := CheckExpectedRoute(opts.Expected, item, view); err != nil {
			return err
		}
		result, err = c.publishCommitted(ctx, store, boundary, source, item, backend, &snapshot)
		return err
	})
	return result, err
}

func (c Controller) publishCommitted(ctx context.Context, store WorkStore, boundary FinalizationGit, source task.RepositorySource, item task.Task, backend FinalizationBackend, snapshot *work.State) (FinalizationResult, error) {
	s := c.Delivery
	selected, err := selectCommittedPublication(snapshot, item)
	if err != nil {
		return FinalizationResult{}, err
	}
	result := FinalizationResult{Repository: source.Repository, Task: item, Branch: selected.prepared.TaskBranch}
	if err := s.recordCommittedDelivery(ctx, store, boundary, source.Repository, item, snapshot, selected, &result); err != nil {
		return result, err
	}
	if err := c.reconcileCommittedDelivery(ctx, store, source.Repository, item, backend, snapshot, selected, &result); err != nil {
		return result, err
	}
	view, err := taskstate.Project(*snapshot)
	if err != nil {
		return result, err
	}
	result.Finalization = taskstate.FinalizationFacts(view)
	return result, nil
}

func (c Controller) reconcileCommittedDelivery(ctx context.Context, store WorkStore, repo task.Repository, item task.Task, backend FinalizationBackend, snapshot *work.State, selected committedPublication, result *FinalizationResult) error {
	s := c.Delivery
	p, w, prepared := selected.publication, selected.workflow, selected.prepared
	switch delivery := p.Delivery.(type) {
	case *work.PullRequestDelivery:
		result.PRURL = delivery.PullRequest.URL
		observed, err := s.recordPublicationPR(ctx, backend, item, result.PRURL)
		if err != nil {
			return err
		}
		result.Task = observed
		if _, ok := w.LastPhase().(*work.PublicationPhase); ok {
			w.Phases = append(w.Phases, &work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: selected.phaseIndex}})
			version, err := store.Save(*snapshot, snapshot.Version)
			if err != nil {
				return err
			}
			snapshot.Version = version
		}
	case *work.DirectDelivery:
		if _, finalizing := w.LastPhase().(*work.FinalizationPhase); !finalizing {
			w.Phases = append(w.Phases, &work.FinalizationPhase{Acceptance: &work.DirectAcceptance{Publication: work.PublicationRef{Phase: selected.phaseIndex}}})
			version, err := store.Save(*snapshot, snapshot.Version)
			if err != nil {
				return err
			}
			snapshot.Version = version
		}
	}
	if _, finalizing := w.LastPhase().(*work.FinalizationPhase); finalizing {
		if err := s.closeDeliveredTask(ctx, store, snapshot, w, backend, item); err != nil {
			return err
		}
		observed, err := backend.Get(ctx, item.ID)
		if err != nil {
			return err
		}
		result.Task = observed
		result.WorktreeCleanup = s.cleanupDeliveredWorktree(ctx, store, repo, observed, backend, prepared.Directory)
	}
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
