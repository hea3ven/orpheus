package taskstate_test

import (
	"context"
	workmodel "github.com/hea3ven/orpheus/internal/work"
	implementation "github.com/hea3ven/orpheus/internal/workflow/implementation"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	reviewphase "github.com/hea3ven/orpheus/internal/workflow/review"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInterruptedImplementationProjectsTerminalAuditFacts(t *testing.T) {
	store := newTestStore(t, time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC))
	run, err := store.SeedImplementation("alpha", "op-interrupted", implementation.Reservation{Context: &workmodel.WorkContext{Directory: "/fixture/work", TaskBranch: "task-branch", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}}, Agent: "implementer", SupervisorPID: 123})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := store.CheckpointPID(run.Address, 456); err != nil {
		t.Fatalf("record child PID: %v", err)
	}

	view, err := store.Load("alpha", "op-interrupted")
	require.NoError(t, err)
	changed, err := implementation.InterruptionChange(run.Address, "supervisor_and_child_pids_absent", "doctor_fix")(view.Work)
	require.NoError(t, err)
	require.True(t, changed)
	interruptedView, err := taskstate.Project(*view.Work)
	interrupted, _ := taskstate.LatestRun(interruptedView)

	if err != nil {
		t.Fatalf("interrupt run: %v", err)
	}
	if interrupted.Status != taskstate.RunStatusInterrupted || interrupted.Execution.FinishedAt == nil {
		t.Fatalf("interrupted run = %#v, want terminal interrupted run", interrupted)
	}
	if interrupted.Execution.SupervisorPID != 123 || interrupted.Execution.ChildPID != 456 {
		t.Fatalf("process facts = %#v, want persisted PIDs", interrupted.Execution)
	}
	state := interruptedView
	event := state.Events[len(state.Events)-1]
	if event.Type != taskstate.EventRunInterrupted || event.InterruptionReason != "supervisor_and_child_pids_absent" || event.InterruptionTrigger != "doctor_fix" {
		t.Fatalf("event = %#v, want interruption audit facts", event)
	}
}

func TestInterruptedReviewPreservesProcessAuditAndOnlyExcludesTheInterruptedStepFindings(t *testing.T) {
	history := projectionHistory(t)
	history.Workflows[0].Phases = history.Workflows[0].Phases[:2]
	history.Workflows[0].ReviewContracts = nil
	store, raw := newPolicyStore(t, history)
	recorder := phaseexec.Runner{Store: raw, RepositoryID: "repo", TaskID: "task"}
	contract := workmodel.ReviewContract{PipelineKey: "ai", Slots: []workmodel.ReviewSlot{{Kind: workmodel.ReviewAgent, ConfigKey: "first"}, {Kind: workmodel.ReviewAgent, ConfigKey: "second"}}}
	review, err := store.SeedReview("repo", "task", reviewphase.Reservation{Pipeline: "ai", Contract: contract, Scope: projectionScope(1)})
	require.NoError(t, err)
	first := taskstate.AgentExecution{Agent: "first", Harness: "pi", Command: "fixture-reviewer", Process: &workmodel.RunningProcess{}, StartedAt: review.StartedAt}
	review, err = store.ObserveStep("repo", "task", review.Attempt, reviewphase.StepObservation{Address: review.Address, Kind: taskstate.ReviewStepKindAgentReview, Name: "first", Execution: &first})
	require.NoError(t, err)
	firstRef := review.Steps[0].Address
	_, err = recorder.CommitHeld(context.Background(), reviewphase.FindingChange(firstRef, reviewphase.FindingInput{Classification: workmodel.FindingBlocking, Title: "first finding", Description: "Authoritative report"}))
	require.NoError(t, err)
	zero := 0
	_, err = recorder.CommitHeld(context.Background(), reviewphase.ProcessChange(review.Attempt, "first", reviewphase.ProcessObservation{Address: firstRef, Process: &workmodel.ExitedProcess{Code: &zero, At: review.StartedAt.Add(time.Second)}}))
	require.NoError(t, err)
	second := taskstate.AgentExecution{Agent: "second", Harness: "pi", Command: "fixture-reviewer", Process: &workmodel.RunningProcess{}, StartedAt: review.StartedAt.Add(3 * time.Second), SupervisorPID: 123}
	review, err = store.ObserveStep("repo", "task", review.Attempt, reviewphase.StepObservation{Address: review.Address, Kind: taskstate.ReviewStepKindAgentReview, Name: "second", Execution: &second})
	require.NoError(t, err)
	secondRef := review.Steps[1].Address
	_, err = recorder.CommitHeld(context.Background(), reviewphase.ChildPIDChange(secondRef, 456))
	require.NoError(t, err)
	_, err = recorder.CommitHeld(context.Background(), reviewphase.FindingChange(secondRef, reviewphase.FindingInput{Classification: workmodel.FindingBlocking, Title: "second finding", Description: "Interrupted audit report"}))
	require.NoError(t, err)

	_, err = recorder.CommitHeld(context.Background(), reviewphase.InterruptionChange(secondRef, "supervisor_and_child_pids_absent", "doctor_fix"))
	require.NoError(t, err)
	interrupted, err := store.CurrentReviewAt(review.Address)
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
	_, err = recorder.CommitHeld(context.Background(), reviewphase.FindingChange(secondRef, reviewphase.FindingInput{Classification: workmodel.FindingAdvisory, Title: "late callback", Description: "Child no longer owns this boundary"}))
	require.Error(t, err)
	refused, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, loaded, refused)
}
