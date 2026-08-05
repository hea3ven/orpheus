package taskstate_test

import (
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
)

func TestStoreInterruptRunRecordsTerminalAuditFacts(t *testing.T) {
	store := newTestStore(t, time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))
	run, err := store.StartRun("alpha", "op-interrupted", taskstate.StartRunOptions{Agent: "implementer", SupervisorPID: 123})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := store.RecordRunChildPID("alpha", "op-interrupted", run.Attempt, 456); err != nil {
		t.Fatalf("record child PID: %v", err)
	}
	interrupted, err := store.InterruptRun("alpha", "op-interrupted", run.Attempt, taskstate.InterruptRunOptions{
		Reason:  "supervisor_and_child_pids_absent",
		Trigger: "doctor_fix",
	})
	if err != nil {
		t.Fatalf("interrupt run: %v", err)
	}
	if interrupted.Status != taskstate.RunStatusInterrupted || interrupted.Execution.FinishedAt == nil {
		t.Fatalf("interrupted run = %#v, want terminal interrupted run", interrupted)
	}
	if interrupted.Execution.SupervisorPID != 123 || interrupted.Execution.ChildPID != 456 {
		t.Fatalf("process facts = %#v, want persisted PIDs", interrupted.Execution)
	}
	state, err := store.Load("alpha", "op-interrupted")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	event := state.Events[len(state.Events)-1]
	if event.Type != taskstate.EventRunInterrupted || event.InterruptionReason != "supervisor_and_child_pids_absent" || event.InterruptionTrigger != "doctor_fix" {
		t.Fatalf("event = %#v, want interruption audit facts", event)
	}
}
