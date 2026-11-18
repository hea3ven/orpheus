package review

import (
	"errors"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// ProcessObservation records actual reviewer process facts, independently of
// findings and the separately attempted usage capture.
type ProcessObservation struct {
	Address          work.AttemptRef
	Process          work.ProcessResult
	ChildPID         int
	ExecutionFailure *work.ReviewExecutionFailure
	ReadOnlyFailure  *work.ReadOnlyFailure
}

func currentReview(current *work.State, owner work.AttemptRef) (*work.Workflow, *work.ReviewPhase, error) {
	w := current.ActiveWorkflow()
	if w == nil || owner.RepositoryID != current.RepositoryID || owner.TaskID != current.TaskID || owner.Workflow != len(current.Workflows)-1 || owner.Phase < 0 || owner.Phase != len(w.Phases)-1 || owner.BranchSync != nil {
		return nil, nil, errors.New("review address does not own the current workflow phase")
	}
	p, ok := w.LastPhase().(*work.ReviewPhase)
	if !ok {
		return nil, nil, errors.New("current phase is not review")
	}
	if owner.Step != nil && (*owner.Step < 0 || *owner.Step != len(p.Steps)-1) {
		return nil, nil, errors.New("review callback came from a superseded step")
	}
	return w, p, nil
}

// ChildPIDChange checkpoints only the direct child at its reserved live owner.
func ChildPIDChange(owner work.AttemptRef, pid int) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		if err := current.CheckActiveAttempt(owner); err != nil {
			return false, err
		}
		w, _, err := currentReview(current, owner)
		if err != nil {
			return false, err
		}
		execution, err := w.Execution(work.ExecutionRef{Phase: owner.Phase, Step: owner.Step})
		if err != nil {
			return false, err
		}
		if pid <= 0 || execution.ChildPID != 0 && execution.ChildPID != pid {
			return false, errors.New("reviewer direct-child identity conflicts with its owner")
		}
		if execution.ChildPID == pid {
			return false, nil
		}
		execution.ChildPID = pid
		return true, nil
	}
}

// ProcessChange settles the exact reserved slot using its observed native exit
// or startup failure. Read-only recovery is retained in this same process save.
func ProcessChange(attempt int, stepName string, observed ProcessObservation) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		owner := observed.Address
		if err := current.CheckActiveAttempt(owner); err != nil {
			return false, err
		}
		w, phase, err := currentReview(current, owner)
		if err != nil {
			return false, err
		}
		projected, err := taskstate.Project(*current)
		if err != nil {
			return false, err
		}
		latest, ok := taskstate.LatestReview(projected)
		if !ok || latest.Attempt != attempt || owner.Step == nil {
			return false, errors.New("review execution result has a different owner")
		}
		step, ok := phase.Steps[*owner.Step].(*work.AgentReviewStep)
		if !ok || w.ReviewContracts[phase.Contract].Slots[step.Slot].ConfigKey != stepName {
			return false, errors.New("execution result does not identify its captured review slot")
		}
		if observed.Process == nil {
			return false, errors.New("reviewer completion requires the actual observed process outcome")
		}
		if observed.ChildPID < 0 || step.Execution.ChildPID != 0 && step.Execution.ChildPID != observed.ChildPID {
			return false, errors.New("review process identity differs from its recorded child")
		}
		if observed.ChildPID > 0 {
			step.Execution.ChildPID = observed.ChildPID
		}
		step.Execution.Process = observed.Process
		step.ExecutionFailure = observed.ExecutionFailure
		if observed.ReadOnlyFailure != nil {
			step.ReadOnlyFailures = append(step.ReadOnlyFailures, *observed.ReadOnlyFailure)
			if phase.CheckoutRecovery == nil {
				phase.CheckoutRecovery = &work.CheckoutRecovery{At: observed.ReadOnlyFailure.At, Reason: observed.ReadOnlyFailure.Reason}
			}
		}
		return true, nil
	}
}

// UsageChange supplements its exact historical reviewer even when terminal
// recording failed. Successor validation protects already known measurements.
func UsageChange(owner work.AttemptRef, usage taskstate.RecordRunUsageOptions) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		if owner.RepositoryID != current.RepositoryID || owner.TaskID != current.TaskID || owner.Workflow < 0 || owner.Workflow >= len(current.Workflows) || owner.Step == nil || owner.BranchSync != nil {
			return false, errors.New("usage does not identify a review execution owner")
		}
		execution, err := current.Workflows[owner.Workflow].Execution(work.ExecutionRef{Phase: owner.Phase, Step: owner.Step})
		if err != nil {
			return false, err
		}
		taskstate.ApplyObservedUsage(execution, usage, time.Now().UTC())
		return true, nil
	}
}
