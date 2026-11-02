package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

func (s Controller) advanceReview(ctx context.Context, inv *advanceInvocation, view taskstate.TaskState, route TaskRunRoute, result *AdvanceResult) (bool, error) {
	// A blocked result with no selected repair is a stopping boundary, not an
	// invitation to run the same review repeatedly within this invocation.
	if inv.lastStatus == taskstate.ReviewStatusBlocked && inv.lastReview.Review.Address.Workflow == route.Owner.Workflow && inv.lastReview.Review.Address.Phase == route.Owner.Phase {
		return true, nil
	}
	if _, err := s.invocationReviewLimit(inv); err != nil {
		return true, err
	}
	current, err := s.prepareReview(ctx, inv, view, route)
	if err != nil {
		outcome, startErr := reviewLifecycleStartError(current, err)
		result.Review = &outcome
		return true, startErr
	}
	inv.pipelineName = current.Pipeline.Name
	if current.Resumed() && inv.agentName == "" {
		inv.agentName, err = resumedReviewImplementerName(current)
		if err != nil {
			return true, err
		}
	}
	status, err := s.executeReviewAttempt(ctx, current)
	if err != nil {
		return true, err
	}
	inv.reviewAttempts++
	inv.lastReview, inv.lastStatus = current, status
	outcome := ReviewLifecycleOutcome{Context: current}
	switch status {
	case taskstate.ReviewStatusPassed:
		outcome.Kind = ReviewLifecycleOutcomePassed
	case taskstate.ReviewStatusBlocked:
		outcome.Kind = ReviewLifecycleOutcomeBlocked
	case taskstate.ReviewStatusWaitingForManual:
		outcome.Kind = ReviewLifecycleOutcomeWaitingForManual
	case taskstate.ReviewStatusWaitingForAutomatedDecision:
		outcome.Kind = ReviewLifecycleOutcomeWaitingForAutomatedDecision
	case taskstate.ReviewStatusAborted:
		outcome.Kind = ReviewLifecycleOutcomeAborted
	default:
		outcome.Kind = ReviewLifecycleOutcomeOperationalFail
	}
	result.Review = &outcome
	return status != taskstate.ReviewStatusPassed && status != taskstate.ReviewStatusBlocked, nil
}

func (s Controller) prepareReview(ctx context.Context, inv *advanceInvocation, view taskstate.TaskState, route TaskRunRoute) (ReviewAttemptContext, error) {
	if err := s.validate(); err != nil {
		return ReviewAttemptContext{}, err
	}
	base := inv.base
	base.owner = route.Owner
	target, err := ReviewTarget(s.RunStore, s.Paths, base)
	if err != nil {
		return ReviewAttemptContext{}, err
	}
	base.Target, base.Workdir = target, target.Worktree
	base, err = s.validateReviewCandidate(ctx, base)
	if err != nil {
		return ReviewAttemptContext{}, err
	}
	if route.Action == TaskRunActionResumeReview {
		paused, ok := taskstate.LatestReview(view)
		if !ok {
			return ReviewAttemptContext{}, errors.New("current review is missing")
		}
		return s.resumeReview(base, paused, "")
	}
	pipelineName := inv.pipelineName
	if latest, ok := taskstate.LatestReview(view); ok && latest.AutomatedBlockerDecisionInterrupted && pipelineName == "" {
		pipelineName = latest.Pipeline
	}
	pipeline, err := ResolveTaskReviewPipeline(s.Paths, base.Source.Repository, pipelineName)
	if err != nil {
		return ReviewAttemptContext{}, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	return s.startFreshReview(base, pipeline)
}

// advancePublication applies the post-review proposal policy for every passed
// review, including answers recorded outside the attached pipeline. Respond
// records decisions only; task creation happens here before entering Publication.
func (s Controller) advancePublication(ctx context.Context, inv *advanceInvocation, route TaskRunRoute, result *AdvanceResult) (bool, error) {
	current := inv.lastReview
	if current.Review.Address.Workflow != route.Owner.Workflow || current.Review.Address.Phase != route.Owner.Phase || current.Review.Address.RepositoryID == "" {
		completed, err := s.RunStore.CurrentReviewAt(*route.Owner)
		if err != nil {
			return true, err
		}
		current = ReviewAttemptContext{ReviewContext: inv.base, Review: completed}
		if err := s.notify(current.RepoID(), current.Task, true, nil); err != nil {
			return true, err
		}
	}
	shouldPublish, err := s.processSeparateTaskReviewCandidates(ctx, current)
	if err != nil {
		_, _ = current.store.RecordReviewFailure(current.Review.Address, err)
		return true, err
	}
	if !shouldPublish {
		if _, err := current.store.RecordReviewAbort(current.Review.Address, "Operator declined continuation after follow-up creation failure"); err != nil {
			_, _ = current.store.RecordReviewFailure(current.Review.Address, err)
			return true, fmt.Errorf("task run %s: record aborted review: %w", current.TaskID(), err)
		}
		result.Review = &ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeAborted, Context: current}
		return true, nil
	}
	if err := s.preparePublication(ctx, current); err != nil {
		return true, err
	}
	result.Review = &ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomePublicationPending, Context: current}
	return true, s.notify(current.RepoID(), current.Task, false, nil)
}

func (s Controller) preparePublication(ctx context.Context, current ReviewAttemptContext) error {
	snapshot, err := current.store.Load(current.RepoID(), current.TaskID())
	if err != nil {
		return err
	}
	if _, err := taskstate.CompletionRunsForReview(snapshot); err != nil {
		return err
	}
	reviewPhase := snapshot.Work.Workflows[current.Review.Address.Workflow].Phases[current.Review.Address.Phase].(*work.ReviewPhase)
	intent := reviewPhase.Publication
	if intent == nil {
		options, err := ResolvePublicationOptions(s.Paths, current.Source.Repository)
		if err != nil {
			return err
		}
		rendered, err := BuildPublicationPullRequestContentFromStateWithOptions(options, current.Task, snapshot)
		if err != nil {
			return err
		}
		var target work.PublicationTarget = &work.PullRequestTarget{}
		flow, err := s.effectiveIntegrationFlow(current)
		if err != nil {
			return err
		}
		if flow == publication.IntegrationFlowDirectMerge {
			target = &work.DirectMergeTarget{}
		}
		intent = &work.PublicationIntent{Target: target, Content: rendered.Title + "\n\n" + rendered.Body}
	}
	return state.WithGlobalMutationLockLogger(ctx, s.Paths, "reserve publication", s.Logger, func() error {
		_, err := current.store.PreparePublication(current.Review.Address, intent.Target, intent.Content)
		return err
	})
}

func (s Controller) startFreshReview(base ReviewContext, pipeline review.Pipeline) (ReviewAttemptContext, error) {
	if base.owner == nil {
		return ReviewAttemptContext{}, errors.New("review reservation requires selected work ownership")
	}
	if err := s.guardFreshReviewBlockers(base); err != nil {
		return ReviewAttemptContext{ReviewContext: base, Pipeline: pipeline}, err
	}
	prepared, err := s.preparePipeline(base, pipeline)
	if err != nil {
		return prepared, err
	}
	snapshot, err := base.store.Load(base.RepoID(), base.TaskID())
	if err != nil {
		return prepared, err
	}
	if snapshot.Work == nil || snapshot.Work.ActiveWorkflow() == nil {
		return prepared, fmt.Errorf("review requires an active workflow")
	}
	actual, err := SelectTaskRunRoute(base.Task, snapshot)
	if err != nil {
		return prepared, err
	}
	if !sameRouteOwner(base.owner, actual.Owner) {
		return prepared, fmt.Errorf("review advancement is stale; current work changed")
	}
	options, err := s.freshReviewOptions(base, pipeline, snapshot)
	if err != nil {
		return prepared, err
	}
	var reviewAttempt taskstate.ReviewAttempt
	err = state.WithGlobalMutationLockLogger(context.Background(), s.Paths, "reserve task review", s.Logger, func() error {
		var reserveErr error
		reviewAttempt, reserveErr = base.store.StartReviewWithOptions(base.RepoID(), base.TaskID(), options)
		return reserveErr
	})
	if err != nil {
		return prepared, fmt.Errorf("task run %s: start review attempt: %w", base.TaskID(), err)
	}
	prepared.Review = reviewAttempt
	prepared.execution = review.FreshExecution()
	return prepared, nil
}

func (s Controller) freshReviewOptions(base ReviewContext, pipeline review.Pipeline, snapshot taskstate.TaskState) (taskstate.StartReviewOptions, error) {
	w := snapshot.Work.ActiveWorkflow()
	output, ok := w.LatestOutput()
	if !ok {
		return taskstate.StartReviewOptions{}, fmt.Errorf("review requires captured implementation output")
	}
	effort := w.Phases[output.Phase].(*work.ImplementationPhase)
	boundary := s.CandidateGit
	if boundary == nil {
		boundary = LocalReviewCandidateGit{}
	}
	destinationBranch := base.Source.Repository.DefaultBranch
	var intent *work.PublicationIntent
	if latest, ok := taskstate.LatestReview(snapshot); ok && latest.Address.Workflow == len(snapshot.Work.Workflows)-1 {
		prior := w.Phases[latest.Address.Phase].(*work.ReviewPhase)
		if prior.Publication != nil {
			intent = prior.Publication
			destinationBranch = prior.Scope.Destination.Branch
		}
	}
	destination, err := boundary.ObserveDestination(context.Background(), base.Workdir, destinationBranch)
	if err != nil {
		return taskstate.StartReviewOptions{}, err
	}
	contract := reviewContract(pipeline)
	scope := work.ReviewScope{Requirements: reviewRequirements(base.Task), Output: output, DeltaFrom: effort.Input, Destination: &destination}

	return taskstate.StartReviewOptions{Pipeline: pipeline.Name, Step: pipeline.Steps[0].Name, Contract: contract, Scope: scope, Publication: intent, ExpectedVersion: &snapshot.Work.Version}, nil
}

func reviewContract(pipeline review.Pipeline) work.ReviewContract {
	contract := work.ReviewContract{PipelineKey: pipeline.Name}
	for _, step := range pipeline.Steps {
		contract.Slots = append(contract.Slots, work.ReviewSlot{Kind: work.ReviewStepKind(step.Kind), ConfigKey: step.Name})
	}
	return contract
}
