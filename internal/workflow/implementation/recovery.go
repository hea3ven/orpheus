package implementation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workstate"
)

const reconcileImplementationRunLockOperation = "reconcile implementation run"

// InspectRun applies the shared attached-execution policy to a running implementer.
func InspectRun(run taskstate.RunAttempt, probe phaseexec.ProcessProbe) phaseexec.AttachedExecutionInspection {
	if run.Status != taskstate.RunStatusRunning {
		return phaseexec.AttachedExecutionInspection{Condition: phaseexec.AttachedExecutionNotRunning}
	}
	return phaseexec.InspectAttachedExecution(run.Execution, probe)
}

// RecoveryStore supplies the current service projection at the existing invocation boundary.
type RecoveryStore interface {
	Load(repoID, taskID string) (taskstate.TaskState, error)
}

// ExecutionRecovery selects one process inspection and records only a proven
// interruption. The observation is transient; restart inspects saved facts again.
type ExecutionRecovery struct {
	ExpectedAttempt int
	Trigger         string
	Probe           phaseexec.ProcessProbe
	observed        *phaseexec.AttachedExecutionInspection
}

func (s *ExecutionRecovery) Next(current work.State) (phaseexec.Selection[struct{}, phaseexec.AttachedExecutionInspection], error) {
	if s.observed != nil {
		return phaseexec.Return[struct{}](*s.observed), nil
	}
	projected, err := taskstate.Project(current)
	if err != nil {
		return phaseexec.Selection[struct{}, phaseexec.AttachedExecutionInspection]{}, err
	}
	run, ok := taskstate.ActiveRun(projected)
	if !ok || s.ExpectedAttempt > 0 && run.Attempt != s.ExpectedAttempt {
		return phaseexec.Return[struct{}](phaseexec.AttachedExecutionInspection{Condition: phaseexec.AttachedExecutionNotRunning}), nil
	}
	return phaseexec.Run[struct{}, phaseexec.AttachedExecutionInspection](phaseexec.Operation{Name: reconcileImplementationRunLockOperation, Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		if err := current.CheckActiveAttempt(run.Address); err != nil {
			return phaseexec.Prepared{}, err
		}
		return phaseexec.Prepared{Run: func(context.Context) phaseexec.Result {
			inspection := InspectRun(run, s.Probe)
			s.observed = &inspection
			if inspection.Condition != phaseexec.AttachedExecutionRecoverable {
				return phaseexec.Result{}
			}
			return phaseexec.Result{Update: InterruptionChange(run.Address, inspection.Reason, s.Trigger)}
		}}, nil
	}}), nil
}

// InterruptionChange preserves process identity while retaining a safely
// reconciled disappearance, rather than fabricating a native exit.
func InterruptionChange(owner work.AttemptRef, reason, trigger string) phaseexec.Change {
	at := time.Now().UTC()
	return func(current *work.State) (bool, error) {
		if strings.TrimSpace(reason) == "" || strings.TrimSpace(trigger) == "" {
			return false, errors.New("interruption requires observed reason and trigger")
		}
		phase, err := currentImplementation(current, owner)
		if err != nil {
			return false, err
		}
		phase.Execution.Process = &work.InterruptedProcess{At: at, Reason: reason, Trigger: trigger}
		return true, nil
	}
}

// ReconcileRun keeps the existing mutation scope and returns actual inspection
// evidence even if its receipt fails. It never retries an external observation.
func ReconcileRun(ctx context.Context, paths state.Paths, store RecoveryStore, repoID, taskID string, expectedAttempt int, trigger string, probe phaseexec.ProcessProbe) (phaseexec.AttachedExecutionInspection, error) {
	if store == nil {
		return phaseexec.AttachedExecutionInspection{}, fmt.Errorf("implementation run recovery store is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runner := phaseexec.Runner{Paths: paths, Store: workstate.NewStore(paths), RepositoryID: repoID, TaskID: taskID}
	phase := ExecutionRecovery{ExpectedAttempt: expectedAttempt, Trigger: trigger, Probe: probe}
	var inspection phaseexec.AttachedExecutionInspection
	err := state.WithGlobalMutationLockLogger(ctx, paths, reconcileImplementationRunLockOperation, nil, func() error {
		view, err := store.Load(repoID, taskID)
		if err != nil {
			return err
		}
		if view.Work == nil {
			inspection = phaseexec.AttachedExecutionInspection{Condition: phaseexec.AttachedExecutionNotRunning}
			return nil
		}
		current := *view.Work
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
