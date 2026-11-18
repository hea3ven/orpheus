package implementation_test

import (
	"context"
	"errors"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	workmodel "github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workfixture"
	"github.com/hea3ven/orpheus/internal/workflow/implementation"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"path/filepath"
	"testing"
)

func TestInspectImplementationRun(t *testing.T) {
	tests := []struct {
		name      string
		run       taskstate.RunAttempt
		probe     phaseexec.ProcessProbe
		condition phaseexec.AttachedExecutionCondition
		reason    string
	}{
		{name: "legacy run is unverifiable", run: runningRecoveryRun(0, 0), condition: phaseexec.AttachedExecutionUnverifiable, reason: "missing_supervisor_pid_legacy_run"},
		{name: "live supervisor blocks recovery", run: runningRecoveryRun(10, 0), probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessLive}), condition: phaseexec.AttachedExecutionLive, reason: "supervisor_pid_live"},
		{name: "missing child is assumed not started", run: runningRecoveryRun(10, 0), probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent}), condition: phaseexec.AttachedExecutionRecoverable, reason: "supervisor_absent_child_pid_not_recorded_assume_not_started"},
		{name: "live child blocks recovery", run: runningRecoveryRun(10, 11), probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent, 11: agentexec.ProcessLive}), condition: phaseexec.AttachedExecutionLive, reason: "child_pid_live"},
		{name: "missing supervisor and child recover", run: runningRecoveryRun(10, 11), probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent, 11: agentexec.ProcessAbsent}), condition: phaseexec.AttachedExecutionRecoverable, reason: "supervisor_and_child_pids_absent"},
		{name: "probe error is unverifiable", run: runningRecoveryRun(10, 11), probe: func(int) (agentexec.ProcessLiveness, error) {
			return agentexec.ProcessUnknown, errors.New("permission denied")
		}, condition: phaseexec.AttachedExecutionUnverifiable, reason: "supervisor_pid_probe_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := implementation.InspectRun(tt.run, tt.probe)
			if got.Condition != tt.condition || got.Reason != tt.reason {
				t.Fatalf("inspection = %#v, want condition=%q reason=%q", got, tt.condition, tt.reason)
			}
		})
	}
}

func TestReconcileImplementationRunRequiresExpectedActiveAttempt(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := workfixture.NewStore(paths)
	run, err := store.SeedImplementation("alpha", "op-1", implementation.Reservation{Context: &workmodel.WorkContext{Directory: "/fixture/work", TaskBranch: "task-branch", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}}, Agent: "implementer", SupervisorPID: 10})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}

	inspection, err := implementation.ReconcileRun(
		context.Background(), paths, store, "alpha", "op-1", run.Attempt+1, "doctor_fix", recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent}),
	)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if inspection.Condition != phaseexec.AttachedExecutionNotRunning {
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

func recoveryProbe(values map[int]agentexec.ProcessLiveness) phaseexec.ProcessProbe {
	return func(pid int) (agentexec.ProcessLiveness, error) {
		return values[pid], nil
	}
}
