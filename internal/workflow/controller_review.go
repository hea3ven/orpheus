package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/review"
)

func (s Controller) advanceReview(ctx context.Context, inv *advanceInvocation, view taskstate.TaskState, route TaskRunRoute, result *AdvanceResult) (bool, error) {
	if route.Action == TaskRunActionRecoverReviewCheckout {
		return true, s.resolveCheckoutRecovery(ctx, inv.base, view, *route.Owner)
	}
	// A blocked result with no selected repair is a stopping boundary, not an
	// invitation to run the same review repeatedly within this invocation.
	if inv.lastStatus == taskstate.ReviewStatusBlocked && inv.lastReview.Review.Address.Workflow == route.Owner.Workflow && inv.lastReview.Review.Address.Phase == route.Owner.Phase {
		return true, nil
	}
	if _, err := s.invocationReviewLimit(inv); err != nil {
		return true, err
	}
	current, err := s.admitReview(ctx, inv.base, review.AdmissionIntent{Owner: *route.Owner, Resume: route.Action == TaskRunActionResumeReview, PipelineName: inv.pipelineName})
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

func (s Controller) resolveCheckoutRecovery(ctx context.Context, base ReviewContext, view taskstate.TaskState, owner work.AttemptRef) error {
	base.Workdir = view.Work.Workflows[owner.Workflow].Context().Directory
	latest, ok := taskstate.LatestReview(view)
	if !ok {
		return errors.New("checkout recovery has no review owner")
	}
	presentation, err := s.Frontend.PipelinePresentation(ReviewAttemptContext{ReviewContext: base, Review: latest})
	if err != nil {
		return err
	}
	return review.ResolveCheckoutRecovery(review.CheckoutRecoveryOptions{
		Context: ctx, Owner: owner, Runner: s.reviewRunner(base.RepoID(), base.TaskID()),
		Prompt: presentation.PromptCheckoutRecovery, Restore: s.ReviewEffects.RestoreCandidate, Output: presentation.Stderr,
	})
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
	outcome, err := s.advanceReviewFollowUps(ctx, current)
	if err != nil {
		if !preservesReviewDecision(err) {
			_, _ = s.reviewRunner(current.RepoID(), current.TaskID()).Commit(context.WithoutCancel(ctx), review.FailureChange(current.Review.Address, err))
		}
		return true, err
	}
	if outcome.Aborted {
		result.Review = &ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeAborted, Context: current}
		return true, nil
	}
	if err := s.preparePublication(ctx, current); err != nil {
		return true, err
	}
	result.Review = &ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomePublicationPending, Context: current}
	return true, s.notify(current.RepoID(), current.Task, false, nil)
}

func (s Controller) admitReview(ctx context.Context, base ReviewContext, intent review.AdmissionIntent) (ReviewAttemptContext, error) {
	prepared := ReviewAttemptContext{ReviewContext: base}
	runner := s.reviewRunner(base.RepoID(), base.TaskID())
	current, err := runner.Load(ctx)
	if err != nil {
		return prepared, err
	}
	phase := review.Admission{
		Paths: s.Paths, Repository: base.Source.Repository, Task: base.Task, Git: s.CandidateGit,
		HasAgentLauncher: s.AgentLauncher != nil, ReportSelection: s.Frontend.ReportReviewSelection,
	}
	for {
		selected, err := phase.Next(current, intent)
		if err != nil {
			return prepared, err
		}
		op, executing := selected.Operation()
		if request, ok := selected.Input(); ok {
			dispositions, promptErr := s.Frontend.PromptFreshReviewBlockerDispositions(base, request.Blockers)
			interrupted := errors.Is(promptErr, review.ErrFreshReviewBlockerDispositionInterrupted)
			if promptErr != nil && !interrupted {
				return prepared, fmt.Errorf("task run %s: prompt fresh review blocker dispositions: %w", base.TaskID(), promptErr)
			}
			op, err = phase.Respond(current, request.ID, review.PriorFindingsAnswer{Dispositions: dispositions, Interrupted: interrupted})
			if err != nil {
				return prepared, fmt.Errorf("task run %s: %w", base.TaskID(), err)
			}
			executing = true
		}
		if executing {
			current, err = runner.Execute(ctx, current, op)
			if err != nil {
				return prepared, fmt.Errorf("task run %s: admit review attempt: %w", base.TaskID(), err)
			}
			continue
		}
		outcome, _ := selected.Outcome()
		if blocked, ok := outcome.(review.AdmissionBlocked); ok {
			prepared.Target, prepared.Workdir = blocked.Target, blocked.Target.Worktree
			prepared.Pipeline = blocked.Pipeline
			return prepared, freshReviewBlockerGuardError{interrupted: blocked.Interrupted}
		}
		ready := outcome.(review.AdmittedReview)
		prepared.Target, prepared.Workdir = ready.Target, ready.Target.Worktree
		prepared = ReviewAttemptContext{
			ReviewContext: prepared.ReviewContext,
			Review:        ready.Review, Pipeline: ready.Pipeline,
			AgentConfig: ready.AgentConfig, execution: ready.Execution, current: &current,
		}
		if prepared.Resumed() {
			if err := s.Frontend.ReviewResumed(prepared); err != nil {
				return prepared, err
			}
		}
		return prepared, nil
	}
}
