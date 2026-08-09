package taskstate_test

import (
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
)

func TestStoreInterruptPrimaryReviewExecutionRecordsAtomicAuditFacts(t *testing.T) {
	store := newTestStore(t, time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))
	review, err := store.StartReviewWithOptions("alpha", "op-interrupted-review", taskstate.StartReviewOptions{Pipeline: "ai", Step: "ai-review"})
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	execution := taskstate.AgentExecution{
		Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning,
		Agent: "reviewer", StartedAt: review.StartedAt, SupervisorPID: 123,
	}
	if _, err := store.RecordReviewStep("alpha", "op-interrupted-review", review.Attempt, taskstate.RecordReviewStepOptions{Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &execution}); err != nil {
		t.Fatalf("record review step: %v", err)
	}
	if _, err := store.RecordReviewStepChildPID("alpha", "op-interrupted-review", review.Attempt, "ai-review", 456); err != nil {
		t.Fatalf("record review child PID: %v", err)
	}
	if _, err := store.RecordReviewFinding("alpha", "op-interrupted-review", review.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Title: "retained", Description: "audit finding"}); err != nil {
		t.Fatalf("record finding: %v", err)
	}
	interrupted, err := store.InterruptPrimaryReviewExecution("alpha", "op-interrupted-review", review.Attempt, "ai-review", taskstate.InterruptPrimaryReviewExecutionOptions{
		Reason: "supervisor_and_child_pids_absent", Trigger: "doctor_fix",
	})
	if err != nil {
		t.Fatalf("interrupt primary review: %v", err)
	}
	if interrupted.Status != taskstate.ReviewStatusFailed || interrupted.FinishedAt == nil || len(interrupted.Findings) != 1 {
		t.Fatalf("interrupted review = %#v, want failed review with retained findings", interrupted)
	}
	step := interrupted.Steps[0]
	if step.Execution.Status != taskstate.RunStatusInterrupted || step.Execution.FinishedAt == nil || step.Execution.SupervisorPID != 123 || step.Execution.ChildPID != 456 {
		t.Fatalf("interrupted execution = %#v, want retained PIDs and terminal interruption", step.Execution)
	}
	if step.Execution.InterruptionReason != "supervisor_and_child_pids_absent" || step.Execution.InterruptionTrigger != "doctor_fix" {
		t.Fatalf("interruption facts = %#v, want recovery reason and trigger", step.Execution)
	}
	state, err := store.Load("alpha", "op-interrupted-review")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	event := state.Events[len(state.Events)-1]
	if event.Type != taskstate.EventReviewInterrupted || event.InterruptionReason != "supervisor_and_child_pids_absent" || event.InterruptionTrigger != "doctor_fix" {
		t.Fatalf("event = %#v, want review interruption audit facts", event)
	}
}

func TestInterruptedPrimaryReviewExcludesOnlyItsFindings(t *testing.T) {
	store := newTestStore(t, time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))
	review, err := store.StartReviewWithOptions("alpha", "op-multi-review", taskstate.StartReviewOptions{Pipeline: "ai", Step: "first"})
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	first := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, Agent: "first", StartedAt: review.StartedAt}
	if _, err := store.RecordReviewStep("alpha", "op-multi-review", review.Attempt, taskstate.RecordReviewStepOptions{Kind: taskstate.ReviewStepKindAgentReview, Name: "first", Execution: &first}); err != nil {
		t.Fatalf("record first step: %v", err)
	}
	if _, err := store.FinishReviewStepExecution("alpha", "op-multi-review", review.Attempt, "first", taskstate.FinishReviewStepExecutionOptions{Status: taskstate.RunStatusSucceeded}); err != nil {
		t.Fatalf("finish first step: %v", err)
	}
	if _, err := store.RecordReviewFinding("alpha", "op-multi-review", review.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Step: "first", Title: "first finding", Description: "authoritative"}); err != nil {
		t.Fatalf("record first finding: %v", err)
	}
	second := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, Agent: "second", StartedAt: review.StartedAt, SupervisorPID: 10}
	if _, err := store.RecordReviewStep("alpha", "op-multi-review", review.Attempt, taskstate.RecordReviewStepOptions{Kind: taskstate.ReviewStepKindAgentReview, Name: "second", Execution: &second}); err != nil {
		t.Fatalf("record second step: %v", err)
	}
	if _, err := store.RecordReviewFinding("alpha", "op-multi-review", review.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Step: "second", Title: "second finding", Description: "audit-only"}); err != nil {
		t.Fatalf("record second finding: %v", err)
	}
	if _, err := store.InterruptPrimaryReviewExecution("alpha", "op-multi-review", review.Attempt, "second", taskstate.InterruptPrimaryReviewExecutionOptions{Reason: "supervisor_absent_child_pid_not_recorded_assume_not_started", Trigger: "task_run"}); err != nil {
		t.Fatalf("interrupt second step: %v", err)
	}
	state, err := store.Load("alpha", "op-multi-review")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	latest, _ := taskstate.LatestReview(state)
	indexes := taskstate.UntargetedBlockingFindingIndexes(latest)
	if len(indexes) != 1 || indexes[0] != 0 {
		t.Fatalf("untargeted blocker indexes = %v, want only completed first-step finding", indexes)
	}
	if taskstate.InterruptedPrimaryReviewFinding(latest, latest.Findings[0]) || !taskstate.InterruptedPrimaryReviewFinding(latest, latest.Findings[1]) {
		t.Fatalf("interrupted finding scope = %#v, want only second-step finding audit-only", latest.Findings)
	}
}

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
