package workflow

import (
	"context"
	"errors"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

// PreparedImplementation is a reservation made under mutation protection. The
// attached handler runs after that lock is released, so child completion can save
// to Start.Attempt.Address while the controller waits.
type PreparedImplementation struct {
	Dispatch DispatchService
	Start    DispatchStartResult
	Prompt   string
}

// PhaseHandlers supply effects and frontend-specific agent I/O, not routing
// policy. Preparation reserves an implementation; implementation reports an
// actual process result. The controller uses its specialized services for review,
// publication and closure. External review waits for explicit reconciliation.
type PhaseHandlers struct {
	Prepare   func(context.Context, TaskRunRoute) (PreparedImplementation, error)
	Implement func(context.Context, PreparedImplementation) agent.ExecutionResult
}

// AdvanceOptions supplies intent and the inspected action for one invocation.
// Expected is checked again by the reserving handler under mutation protection.
type AdvanceOptions struct {
	Task         task.Task
	RepositoryID string
	Expected     TaskRunRoute
	AgentName    string
	PipelineName string
}

// AdvanceResult contains typed handler results; terminal rendering belongs to CLI.
type AdvanceResult struct {
	Route    TaskRunRoute
	Review   *ReviewLifecycleOutcome
	Delivery *FinalizationResult
}

// Advance coordinates the built-in phases. Neither CLI nor a phase handler
// chooses the next phase from a handler's output.
func (s Controller) Advance(ctx context.Context, opts AdvanceOptions, handlers PhaseHandlers) (AdvanceResult, error) {
	result := AdvanceResult{Route: opts.Expected}
	if s.RunStore == nil {
		return result, errors.New("controller state store is required")
	}
	err := state.WithGlobalMutationLockLogger(ctx, s.Paths, "inspect workflow advancement", s.Logger, func() error {
		view, err := s.RunStore.Load(opts.RepositoryID, opts.Task.ID)
		if err != nil {
			return err
		}
		return CheckExpectedRoute(&opts.Expected, opts.Task, view)
	})
	if err != nil {
		return result, err
	}
	switch opts.Expected.Action {
	case TaskRunActionStartImplementation:
		return s.implementAndReview(ctx, opts, handlers)
	case TaskRunActionTargetedRepair, TaskRunActionStartReview, TaskRunActionResumeReview:
		outcome, err := s.runReview(ctx, ReviewLifecycleOptions{TaskID: opts.Task.ID, PipelineName: opts.PipelineName, DispatchAgentName: opts.AgentName, Expected: &opts.Expected})
		result.Review = &outcome
		return result, err
	case TaskRunActionPreparePublication:
		base, err := s.resolveReviewTask(ctx, opts.Task.ID)
		if err != nil {
			return result, err
		}
		completed, err := s.RunStore.CurrentReviewAt(*opts.Expected.Owner)
		if err != nil {
			return result, err
		}
		outcome, _, err := s.handleAutonomousReviewResult(ctx, ReviewAttemptContext{ReviewContext: base, Review: completed}, taskstate.ReviewStatusPassed)
		result.Review = &outcome
		return result, err
	case TaskRunActionPublish, TaskRunActionRetryFinalization:
		delivered, err := s.Finalize(ctx, FinalizeOptions{TaskID: opts.Task.ID, Expected: &opts.Expected})
		result.Delivery = &delivered
		return result, err
	case TaskRunActionImplementationActive, TaskRunActionReviewActive, TaskRunActionOpenPR, TaskRunActionCompleted:
		return result, nil
	default:
		return result, errors.New("unsupported workflow action")
	}
}

func (s Controller) checkPreparedImplementation(ctx context.Context, opts AdvanceOptions, prepared PreparedImplementation) error {
	return state.WithGlobalMutationLockLogger(ctx, s.Paths, "verify implementation reservation", s.Logger, func() error {
		owner := prepared.Start.Attempt.Address
		if owner.RepositoryID != opts.RepositoryID || owner.TaskID != opts.Task.ID || owner.Step != nil || owner.BranchSync != nil {
			return errors.New("preparation returned a different implementation owner")
		}
		view, err := s.RunStore.Load(opts.RepositoryID, opts.Task.ID)
		if err != nil {
			return err
		}
		if view.Work == nil {
			return errors.New("preparation did not reserve implementation")
		}
		return view.Work.CheckActiveAttempt(owner)
	})
}

func (s Controller) implementAndReview(ctx context.Context, opts AdvanceOptions, handlers PhaseHandlers) (AdvanceResult, error) {
	result := AdvanceResult{Route: opts.Expected}
	if handlers.Prepare == nil || handlers.Implement == nil {
		return result, errors.New("preparation and implementation handlers are required")
	}
	prepared, err := handlers.Prepare(ctx, opts.Expected)
	if err != nil {
		return result, err
	}
	if err := s.checkPreparedImplementation(ctx, opts, prepared); err != nil {
		return result, err
	}
	execution := handlers.Implement(ctx, prepared)
	if err := prepared.Dispatch.RecordExecution(prepared.Start.Attempt.Address, execution); err != nil {
		return result, err
	}
	outcome, reviewed, err := s.reviewAfterImplementation(ctx, ReviewAfterRunCompletionOptions{
		RepoID: opts.RepositoryID, TaskID: opts.Task.ID, RunAttempt: prepared.Start.Attempt.Attempt,
		SelectedDispatchAgentName: prepared.Start.Command.AgentName, FallbackDispatchAgentName: opts.AgentName, PipelineName: opts.PipelineName,
	})
	if reviewed {
		result.Review = &outcome
	}
	return result, err
}
