package taskstate_test

import (
	"errors"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewerExitAtomicallyRetainsCheckoutRecoveryUntilSuccessfulRestoration(t *testing.T) {
	history := projectionHistory(t)
	history.Workflows[0].Phases = history.Workflows[0].Phases[:2]
	history.Workflows[0].ReviewContracts = nil
	store, _ := newPolicyStore(t, history)
	contract := work.ReviewContract{PipelineKey: "ai", Slots: []work.ReviewSlot{{Kind: work.ReviewAgent, ConfigKey: "reviewer"}}}
	attempt, err := store.StartReviewWithOptions("repo", "task", taskstate.StartReviewOptions{Pipeline: "ai", Contract: contract, Scope: projectionScope(1)})
	require.NoError(t, err)
	execution := taskstate.AgentExecution{Agent: "reviewer", Harness: "pi", Command: "fixture-reviewer", Process: &work.RunningProcess{}, StartedAt: attempt.StartedAt}
	attempt, err = store.RecordReviewStep("repo", "task", attempt.Attempt, taskstate.RecordReviewStepOptions{Address: attempt.Address, Kind: taskstate.ReviewStepKindAgentReview, Name: "reviewer", Execution: &execution})
	require.NoError(t, err)
	code := 0
	failure := work.ReadOnlyFailure{At: attempt.StartedAt, Reason: "reviewer changed the checkout"}
	_, err = store.FinishReviewStepExecution("repo", "task", attempt.Attempt, "reviewer", taskstate.FinishReviewStepExecutionOptions{Address: attempt.Steps[0].Address, Process: &work.ExitedProcess{Code: &code, At: attempt.StartedAt.Add(time.Second)}, ReadOnlyFailure: &failure})
	require.NoError(t, err)
	// A controller can stop here; no later pipeline write is needed for recovery.
	loaded, err := store.Load("repo", "task")
	require.NoError(t, err)
	phase := loaded.Work.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
	require.NotNil(t, phase.CheckoutRecovery)
	assert.Nil(t, phase.CheckoutRecovery.RestoredAt)
	assert.Equal(t, "reviewer changed the checkout", phase.CheckoutRecovery.Reason)
	assert.Equal(t, taskstate.RunStatusSucceeded, loaded.Reviews[0].Steps[0].Execution.Status)
	assert.Equal(t, taskstate.ReviewStatusFailed, loaded.Reviews[0].Status)

	require.NoError(t, store.RecordCheckoutRestoration(attempt.Address, errors.New("index locked")))
	require.NoError(t, store.RecordCheckoutRestoration(attempt.Address, nil))
	loaded, err = store.Load("repo", "task")
	require.NoError(t, err)
	phase = loaded.Work.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
	require.NotNil(t, phase.CheckoutRecovery.RestoredAt)
	require.Len(t, phase.CheckoutRecovery.Failures, 1)
	assert.Equal(t, "index locked", phase.CheckoutRecovery.Failures[0].Reason)
	assert.Equal(t, []work.ReadOnlyFailure{failure}, phase.Steps[0].(*work.AgentReviewStep).ReadOnlyFailures)
	assert.Equal(t, taskstate.ReviewStatusFailed, loaded.Reviews[0].Status)
}
