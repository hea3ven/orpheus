package doctor_test

import (
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/doctor"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
)

func TestRunReportsAndRepairsRecoverableImplementationRun(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := taskstate.NewStore(paths)
	run, err := store.StartRun("alpha", "op-recover", taskstate.StartRunOptions{Agent: "implementer", SupervisorPID: 10})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := store.RecordRunChildPID("alpha", "op-recover", run.Attempt, 11); err != nil {
		t.Fatalf("record child PID: %v", err)
	}
	opts := doctor.Options{
		Paths:    paths,
		Registry: registry.Registry{Repos: []registry.Repo{{ID: "alpha", Path: t.TempDir()}}},
		Probe: workflow.ProcessProbe(func(int) (agentexec.ProcessLiveness, error) {
			return agentexec.ProcessAbsent, nil
		}),
	}

	result, err := doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if len(result.ImplementationRows) != 1 || result.ImplementationRows[0].Outcome != string(workflow.ImplementationRunRecoverable) {
		t.Fatalf("implementation rows = %#v, want recoverable row", result.ImplementationRows)
	}
	loaded, err := store.Load("alpha", "op-recover")
	if err != nil {
		t.Fatalf("load after report: %v", err)
	}
	if loaded.Runs[0].Status != taskstate.RunStatusRunning {
		t.Fatalf("reported run status = %q, want running", loaded.Runs[0].Status)
	}

	opts.Fix = true
	result, err = doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor --fix: %v", err)
	}
	if len(result.ImplementationRows) != 1 || result.ImplementationRows[0].Outcome != "interrupted" {
		t.Fatalf("fixed implementation rows = %#v, want interrupted row", result.ImplementationRows)
	}
	loaded, err = store.Load("alpha", "op-recover")
	if err != nil {
		t.Fatalf("load after fix: %v", err)
	}
	if loaded.Runs[0].Status != taskstate.RunStatusInterrupted {
		t.Fatalf("fixed run status = %q, want interrupted", loaded.Runs[0].Status)
	}
}
