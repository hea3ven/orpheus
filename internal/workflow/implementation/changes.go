package implementation

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// ReserveChange starts only in an already established context. Preparation owns
// establishing that context; an execution reservation cannot manufacture one.
func ReserveChange(opts Reservation, at time.Time) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		w := current.ActiveWorkflow()
		if w == nil || w.Context() == nil {
			return false, errors.New("implementation requires an established workflow context")
		}
		c := w.Context()
		if opts.Context != nil && *opts.Context != *c || opts.WorkDirectory != "" && opts.WorkDirectory != c.Directory || opts.Worktree != "" && opts.Worktree != c.Directory || opts.Branch != "" && opts.Branch != c.TaskBranch || opts.BaselineCommit != "" && opts.BaselineCommit != c.Baseline.Commit {
			return false, errors.New("implementation request differs from the prepared workflow context")
		}
		if previous, ok := w.LastPhase().(*work.ImplementationPhase); ok {
			if _, running := previous.Execution.Process.(*work.RunningProcess); running {
				return false, taskstate.ErrActiveRun
			}
		}
		targets, advisories, err := findingInputs(*current, opts.ReviewFollowUp)
		if err != nil {
			return false, err
		}
		selection := opts.Selection
		if selection.IsZero() {
			selection = taskstate.NewAgentSelection(opts.Harness, opts.Model, opts.Thinking)
		}
		execution, err := taskstate.ObservedExecution(taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeImplementation, Status: taskstate.RunStatusRunning, Agent: opts.Agent, Profile: opts.Profile, Harness: selection.Harness, Model: selection.Model, Thinking: selection.Thinking, Interactive: opts.Interactive, Command: opts.Command, Args: append([]string(nil), opts.Args...), SessionName: opts.SessionName, StartedAt: at, SupervisorPID: opts.SupervisorPID})
		if err != nil {
			return false, err
		}
		execution.Launch, err = executionLaunch(current, opts.Launch)
		if err != nil {
			return false, err
		}
		p := &work.ImplementationPhase{Execution: execution, TargetFindings: targets, AdvisoryFindings: advisories}
		if input, exists := w.LatestOutput(); exists {
			p.Input = &input
		}
		w.Phases = append(w.Phases, p)
		return true, nil
	}
}

func findingInputs(current work.State, followUp *taskstate.ReviewFollowUp) ([]work.FindingRef, []work.FindingRef, error) {
	if followUp == nil {
		return nil, nil, nil
	}
	view, err := taskstate.Project(current)
	if err != nil {
		return nil, nil, err
	}
	review, ok := taskstate.LatestReview(view)
	if !ok || review.Attempt != followUp.ReviewAttempt {
		return nil, nil, errors.New("repair targets do not identify the current review")
	}
	resolve := func(indexes []int) ([]work.FindingRef, error) {
		var refs []work.FindingRef
		for _, index := range indexes {
			if index < 0 || index >= len(review.Findings) || review.Findings[index].Address == nil {
				return nil, fmt.Errorf("repair finding index %d is out of range", index)
			}
			refs = append(refs, *review.Findings[index].Address)
		}
		return refs, nil
	}
	targets, err := resolve(followUp.FindingIndexes)
	if err != nil {
		return nil, nil, err
	}
	advisories, err := resolve(followUp.AdvisoryFindingIndexes)
	return targets, advisories, err
}

func executionLaunch(current *work.State, launch *taskstate.AgentLaunch) (work.ExecutionLaunch, error) {
	if launch == nil {
		return nil, nil
	}
	if launch.Mode == taskstate.AgentLaunchFresh {
		return &work.FreshExecution{FallbackReason: launch.FallbackReason}, nil
	}
	if launch.Mode != taskstate.AgentLaunchResumed {
		return nil, errors.New("unknown execution launch mode")
	}
	source := launch.SourceExecution
	if source == nil || source.RepositoryID != current.RepositoryID || source.TaskID != current.TaskID || source.Workflow < 0 || source.Workflow != len(current.Workflows)-1 || source.Step != nil || source.BranchSync != nil {
		return nil, errors.New("resumed source execution does not belong to the owning workflow")
	}
	ref := work.ExecutionRef{Phase: source.Phase}
	if _, err := current.Workflows[source.Workflow].Execution(ref); err != nil {
		return nil, fmt.Errorf("resumed source execution: %w", err)
	}
	var baseline *work.Usage
	if u := launch.UsageBaseline; u != nil {
		baseline = &work.Usage{InputTokens: int64(u.InputTokens), CachedInputTokens: int64(u.CachedInputTokens), OutputTokens: int64(u.OutputTokens), ReasoningOutputTokens: int64(u.ReasoningOutputTokens), TotalTokens: int64(u.TotalTokens)}
	}
	return &work.ResumedExecution{From: ref, UsageBaseline: baseline, CostBaselineMicroUSD: launch.CostBaseline}, nil
}

func currentImplementation(current *work.State, owner work.AttemptRef) (*work.ImplementationPhase, error) {
	if err := current.CheckActiveAttempt(owner); err != nil {
		return nil, err
	}
	if owner.Step != nil || owner.BranchSync != nil {
		return nil, errors.New("callback does not identify implementation")
	}
	p, ok := current.Workflows[owner.Workflow].Phases[owner.Phase].(*work.ImplementationPhase)
	if !ok {
		return nil, errors.New("callback does not identify implementation")
	}
	return p, nil
}

func ChildPIDChange(owner work.AttemptRef, pid int) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		p, err := currentImplementation(current, owner)
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
	}
}

func ProcessChange(owner work.AttemptRef, result work.ProcessResult, pid int) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		p, err := currentImplementation(current, owner)
		if err != nil {
			return false, err
		}
		if result == nil {
			return false, errors.New("implementation requires an actual process observation")
		}
		if pid < 0 || p.Execution.ChildPID != 0 && pid != p.Execution.ChildPID {
			return false, errors.New("observed process identity differs from its recorded child")
		}
		if pid > 0 {
			p.Execution.ChildPID = pid
		}
		p.Execution.Process = result
		return true, nil
	}
}

func UsageChange(owner work.AttemptRef, opts taskstate.RecordRunUsageOptions) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		if owner.Workflow < 0 || owner.Workflow >= len(current.Workflows) || owner.Step != nil || owner.BranchSync != nil {
			return false, errors.New("usage does not identify an implementation owner")
		}
		execution, err := current.Workflows[owner.Workflow].Execution(work.ExecutionRef{Phase: owner.Phase})
		if err != nil {
			return false, err
		}
		taskstate.ApplyObservedUsage(execution, opts, time.Now().UTC())
		return true, nil
	}
}

// CompletionChange records a handoff independently of the attached process.
// The first accepted handoff remains authoritative for this exact owner.
func CompletionChange(owner work.AttemptRef, opts taskstate.CompleteRunOptions, at time.Time) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		p, err := currentImplementation(current, owner)
		if err != nil {
			return false, err
		}
		if p.Completion != nil {
			return false, nil
		}
		w := &current.Workflows[owner.Workflow]
		supplied := opts.PublicationCommitSubject != "" || opts.PublicationCommitBody != "" || opts.PRDescription != ""
		if w.PublicationLocked() && supplied {
			return false, errors.New("publication content is read-only after operator proceed")
		}
		if w.Publication == nil {
			w.Publication = &work.PublicationContent{CommitSubject: opts.CommitSubject, CommitBody: opts.CommitBody}
		}
		if opts.PublicationCommitSubject != "" {
			w.Publication.CommitSubject = opts.PublicationCommitSubject
		}
		if opts.PublicationCommitBody != "" {
			w.Publication.CommitBody = opts.PublicationCommitBody
		}
		if opts.PRDescription != "" {
			w.Publication.PRDescription = opts.PRDescription
		}
		if strings.TrimSpace(w.Publication.PRDescription) == "" {
			return false, errors.New("initial PR description is required")
		}
		p.Completion = &work.Completion{CommitSubject: opts.CommitSubject, CommitBody: opts.CommitBody, TechnicalExplanation: opts.TechnicalExplanation, CompletedAt: at}
		return true, nil
	}
}
