package implementation

import (
	"context"
	"errors"
	"fmt"

	"os"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/preparation"
)

// AgentRunner supplies the frontend's attached launcher and execution streams.
type AgentRunner interface {
	RunImplementation(context.Context, ImplementationRun) agent.ExecutionResult
}

type ImplementationRun struct {
	RepoID string
	TaskID string
	Start  DispatchStartResult
}

type DispatchIntent struct {
	Start DispatchStartOptions
	Setup *gitmeta.TaskWorktreeSetupResult
}

type DispatchRequest struct{}
type DispatchOutcome interface{ implementationOutcome() }

// ContextRequired hands checkout establishment back to workflow. It carries an
// admitted target, not permission to reserve or execute an implementation.
type ContextRequired struct {
	Task   task.Task
	Intent preparation.Intent
}

func (ContextRequired) implementationOutcome() {}

type ExecutionFinished struct {
	Start     DispatchStartResult
	Completed bool
}

func (ExecutionFinished) implementationOutcome() {}

// Next derives one operation from committed facts and invocation-local frozen
// configuration. A restart re-admits the task; it never replays a queued launch.
func (s *Dispatch) Next(current work.State, intent DispatchIntent) (phaseexec.Selection[DispatchRequest, DispatchOutcome], error) {
	view, err := taskstate.Project(current)
	if err != nil {
		return phaseexec.Selection[DispatchRequest, DispatchOutcome]{}, err
	}
	s.history = view
	if s.plan == nil {
		return phaseexec.Run[DispatchRequest, DispatchOutcome](s.admissionOperation(intent.Start)), nil
	}
	if intent.Setup == nil {
		p := s.plan
		return phaseexec.Return[DispatchRequest, DispatchOutcome](ContextRequired{Task: p.taskItem, Intent: preparation.Intent{Expected: p.expected, TargetKind: p.targetKind, AllowDirty: p.followUp != nil || p.continuing, OwnedContext: dispatchMetadataMatches(p.taskItem.OrpheusMetadata(), p.expected)}}), nil
	}
	if s.owner == nil {
		return phaseexec.Run[DispatchRequest, DispatchOutcome](s.reserveOperation(*intent.Setup)), nil
	}
	if !s.executed {
		return phaseexec.Run[DispatchRequest, DispatchOutcome](s.executionOperation(*intent.Setup)), nil
	}
	start, err := s.startResult(current, *intent.Setup)
	if err != nil {
		return phaseexec.Selection[DispatchRequest, DispatchOutcome]{}, err
	}
	exit, observed := start.Attempt.Execution.Process.(*work.ExitedProcess)
	completed := observed && exit.Code != nil && *exit.Code == 0 && start.Attempt.Completion != nil
	return phaseexec.Return[DispatchRequest, DispatchOutcome](ExecutionFinished{Start: start, Completed: completed}), nil
}

func (s *Dispatch) admissionOperation(opts DispatchStartOptions) phaseexec.Operation {
	return phaseexec.Operation{Name: "admit implementation", Prepare: func(*work.State) (phaseexec.Prepared, error) {
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			plan, err := s.validateStartPhase(ctx, opts)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			if plan.followUp == nil {
				if err := validateImplementationReference(opts.Source.Repository, plan.taskItem); err != nil {
					return phaseexec.Result{Err: err}
				}
			}
			prepared, err := s.resolveCommandPhase(ctx, opts, plan)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			s.plan, s.prepared = &plan, &prepared
			return phaseexec.Result{}
		}}, nil
	}}
}

func (s *Dispatch) reserveOperation(setup gitmeta.TaskWorktreeSetupResult) phaseexec.Operation {
	return phaseexec.Operation{Name: "reserve implementation execution", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		e := s.prepared.Execution()
		changed, err := ReserveChange(Reservation{Agent: e.Agent, Profile: e.Profile, Selection: e.AgentSelection(), Interactive: e.Interactive, Command: e.Command, Args: e.Args, SessionName: e.SessionName, WorkDirectory: setup.WorktreePath, BaselineCommit: setup.StartingCommit, Branch: setup.Branch, Worktree: setup.WorktreePath, ReviewFollowUp: taskstateReviewFollowUp(s.plan.followUp), Launch: e.Launch, SupervisorPID: os.Getpid()}, time.Now().UTC())(current)
		if err != nil {
			return phaseexec.Prepared{}, fmt.Errorf("record run start: %w", err)
		}
		ref := work.AttemptRef{RepositoryID: current.RepositoryID, TaskID: current.TaskID, Workflow: len(current.Workflows) - 1, Phase: len(current.ActiveWorkflow().Phases) - 1}
		s.owner = &ref
		return phaseexec.Prepared{Changed: changed}, nil
	}}
}

func (s *Dispatch) startResult(current work.State, setup gitmeta.TaskWorktreeSetupResult) (DispatchStartResult, error) {
	view, err := taskstate.Project(current)
	if err != nil {
		return DispatchStartResult{}, err
	}
	run, ok := taskstate.LatestRun(view)
	if !ok || s.owner == nil || !s.owner.Equal(run.Address) {
		return DispatchStartResult{}, errors.New("implementation no longer owns its result")
	}
	return DispatchStartResult{Repository: s.plan.repository, Task: s.plan.taskItem, Setup: setup, Prepared: *s.prepared, Attempt: run, ExecutionDir: s.plan.expected.WorktreePath}, nil
}

func (s *Dispatch) executionOperation(setup gitmeta.TaskWorktreeSetupResult) phaseexec.Operation {
	return phaseexec.Operation{Name: "execute implementation", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		p, err := currentImplementation(current, *s.owner)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		if _, running := p.Execution.Process.(*work.RunningProcess); !running {
			return phaseexec.Prepared{}, errors.New("implementation is no longer reserved for attached execution")
		}
		if s.Run == nil || s.Results.Commit == nil {
			return phaseexec.Prepared{}, errors.New("implementation execution and result recorder are required")
		}
		start, err := s.startResult(*current, setup)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		return phaseexec.Prepared{Scope: phaseexec.ReleaseForEffect, Run: func(ctx context.Context) phaseexec.Result {
			var result agent.ExecutionResult
			if s.BeforeRun != nil {
				result.Err = s.BeforeRun()
			}
			if result.Err == nil {
				result = s.Run(ctx, ImplementationRun{RepoID: current.RepositoryID, TaskID: current.TaskID, Start: start})
			}
			err := RecordExecution(ctx, s.Results, start.Attempt.Address, result)
			s.executed = true
			return phaseexec.Result{Err: err}
		}}, nil
	}}
}

// RecordExecution preserves usage-before-process independent persistence, even
// when usage or launch reporting fails. Completion is never inferred from exit.
func RecordExecution(ctx context.Context, recorder phaseexec.AgentResults, owner work.AttemptRef, result agent.ExecutionResult) error {
	process := func(current *work.State) (bool, error) {
		var observed work.ProcessResult
		pid := 0
		switch {
		case !result.Started:
			if result.Err == nil {
				return false, errors.New("launcher supplied no observed startup or outcome")
			}
			observed = &work.StartFailedProcess{At: time.Now().UTC(), Reason: result.Err.Error()}
		case result.Exit != nil:
			code := result.Exit.Code
			pid = result.Exit.PID
			observed = &work.ExitedProcess{At: result.Exit.At, Code: &code}
		default:
			return false, errors.New("implementation exit is unresolved; inspect execution recovery before retrying")
		}
		return ProcessChange(owner, observed, pid)(current)
	}
	if !result.Started {
		_, err := recorder.Commit(context.WithoutCancel(ctx), process)
		return errors.Join(result.Err, err)
	}
	_, usageErr, processErr := recorder.UsageThenProcess(ctx, UsageChange(owner, result.Usage), process)
	if usageErr != nil {
		usageErr = fmt.Errorf("record implementation usage: %w", usageErr)
	}
	return errors.Join(result.Err, usageErr, processErr)
}
