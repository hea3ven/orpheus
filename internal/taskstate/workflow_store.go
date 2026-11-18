package taskstate

import (
	"errors"
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

// ApplyObservedUsage supplements execution facts without inferring missing usage.
func ApplyObservedUsage(execution *work.AgentExecution, opts RecordRunUsageOptions, at time.Time) {
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

// ObservedExecution converts actual process and telemetry observations to domain facts.
func ObservedExecution(view AgentExecution) (work.AgentExecution, error) {
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
	ApplyObservedUsage(&execution, RecordRunUsageOptions{Session: view.Session, Usage: view.Usage, UsageCost: view.UsageCost, UsageCapture: view.UsageCapture}, view.StartedAt)
	return execution, nil
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
		ApplyObservedUsage(execution, opts, s.nowUTC())
		return true, nil
	})
	return err
}
