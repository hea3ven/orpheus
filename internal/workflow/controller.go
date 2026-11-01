package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

// Controller owns built-in progression. Each phase handler performs only its
// selected work; the loop reloads owned results before selecting another phase.
// No cursor or executable route is accepted from the caller or persisted.
type Controller struct {
	ReviewLifecycleService
	Delivery FinalizationService
}

// AdvanceOptions contains operator choices, not a preselected workflow action.
// Applicability is validated once, against the invocation's initial work.
type AdvanceOptions struct {
	TaskID       string
	AgentName    string
	PipelineName string
	RepoRootMode bool
}

// ImplementationResult reports completion at an exact implementation owner.
type ImplementationResult struct {
	Owner     work.AttemptRef
	Completed bool
}

// AdvanceResult supplies semantic outcomes for frontend rendering.
type AdvanceResult struct {
	Route          TaskRunRoute
	Review         *ReviewLifecycleOutcome
	Delivery       *FinalizationResult
	Implementation *ImplementationResult
	Inspection     ImplementationRunInspection
	View           View
}

// advanceInvocation carries invocation policy and presentation context, never an
// authoritative position. Every iteration derives current work from storage.
type advanceInvocation struct {
	options        AdvanceOptions
	base           ReviewContext
	agentName      string
	pipelineName   string
	reviewAttempts int
	reviewLimit    int
	lastReview     ReviewAttemptContext
	lastStatus     taskstate.ReviewStatus
}

// Advance continues until work needs operator input, execution cannot safely
// proceed, the repair budget is exhausted, or delivery/closure reaches a boundary.
// Reservation and result writes are protected; attached work releases the lock.
func (s Controller) Advance(ctx context.Context, opts AdvanceOptions) (AdvanceResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var result AdvanceResult
	if s.RunStore == nil || s.BackendFactory == nil {
		return result, errors.New("controller state store and task backend are required")
	}
	base, err := s.resolveReviewTask(ctx, opts.TaskID)
	if err != nil {
		return result, err
	}
	prepared, err := s.prepareAdvance(ctx, base)
	if err != nil {
		return result, err
	}
	result.Inspection = prepared.Inspection
	if reviewStartupStopsForInspection(prepared.ReviewInspection) {
		outcome := reviewLifecycleOutcome(ReviewAttemptContext{ReviewContext: base}, prepared.ReviewInspection)
		result.Review = &outcome
		return result, nil
	}
	invocation := advanceInvocation{options: opts, base: base, agentName: strings.TrimSpace(opts.AgentName), pipelineName: strings.TrimSpace(opts.PipelineName)}
	first := true
	for {
		if !first {
			invBase, err := s.resolveReviewTask(ctx, opts.TaskID)
			if err != nil {
				return result, err
			}
			invocation.base = invBase
		}
		view, route, err := s.selectWork(ctx, invocation.base)
		if err != nil {
			return result, err
		}
		result.Route = route
		if first {
			if err := validateAdvanceOptions(base.TaskID(), route.Action, opts); err != nil {
				return result, err
			}
			first = false
		}
		stop, err := s.advancePhase(ctx, &invocation, view, route, &result)
		if err != nil || stop {
			current, viewErr := s.Inspect(ctx, opts.TaskID)
			result.View = current
			if err == nil {
				err = viewErr
			}
			return result, err
		}
	}
}

func (s Controller) selectWork(ctx context.Context, base ReviewContext) (taskstate.TaskState, TaskRunRoute, error) {
	var view taskstate.TaskState
	var route TaskRunRoute
	err := state.WithGlobalMutationLockLogger(ctx, s.Paths, "select workflow work", s.Logger, func() error {
		var err error
		view, err = s.RunStore.Load(base.RepoID(), base.TaskID())
		if err != nil {
			return err
		}
		route, err = SelectTaskRunRoute(base.Task, view)
		return err
	})
	return view, route, err
}

func (s Controller) advancePhase(ctx context.Context, inv *advanceInvocation, view taskstate.TaskState, route TaskRunRoute, result *AdvanceResult) (bool, error) {
	switch route.Action {
	case TaskRunActionStartImplementation, TaskRunActionTargetedRepair:
		return s.advanceImplementation(ctx, inv, view, route, result)
	case TaskRunActionStartReview, TaskRunActionResumeReview:
		return s.advanceReview(ctx, inv, view, route, result)
	case TaskRunActionPreparePublication:
		current := inv.lastReview
		if current.Review.Address.Workflow != route.Owner.Workflow || current.Review.Address.Phase != route.Owner.Phase || current.Review.Address.RepositoryID == "" {
			completed, err := s.RunStore.CurrentReviewAt(*route.Owner)
			if err != nil {
				return true, err
			}
			current = ReviewAttemptContext{ReviewContext: inv.base, Review: completed}
		}
		if err := s.preparePublication(ctx, current); err != nil {
			return true, err
		}
		outcome := ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomePublicationPending, Context: current}
		result.Review = &outcome
		return true, s.notify(inv.base.RepoID(), inv.base.Task, false, nil)
	case TaskRunActionPublish, TaskRunActionRetryFinalization:
		delivered, next, err := s.advanceDeliveryPhase(ctx, FinalizeOptions{TaskID: inv.base.TaskID()}, &route)
		result.Delivery = &delivered
		return next == nil, err
	case TaskRunActionNeedsAttention:
		return true, errors.New(route.Advice)
	case TaskRunActionAwaitDecision, TaskRunActionImplementationActive, TaskRunActionReviewActive, TaskRunActionOpenPR, TaskRunActionCompleted:
		return true, s.notify(inv.base.RepoID(), inv.base.Task, true, nil)
	default:
		return true, fmt.Errorf("unsupported workflow action %q", route.Action)
	}
}

func validateAdvanceOptions(taskID string, action TaskRunAction, opts AdvanceOptions) error {
	if action == TaskRunActionStartImplementation || action == TaskRunActionTargetedRepair {
		return nil
	}
	if opts.RepoRootMode {
		return fmt.Errorf("task run %s: --repo-root only applies when launching implementation work", taskID)
	}
	if action == TaskRunActionStartReview {
		return nil
	}
	if strings.TrimSpace(opts.AgentName) != "" {
		return fmt.Errorf("task run %s: --agent cannot affect the selected workflow path", taskID)
	}
	if strings.TrimSpace(opts.PipelineName) != "" {
		return fmt.Errorf("task run %s: --pipeline cannot affect the selected workflow path", taskID)
	}
	return nil
}

func (s Controller) prepareAdvance(ctx context.Context, base ReviewContext) (PreparedTaskRun, error) {
	if base.Task.Status == task.StatusOpen {
		snapshot, err := s.RunStore.Load(base.RepoID(), base.TaskID())
		if err != nil {
			return PreparedTaskRun{}, err
		}
		route, err := SelectTaskRunRoute(base.Task, snapshot)
		if err != nil {
			return PreparedTaskRun{}, err
		}
		if route.Action == TaskRunActionNeedsAttention {
			return PreparedTaskRun{}, errors.New(route.Advice)
		}
	}
	return PrepareTaskRun(ctx, PrepareTaskRunOptions{Paths: s.Paths, Store: s.RunStore, RepoID: base.RepoID(), TaskID: base.TaskID(), Task: base.Task, Probe: s.ProcessProbe, Trigger: "task_run"})
}
