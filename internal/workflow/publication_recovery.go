package workflow

import (
	"context"
	"errors"
	"strings"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
)

// PublicationRecoveryResponse explicitly confirms the disclosed restoration.
// Manual repair and pause use PauseResponse and leave checkout and state intact.
type PublicationRecoveryResponse struct{ Checkout gitmeta.PublicationRecovery }

func (PublicationRecoveryResponse) workflowResponse() {}

func publicationRequest(w *work.Workflow, p *work.PublicationPhase) gitmeta.PublicCommitRequest {
	input := p.Plan.Input.Code
	return gitmeta.PublicCommitRequest{Directory: w.Context().Directory, Branch: w.Context().TaskBranch, Parent: p.Operation.Parent, Ref: p.Operation.Ref, Message: p.Plan.Content, Input: gitmeta.Revision{Commit: input.Commit, Tree: input.Tree, RetainedRef: input.RetainedRef}}
}

// InspectPublicationRecovery discloses the reset target and discard scope without
// resetting, inferring an intermediate state, or reconstructing a lost commit.
func (s Controller) InspectPublicationRecovery(ctx context.Context, id InteractionID) (gitmeta.PublicationRecovery, error) {
	var result gitmeta.PublicationRecovery
	err := state.WithGlobalMutationLockLogger(ctx, s.Paths, "inspect publication recovery", s.Logger, func() error {
		base, err := s.resolveReviewTask(ctx, id.Owner.TaskID)
		if err != nil {
			return err
		}
		store := s.Delivery.Store
		if store == nil {
			store = workstate.NewStore(s.Paths)
		}
		snapshot, err := store.Load(base.RepoID(), base.TaskID())
		if err != nil {
			return err
		}
		view, err := interactionView(base.Task, snapshot)
		if err != nil {
			return err
		}
		if id.Kind != InteractionPublicationRecovery {
			return ErrStaleInteraction
		}
		if err := s.validateResponseTarget(base, snapshot, view.Pending, id); err != nil {
			return err
		}
		backend, err := s.BackendFactory(base.Source)
		if err != nil {
			return err
		}
		w := snapshot.ActiveWorkflow()
		if err := verifyPublicationOwnership(ctx, base.Source, base.Task, backend, w); err != nil {
			return err
		}
		result, err = s.publicationBoundary().InspectPublicationRecovery(ctx, publicationRequest(w, w.LastPhase().(*work.PublicationPhase)))
		return err
	})
	return result, err
}

func (s Controller) restorePublication(ctx context.Context, base ReviewContext, store WorkStore, snapshot *work.State, response PublicationRecoveryResponse) (View, error) {
	w := snapshot.ActiveWorkflow()
	p := w.LastPhase().(*work.PublicationPhase)
	backend, err := s.BackendFactory(base.Source)
	if err != nil {
		return View{}, err
	}
	if err := verifyPublicationOwnership(ctx, base.Source, base.Task, backend, w); err != nil {
		return View{}, err
	}
	if !publicationRequirements(w, p, base.Task) {
		return View{}, errors.New("requirements changed; repair the interrupted checkout manually before fresh review")
	}
	req := publicationRequest(w, p)
	if err := s.publicationBoundary().RestorePublicationInput(ctx, req, response.Checkout); err != nil {
		return View{}, err
	}
	// The old operation intent already precedes this confirmed reset and retry.
	// Any failure before the public record leaves the same recovery decision pending.
	if _, err := packagePublicOperation(ctx, store, s.publicationBoundary(), snapshot, p, req); err != nil {
		return View{}, err
	}
	return interactionView(base.Task, *snapshot)
}

func releasePublicationHistory(ctx context.Context, store WorkStore, boundary FinalizationGit, snapshot *work.State, selected committedPublication) error {
	p := selected.publication
	if _, ok := p.Delivery.(*work.PullRequestDelivery); !ok || p.PrivateRefsReleasedAt != nil {
		return nil
	}
	owner := work.AttemptRef{RepositoryID: snapshot.RepositoryID, TaskID: snapshot.TaskID, Workflow: selected.workflowIndex, Phase: 0}
	revisions := strings.TrimSuffix(outputRefNamespace(owner), "/0") + "/"
	if err := boundary.ReleasePrivatePublicationRefs(ctx, selected.prepared.Directory, []string{revisions, publicationInputNamespace(owner) + "/"}); err != nil {
		return err
	}
	at := time.Now().UTC()
	p.PrivateRefsReleasedAt = &at
	return savePublicationOperation(store, snapshot)
}
