package workflow

import (
	"context"
	"fmt"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

const reconcileImplementationRunLockOperation = "reconcile implementation run"

// ProcessProbe observes local PID liveness without owning task-state mutation.
type ProcessProbe func(pid int) (agentexec.ProcessLiveness, error)

// ImplementationRunCondition is the safe recovery classification of a running
// implementation attempt.
type ImplementationRunCondition string

const (
	ImplementationRunNotRunning   ImplementationRunCondition = "not_running"
	ImplementationRunRecoverable  ImplementationRunCondition = "recoverable"
	ImplementationRunLive         ImplementationRunCondition = "live"
	ImplementationRunUnverifiable ImplementationRunCondition = "unverifiable"
)

// ImplementationRunInspection contains a condition and operator-facing reason.
type ImplementationRunInspection struct {
	Condition ImplementationRunCondition
	Reason    string
}

// InspectImplementationRun applies the shared stale-run policy without mutating
// state. Legacy runs and probe failures remain deliberately unverifiable.
func InspectImplementationRun(run taskstate.RunAttempt, probe ProcessProbe) ImplementationRunInspection {
	if run.Status != taskstate.RunStatusRunning {
		return ImplementationRunInspection{Condition: ImplementationRunNotRunning}
	}
	if run.Execution.SupervisorPID <= 0 {
		return ImplementationRunInspection{Condition: ImplementationRunUnverifiable, Reason: "missing_supervisor_pid_legacy_run"}
	}
	if probe == nil {
		probe = agentexec.ProbePID
	}
	supervisor, err := probe(run.Execution.SupervisorPID)
	if err != nil || supervisor == agentexec.ProcessUnknown {
		return ImplementationRunInspection{Condition: ImplementationRunUnverifiable, Reason: "supervisor_pid_probe_failed"}
	}
	switch supervisor {
	case agentexec.ProcessLive:
		return ImplementationRunInspection{Condition: ImplementationRunLive, Reason: "supervisor_pid_live"}
	case agentexec.ProcessAbsent:
	default:
		return ImplementationRunInspection{Condition: ImplementationRunUnverifiable, Reason: "supervisor_pid_probe_failed"}
	}
	if run.Execution.ChildPID <= 0 {
		return ImplementationRunInspection{Condition: ImplementationRunRecoverable, Reason: "supervisor_absent_child_pid_not_recorded_assume_not_started"}
	}
	child, err := probe(run.Execution.ChildPID)
	if err != nil || child == agentexec.ProcessUnknown {
		return ImplementationRunInspection{Condition: ImplementationRunUnverifiable, Reason: "child_pid_probe_failed"}
	}
	switch child {
	case agentexec.ProcessLive:
		return ImplementationRunInspection{Condition: ImplementationRunLive, Reason: "child_pid_live"}
	case agentexec.ProcessAbsent:
		return ImplementationRunInspection{Condition: ImplementationRunRecoverable, Reason: "supervisor_and_child_pids_absent"}
	default:
		return ImplementationRunInspection{Condition: ImplementationRunUnverifiable, Reason: "child_pid_probe_failed"}
	}
}

// ImplementationRunRecoveryStore is the state mutation boundary used by stale
// implementation-run reconciliation.
type ImplementationRunRecoveryStore interface {
	Load(repoID, taskID string) (taskstate.TaskState, error)
	InterruptRun(repoID, taskID string, attempt int, opts taskstate.InterruptRunOptions) (taskstate.RunAttempt, error)
}

// ReconcileImplementationRun rechecks the latest state under mutation
// protection and records an interruption only when it is recoverable.
func ReconcileImplementationRun(
	ctx context.Context,
	paths state.Paths,
	store ImplementationRunRecoveryStore,
	repoID string,
	taskID string,
	expectedAttempt int,
	trigger string,
	probe ProcessProbe,
) (ImplementationRunInspection, error) {
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
		_, err = store.InterruptRun(repoID, taskID, run.Attempt, taskstate.InterruptRunOptions{
			Reason:  inspection.Reason,
			Trigger: trigger,
		})
		return err
	})
	if err != nil {
		return inspection, err
	}
	return inspection, nil
}
