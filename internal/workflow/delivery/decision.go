package delivery

import (
	"context"
	"errors"
	"fmt"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"reflect"
)

// continuation retains selection facts, not consent or delivery operations.
func publicationContinuation(p *work.PublicationPhase) work.PublicationPhase {
	return work.PublicationPhase{Review: p.Review, Input: p.Input, Destination: p.Destination, Comparison: p.Comparison, Target: p.Target}
}

func (s *Service) changePublicationChoices(ctx context.Context, snapshot *work.State, owner work.AttemptRef, proposal FlowAnswer) (bool, error) {
	if err := s.verifyPublicationInput(ctx, *snapshot, owner); err != nil {
		return false, err
	}
	_, selectedPR := snapshot.ActiveWorkflow().LastPhase().(*work.PublicationPhase).Target.(*work.PullRequestTarget)
	if proposal.Flow == publication.IntegrationFlowPullRequest && proposal.PRDraft == nil && !selectedPR {
		draft, err := s.defaultPRDraft(s.Source.Repository.PRDraft)
		if err != nil {
			return false, err
		}
		proposal.PRDraft = &draft
	}
	return revisePublication(snapshot, proposal)
}

func revisePublication(snapshot *work.State, response FlowAnswer) (bool, error) {
	if response.Flow == "" {
		return false, errors.New("publication requires an explicit flow")
	}
	if err := publication.ValidateIntegrationFlow(response.Flow); err != nil {
		return false, err
	}
	w := snapshot.ActiveWorkflow()
	previous := w.LastPhase().(*work.PublicationPhase)
	next := publicationContinuation(previous)
	next.Target = &work.DirectMergeTarget{}
	if response.Flow == publication.IntegrationFlowPullRequest {
		target := work.PullRequestTarget{}
		if previousTarget, ok := previous.Target.(*work.PullRequestTarget); ok {
			target = *previousTarget
		}
		if response.PRDraft != nil {
			target.Draft = *response.PRDraft
		}
		next.Target = &target
	} else if response.PRDraft != nil {
		return false, errors.New("draft status applies only to pull-request publication")
	}
	if reflect.DeepEqual(next, publicationContinuation(previous)) {
		return false, nil
	}
	if err := continueDirectPublication(w, previous, &next); err != nil {
		return false, err
	}
	w.Phases = append(w.Phases, &next)
	return true, nil
}

func (s *Service) observePublicationInput(ctx context.Context, w *work.Workflow, owner work.AttemptRef, plan *work.PublicationPhase, consent *work.PublicationConsent) error {
	r := w.Phases[plan.Review.Phase].(*work.ReviewPhase)
	latest, ok := w.LatestOutput()
	if !ok || latest != r.Scope.Output {
		return errors.New("controller-produced output requires its own review before publication")
	}
	reviewed, err := w.Output(r.Scope.Output)
	if err != nil {
		return err
	}
	prepared := w.Context()
	req := gitmeta.PublicationInputRequest{Directory: prepared.Directory, Branch: prepared.TaskBranch, Destination: w.FixedDestination().Branch, Ref: phaseexec.PublicationInputNamespace(owner), Reviewed: gitmeta.Revision{Commit: reviewed.Commit, Tree: reviewed.Tree, RetainedRef: reviewed.RetainedRef}}
	_, direct := plan.Target.(*work.DirectMergeTarget)
	if consent != nil && direct {
		req.DeliveryRef = phaseexec.RevisionNamespace(owner) + "/delivery"
		req.AcceptedDestination = plan.Destination.Commit
		req.Accepted = &gitmeta.Revision{Commit: plan.Input.Code.Commit, Tree: plan.Input.Code.Tree, RetainedRef: plan.Input.Code.RetainedRef}
	}
	observed, err := s.boundary().ObservePublicationInput(ctx, req)
	if err != nil {
		return err
	}
	code := work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: observed.Revision.Commit, Tree: observed.Revision.Tree}, RetainedRef: observed.Revision.RetainedRef}
	unchanged := code.CodeIdentity == plan.Input.Code.CodeIdentity
	if unchanged {
		code.RetainedRef = plan.Input.Code.RetainedRef
	}
	provenance := work.InputOperator
	if code.CodeIdentity == reviewed.CodeIdentity {
		provenance = work.InputReviewed
	}
	plan.Input = work.PublicationInput{Code: code, Provenance: provenance}
	plan.Comparison = work.PublicationComparison{Relationship: observed.Relationship, Changes: observed.Changes}

	// An accepted unchanged operation reconciles against its original destination
	// checkpoint; observing its own remote merge must not replace its valid grant.
	if consent != nil && unchanged && observed.Recovering {
		return nil
	}
	plan.Destination, err = s.boundary().ObserveDestination(ctx, prepared.Directory, w.FixedDestination().Branch)
	return err
}

// refreshPublicationSnapshot appends a new unconsented phase when actual input or
// destination context changes. Previous decisions remain immutable history.
func (s *Service) refreshPublicationSnapshot(ctx context.Context, source task.RepositorySource, item task.Task, backend task.Lister, snapshot *work.State) (bool, error) {
	w := snapshot.ActiveWorkflow()
	if w == nil {
		return false, nil
	}
	p, ok := w.LastPhase().(*work.PublicationPhase)
	if !ok || p.Delivery != nil {
		return false, nil
	}
	if err := verifyPublicationOwnership(ctx, source, item, backend, w); err != nil {
		return false, err
	}

	if _, direct := p.Target.(*work.DirectMergeTarget); direct {
		if prepared := w.PublicPreparation(p); prepared != nil && prepared.Operation != nil && prepared.Operation.Public != nil {
			return s.refreshDirectPublication(ctx, source.Repository, snapshot, w, p)
		}
	}
	if unchanged, err := s.recordedPublicationUnchanged(ctx, w, p); err != nil || unchanged {
		return false, err
	}

	owner := work.AttemptRef{RepositoryID: snapshot.RepositoryID, TaskID: snapshot.TaskID, Workflow: len(snapshot.Workflows) - 1, Phase: len(w.Phases) - 1}
	plan := publicationContinuation(p)
	if err := s.observePublicationInput(ctx, w, owner, &plan, p.Consent); err != nil {
		return false, err
	}
	if p.Operation != nil && plan.Input == p.Input {
		plan.Destination = p.Destination
	}
	if reflect.DeepEqual(plan, publicationContinuation(p)) {
		return false, nil
	}
	w.Phases = append(w.Phases, &plan)
	return true, nil
}

func verifyPublicationOwnership(ctx context.Context, source task.RepositorySource, item task.Task, backend task.Lister, w *work.Workflow) error {
	prepared := w.Context()
	metadata := item.OrpheusMetadata()
	if !metadata.HasBranch || !metadata.HasWorktree || metadata.Branch != prepared.TaskBranch || metadata.Worktree != prepared.Directory {
		return errors.New("task-source execution target differs from publication ownership")
	}
	if err := tasktarget.CheckBranchOwnership(ctx, backend, item.ID, prepared.TaskBranch); err != nil {
		return err
	}
	p, publishing := w.LastPhase().(*work.PublicationPhase)
	direct := false
	if publishing {
		_, direct = p.Target.(*work.DirectMergeTarget)
	}
	if direct || prepared.Directory == source.Repository.Path {
		return tasktarget.CheckRepoRootAvailable(ctx, backend, source.Repository, item.ID, gitmeta.TaskWorktreeSetupResult{Branch: prepared.TaskBranch, WorktreePath: source.Repository.Path})
	}
	return nil
}

func (s *Service) defaultPRDraft(repository *bool) (bool, error) {
	config, err := publication.LoadConfig(s.Paths)
	if err != nil {
		return false, err
	}
	return publication.ResolvePRDraft(repository, config.PRDraft), nil
}

func (s *Service) verifyPublicationInput(ctx context.Context, snapshot work.State, owner work.AttemptRef) error {
	w := &snapshot.Workflows[owner.Workflow]
	p := w.Phases[owner.Phase].(*work.PublicationPhase)
	backend := s.Backend
	if err := verifyPublicationOwnership(ctx, s.Source, s.Task, backend, w); err != nil {
		return err
	}
	if p.PreparedFrom != nil {
		current, err := s.boundary().InspectPublicationCheckout(ctx, w.Context().Directory, w.Context().TaskBranch)
		if err != nil {
			return err
		}
		destination, err := s.boundary().ObserveDestination(ctx, w.Context().Directory, w.FixedDestination().Branch)
		if err != nil {
			return err
		}
		if current.Commit != p.Input.Code.Commit || current.Tree != p.Input.Code.Tree || destination != p.Destination {
			return phaseexec.ErrStaleInput
		}
		return nil
	}
	observed := publicationContinuation(p)
	if err := s.observePublicationInput(ctx, w, owner, &observed, nil); err != nil {
		return err
	}
	if !reflect.DeepEqual(observed, publicationContinuation(p)) {
		return fmt.Errorf("%w: publication input or destination context changed; continue task run for a new decision", phaseexec.ErrStaleInput)
	}
	return nil
}

func publicationReviewGap(input work.PublicationInput, reviewed string) string {
	if input.Provenance == work.InputReviewed {
		return ""
	}
	return fmt.Sprintf("\n\n## Publication review gap\n\nThe operator selected commit %s (tree %s) instead of reviewed commit %s. These differences were explicitly accepted for publication, not reported as reviewed.", input.Code.Commit, input.Code.Tree, reviewed)
}

func (s *Service) recordedPublicationUnchanged(ctx context.Context, w *work.Workflow, p *work.PublicationPhase) (bool, error) {
	if p.Operation == nil {
		return false, nil
	}
	if p.Operation.Public == nil {
		return true, nil
	}
	current, err := s.boundary().InspectPublicationCheckout(ctx, w.Context().Directory, w.Context().TaskBranch)
	if err != nil {
		return false, err
	}
	if current.Commit == p.Operation.Public.Commit && current.Tree == p.Operation.Public.Tree && current.Tree == p.Input.Code.Tree {
		return true, nil
	}
	remote, err := s.boundary().ObservePublicHead(ctx, w.Context().Directory, w.Context().TaskBranch)
	if err != nil {
		return false, err
	}
	if remote != "" {
		return false, errors.New("recorded publication checkout changed after remote delivery; reconcile public history without another initial squash")
	}
	return false, nil
}

func continueDirectPublication(w *work.Workflow, previous, next *work.PublicationPhase) error {
	prepared := w.PublicPreparation(previous)
	if prepared == nil || prepared.Operation == nil || prepared.Operation.Public == nil {
		return nil
	}
	if _, direct := previous.Target.(*work.DirectMergeTarget); !direct {
		return nil
	}
	if _, direct := next.Target.(*work.DirectMergeTarget); !direct {
		return errors.New("prepared direct delivery cannot change integration flow")
	}
	next.PreparedFrom = previous.PreparedFrom
	if next.PreparedFrom == nil {
		next.PreparedFrom = &work.PublicationRef{Phase: len(w.Phases) - 1}
	}
	next.SyncFrom = previous.SyncFrom
	return nil
}
