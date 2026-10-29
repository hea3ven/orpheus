package taskstate_test

import (
	workmodel "github.com/hea3ven/orpheus/internal/work"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreInterruptRunRecordsTerminalAuditFacts(t *testing.T) {
	store := newTestStore(t, time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))
	run, err := store.StartRun("alpha", "op-interrupted", taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: "/fixture/work", TaskBranch: "task-branch", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}}, Agent: "implementer", SupervisorPID: 123})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := store.RecordRunChildPIDAt(run.Address, 456); err != nil {
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

func TestInterruptedReviewPreservesProcessAuditAndOnlyExcludesTheInterruptedStepFindings(t *testing.T) {
	history := projectionHistory(t)
	history.Workflows[0].Phases = history.Workflows[0].Phases[:2]
	history.Workflows[0].ReviewContracts = nil
	store, _ := newPolicyStore(t, history)
	contract := workmodel.ReviewContract{PipelineKey: "ai", Slots: []workmodel.ReviewSlot{{Kind: workmodel.ReviewAgent, ConfigKey: "first"}, {Kind: workmodel.ReviewAgent, ConfigKey: "second"}}}
	review, err := store.StartReviewWithOptions("repo", "task", taskstate.StartReviewOptions{Pipeline: "ai", Contract: contract, Scope: projectionScope(1)})
	require.NoError(t, err)
	first := taskstate.AgentExecution{Agent: "first", Harness: "pi", Command: "fixture-reviewer", Process: &workmodel.RunningProcess{}, StartedAt: review.StartedAt}
	review, err = store.RecordReviewStep("repo", "task", review.Attempt, taskstate.RecordReviewStepOptions{Address: review.Address, Kind: taskstate.ReviewStepKindAgentReview, Name: "first", Execution: &first})
	require.NoError(t, err)
	firstRef := review.Steps[0].Address
	_, err = store.RecordReviewFinding("repo", "task", review.Attempt, taskstate.ReviewFinding{Actor: &firstRef, Type: taskstate.FindingTypeBlocking, Step: "first", Title: "first finding", Description: "Authoritative report"})
	require.NoError(t, err)
	zero := 0
	_, err = store.FinishReviewStepExecution("repo", "task", review.Attempt, "first", taskstate.FinishReviewStepExecutionOptions{Address: firstRef, Process: &workmodel.ExitedProcess{Code: &zero, At: review.StartedAt.Add(time.Second)}})
	require.NoError(t, err)
	second := taskstate.AgentExecution{Agent: "second", Harness: "pi", Command: "fixture-reviewer", Process: &workmodel.RunningProcess{}, StartedAt: review.StartedAt.Add(3 * time.Second), SupervisorPID: 123}
	review, err = store.RecordReviewStep("repo", "task", review.Attempt, taskstate.RecordReviewStepOptions{Address: review.Address, Kind: taskstate.ReviewStepKindAgentReview, Name: "second", Execution: &second})
	require.NoError(t, err)
	secondRef := review.Steps[1].Address
	_, err = store.RecordReviewStepChildPIDAt(secondRef, 456)
	require.NoError(t, err)
	_, err = store.RecordReviewFinding("repo", "task", review.Attempt, taskstate.ReviewFinding{Actor: &secondRef, Type: taskstate.FindingTypeBlocking, Step: "second", Title: "second finding", Description: "Interrupted audit report"})
	require.NoError(t, err)

	interrupted, err := store.InterruptReviewExecution("repo", "task", review.Attempt, "second", taskstate.InterruptReviewExecutionOptions{Reason: "supervisor_and_child_pids_absent", Trigger: "doctor_fix"})

	require.NoError(t, err)
	assert.Equal(t, taskstate.ReviewStatusFailed, interrupted.Status)
	require.NotNil(t, interrupted.FinishedAt)
	execution := interrupted.Steps[1].Execution
	require.NotNil(t, execution)
	assert.Equal(t, 123, execution.SupervisorPID)
	assert.Equal(t, 456, execution.ChildPID)
	assert.Equal(t, taskstate.RunStatusInterrupted, execution.Status)
	assert.IsType(t, &workmodel.InterruptedProcess{}, execution.Process)
	assert.Equal(t, "supervisor_and_child_pids_absent", execution.InterruptionReason)
	assert.Equal(t, "doctor_fix", execution.InterruptionTrigger)
	require.Len(t, interrupted.Findings, 2)
	assert.Equal(t, "second finding", interrupted.Findings[1].Title)
	assert.Equal(t, []int{0}, taskstate.UntargetedBlockingFindingIndexes(interrupted))
	assert.False(t, taskstate.AuditOnlyReviewFinding(interrupted, interrupted.Findings[0]))
	assert.True(t, taskstate.AuditOnlyReviewFinding(interrupted, interrupted.Findings[1]))
	loaded, err := store.Load("repo", "task")
	require.NoError(t, err)
	event := loaded.Events[len(loaded.Events)-1]
	assert.Equal(t, taskstate.EventReviewInterrupted, event.Type)
	assert.Equal(t, secondRef, event.Address)
	assert.Equal(t, *execution.FinishedAt, event.At)
	assert.Equal(t, "supervisor_and_child_pids_absent", event.InterruptionReason)
	_, err = store.RecordReviewFinding("repo", "task", review.Attempt, taskstate.ReviewFinding{Actor: &secondRef, Type: taskstate.FindingTypeAdvisory, Title: "late callback", Description: "Child no longer owns this boundary"})
	require.Error(t, err)
	refused, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, loaded, refused)
}
