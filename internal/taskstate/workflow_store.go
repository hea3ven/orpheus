package taskstate

import (
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
)

// updateWorkflow coordinates a domain mutation, separately from the validated
// atomic adapter. The adapter independently reloads the prior persistence version.
func (s Store) updateWorkflow(repoID, taskID string, update func(*work.State) (bool, error)) (TaskState, error) {
	adapter := workstate.NewStore(s.paths)
	snapshot, err := adapter.Load(repoID, taskID)
	if err != nil {
		return TaskState{}, err
	}
	changed, err := update(&snapshot)
	if err != nil {
		return TaskState{}, err
	}
	if changed {
		version, err := adapter.Save(snapshot, snapshot.Version)
		if err != nil {
			return TaskState{}, err
		}
		snapshot.Version = version
	}
	return Project(snapshot)
}

func prepareWorkflow(snapshot *work.State, context *work.WorkContext, at time.Time) (bool, error) {
	if context == nil {
		return false, errors.New("preparation requires observed baseline commit, tree and owned retention ref")
	}
	if len(snapshot.Workflows) == 0 {
		snapshot.Workflows = append(snapshot.Workflows, work.Workflow{})
	}
	w := snapshot.ActiveWorkflow()
	if w == nil {
		return false, errors.New("closed work requires an explicit workflow reset")
	}
	if established := w.Context(); established != nil {
		if !reflect.DeepEqual(*established, *context) {
			return false, errors.New("observed preparation differs from this workflow's immutable context")
		}
		return false, nil
	}
	copyOf := *context
	w.Phases = append(w.Phases, &work.PreparationPhase{Context: &copyOf, EstablishedAt: &at})
	return true, nil
}

func currentImplementation(snapshot *work.State, ref work.AttemptRef) (*work.ImplementationPhase, error) {
	if err := snapshot.CheckActiveAttempt(ref); err != nil {
		return nil, err
	}
	if ref.Step != nil || ref.BranchSync != nil {
		return nil, errors.New("callback does not identify implementation")
	}
	return snapshot.Workflows[ref.Workflow].Phases[ref.Phase].(*work.ImplementationPhase), nil
}

func viewRunAt(view TaskState, ref work.AttemptRef) (RunAttempt, error) {
	for _, run := range view.Runs {
		if run.Address.Workflow == ref.Workflow && run.Address.Phase == ref.Phase {
			return run, nil
		}
	}
	return RunAttempt{}, errors.New("implementation address is not present in workflow history")
}

func viewReviewAt(view TaskState, ref work.AttemptRef) (ReviewAttempt, error) {
	for _, review := range view.Reviews {
		if review.Address.Workflow == ref.Workflow && review.Address.Phase == ref.Phase {
			return review, nil
		}
	}
	return ReviewAttempt{}, errors.New("review address is not present in workflow history")
}

func workUsage(usage *AgentUsage) *work.Usage {
	if usage == nil {
		return nil
	}
	return &work.Usage{InputTokens: int64(usage.InputTokens), CachedInputTokens: int64(usage.CachedInputTokens), OutputTokens: int64(usage.OutputTokens), ReasoningOutputTokens: int64(usage.ReasoningOutputTokens), TotalTokens: int64(usage.TotalTokens)}
}

func applyObservedUsage(execution *work.AgentExecution, opts RecordRunUsageOptions, at time.Time) {
	if opts.Session != nil {
		execution.Session = &work.Session{ID: opts.Session.ID, LogPath: opts.Session.LogPath}
	}
	if opts.Usage != nil {
		execution.Usage = workUsage(opts.Usage)
	}
	if opts.UsageCost != nil {
		cost := opts.UsageCost
		execution.Cost = &work.UsageCost{Kind: cost.Kind, Currency: cost.Currency, AmountMicroUSD: cost.AmountMicroUSD, Source: cost.Source, Notes: cost.Notes}
		if cost.Pricing != nil {
			pricing := work.UsagePricing(*cost.Pricing)
			execution.Cost.Pricing = &pricing
		}
	}
	if opts.Model != "" {
		execution.ObservedModel = opts.Model
	}
	if !opts.UsageCapture.IsZero() {
		capture := opts.UsageCapture
		if capture.CapturedAt == nil {
			capture.CapturedAt = execution.UsageCapture.CapturedAt
			if capture.CapturedAt == nil && execution.UsageCapture.Status != work.UsageCaptured {
				capture.CapturedAt = &at
			}
		}
		execution.UsageCapture = work.UsageCapture{Status: work.UsageCaptureStatus(capture.Status), Reason: capture.Reason, CandidateCount: capture.CandidateCount, CapturedAt: capture.CapturedAt}
	}
}

func observedExecution(view AgentExecution) (work.AgentExecution, error) {
	process := view.Process
	if process == nil && view.Status == RunStatusRunning {
		// This is observed attached supervision, not evidence of a successful
		// child exit. A start failure or exit still needs its own observation.
		process = &work.RunningProcess{}
	}
	if process == nil {
		return work.AgentExecution{}, errors.New("execution requires an explicit observed process result")
	}
	execution := work.AgentExecution{ObservedModel: view.ObservedModel, Profile: view.Profile, Harness: view.Harness, Model: view.Model, Thinking: view.Thinking, Interactive: view.Interactive, SessionName: view.SessionName, Command: view.Command, Args: cloneStrings(view.Args), SupervisorPID: view.SupervisorPID, ChildPID: view.ChildPID, StartedAt: view.StartedAt, Duration: time.Duration(view.DurationMillis) * time.Millisecond, Process: process}
	if execution.Profile == "" {
		execution.Profile = view.Agent
	}
	applyObservedUsage(&execution, RecordRunUsageOptions{Session: view.Session, Usage: view.Usage, UsageCost: view.UsageCost, UsageCapture: view.UsageCapture}, view.StartedAt)
	return execution, nil
}

// FinishObservedRun records an actual process result at the owning address.
// Aggregate statuses and missing PIDs cannot substitute for a reaped-child exit.
func (s Store) FinishObservedRun(ref work.AttemptRef, result work.ProcessResult, childPID int) (RunAttempt, error) {
	view, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		p, err := currentImplementation(snapshot, ref)
		if err != nil {
			return false, err
		}
		if result == nil {
			return false, errors.New("implementation requires an actual process observation")
		}
		if childPID < 0 || p.Execution.ChildPID != 0 && childPID != p.Execution.ChildPID {
			return false, errors.New("observed process identity differs from its recorded child")
		}
		if childPID > 0 {
			p.Execution.ChildPID = childPID
		}
		p.Execution.Process = result
		if exit, ok := result.(*work.ExitedProcess); ok && exit != nil && !exit.At.Before(p.Execution.StartedAt) {
			p.Execution.Duration = exit.At.Sub(p.Execution.StartedAt)
		}
		return true, nil
	})
	if err != nil {
		return RunAttempt{}, err
	}
	return viewRunAt(view, ref)
}

// RecordRunChildPIDAt binds a native direct child to an exact live owner.
func (s Store) RecordRunChildPIDAt(ref work.AttemptRef, pid int) (RunAttempt, error) {
	view, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		p, err := currentImplementation(snapshot, ref)
		if err != nil {
			return false, err
		}
		if pid <= 0 || p.Execution.ChildPID != 0 && p.Execution.ChildPID != pid {
			return false, errors.New("direct child identity conflicts with implementation ownership")
		}
		if p.Execution.ChildPID == pid {
			return false, nil
		}
		p.Execution.ChildPID = pid
		return true, nil
	})
	if err != nil {
		return RunAttempt{}, err
	}
	return viewRunAt(view, ref)
}

// CompleteImplementation records a handoff without changing the process result.
// The first handoff remains authoritative, including repeated same-owner calls.
func (s Store) CompleteImplementation(ref work.AttemptRef, opts CompleteRunOptions) (RunAttempt, error) {
	view, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		p, err := currentImplementation(snapshot, ref)
		if err != nil {
			return false, err
		}
		if p.Completion != nil {
			return false, nil
		}
		p.Completion = &work.Completion{Summary: opts.Summary, Description: opts.Description, DetailedDescription: opts.DetailedDescription, TechnicalExplanation: opts.TechnicalExplanation, CompletedAt: s.nowUTC()}
		return true, nil
	})
	if err != nil {
		return RunAttempt{}, err
	}
	return viewRunAt(view, ref)
}

func implementationTargets(snapshot *work.State, followUp *ReviewFollowUp) ([]work.FindingRef, error) {
	if followUp == nil {
		return nil, nil
	}
	view, err := Project(*snapshot)
	if err != nil {
		return nil, err
	}
	review, exists := LatestReview(view)
	if !exists || review.Attempt != followUp.ReviewAttempt {
		return nil, errors.New("repair targets do not identify the current review")
	}
	var targets []work.FindingRef
	for _, index := range followUp.FindingIndexes {
		if index < 0 || index >= len(review.Findings) || review.Findings[index].Address == nil {
			return nil, fmt.Errorf("repair finding index %d is out of range", index)
		}
		targets = append(targets, *review.Findings[index].Address)
	}
	return targets, nil
}

func workExecutionLaunch(snapshot *work.State, launch *AgentLaunch) (work.ExecutionLaunch, error) {
	if launch == nil {
		return nil, nil
	}
	if launch.Mode == AgentLaunchFresh {
		return &work.FreshExecution{FallbackReason: launch.FallbackReason}, nil
	}
	if launch.Mode != AgentLaunchResumed {
		return nil, errors.New("unknown execution launch mode")
	}
	view, err := Project(*snapshot)
	if err != nil {
		return nil, err
	}
	for _, source := range view.Runs {
		if source.Address.Workflow == view.CurrentWorkflow && source.Attempt == launch.SourceRunAttempt {
			return &work.ResumedExecution{From: work.ExecutionRef{Phase: source.Address.Phase}, UsageBaseline: workUsage(launch.UsageBaseline), CostBaselineMicroUSD: launch.CostBaseline}, nil
		}
	}
	return nil, errors.New("resumed source execution does not belong to the owning workflow")
}

// RecordRunUsageAt permits supplemental observations on an exact historical
// execution. Successor validation preserves already known measurements.
func (s Store) RecordRunUsageAt(ref work.AttemptRef, opts RecordRunUsageOptions) (RunAttempt, error) {
	view, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		if ref.Workflow < 0 || ref.Workflow >= len(snapshot.Workflows) || ref.Step != nil || ref.BranchSync != nil {
			return false, errors.New("usage does not identify an implementation owner")
		}
		execution, err := snapshot.Workflows[ref.Workflow].Execution(work.ExecutionRef{Phase: ref.Phase})
		if err != nil {
			return false, err
		}
		applyObservedUsage(execution, opts, s.nowUTC())
		return true, nil
	})
	if err != nil {
		return RunAttempt{}, err
	}
	return viewRunAt(view, ref)
}

// RecordExecutionUsage supplements the exact historical owner with actual native
// observations. Successor validation protects already known immutable evidence.
func (s Store) RecordExecutionUsage(ref work.AttemptRef, opts RecordRunUsageOptions) error {
	_, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		if ref.Workflow < 0 || ref.Workflow >= len(snapshot.Workflows) {
			return false, errors.New("usage has no owning workflow")
		}
		execution, err := snapshot.Workflows[ref.Workflow].Execution(work.ExecutionRef{Phase: ref.Phase, Step: ref.Step, BranchSync: ref.BranchSync})
		if err != nil {
			return false, err
		}
		applyObservedUsage(execution, opts, s.nowUTC())
		return true, nil
	})
	return err
}

func implementationAdvisories(snapshot *work.State, followUp *ReviewFollowUp) ([]work.FindingRef, error) {
	if followUp == nil {
		return nil, nil
	}
	view, err := Project(*snapshot)
	if err != nil {
		return nil, err
	}
	review, ok := LatestReview(view)
	if !ok || review.Attempt != followUp.ReviewAttempt {
		return nil, errors.New("advisories require the owning current review")
	}
	var refs []work.FindingRef
	for _, index := range followUp.AdvisoryFindingIndexes {
		if index < 0 || index >= len(review.Findings) || review.Findings[index].Address == nil {
			return nil, errors.New("advisory input is outside the owning review")
		}
		refs = append(refs, *review.Findings[index].Address)
	}
	return refs, nil
}
