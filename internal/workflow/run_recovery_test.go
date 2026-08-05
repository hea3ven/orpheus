package workflow_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
)

func TestInspectImplementationRun(t *testing.T) {
	tests := []struct {
		name      string
		run       taskstate.RunAttempt
		probe     workflow.ProcessProbe
		condition workflow.ImplementationRunCondition
		reason    string
	}{
		{name: "legacy run is unverifiable", run: runningRecoveryRun(0, 0), condition: workflow.ImplementationRunUnverifiable, reason: "missing_supervisor_pid_legacy_run"},
		{name: "live supervisor blocks recovery", run: runningRecoveryRun(10, 0), probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessLive}), condition: workflow.ImplementationRunLive, reason: "supervisor_pid_live"},
		{name: "missing child is assumed not started", run: runningRecoveryRun(10, 0), probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent}), condition: workflow.ImplementationRunRecoverable, reason: "supervisor_absent_child_pid_not_recorded_assume_not_started"},
		{name: "live child blocks recovery", run: runningRecoveryRun(10, 11), probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent, 11: agentexec.ProcessLive}), condition: workflow.ImplementationRunLive, reason: "child_pid_live"},
		{name: "missing supervisor and child recover", run: runningRecoveryRun(10, 11), probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent, 11: agentexec.ProcessAbsent}), condition: workflow.ImplementationRunRecoverable, reason: "supervisor_and_child_pids_absent"},
		{name: "probe error is unverifiable", run: runningRecoveryRun(10, 11), probe: func(int) (agentexec.ProcessLiveness, error) {
			return agentexec.ProcessUnknown, errors.New("permission denied")
		}, condition: workflow.ImplementationRunUnverifiable, reason: "supervisor_pid_probe_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workflow.InspectImplementationRun(tt.run, tt.probe)
			if got.Condition != tt.condition || got.Reason != tt.reason {
				t.Fatalf("inspection = %#v, want condition=%q reason=%q", got, tt.condition, tt.reason)
			}
		})
	}
}

func TestPrepareTaskRunReconcilesAndRoutesRecoveredAttempt(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := taskstate.NewStore(paths)
	run, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "implementer", SupervisorPID: 10})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}

	prepared, err := workflow.PrepareTaskRun(context.Background(), workflow.PrepareTaskRunOptions{
		Paths: paths, Store: store, RepoID: "alpha", TaskID: "op-1", Task: task.Task{ID: "op-1"}, Trigger: "task_run",
		Probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent}),
	})
	if err != nil {
		t.Fatalf("prepare task run: %v", err)
	}
	if prepared.Inspection.Condition != workflow.ImplementationRunRecoverable {
		t.Fatalf("inspection = %#v, want recoverable", prepared.Inspection)
	}
	if prepared.State.Runs[0].Attempt != run.Attempt || prepared.State.Runs[0].Status != taskstate.RunStatusInterrupted {
		t.Fatalf("prepared state = %#v, want interrupted attempt", prepared.State.Runs)
	}
	if prepared.Route.Action != workflow.TaskRunActionStartImplementation {
		t.Fatalf("route = %#v, want replacement implementation", prepared.Route)
	}
}

func TestReconcileImplementationRunRequiresExpectedActiveAttempt(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := taskstate.NewStore(paths)
	run, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "implementer", SupervisorPID: 10})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}

	inspection, err := workflow.ReconcileImplementationRun(
		context.Background(), paths, store, "alpha", "op-1", run.Attempt+1, "doctor_fix", recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent}),
	)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if inspection.Condition != workflow.ImplementationRunNotRunning {
		t.Fatalf("inspection = %#v, want not running due to attempt mismatch", inspection)
	}
	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Runs[0].Status != taskstate.RunStatusRunning {
		t.Fatalf("run status = %q, want unchanged running", loaded.Runs[0].Status)
	}
}

func runningRecoveryRun(supervisorPID, childPID int) taskstate.RunAttempt {
	return taskstate.RunAttempt{Attempt: 1, Status: taskstate.RunStatusRunning, Execution: taskstate.AgentExecution{
		SupervisorPID: supervisorPID,
		ChildPID:      childPID,
	}}
}

func recoveryProbe(values map[int]agentexec.ProcessLiveness) workflow.ProcessProbe {
	return func(pid int) (agentexec.ProcessLiveness, error) {
		return values[pid], nil
	}
}
