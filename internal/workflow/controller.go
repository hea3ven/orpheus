package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

// Controller is the built-in phase progression authority. Specialized services
// reserve typed occurrences and record owned facts; only the controller chooses
// implementation, review, repair, publication, external review, or finalization.
// There is no persisted cursor or configurable graph.
type Controller struct {
	ReviewLifecycleService
	Delivery FinalizationService
}

func (s Controller) runReview(
	ctx context.Context,
	opts ReviewLifecycleOptions,
) (outcome ReviewLifecycleOutcome, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = gitmeta.ContextWithLogger(ctx, s.Logger)
	span := logging.Start(ctx, s.Logger, "task review workflow",
		slog.String("component", "workflow"),
		slog.String("operation", "task_review"),
		slog.String("task_id", opts.TaskID),
		slog.String("pipeline", strings.TrimSpace(opts.PipelineName)),
	)
	defer func() {
		attrs := []slog.Attr{}
		if outcome.Kind != "" {
			attrs = append(attrs, slog.String("outcome", string(outcome.Kind)))
		}
		if outcome.Context.Source.Repository.ID != "" {
			attrs = append(attrs, slog.String("repo_id", outcome.Context.RepoID()))
		}
		span.FinishError(ctx, err, attrs...)
	}()

	return s.runReviewLifecycle(ctx, opts)
}

func (s Controller) runReviewLifecycle(
	ctx context.Context,
	opts ReviewLifecycleOptions,
) (ReviewLifecycleOutcome, error) {
	if err := s.validate(); err != nil {
		return reviewLifecycleOperationalFailure(ReviewAttemptContext{}, err), err
	}
	preflight, inspection, handled, err := s.recoverReviewBeforeRouting(ctx, opts.TaskID)
	if err != nil {
		return reviewLifecycleOperationalFailure(preflight, err), err
	}
	if handled {
		return reviewLifecycleOutcome(preflight, inspection), nil
	}
	if err := s.checkReviewAdvancement(ctx, preflight.ReviewContext, opts.Expected); err != nil {
		return reviewLifecycleOperationalFailure(preflight, err), err
	}
	maxAttempts, err := reviewMaxAutonomousReviewAttempts(s.Paths)
	if err != nil {
		err = fmt.Errorf("task run %s: %w", opts.TaskID, err)
		return reviewLifecycleOperationalFailure(preflight, err), err
	}
	start, err := s.startReview(ctx, opts.TaskID, opts.PipelineName, opts.Expected)
	if err != nil {
		return reviewLifecycleStartError(start, err)
	}
	dispatchAgentName := opts.DispatchAgentName
	if start.Resumed() && strings.TrimSpace(dispatchAgentName) == "" {
		dispatchAgentName, err = resumedReviewImplementerName(start)
		if err != nil {
			err = fmt.Errorf("task run %s: %w", start.TaskID(), err)
			return reviewLifecycleOperationalFailure(start, err), err
		}
	}
	return s.executeAutonomousReviewLoop(ctx, start, autonomousReviewLoopOptions{
		maxAttempts:       maxAttempts,
		dispatchAgentName: dispatchAgentName,
	})
}

func (s Controller) reviewAfterImplementation(
	ctx context.Context,
	opts ReviewAfterRunCompletionOptions,
) (ReviewLifecycleOutcome, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.RunStore == nil {
		err := errors.New("review lifecycle run store is required")
		return reviewLifecycleOperationalFailure(ReviewAttemptContext{}, err), false, err
	}
	shouldReview, err := CompletedTaskRunReadyForReview(s.RunStore, opts.RepoID, opts.TaskID, opts.RunAttempt)
	if err != nil {
		err = fmt.Errorf("task run %s: inspect completion before review: %w", opts.TaskID, err)
		return reviewLifecycleOperationalFailure(ReviewAttemptContext{}, err), false, err
	}
	if !shouldReview {
		return ReviewLifecycleOutcome{}, false, nil
	}
	dispatchAgentName := strings.TrimSpace(opts.SelectedDispatchAgentName)
	if dispatchAgentName == "" {
		dispatchAgentName = strings.TrimSpace(opts.FallbackDispatchAgentName)
	}
	outcome, err := s.runReview(ctx, ReviewLifecycleOptions{
		TaskID:            opts.TaskID,
		PipelineName:      opts.PipelineName,
		DispatchAgentName: dispatchAgentName,
	})
	return outcome, true, err
}

func (s Controller) executeAutonomousReviewLoop(
	ctx context.Context,
	start ReviewAttemptContext,
	opts autonomousReviewLoopOptions,
) (ReviewLifecycleOutcome, error) {
	if opts.maxAttempts <= 0 {
		err := fmt.Errorf("task run %s: autonomous review attempt budget must be positive", start.TaskID())
		return reviewLifecycleOperationalFailure(start, err), err
	}

	current := start
	for attemptsUsed := 1; ; attemptsUsed++ {
		result, err := s.executeAutonomousReviewAttempt(ctx, current)
		if err != nil {
			return reviewLifecycleOperationalFailure(current, err), err
		}
		if outcome, terminal, err := s.handleAutonomousReviewResult(ctx, current, result); terminal {
			return outcome, err
		}

		latest, indexes, ok, err := latestAutonomousReviewBlockers(current.store, current.RepoID(), current.TaskID())
		if err != nil {
			return reviewLifecycleOperationalFailure(current, err), err
		}
		if !ok {
			return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeBlocked, Context: current}, nil
		}
		exhausted, err := s.enforceAutonomousReviewBudget(current, latest.Attempt, attemptsUsed, opts.maxAttempts)
		if err != nil {
			return reviewLifecycleOperationalFailure(current, err), err
		}
		if exhausted {
			return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeExhausted, Context: current}, nil
		}

		if err := s.runAutonomousReviewFollowUp(ctx, current, opts.dispatchAgentName, latest.Attempt, indexes); err != nil {
			return autonomousFollowUpFailure(current, err)
		}
		next, err := s.startFreshAutonomousReview(ctx, current.ReviewContext, current.Pipeline)
		if err != nil {
			return reviewLifecycleOperationalFailure(current, err), err
		}
		current = next
	}
}

func (s Controller) handleAutonomousReviewResult(
	ctx context.Context,
	current ReviewAttemptContext,
	result taskstate.ReviewStatus,
) (ReviewLifecycleOutcome, bool, error) {
	switch result {
	case taskstate.ReviewStatusPassed:
		snapshot, err := current.store.Load(current.RepoID(), current.TaskID())
		if err != nil {
			return reviewLifecycleOperationalFailure(current, err), true, err
		}
		if _, err := taskstate.CompletionRunsForReview(snapshot); err != nil {
			return reviewLifecycleOperationalFailure(current, err), true, err
		}
		publicationOptions, err := ResolvePublicationOptions(s.Paths, current.Source.Repository)
		if err != nil {
			return reviewLifecycleOperationalFailure(current, err), true, err
		}
		rendered, err := BuildPublicationPullRequestContentFromStateWithOptions(publicationOptions, current.Task, snapshot)
		if err != nil {
			return reviewLifecycleOperationalFailure(current, err), true, err
		}
		content := rendered.Title + "\n\n" + rendered.Body
		var target work.PublicationTarget = &work.PullRequestTarget{}
		flow, err := s.effectiveIntegrationFlow(current)
		if err != nil {
			return reviewLifecycleOperationalFailure(current, err), true, err
		}
		if flow == publication.IntegrationFlowDirectMerge {
			target = &work.DirectMergeTarget{}
		}
		err = state.WithGlobalMutationLockLogger(ctx, s.Paths, "reserve publication", s.Logger, func() error {
			_, err := current.store.PreparePublication(current.Review.Address, target, content)
			return err
		})
		if err != nil {
			return reviewLifecycleOperationalFailure(current, err), true, err
		}
		return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomePublicationPending, Context: current}, true, nil
	case taskstate.ReviewStatusBlocked:
		return ReviewLifecycleOutcome{}, false, nil
	case taskstate.ReviewStatusWaitingForManual:
		return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeWaitingForManual, Context: current}, true, nil
	case taskstate.ReviewStatusWaitingForAutomatedDecision:
		return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeWaitingForAutomatedDecision, Context: current}, true, nil
	case taskstate.ReviewStatusAborted:
		return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeAborted, Context: current}, true, nil
	case taskstate.ReviewStatusFailed:
		return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeOperationalFail, Context: current}, true, nil
	default:
		return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeBlocked, Context: current}, true, nil
	}
}

func (s Controller) startReview(
	ctx context.Context,
	taskID string,
	pipelineName string,
	expected *TaskRunRoute,
) (ReviewAttemptContext, error) {
	base, err := s.resolveReviewTask(ctx, taskID)
	if err != nil {
		return ReviewAttemptContext{}, err
	}
	inspection, handled, err := s.reconcileActiveReviewExecution(ctx, base)
	if err != nil {
		return ReviewAttemptContext{ReviewContext: base}, err
	}
	if handled {
		return ReviewAttemptContext{ReviewContext: base}, reviewStartupInspectionError(inspection)
	}
	view, err := base.store.Load(base.RepoID(), base.TaskID())
	if err != nil {
		return ReviewAttemptContext{}, err
	}
	if err := CheckExpectedRoute(expected, base.Task, view); err != nil {
		return ReviewAttemptContext{}, err
	}
	selected, err := SelectTaskRunRoute(base.Task, view)
	if err != nil {
		return ReviewAttemptContext{}, err
	}
	switch selected.Action {
	case TaskRunActionStartReview, TaskRunActionResumeReview, TaskRunActionTargetedRepair:
	default:
		return ReviewAttemptContext{}, fmt.Errorf("current phase selects %s, not review or repair", selected.Action)
	}
	base.owner = selected.Owner
	requestedPipeline, err := s.resolveRequestedReviewPipeline(base, pipelineName)
	if err != nil {
		return ReviewAttemptContext{}, err
	}
	target, err := ReviewTarget(s.RunStore, s.Paths, base)
	if err != nil {
		return ReviewAttemptContext{}, err
	}
	base.Target, base.Workdir = target, target.Worktree
	// A retryable failed repair owns no reviewable output. Continue the preserved
	// blocker plan first; only its new successfully captured effort admits review.
	if blocked, eligible, err := latestEligibleBlockedReview(s.RunStore, base); err != nil {
		return ReviewAttemptContext{}, err
	} else if eligible {
		return s.continueBlockedReview(base, blocked, pipelineName)
	}
	base, err = s.validateReviewCandidate(ctx, base)
	if err != nil {
		return ReviewAttemptContext{}, err
	}
	if resumed, ok, err := s.routeReviewResumption(base, pipelineName); err != nil || ok {
		return resumed, err
	}
	return s.selectFreshReview(base, requestedPipeline, pipelineName)
}

func (s Controller) routeReviewResumption(
	base ReviewContext,
	pipelineName string,
) (ReviewAttemptContext, bool, error) {
	paused, ok, err := latestAutomatedDecisionWaitingReview(s.RunStore, base)
	if err != nil {
		return ReviewAttemptContext{}, false, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	if ok {
		resumed, err := s.resumeReview(base, paused, pipelineName)
		return resumed, true, err
	}
	paused, ok, err = latestManualWaitingReview(s.RunStore, base)
	if err != nil {
		return ReviewAttemptContext{}, false, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	if ok {
		resumed, err := s.resumeReview(base, paused, pipelineName)
		return resumed, true, err
	}
	blocked, ok, err := latestEligibleBlockedReview(s.RunStore, base)
	if err != nil {
		return ReviewAttemptContext{}, false, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	if !ok {
		return ReviewAttemptContext{}, false, nil
	}
	resumed, err := s.continueBlockedReview(base, blocked, pipelineName)
	return resumed, true, err
}

func (s Controller) selectFreshReview(
	base ReviewContext,
	requestedPipeline *review.Pipeline,
	pipelineName string,
) (ReviewAttemptContext, error) {
	if requestedPipeline != nil {
		return s.startFreshReview(base, *requestedPipeline)
	}
	interrupted, ok, err := latestInterruptedAutomatedBlockerReview(s.RunStore, base)
	if err != nil {
		return ReviewAttemptContext{}, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	if ok {
		pipelineName = interrupted.Pipeline
	}
	pipeline, err := ResolveTaskReviewPipeline(s.Paths, base.Source.Repository, pipelineName)
	if err != nil {
		return ReviewAttemptContext{}, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	return s.startFreshReview(base, pipeline)
}

func (s Controller) startFreshReview(base ReviewContext, pipeline review.Pipeline) (ReviewAttemptContext, error) {
	if base.owner == nil {
		view, err := base.store.Load(base.RepoID(), base.TaskID())
		if err != nil {
			return ReviewAttemptContext{}, err
		}
		route, err := SelectTaskRunRoute(base.Task, view)
		if err != nil {
			return ReviewAttemptContext{}, err
		}
		base.owner = route.Owner
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

func (s Controller) startFreshAutonomousReview(ctx context.Context, base ReviewContext, pipeline review.Pipeline) (ReviewAttemptContext, error) {
	base.owner = nil
	validated, err := s.validateReviewCandidate(ctx, base)
	if err != nil {
		return ReviewAttemptContext{}, err
	}
	current, err := ResolveTaskReviewPipeline(base.paths, base.Source.Repository, pipeline.Name)
	if err != nil {
		return ReviewAttemptContext{}, err
	}
	return s.startFreshReview(validated, current)
}

func (s Controller) checkReviewAdvancement(ctx context.Context, base ReviewContext, expected *TaskRunRoute) error {
	return state.WithGlobalMutationLockLogger(ctx, s.Paths, "inspect review advancement", s.Logger, func() error {
		view, err := base.store.Load(base.RepoID(), base.TaskID())
		if err != nil {
			return err
		}
		if err := CheckExpectedRoute(expected, base.Task, view); err != nil {
			return err
		}
		route, err := SelectTaskRunRoute(base.Task, view)
		if err != nil {
			return err
		}
		switch route.Action {
		case TaskRunActionStartReview, TaskRunActionResumeReview, TaskRunActionTargetedRepair:
			return nil
		default:
			return fmt.Errorf("current phase selects %s, not review or repair", route.Action)
		}
	})
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
	destination, err := boundary.ObserveDestination(context.Background(), base.Workdir, base.Source.Repository.DefaultBranch)
	if err != nil {
		return taskstate.StartReviewOptions{}, err
	}
	contract := work.ReviewContract{PipelineKey: pipeline.Name}
	for _, step := range pipeline.Steps {
		contract.Slots = append(contract.Slots, work.ReviewSlot{Kind: work.ReviewStepKind(step.Kind), ConfigKey: step.Name})
	}
	scope := work.ReviewScope{Requirements: base.Task.Title + "\n\n" + base.Task.Description + "\n\n" + base.Task.AcceptanceCriteria, Output: output, DeltaFrom: effort.Input, Destination: &destination}

	return taskstate.StartReviewOptions{Pipeline: pipeline.Name, Step: pipeline.Steps[0].Name, Contract: contract, Scope: scope, ExpectedVersion: &snapshot.Work.Version}, nil
}
