package review_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewerExitAtomicallyRetainsCheckoutRecoveryUntilSuccessfulRestoration(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	require.NoError(t, err)
	initial := testutil.ReviewableWork("repo", "task", "/fixture/work", "task-branch")
	w := initial.ActiveWorkflow()
	at := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	w.ReviewContracts = []work.ReviewContract{{PipelineKey: "ai", Slots: []work.ReviewSlot{{Kind: work.ReviewAgent, ConfigKey: "reviewer"}}}}
	w.Phases = append(w.Phases, &work.ReviewPhase{Contract: 0, StartedAt: at, Scope: work.ReviewScope{Requirements: "Fixture task requirements", Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "destination"}}, Steps: []work.ReviewStep{
		&work.AgentReviewStep{Slot: 0, Execution: work.AgentExecution{Profile: "reviewer", Harness: "pi", Command: "fixture-reviewer", Process: &work.RunningProcess{}, StartedAt: at, ChildPID: 42}, Report: &work.ReviewReport{Findings: []work.Finding{{Title: "Retained finding", Description: "Already reported by the child", Classification: work.FindingAdvisory}}}},
	}})
	raw := workstate.NewStore(paths)
	initial.Version, err = raw.Save(initial, 0)
	require.NoError(t, err)
	runner := phaseexec.Runner{Store: raw, Paths: paths, RepositoryID: "repo", TaskID: "task"}
	stepIndex := 0
	owner := work.AttemptRef{RepositoryID: "repo", TaskID: "task", Phase: 2, Step: &stepIndex}
	code := 0
	failure := work.ReadOnlyFailure{At: at, Reason: "reviewer changed the checkout"}
	observation := review.ProcessObservation{Address: owner, ChildPID: 99, Process: &work.ExitedProcess{Code: &code, At: at.Add(time.Second)}, ReadOnlyFailure: &failure}

	refused, err := runner.Commit(context.Background(), review.ProcessChange(1, "reviewer", observation))
	require.ErrorContains(t, err, "process identity")
	assert.Equal(t, initial, refused)
	persisted, err := raw.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, initial, persisted, "a refused process cannot partly attach recovery")
	observation.ChildPID = 42
	_, err = runner.Commit(context.Background(), review.ProcessChange(1, "reviewer", observation))
	require.NoError(t, err)
	// A controller can stop here; no later pipeline write is needed for recovery.
	store := taskstate.NewStore(paths)
	loaded, err := store.Load("repo", "task")
	require.NoError(t, err)
	phase := loaded.Work.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
	require.NotNil(t, phase.CheckoutRecovery)
	assert.Nil(t, phase.CheckoutRecovery.RestoredAt)
	assert.Equal(t, "reviewer changed the checkout", phase.CheckoutRecovery.Reason)
	assert.Equal(t, taskstate.RunStatusSucceeded, loaded.Reviews[0].Steps[0].Execution.Status)
	assert.Equal(t, int64(1000), loaded.Reviews[0].Steps[0].Execution.DurationMillis)
	assert.Equal(t, taskstate.ReviewStatusFailed, loaded.Reviews[0].Status)
	require.Len(t, loaded.Reviews[0].Findings, 1)
	assert.Equal(t, "Retained finding", loaded.Reviews[0].Findings[0].Title)

	parent := owner
	parent.Step = nil
	for _, failure := range []error{errors.New("index locked"), nil} {
		err := review.ResolveCheckoutRecovery(review.CheckoutRecoveryOptions{
			Context: context.Background(), Runner: runner, Owner: parent,
			Prompt:  func(review.CheckoutViolation) (bool, error) { return true, nil },
			Restore: func(context.Context, string, review.CommittedCandidate) error { return failure },
		})
		require.ErrorContains(t, err, "review remains failed")
		if failure != nil {
			require.ErrorIs(t, err, failure)
		}
	}
	loaded, err = store.Load("repo", "task")
	require.NoError(t, err)
	phase = loaded.Work.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
	require.NotNil(t, phase.CheckoutRecovery.RestoredAt)
	require.Len(t, phase.CheckoutRecovery.Failures, 1)
	assert.Equal(t, "index locked", phase.CheckoutRecovery.Failures[0].Reason)
	assert.Equal(t, []work.ReadOnlyFailure{failure}, phase.Steps[0].(*work.AgentReviewStep).ReadOnlyFailures)
	assert.Equal(t, taskstate.ReviewStatusFailed, loaded.Reviews[0].Status)
}
