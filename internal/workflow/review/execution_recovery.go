package review

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

const reconcileReviewLockOperation = "reconcile review execution"

// Execution identifies the sole recoverable boundary: a running
// agent-review step in the latest running review attempt.
type Execution struct {
	Attempt   int
	StepName  string
	Execution taskstate.AgentExecution
}

// ActiveExecution returns an execution only when its containing review
// is the latest running attempt.
func ActiveExecution(taskState taskstate.TaskState) (Execution, bool) {
	latest, ok := taskstate.LatestReview(taskState)
	if !ok {
		return Execution{}, false
	}
	for index := len(latest.Steps) - 1; index >= 0; index-- {
		step := latest.Steps[index]
		if step.Kind != taskstate.ReviewStepKindAgentReview || step.Execution == nil {
			continue
		}
		if step.Execution.Purpose != taskstate.AgentExecutionPurposeReview || step.Execution.Status != taskstate.RunStatusRunning {
			continue
		}
		return Execution{Attempt: latest.Attempt, StepName: step.Name, Execution: *step.Execution}, true
	}
	return Execution{}, false
}

// ExecutionRecovery observes the current reviewer under the existing mutation
// scope. Its observation is invocation-local, not a persisted recovery cursor.
type ExecutionRecovery struct {
	ExpectedAttempt int
	Trigger         string
	Probe           phaseexec.ProcessProbe
	observed        *phaseexec.AttachedExecutionInspection
}

// Next returns inspection work or its semantic recovery boundary.
func (s *ExecutionRecovery) Next(current work.State) (phaseexec.Selection[struct{}, phaseexec.AttachedExecutionInspection], error) {
	if s.observed != nil {
		return phaseexec.Return[struct{}](*s.observed), nil
	}
	projected, err := taskstate.Project(current)
	if err != nil {
		return phaseexec.Selection[struct{}, phaseexec.AttachedExecutionInspection]{}, err
	}
	execution, ok := ActiveExecution(projected)
	if !ok || s.ExpectedAttempt > 0 && execution.Attempt != s.ExpectedAttempt {
		return phaseexec.Return[struct{}](inspectionAfterMissingReview(projected, s.ExpectedAttempt)), nil
	}
	return phaseexec.Run[struct{}, phaseexec.AttachedExecutionInspection](phaseexec.Operation{
		Name: reconcileReviewLockOperation,
		Prepare: func(current *work.State) (phaseexec.Prepared, error) {
			if err := current.CheckActiveAttempt(execution.Execution.Address); err != nil {
				return phaseexec.Prepared{}, err
			}
			return phaseexec.Prepared{Run: func(context.Context) phaseexec.Result {
				inspection := phaseexec.InspectAttachedExecution(execution.Execution, s.Probe)
				s.observed = &inspection
				if inspection.Condition != phaseexec.AttachedExecutionRecoverable {
					return phaseexec.Result{}
				}
				return phaseexec.Result{Update: InterruptionChange(execution.Execution.Address, inspection.Reason, s.Trigger)}
			}}, nil
		},
	}), nil
}

// InterruptionChange retains a safely reconciled reviewer process observation.
// The containing review's failed outcome is derived from that same saved fact.
func InterruptionChange(owner work.AttemptRef, reason, trigger string) phaseexec.Change {
	at := time.Now().UTC()
	return func(current *work.State) (bool, error) {
		if strings.TrimSpace(reason) == "" || strings.TrimSpace(trigger) == "" {
			return false, errors.New("interruption requires observed reason and trigger")
		}
		if err := current.CheckActiveAttempt(owner); err != nil {
			return false, err
		}
		_, phase, err := currentReview(current, owner)
		if err != nil {
			return false, err
		}
		if owner.Step == nil {
			return false, errors.New("interruption requires the active last reviewer")
		}
		step, ok := phase.Steps[*owner.Step].(*work.AgentReviewStep)
		if !ok {
			return false, errors.New("interruption requires the active last reviewer")
		}
		step.Execution.Process = &work.InterruptedProcess{At: at, Reason: reason, Trigger: trigger}
		return true, nil
	}
}

// ReconcileExecution revalidates the latest review under mutation protection.
// Its runner returns committed recovery observations directly to Next.
func ReconcileExecution(ctx context.Context, runner phaseexec.Runner, expectedAttempt int, trigger string, probe phaseexec.ProcessProbe) (phaseexec.AttachedExecutionInspection, error) {
	phase := ExecutionRecovery{ExpectedAttempt: expectedAttempt, Trigger: trigger, Probe: probe}
	var inspection phaseexec.AttachedExecutionInspection
	err := state.WithGlobalMutationLockLogger(ctx, runner.Paths, reconcileReviewLockOperation, runner.Logger, func() error {
		current, err := runner.Load(ctx)
		if err != nil {
			return err
		}
		for {
			selected, err := phase.Next(current)
			if err != nil {
				return err
			}
			if op, ok := selected.Operation(); ok {
				current, err = runner.ExecuteHeld(ctx, current, op)
				if err != nil {
					if phase.observed != nil {
						inspection = *phase.observed
					}
					return err
				}
				continue
			}
			inspection, _ = selected.Outcome()
			return nil
		}
	})
	return inspection, err
}

func inspectionAfterMissingReview(taskState taskstate.TaskState, expectedAttempt int) phaseexec.AttachedExecutionInspection {
	if expectedAttempt > 0 {
		for _, review := range taskState.Reviews {
			if taskState.Work != nil && review.Address.Workflow != taskState.CurrentWorkflow {
				continue
			}
			if review.Attempt == expectedAttempt && taskstate.ReviewExecutionInterrupted(review) {
				return phaseexec.AttachedExecutionInspection{Condition: phaseexec.AttachedExecutionAlreadyRecovered, Reason: "review_interrupted_by_concurrent_recovery"}
			}
		}
	}
	return phaseexec.AttachedExecutionInspection{Condition: phaseexec.AttachedExecutionNotRunning}
}
