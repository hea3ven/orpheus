package workflow

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
)

// PublicationPlanResponse replaces publication-owned intent, never authorizes it.
// Destination is fixed on initial review entry and is not an editable choice.
type PublicationPlanResponse struct {
	Flow    publication.IntegrationFlow
	Content string
}

func (PublicationPlanResponse) workflowResponse() {}

func reviewRequirements(item task.Task) string {
	return item.Title + "\n\n" + item.Description + "\n\n" + item.AcceptanceCriteria
}

func revisePublication(snapshot *work.State, response PublicationPlanResponse) (bool, error) {
	if response.Flow == "" {
		return false, errors.New("publication plan requires an explicit flow")
	}
	if err := publication.ValidateIntegrationFlow(response.Flow); err != nil {
		return false, err
	}
	title, body, ok := strings.Cut(response.Content, "\n\n")
	if !ok || strings.TrimSpace(title) == "" || strings.TrimSpace(body) == "" {
		return false, errors.New("publication content requires title and body separated by a blank line")
	}
	w := snapshot.ActiveWorkflow()
	previous := w.LastPhase().(*work.PublicationPhase)
	plan := previous.Plan
	plan.Content = response.Content
	plan.Target = &work.DirectMergeTarget{}
	if response.Flow == publication.IntegrationFlowPullRequest {
		plan.Target = &work.PullRequestTarget{}
		if old, ok := previous.Plan.Target.(*work.PullRequestTarget); ok {
			plan.Target = old
		}
	}
	if reflect.DeepEqual(plan, previous.Plan) {
		return false, nil
	}
	w.Phases = append(w.Phases, &work.PublicationPhase{Plan: plan})
	return true, nil
}

func (s Controller) publicationBoundary() FinalizationGit {
	if s.Delivery.Git != nil {
		return s.Delivery.Git
	}
	return LocalFinalizationGit{}
}

func (s Controller) observePublicationPlan(ctx context.Context, w *work.Workflow, owner work.AttemptRef, plan *work.PublicationPlan, consent *work.PublicationConsent) error {
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
	req := gitmeta.PublicationInputRequest{Directory: prepared.Directory, Branch: prepared.TaskBranch, Destination: w.FixedDestination().Branch, Ref: publicationInputNamespace(owner), Reviewed: gitmeta.Revision{Commit: reviewed.Commit, Tree: reviewed.Tree, RetainedRef: reviewed.RetainedRef}}
	_, direct := plan.Target.(*work.DirectMergeTarget)
	if consent != nil && direct {
		req.DeliveryRef = outputRefNamespace(owner) + "/delivery"
		req.AcceptedDestination = plan.Destination.Commit
		req.Accepted = &gitmeta.Revision{Commit: plan.Input.Code.Commit, Tree: plan.Input.Code.Tree, RetainedRef: plan.Input.Code.RetainedRef}
	}
	observed, err := s.publicationBoundary().ObservePublicationInput(ctx, req)
	if err != nil {
		return err
	}
	code := work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: observed.Revision.Commit, Tree: observed.Revision.Tree}, RetainedRef: observed.Revision.RetainedRef}
	unchanged := code.CodeIdentity == plan.Input.Code.CodeIdentity
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
	plan.Destination, err = s.publicationBoundary().ObserveDestination(ctx, prepared.Directory, w.FixedDestination().Branch)
	return err
}

func publicationRequirements(w *work.Workflow, p *work.PublicationPhase, item task.Task) bool {
	return w.Phases[p.Plan.Review.Phase].(*work.ReviewPhase).Scope.Requirements == reviewRequirements(item)
}

// refreshPublicationSnapshot appends a new unconsented plan when actual input or
// destination context changes. Previous decisions remain immutable history.
func (s Controller) refreshPublicationSnapshot(ctx context.Context, source task.RepositorySource, item task.Task, backend task.Lister, snapshot *work.State) (bool, error) {
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
	owner := work.AttemptRef{RepositoryID: snapshot.RepositoryID, TaskID: snapshot.TaskID, Workflow: len(snapshot.Workflows) - 1, Phase: len(w.Phases) - 1}
	if !publicationRequirements(w, p, item) {
		return s.reviewChangedRequirements(ctx, source, item, snapshot, owner, p)
	}
	plan := p.Plan
	if err := s.observePublicationPlan(ctx, w, owner, &plan, p.Consent); err != nil {
		return false, err
	}
	if reflect.DeepEqual(plan, p.Plan) {
		return false, nil
	}
	w.Phases = append(w.Phases, &work.PublicationPhase{Plan: plan})
	return true, nil
}

func verifyPublicationOwnership(ctx context.Context, source task.RepositorySource, item task.Task, backend task.Lister, w *work.Workflow) error {
	prepared := w.Context()
	metadata := item.OrpheusMetadata()
	if !metadata.HasBranch || !metadata.HasWorktree || metadata.Branch != prepared.TaskBranch || metadata.Worktree != prepared.Directory {
		return errors.New("task-source execution target differs from publication ownership")
	}
	if err := ensureTaskBranchUnowned(ctx, backend, item.ID, prepared.TaskBranch); err != nil {
		return err
	}
	if prepared.Directory == source.Repository.Path {
		return ensureRepoRootDispatchAvailable(ctx, backend, source.Repository, item.ID, gitmeta.TaskWorktreeSetupResult{Branch: prepared.TaskBranch, WorktreePath: prepared.Directory})
	}
	return nil
}

func (s Controller) reviewChangedRequirements(ctx context.Context, source task.RepositorySource, item task.Task, snapshot *work.State, owner work.AttemptRef, p *work.PublicationPhase) (bool, error) {
	// Operator selection is not an exemption for changed requirements. Admission
	// still requires the exact controller output before fresh review can start.
	if err := s.verifyInteractionRevision(ctx, *snapshot, owner); err != nil {
		return false, fmt.Errorf("changed requirements require fresh review of controller output: %w", err)
	}
	pipeline, err := ResolveTaskReviewPipeline(s.Delivery.Paths, source.Repository, "")
	if err != nil {
		return false, err
	}
	w := snapshot.ActiveWorkflow()
	scope := w.Phases[p.Plan.Review.Phase].(*work.ReviewPhase).Scope
	scope.Requirements = reviewRequirements(item)
	destination, err := s.publicationBoundary().ObserveDestination(ctx, w.Context().Directory, w.FixedDestination().Branch)
	if err != nil {
		return false, err
	}
	scope.Destination = &destination
	w.ReviewContracts = append(w.ReviewContracts, reviewContract(pipeline))
	w.Phases = append(w.Phases, &work.ReviewPhase{Contract: len(w.ReviewContracts) - 1, Scope: scope, StartedAt: time.Now().UTC()})
	return true, nil
}

func (s Controller) refreshPublication(ctx context.Context, base ReviewContext, expected TaskRunRoute) (bool, error) {
	changed := false
	err := state.WithGlobalMutationLockLogger(ctx, s.Paths, "refresh publication input", s.Logger, func() error {
		store := s.Delivery.Store
		if store == nil {
			store = workstate.NewStore(s.Paths)
		}
		snapshot, err := store.Load(base.RepoID(), base.TaskID())
		if err != nil {
			return err
		}
		view, err := taskstate.Project(snapshot)
		if err != nil {
			return err
		}
		if err := CheckExpectedRoute(&expected, base.Task, view); err != nil {
			return err
		}
		backend, err := s.BackendFactory(base.Source)
		if err != nil {
			return err
		}
		changed, err = s.refreshPublicationSnapshot(ctx, base.Source, base.Task, backend, &snapshot)
		if err != nil || !changed {
			return err
		}
		_, err = store.Save(snapshot, snapshot.Version)
		return err
	})
	return changed, err
}

func (s Controller) effectiveIntegrationFlow(ctx ReviewAttemptContext) (publication.IntegrationFlow, error) {
	state, err := s.RunStore.Load(ctx.RepoID(), ctx.TaskID())
	if err != nil {
		return "", fmt.Errorf("load task state for integration flow: %w", err)
	}
	if locked := taskstate.FinalizationFacts(state).IntegrationFlow; strings.TrimSpace(string(locked)) != "" {
		return locked, nil
	}
	config, err := publication.LoadConfig(s.Paths)
	if err != nil {
		return "", err
	}
	return publication.ResolveIntegrationFlow("", publication.IntegrationFlow(ctx.Source.Repository.IntegrationFlow), config.IntegrationFlow), nil
}

func (s Controller) verifyPublicationPlan(ctx context.Context, base ReviewContext, snapshot work.State, owner work.AttemptRef) error {
	w := &snapshot.Workflows[owner.Workflow]
	p := w.Phases[owner.Phase].(*work.PublicationPhase)
	backend, err := s.BackendFactory(base.Source)
	if err != nil {
		return err
	}
	if err := verifyPublicationOwnership(ctx, base.Source, base.Task, backend, w); err != nil {
		return err
	}
	if !publicationRequirements(w, p, base.Task) {
		return errors.New("task requirements changed; continue task run for fresh review")
	}
	observed := p.Plan
	if err := s.observePublicationPlan(ctx, w, owner, &observed, nil); err != nil {
		return err
	}
	if !reflect.DeepEqual(observed, p.Plan) {
		return fmt.Errorf("%w: publication input or destination context changed; continue task run for a new decision", ErrStaleInteraction)
	}
	return nil
}
