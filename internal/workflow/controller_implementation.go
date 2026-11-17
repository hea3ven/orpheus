package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/hea3ven/orpheus/internal/workflow/review"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/implementation"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/preparation"
	"github.com/hea3ven/orpheus/internal/workstate"
)

func (s ReviewLifecycleService) outputService() implementation.Service {
	boundary := s.OutputGit
	if boundary == nil {
		boundary = implementation.LocalOutputGit{}
	}
	return implementation.Service{Paths: s.Paths, Store: workstate.NewStore(s.Paths), Git: boundary, Logger: s.Logger}
}

func (s Controller) advanceImplementation(ctx context.Context, inv *advanceInvocation, view taskstate.TaskState, route TaskRunRoute, result *AdvanceResult) (bool, error) {
	if s.AgentRunner == nil {
		return true, errors.New("implementation agent runner is required")
	}
	repair := route.Action == TaskRunActionTargetedRepair
	var current ReviewAttemptContext
	if repair {
		var exhausted bool
		var err error
		current, exhausted, err = s.admitRepair(inv, view)
		if err != nil {
			return true, err
		}
		if exhausted {
			outcome := ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeExhausted, Context: current}
			result.Review = &outcome
			return true, nil
		}
	}
	finished, err := s.runImplementation(ctx, inv, route)
	if err != nil {
		return true, err
	}
	result.Implementation = &ImplementationResult{Owner: finished.Start.Attempt.Address, Completed: finished.Completed}
	inv.agentName = finished.Start.Attempt.Execution.Agent
	if !finished.Completed {
		if repair {
			if err := s.Frontend.FollowUpRunIncomplete(current, finished.Start.Attempt.Attempt); err != nil {
				return true, err
			}
			outcome := ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeBlocked, Context: current}
			result.Review = &outcome
		}
		return true, nil
	}
	// The next controller turn finishes implementation before admitting review.
	return false, nil
}

func (s Controller) finishImplementation(ctx context.Context, inv *advanceInvocation, route TaskRunRoute) (bool, error) {
	if route.Owner == nil {
		return true, errors.New("implementation capture requires its phase owner")
	}
	if err := s.notify(inv.base.RepoID(), inv.base.Task, false, &StepView{Name: "record-revision", Kind: "system", Attempt: 1}); err != nil {
		return true, err
	}
	_, err := s.outputService().Capture(ctx, *route.Owner)
	return false, err
}

func (s Controller) repairContext(inv *advanceInvocation, view taskstate.TaskState) (ReviewAttemptContext, error) {
	latest, ok := taskstate.LatestReview(view)
	if !ok {
		return ReviewAttemptContext{}, errors.New("repair requires its owning review")
	}
	pipeline, err := review.ResolvePausedTaskReviewPipeline(s.Paths, inv.base.Source.Repository, latest, inv.pipelineName)
	if err != nil {
		return ReviewAttemptContext{}, err
	}
	inv.pipelineName = pipeline.Name
	return ReviewAttemptContext{ReviewContext: inv.base, Review: latest, Pipeline: pipeline}, nil
}

func (s Controller) runImplementation(ctx context.Context, inv *advanceInvocation, route TaskRunRoute) (implementation.ExecutionFinished, error) {
	backend, err := s.BackendFactory(inv.base.Source)
	if err != nil {
		return implementation.ExecutionFinished{}, err
	}
	runner := phaseexec.Runner{Store: workstate.NewStore(s.Paths), Paths: s.Paths, RepositoryID: inv.base.RepoID(), TaskID: inv.base.TaskID(), Logger: s.Logger}
	current, err := runner.Load(ctx)
	if err != nil {
		return implementation.ExecutionFinished{}, err
	}
	phase := implementation.Dispatch{Paths: s.Paths, Parents: s.Parents, Logger: s.Logger, UsageCaptureEnv: s.UsageCaptureEnv, Run: s.AgentRunner.RunImplementation, BeforeRun: func() error { return s.notifyImplementation(inv.base) }, Results: phaseexec.AgentResults{Commit: runner.Commit}}
	intent := implementation.DispatchIntent{Start: implementation.DispatchStartOptions{TaskID: inv.base.TaskID(), Source: inv.base.Source, Backend: backend, Task: inv.base.Task, AgentName: inv.agentName, RepoRootMode: inv.options.RepoRootMode && route.Action == TaskRunActionStartImplementation, CheckInvocation: implementationAdmission(&route)}}
	for {
		selected, err := phase.Next(current, intent)
		if err != nil {
			return implementation.ExecutionFinished{}, err
		}
		if op, ok := selected.Operation(); ok {
			current, err = runner.Execute(ctx, current, op)
			if err != nil {
				return implementation.ExecutionFinished{}, err
			}
			continue
		}
		outcome, _ := selected.Outcome()
		switch outcome := outcome.(type) {
		case implementation.ContextRequired:
			preparationPhase := preparation.Service{Paths: s.Paths, Repository: inv.base.Source.Repository, Task: outcome.Task, Git: s.DispatchGit, Backend: backend}
			for {
				selected, err := preparationPhase.Next(current, outcome.Intent)
				if err != nil {
					return implementation.ExecutionFinished{}, err
				}
				if op, ok := selected.Operation(); ok {
					current, err = runner.Execute(ctx, current, op)
					if err != nil {
						return implementation.ExecutionFinished{}, err
					}
					continue
				}
				established, _ := selected.Outcome()
				intent.Setup = &established.Setup
				break
			}
		case implementation.ExecutionFinished:
			return outcome, nil
		default:
			return implementation.ExecutionFinished{}, errors.New("unsupported implementation outcome")
		}
	}
}

// This check is workflow invocation policy, not implementation's target policy.
func implementationAdmission(expected *TaskRunRoute) func(task.Task, taskstate.TaskState) error {
	return func(item task.Task, view taskstate.TaskState) error {
		if err := CheckExpectedRoute(expected, item, view); err != nil {
			return err
		}
		if view.Work != nil && len(view.Work.Workflows) > 0 {
			route, err := SelectTaskRunRoute(item, view)
			if err != nil {
				return err
			}
			if route.Action != TaskRunActionStartImplementation && route.Action != TaskRunActionTargetedRepair {
				return fmt.Errorf("current phase selects %s, not implementation", route.Action)
			}
		}
		return nil
	}
}

func (s Controller) enforceAutonomousReviewBudget(
	current ReviewAttemptContext,
	reviewAttempt int,
	attemptsUsed int,
	maxAttempts int,
) (bool, error) {
	if attemptsUsed < maxAttempts {
		return false, nil
	}
	if _, err := current.store.RecordRepairBudgetStop(current.Review.Address, attemptsUsed, maxAttempts); err != nil {
		return false, fmt.Errorf("task run %s: mark autonomous review budget exhausted: %w", current.TaskID(), err)
	}
	if err := s.Frontend.AutonomousBudgetExhausted(current, reviewAttempt, attemptsUsed); err != nil {
		return false, err
	}
	return true, nil
}

func (s Controller) admitRepair(inv *advanceInvocation, view taskstate.TaskState) (ReviewAttemptContext, bool, error) {
	current, err := s.repairContext(inv, view)
	if err != nil {
		return current, false, err
	}
	if inv.reviewAttempts == 0 {
		inv.reviewAttempts = 1
	}
	maximum, err := s.invocationReviewLimit(inv)
	if err != nil {
		return current, false, err
	}
	exhausted, err := s.enforceAutonomousReviewBudget(current, current.Review.Attempt, inv.reviewAttempts, maximum)
	if err != nil || exhausted {
		return current, exhausted, err
	}
	indexes, _ := taskstate.UntargetedBlockingFindingIndexesForFollowUpInState(view, current.Review)
	err = s.Frontend.AutonomousFollowUp(current, current.Review.Attempt, indexes)
	return current, false, err
}

func (s Controller) invocationReviewLimit(inv *advanceInvocation) (int, error) {
	if inv.reviewLimit == 0 {
		maximum, err := review.MaxAutonomousReviewAttempts(s.Paths)
		if err != nil {
			return 0, err
		}
		inv.reviewLimit = maximum
	}
	return inv.reviewLimit, nil
}

func (s Controller) notifyImplementation(base ReviewContext) error {
	if s.Progress == nil {
		return nil
	}
	snapshot, err := s.RunStore.Load(base.RepoID(), base.TaskID())
	if err != nil {
		return err
	}
	view, err := InspectTask(base.Task, snapshot)
	if err != nil {
		return err
	}
	if view.PhaseAttempt == 1 {
		preparation := view
		preparation.Phase = PhasePreparation
		preparation.Owner = &work.AttemptRef{RepositoryID: base.RepoID(), TaskID: base.TaskID(), Workflow: view.Owner.Workflow, Phase: 0}
		preparation.Step = &StepView{Name: "prepare-workdir", Kind: "system", Attempt: 1}
		if err := s.Progress(Progress{View: preparation}); err != nil {
			return fmt.Errorf("%w: %w", ErrProgressUnavailable, err)
		}
	}
	return s.notify(base.RepoID(), base.Task, false, nil)
}
