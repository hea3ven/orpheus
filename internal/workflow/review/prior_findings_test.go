package review_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPriorFindingDispositionsValidateTogetherButRetainIndependentReceipts(t *testing.T) {
	paths := newTestPaths(t)
	raw := workstate.NewStore(paths)
	initial := testutil.ReviewableWork("repo", "task", "/fixture/work", "task-branch")
	w := initial.ActiveWorkflow()
	at := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	one := 1
	w.ReviewContracts = []work.ReviewContract{{PipelineKey: "prior", Slots: []work.ReviewSlot{{Kind: work.ReviewCheck, ConfigKey: "check"}}}}
	w.Phases = append(w.Phases, &work.ReviewPhase{StartedAt: at, Scope: work.ReviewScope{Requirements: "Prior requirements", Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "destination"}}, Steps: []work.ReviewStep{&work.CheckStep{Slot: 0, Result: &work.CheckResult{ExitCode: &one, FinishedAt: at}, Findings: []work.Finding{
		{Classification: work.FindingBlocking, Title: "First blocker", Description: "First evidence", Dispositions: []work.FindingDisposition{&work.RepairRequired{}}},
		{Classification: work.FindingBlocking, Title: "Second blocker", Description: "Second evidence", Dispositions: []work.FindingDisposition{&work.RepairRequired{}}},
	}}}})
	var err error
	initial.Version, err = raw.Save(initial, 0)
	require.NoError(t, err)
	injected := errors.New("second disposition receipt failed")
	store := &manualSaveStore{Store: raw, failure: injected, failAt: 2}
	runner := phaseexec.Runner{Paths: paths, Store: store, RepositoryID: "repo", TaskID: "task"}
	owner := work.AttemptRef{RepositoryID: "repo", TaskID: "task", Phase: 2}
	var phase review.PriorFindings
	selection, err := phase.Next(initial, owner)
	require.NoError(t, err)
	request, ok := selection.Input()
	require.True(t, ok)
	require.Len(t, request.Blockers, 2)
	answer := review.PriorFindingsAnswer{Dispositions: []review.FreshReviewBlockerDisposition{
		{FindingIndex: 0, Action: review.FreshReviewBlockerActionAddressedManually, Reason: "Verified first repair"},
		{FindingIndex: 1, Action: review.FreshReviewBlockerActionWaive},
	}}
	_, err = phase.Respond(initial, request.ID, answer)
	require.ErrorContains(t, err, "finding 2 requires a reason")
	unchanged, err := raw.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, initial, unchanged)
	assert.Zero(t, store.saves, "the first answer cannot persist before the complete answer is valid")
	answer.Dispositions[1].Reason = "Accepted second risk"
	op, err := phase.Respond(initial, request.ID, answer)
	require.NoError(t, err)
	current, err := runner.Execute(context.Background(), initial, op)
	require.NoError(t, err)
	assert.Equal(t, initial, current, "accepting input does not invent durable progress")
	for range 2 {
		selection, err := phase.Next(current, owner)
		require.NoError(t, err)
		op, ok := selection.Operation()
		require.True(t, ok)
		current, err = runner.Execute(context.Background(), current, op)
		if err != nil {
			require.ErrorIs(t, err, injected)
			break
		}
	}
	assert.Equal(t, 2, store.saves)
	assert.Equal(t, initial.Version+1, current.Version)
	after, err := raw.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, current, after)
	first, err := after.ActiveWorkflow().Finding(work.FindingRef{Phase: 2, Step: 0, Finding: 0})
	require.NoError(t, err)
	require.Len(t, first.Dispositions, 2)
	assert.Equal(t, "Verified first repair", first.Disposition().(*work.AddressedFinding).Reason)
	second, err := after.ActiveWorkflow().Finding(work.FindingRef{Phase: 2, Step: 0, Finding: 1})
	require.NoError(t, err)
	require.Len(t, second.Dispositions, 1, "the failed receipt cannot leak a waiver")

	store.failure = nil
	phase = review.PriorFindings{}
	selection, err = phase.Next(after, owner)
	require.NoError(t, err)
	request, ok = selection.Input()
	require.True(t, ok)
	require.Len(t, request.Blockers, 1)
	assert.Equal(t, 1, request.Blockers[0].Index, "a new invocation asks only about the remaining blocker")
	op, err = phase.Respond(after, request.ID, review.PriorFindingsAnswer{Dispositions: answer.Dispositions[1:]})
	require.NoError(t, err)
	current, err = runner.Execute(context.Background(), after, op)
	require.NoError(t, err)
	selection, err = phase.Next(current, owner)
	require.NoError(t, err)
	op, ok = selection.Operation()
	require.True(t, ok)
	current, err = runner.Execute(context.Background(), current, op)
	require.NoError(t, err)
	assert.Equal(t, initial.Version+2, current.Version)
	selection, err = phase.Next(current, owner)
	require.NoError(t, err)
	outcome, ok := selection.Outcome()
	require.True(t, ok)
	assert.Equal(t, review.PriorFindingsOutcome{}, outcome)
	retained, err := current.ActiveWorkflow().Finding(work.FindingRef{Phase: 2, Step: 0, Finding: 0})
	require.NoError(t, err)
	assert.Equal(t, first, retained, "retry never repeats a recorded earlier decision")
	waived, err := current.ActiveWorkflow().Finding(work.FindingRef{Phase: 2, Step: 0, Finding: 1})
	require.NoError(t, err)
	assert.Equal(t, "Accepted second risk", waived.Disposition().(*work.WaivedFinding).Reason)
	assert.Equal(t, initial.ActiveWorkflow().ReviewContracts, current.ActiveWorkflow().ReviewContracts)
	assert.Equal(t, initial.ActiveWorkflow().Phases[2].(*work.ReviewPhase).Scope, current.ActiveWorkflow().Phases[2].(*work.ReviewPhase).Scope)
	assert.Len(t, current.ActiveWorkflow().Phases, 3, "dispositions do not initialize another phase")
}
