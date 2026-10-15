package workflow

import (
	"context"
	"fmt"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

const (
	reconcileImplementationRunLockOperation = "reconcile implementation run"
	reconcileReviewLockOperation            = "reconcile review execution"
)

// ProcessProbe observes local PID liveness without owning task-state mutation.
type ProcessProbe func(pid int) (agentexec.ProcessLiveness, error)

// AttachedExecutionCondition is the safe recovery classification of a running
// local direct-child execution. It is shared by implementation and review
// workflows so the process-liveness policy cannot diverge.
type AttachedExecutionCondition string

const (
	AttachedExecutionNotRunning       AttachedExecutionCondition = "not_running"
	AttachedExecutionRecoverable      AttachedExecutionCondition = "recoverable"
	AttachedExecutionAlreadyRecovered AttachedExecutionCondition = "already_recovered"
	AttachedExecutionLive             AttachedExecutionCondition = "live"
	AttachedExecutionUnverifiable     AttachedExecutionCondition = "unverifiable"
)

// AttachedExecutionInspection contains a condition and operator-facing reason.
type AttachedExecutionInspection struct {
	Condition AttachedExecutionCondition
	Reason    string
}

// Compatibility aliases preserve the implementation-run API while routing its
// policy through the shared attached-execution inspection.
type ImplementationRunCondition = AttachedExecutionCondition
type ImplementationRunInspection = AttachedExecutionInspection

const (
	ImplementationRunNotRunning   = AttachedExecutionNotRunning
	ImplementationRunRecoverable  = AttachedExecutionRecoverable
	ImplementationRunLive         = AttachedExecutionLive
	ImplementationRunUnverifiable = AttachedExecutionUnverifiable
)

// InspectAttachedExecution applies the shared stale-process policy without
// mutation. Legacy executions and probe failures remain unverifiable.
func InspectAttachedExecution(execution taskstate.AgentExecution, probe ProcessProbe) AttachedExecutionInspection {
	if execution.SupervisorPID <= 0 {
		return AttachedExecutionInspection{Condition: AttachedExecutionUnverifiable, Reason: "missing_supervisor_pid_legacy_run"}
	}
	if probe == nil {
		probe = agentexec.ProbePID
	}
	supervisor, err := probe(execution.SupervisorPID)
	if err != nil || supervisor == agentexec.ProcessUnknown {
		return AttachedExecutionInspection{Condition: AttachedExecutionUnverifiable, Reason: "supervisor_pid_probe_failed"}
	}
	switch supervisor {
	case agentexec.ProcessLive:
		return AttachedExecutionInspection{Condition: AttachedExecutionLive, Reason: "supervisor_pid_live"}
	case agentexec.ProcessAbsent:
	default:
		return AttachedExecutionInspection{Condition: AttachedExecutionUnverifiable, Reason: "supervisor_pid_probe_failed"}
	}
	if execution.ChildPID <= 0 {
		return AttachedExecutionInspection{Condition: AttachedExecutionRecoverable, Reason: "supervisor_absent_child_pid_not_recorded_assume_not_started"}
	}
	child, err := probe(execution.ChildPID)
	if err != nil || child == agentexec.ProcessUnknown {
		return AttachedExecutionInspection{Condition: AttachedExecutionUnverifiable, Reason: "child_pid_probe_failed"}
	}
	switch child {
	case agentexec.ProcessLive:
		return AttachedExecutionInspection{Condition: AttachedExecutionLive, Reason: "child_pid_live"}
	case agentexec.ProcessAbsent:
		return AttachedExecutionInspection{Condition: AttachedExecutionRecoverable, Reason: "supervisor_and_child_pids_absent"}
	default:
		return AttachedExecutionInspection{Condition: AttachedExecutionUnverifiable, Reason: "child_pid_probe_failed"}
	}
}

// InspectImplementationRun applies the shared attached-execution policy to a
// running implementation attempt.
func InspectImplementationRun(run taskstate.RunAttempt, probe ProcessProbe) ImplementationRunInspection {
	if run.Status != taskstate.RunStatusRunning {
		return ImplementationRunInspection{Condition: ImplementationRunNotRunning}
	}
	return InspectAttachedExecution(run.Execution, probe)
}

// ImplementationRunRecoveryStore is the state mutation boundary used by stale
// implementation-run reconciliation.
type ImplementationRunRecoveryStore interface {
	Load(repoID, taskID string) (taskstate.TaskState, error)
	InterruptRun(repoID, taskID string, attempt int, opts taskstate.InterruptRunOptions) (taskstate.RunAttempt, error)
}

// ReconcileImplementationRun rechecks the latest state under mutation
// protection and records an interruption only when it is recoverable.
func ReconcileImplementationRun(ctx context.Context, paths state.Paths, store ImplementationRunRecoveryStore, repoID string, taskID string, expectedAttempt int, trigger string, probe ProcessProbe) (ImplementationRunInspection, error) {
	if store == nil {
		return ImplementationRunInspection{}, fmt.Errorf("implementation run recovery store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var inspection ImplementationRunInspection
	err := state.WithGlobalMutationLockLogger(ctx, paths, reconcileImplementationRunLockOperation, nil, func() error {
		taskState, err := store.Load(repoID, taskID)
		if err != nil {
			return err
		}
		run, ok := taskstate.ActiveRun(taskState)
		if !ok || (expectedAttempt > 0 && run.Attempt != expectedAttempt) {
			inspection = ImplementationRunInspection{Condition: ImplementationRunNotRunning}
			return nil
		}
		inspection = InspectImplementationRun(run, probe)
		if inspection.Condition != ImplementationRunRecoverable {
			return nil
		}
		_, err = store.InterruptRun(repoID, taskID, run.Attempt, taskstate.InterruptRunOptions{Reason: inspection.Reason, Trigger: trigger})
		return err
	})
	if err != nil {
		return inspection, err
	}
	return inspection, nil
}

// ReviewExecution identifies the sole recoverable boundary: a running
// agent-review step in the latest running review attempt.
type ReviewExecution struct {
	Attempt   int
	StepName  string
	Execution taskstate.AgentExecution
}

// ActiveReviewExecution returns an execution only when its containing review
// is the latest running attempt.
func ActiveReviewExecution(taskState taskstate.TaskState) (ReviewExecution, bool) {
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || latest.Status != taskstate.ReviewStatusRunning {
		return ReviewExecution{}, false
	}
	for index := len(latest.Steps) - 1; index >= 0; index-- {
		step := latest.Steps[index]
		if step.Kind != taskstate.ReviewStepKindAgentReview || step.Execution == nil {
			continue
		}
		if step.Execution.Purpose != taskstate.AgentExecutionPurposeReview || step.Execution.Status != taskstate.RunStatusRunning {
			continue
		}
		return ReviewExecution{Attempt: latest.Attempt, StepName: step.Name, Execution: *step.Execution}, true
	}
	return ReviewExecution{}, false
}

// ReviewRecoveryStore is the narrow persisted-state boundary for stale
// reviewer reconciliation.
type ReviewRecoveryStore interface {
	Load(repoID, taskID string) (taskstate.TaskState, error)
	InterruptReviewExecution(repoID, taskID string, attempt int, stepName string, opts taskstate.InterruptReviewExecutionOptions) (taskstate.ReviewAttempt, error)
}

// ReconcileReviewExecution revalidates the latest review under mutation
// protection and atomically records a failed review with its interrupted
// review execution only when the shared process policy proves it stale.
func ReconcileReviewExecution(ctx context.Context, paths state.Paths, store ReviewRecoveryStore, repoID string, taskID string, expectedAttempt int, trigger string, probe ProcessProbe) (AttachedExecutionInspection, error) {
	if store == nil {
		return AttachedExecutionInspection{}, fmt.Errorf("review recovery store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var inspection AttachedExecutionInspection
	err := state.WithGlobalMutationLockLogger(ctx, paths, reconcileReviewLockOperation, nil, func() error {
		taskState, err := store.Load(repoID, taskID)
		if err != nil {
			return err
		}
		execution, ok := ActiveReviewExecution(taskState)
		if !ok || (expectedAttempt > 0 && execution.Attempt != expectedAttempt) {
			inspection = inspectionAfterMissingReview(taskState, expectedAttempt)
			return nil
		}
		inspection = InspectAttachedExecution(execution.Execution, probe)
		if inspection.Condition != AttachedExecutionRecoverable {
			return nil
		}
		_, err = store.InterruptReviewExecution(repoID, taskID, execution.Attempt, execution.StepName, taskstate.InterruptReviewExecutionOptions{Reason: inspection.Reason, Trigger: trigger})
		return err
	})
	if err != nil {
		return inspection, err
	}
	return inspection, nil
}

func inspectionAfterMissingReview(taskState taskstate.TaskState, expectedAttempt int) AttachedExecutionInspection {
	if expectedAttempt > 0 {
		for _, review := range taskState.Reviews {
			if review.Attempt == expectedAttempt && taskstate.ReviewExecutionInterrupted(review) {
				return AttachedExecutionInspection{Condition: AttachedExecutionAlreadyRecovered, Reason: "review_interrupted_by_concurrent_recovery"}
			}
		}
	}
	return AttachedExecutionInspection{Condition: AttachedExecutionNotRunning}
}
