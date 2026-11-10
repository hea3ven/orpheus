package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
)

// PublicationRecoveryResponse explicitly confirms the disclosed restoration.
// Manual repair and pause use PauseResponse and leave checkout and state intact.
type PublicationRecoveryResponse struct{ Checkout gitmeta.PublicationRecovery }

func (PublicationRecoveryResponse) workflowResponse() {}

// PublicationManualRepairResponse selects the actual repaired clean checkout.
// It neither restores files nor infers an unrecorded local commit as completed.
type PublicationManualRepairResponse struct{}

func (PublicationManualRepairResponse) workflowResponse() {}

func recoveryRequest(w *work.Workflow, p *work.PublicationPhase, repo task.Repository) gitmeta.PublicCommitRequest {
	if p.Integration != nil {
		return gitmeta.PublicCommitRequest{Directory: repo.Path, Branch: p.Integration.Destination.Branch, Input: gitmeta.Revision{Commit: p.Integration.Destination.Commit}}
	}
	if p.DirectSync != nil {
		return gitmeta.PublicCommitRequest{Directory: w.Context().Directory, Branch: w.Context().TaskBranch, Input: gitmeta.Revision{Commit: p.DirectSync.Source.Commit}}
	}
	return publicationRequest(w, p)
}

func (s Controller) verifyDirectRecoveryExecution(p *work.PublicationPhase) error {
	if p.DirectSync == nil || p.DirectSync.Execution == nil {
		return nil
	}
	e := p.DirectSync.Execution
	if _, running := e.Process.(*work.RunningProcess); !running {
		return nil
	}
	inspection := InspectAttachedExecution(taskstate.AgentExecution{Status: taskstate.RunStatusRunning, SupervisorPID: e.SupervisorPID, ChildPID: e.ChildPID}, s.ProcessProbe)
	if inspection.Condition != AttachedExecutionRecoverable {
		return fmt.Errorf("direct resolver prevents recovery: %s", inspection.Reason)
	}
	return nil
}

func publicationRequest(w *work.Workflow, p *work.PublicationPhase) gitmeta.PublicCommitRequest {
	input := p.Input.Code
	return gitmeta.PublicCommitRequest{Directory: w.Context().Directory, Branch: w.Context().TaskBranch, Parent: p.Operation.Parent, Ref: p.Operation.Ref, Message: w.Publication.CommitMessage(), Input: gitmeta.Revision{Commit: input.Commit, Tree: input.Tree, RetainedRef: input.RetainedRef}}
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
		p := w.LastPhase().(*work.PublicationPhase)
		if err := s.verifyDirectRecoveryExecution(p); err != nil {
			return err
		}
		result, err = s.publicationBoundary().InspectPublicationRecovery(ctx, recoveryRequest(w, p, base.Source.Repository))
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
	if err := s.verifyDirectRecoveryExecution(p); err != nil {
		return View{}, err
	}
	req := recoveryRequest(w, p, base.Source.Repository)
	if err := s.publicationBoundary().RestorePublicationInput(ctx, req, response.Checkout); err != nil {
		return View{}, err
	}
	if p.DirectSync != nil || p.Integration != nil {
		return s.continueRepairedPublicHistory(ctx, base, store, snapshot)
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
	if _, pr := p.Target.(*work.PullRequestTarget); pr && p.Delivery == nil {
		return nil
	}
	if p.Operation == nil || p.Operation.Public == nil || p.PrivateRefsReleasedAt != nil {
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

func (s Controller) continueRepairedPublication(ctx context.Context, base ReviewContext, store WorkStore, snapshot *work.State) (View, error) {
	w := snapshot.ActiveWorkflow()
	p := w.LastPhase().(*work.PublicationPhase)
	backend, err := s.BackendFactory(base.Source)
	if err != nil {
		return View{}, err
	}
	if err := verifyPublicationOwnership(ctx, base.Source, base.Task, backend, w); err != nil {
		return View{}, err
	}
	if err := s.verifyDirectRecoveryExecution(p); err != nil {
		return View{}, err
	}
	if p.DirectSync != nil || p.Integration != nil {
		return s.continueRepairedPublicHistory(ctx, base, store, snapshot)
	}
	// No public result was recorded. A repaired clean committed input receives a
	// new exact decision, rather than requiring an eventual forced restoration.
	plan := publicationContinuation(p)
	owner := work.AttemptRef{RepositoryID: snapshot.RepositoryID, TaskID: snapshot.TaskID, Workflow: len(snapshot.Workflows) - 1, Phase: len(w.Phases)}
	if err := s.observePublicationInput(ctx, w, owner, &plan, nil); err != nil {
		return View{}, err
	}
	w.Phases = append(w.Phases, &plan)
	if err := savePublicationOperation(store, snapshot); err != nil {
		return View{}, err
	}
	return interactionView(base.Task, *snapshot)
}

func (s Controller) continueRepairedPublicHistory(ctx context.Context, base ReviewContext, store WorkStore, snapshot *work.State) (View, error) {
	w := snapshot.ActiveWorkflow()
	p := w.LastPhase().(*work.PublicationPhase)
	if p.Integration != nil && w.Context().Directory == base.Source.Repository.Path {
		if err := s.Delivery.directBoundary(s.publicationBoundary()).ResumeDirectTaskCheckout(ctx, w.Context().Directory, w.Context().TaskBranch); err != nil {
			return View{}, err
		}
	}
	current, err := s.publicationBoundary().InspectPublicationCheckout(ctx, w.Context().Directory, w.Context().TaskBranch)
	if err != nil {
		return View{}, err
	}
	destination, err := s.publicationBoundary().ObserveDestination(ctx, w.Context().Directory, w.FixedDestination().Branch)
	if err != nil {
		return View{}, err
	}
	if p.Integration != nil {
		selected := committedPublication{publication: p, prepared: w.Context()}
		checkpoints, err := s.Delivery.directBoundary(s.publicationBoundary()).InspectDirectIntegrationCheckpoints(ctx, directIntegrationRequest(base.Source.Repository, selected))
		if err != nil {
			return View{}, err
		}
		if checkpoints.Destination.Commit != p.Integration.Destination.Commit && checkpoints.Destination.Commit != destination.Commit {
			return View{}, fmt.Errorf("local destination %s in %s still needs repair; restore recorded checkpoint %s or the observed destination %s before continuing the selected task input", p.Integration.Destination.Branch, base.Source.Repository.Path, p.Integration.Destination.Commit, destination.Commit)
		}
	}
	if p.DirectSync != nil && p.DirectSync.Execution != nil {
		if _, running := p.DirectSync.Execution.Process.(*work.RunningProcess); running {
			p.DirectSync.Execution.Process = &work.InterruptedProcess{At: time.Now().UTC(), Reason: "Operator recovered abandoned direct-sync checkout", Trigger: "publication_recovery"}
		}
	}
	selected := committedPublication{workflowIndex: len(snapshot.Workflows) - 1, phaseIndex: len(w.Phases) - 1, workflow: w, publication: p, prepared: w.Context()}
	err = s.Delivery.renewDirectPublication(ctx, store, s.publicationBoundary(), snapshot, selected, work.CodeIdentity{Commit: current.Commit, Tree: current.Tree}, destination, "Operator restored or manually repaired public history; result was not separately task reviewed", false)
	if err != nil && !errors.Is(err, ErrPublicationConsentRequired) {
		return View{}, err
	}
	return interactionView(base.Task, *snapshot)
}
