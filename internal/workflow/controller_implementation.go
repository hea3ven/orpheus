package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskbranch"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

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
	} else if err := validateImplementationReference(inv.base.Source.Repository, inv.base.Task); err != nil {
		return true, err
	}
	prepared, err := s.reserveImplementation(ctx, inv, route)
	if err != nil {
		return true, err
	}
	if err := s.notifyImplementation(inv.base); err != nil {
		return true, prepared.dispatch.RecordExecution(prepared.start.Attempt.Address, agent.ExecutionResult{Err: err})
	}
	execution := s.AgentRunner.RunImplementation(ctx, ImplementationRun{RepoID: inv.base.RepoID(), TaskID: inv.base.TaskID(), Start: prepared.start, Prompt: prepared.prompt})
	if err := prepared.dispatch.RecordExecution(prepared.start.Attempt.Address, execution); err != nil {
		return true, err
	}
	completed, err := implementationCompleted(s.RunStore, prepared.start.Attempt.Address)
	if err != nil {
		return true, err
	}
	result.Implementation = &ImplementationResult{Owner: prepared.start.Attempt.Address, Completed: completed}
	inv.agentName = prepared.start.Command.AgentName
	if !completed {
		if repair {
			if err := s.Frontend.FollowUpRunIncomplete(current, prepared.start.Attempt.Attempt); err != nil {
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
	pipeline, err := ResolvePausedTaskReviewPipeline(s.Paths, inv.base.Source.Repository, latest, inv.pipelineName)
	if err != nil {
		return ReviewAttemptContext{}, err
	}
	inv.pipelineName = pipeline.Name
	return ReviewAttemptContext{ReviewContext: inv.base, Review: latest, Pipeline: pipeline}, nil
}

type reservedImplementation struct {
	dispatch DispatchService
	start    DispatchStartResult
	prompt   string
}

func (s Controller) reserveImplementation(ctx context.Context, inv *advanceInvocation, route TaskRunRoute) (reservedImplementation, error) {
	backend, err := s.BackendFactory(inv.base.Source)
	if err != nil {
		return reservedImplementation{}, err
	}
	prepared := reservedImplementation{dispatch: DispatchService{Paths: s.Paths, RunStore: s.RunStore, Git: s.DispatchGit, Parents: s.Parents, Logger: s.Logger, UsageCaptureEnv: s.UsageCaptureEnv}}
	resolve := func(resolver func(DispatchCommandContext, string) (DispatchCommand, string, error)) func(DispatchCommandContext) (DispatchCommand, error) {
		return func(commandContext DispatchCommandContext) (DispatchCommand, error) {
			if resolver == nil {
				return DispatchCommand{AgentName: inv.agentName}, nil
			}
			command, prompt, err := resolver(commandContext, inv.agentName)
			prepared.prompt = prompt
			return command, err
		}
	}
	prepared.start, err = prepared.dispatch.Start(ctx, DispatchStartOptions{Expected: &route, TaskID: inv.base.TaskID(), Source: inv.base.Source, Backend: backend, Task: inv.base.Task, RepoRootMode: inv.options.RepoRootMode && route.Action == TaskRunActionStartImplementation, ResolveCommand: resolve(s.ResolveCommand), ResolveFollowUpCommand: resolve(s.ResolveFollowUpCommand)})
	return prepared, err
}

func implementationCompleted(store DispatchRunStore, owner work.AttemptRef) (bool, error) {
	latest, ok, err := store.LatestRun(owner.RepositoryID, owner.TaskID)
	if err != nil || !ok {
		return false, err
	}
	if !owner.Equal(latest.Address) {
		return false, errors.New("implementation no longer owns its result")
	}
	exit, observed := latest.Execution.Process.(*work.ExitedProcess)
	return observed && exit.Code != nil && *exit.Code == 0 && latest.Completion != nil, nil
}

func validateImplementationReference(repo task.Repository, item task.Task) error {
	title := publication.RequiresExternalRef(repo.TitleTemplate)
	branch := taskbranch.RequiresExternalRef(repo.BranchTemplate)
	if (!title && !branch) || strings.TrimSpace(item.ExternalRef) != "" {
		return nil
	}
	requirement := "publication title template"
	if branch && title {
		requirement = "publication title and task branch templates"
	} else if branch {
		requirement = "task branch template"
	}
	return fmt.Errorf("task run %s: %s requires a task external reference; set it with `orpheus task edit %s --external-ref <reference>`", item.ID, requirement, item.ID)
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
		maximum, err := reviewMaxAutonomousReviewAttempts(s.Paths)
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
