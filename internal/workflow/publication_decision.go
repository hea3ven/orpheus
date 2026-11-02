package workflow

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

// PublicationPlanResponse replaces pending intent, never authorizes it. All
// fields are explicit so an editor cannot silently inherit a changed default.
// A changed destination starts fresh review before another plan is prepared.
type PublicationPlanResponse struct {
	Flow              publication.IntegrationFlow
	DestinationBranch string
	Content           string
}

func (PublicationPlanResponse) workflowResponse() {}

func reviewRequirements(item task.Task) string {
	return item.Title + "\n\n" + item.Description + "\n\n" + item.AcceptanceCriteria
}

func (s Controller) revisePublication(ctx context.Context, base ReviewContext, snapshot *work.State, response PublicationPlanResponse, at time.Time) (bool, error) {
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
	if strings.TrimSpace(response.DestinationBranch) == "" {
		return false, errors.New("publication plan requires a destination branch")
	}
	w := snapshot.ActiveWorkflow()
	previous := w.LastPhase().(*work.PublicationPhase)
	if response.DestinationBranch == w.Context().TaskBranch {
		return false, errors.New("publication destination cannot be the task branch")
	}
	var target work.PublicationTarget = &work.PullRequestTarget{}
	if response.Flow == publication.IntegrationFlowDirectMerge {
		target = &work.DirectMergeTarget{}
	} else if old, ok := previous.Plan.Target.(*work.PullRequestTarget); ok {
		target = old
	}
	boundary := s.CandidateGit
	if boundary == nil {
		boundary = LocalReviewCandidateGit{}
	}
	destination, err := boundary.ObserveDestination(ctx, w.Context().Directory, response.DestinationBranch)
	if err != nil {
		return false, err
	}
	review := w.Phases[previous.Plan.Review.Phase].(*work.ReviewPhase)
	if review.Scope.Destination != nil && *review.Scope.Destination == destination && review.Scope.Requirements == reviewRequirements(base.Task) {
		// Identical submissions retain the operation identity (and stale-response
		// protection); changed intent gets a new immutable occurrence.
		if response.Content == previous.Plan.Content && reflect.DeepEqual(previous.Plan.Target, target) {
			return false, nil
		}
		w.Phases = append(w.Phases, &work.PublicationPhase{Plan: work.PublicationPlan{Review: previous.Plan.Review, Target: target, Content: response.Content}})
		return true, nil
	}
	pipeline, err := ResolveTaskReviewPipeline(s.Paths, base.Source.Repository, "")
	if err != nil {
		return false, err
	}
	contract := reviewContract(pipeline)
	w.ReviewContracts = append(w.ReviewContracts, contract)
	scope := review.Scope
	scope.Destination = &destination
	scope.Requirements = reviewRequirements(base.Task)
	w.Phases = append(w.Phases, &work.ReviewPhase{Contract: len(w.ReviewContracts) - 1, Scope: scope, StartedAt: at, Publication: &work.PublicationIntent{Target: target, Content: response.Content}})
	return true, nil
}

func (s Controller) verifyPublicationScope(ctx context.Context, item task.Task, snapshot work.State, owner work.AttemptRef) error {
	w := &snapshot.Workflows[owner.Workflow]
	p := w.Phases[owner.Phase].(*work.PublicationPhase)
	review := w.Phases[p.Plan.Review.Phase].(*work.ReviewPhase)
	if review.Scope.Requirements != reviewRequirements(item) {
		return errors.New("task requirements changed; revise the publication plan to obtain fresh review")
	}
	boundary := s.CandidateGit
	if boundary == nil {
		boundary = LocalReviewCandidateGit{}
	}
	observed, err := boundary.ObserveDestination(ctx, w.Context().Directory, review.Scope.Destination.Branch)
	if err != nil {
		return err
	}
	if observed != *review.Scope.Destination {
		return errors.New("publication destination context changed; revise the plan to obtain fresh review")
	}
	return nil
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
