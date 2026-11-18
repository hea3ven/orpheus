package taskstate_test

import (
	"context"
	"github.com/hea3ven/orpheus/internal/workfixture"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	reviewphase "github.com/hea3ven/orpheus/internal/workflow/review"
	"path/filepath"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPolicyStore(t *testing.T, initial work.State) (workfixture.Store, workstate.Store) {
	t.Helper()
	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	require.NoError(t, err)
	raw := workstate.NewStore(paths)
	if initial.RepositoryID != "" {
		initial.Version = 0
		_, err = raw.Save(initial, 0)
		require.NoError(t, err)
	}
	at := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	return workfixture.NewStoreWithClock(paths, func() time.Time { at = at.Add(time.Second); return at }), raw
}

func TestReviewPolicyRetainsOwnedFindingsWithoutSelectingPublication(t *testing.T) {
	history := projectionHistory(t)
	history.Workflows[0].Phases = history.Workflows[0].Phases[:2]
	history.Workflows[0].ReviewContracts = nil
	store, raw := newPolicyStore(t, history)
	contract := work.ReviewContract{PipelineKey: "review", Slots: []work.ReviewSlot{{Kind: work.ReviewAgent, ConfigKey: "ai"}, {Kind: work.ReviewManual, ConfigKey: "approval"}}}
	review, err := store.SeedReview("repo", "task", reviewphase.Reservation{Pipeline: "review", Contract: contract, Scope: projectionScope(1)})
	require.NoError(t, err)
	execution := taskstate.AgentExecution{Agent: "reviewer", Harness: "pi", Command: "fixture-reviewer", StartedAt: review.StartedAt, Process: &work.RunningProcess{}}
	review, err = store.ObserveStep("repo", "task", review.Attempt, reviewphase.StepObservation{Address: review.Address, Kind: taskstate.ReviewStepKindAgentReview, Name: "ai", Execution: &execution})
	require.NoError(t, err)
	actor := review.Steps[0].Address
	_, err = (phaseexec.Runner{Store: raw, RepositoryID: "repo", TaskID: "task"}).CommitHeld(context.Background(), reviewphase.FindingChange(actor, reviewphase.FindingInput{Classification: work.FindingAdvisory, Title: "Original advisory", Description: "Optional improvement"}))
	require.NoError(t, err)
	zero := 0
	_, err = (phaseexec.Runner{Store: raw, RepositoryID: "repo", TaskID: "task"}).CommitHeld(context.Background(), reviewphase.ProcessChange(review.Attempt, "ai", reviewphase.ProcessObservation{Address: actor, Process: &work.ExitedProcess{At: review.StartedAt.Add(time.Second), Code: &zero}}))
	require.NoError(t, err)
	review, err = store.CurrentReviewAt(review.Address)
	require.NoError(t, err)
	require.NotNil(t, review.Findings[0].Address)
	target := *review.Findings[0].Address
	pending, err := store.SeedManual(review.Address, "approval")
	require.NoError(t, err)
	manual := pending.Steps[1].Address
	_, err = (phaseexec.Runner{Store: raw, RepositoryID: "repo", TaskID: "task"}).CommitHeld(context.Background(), reviewphase.FindingDecisionChange(manual, target, &work.PromotedFinding{}))
	require.NoError(t, err)
	blocked, err := store.SeedManualDecision(manual, taskstate.ReviewStatusBlocked)
	require.NoError(t, err)
	assert.Equal(t, taskstate.ReviewStatusBlocked, blocked.Status)
	assert.Equal(t, taskstate.FindingTypeAdvisory, blocked.Findings[0].ReportedType)
	assert.Equal(t, taskstate.FindingTypeBlocking, blocked.Findings[0].Type)
	loaded, err := store.Load("repo", "task")
	require.NoError(t, err)
	phase := loaded.Work.Workflows[0].Phases[2].(*work.ReviewPhase)
	assert.Equal(t, []work.FindingRef{target}, phase.Steps[1].(*work.ManualReviewStep).Decision.(*work.RequestedChanges).Targets)
	assert.Equal(t, "main", phase.Scope.Destination.Branch, "review destination remains unchanged")
	pending, err = store.SeedManual(review.Address, "approval")
	require.NoError(t, err)
	manual = pending.Steps[2].Address
	_, err = (phaseexec.Runner{Store: raw, RepositoryID: "repo", TaskID: "task"}).CommitHeld(context.Background(), reviewphase.FindingDecisionChange(manual, target, &work.WaivedFinding{Waiver: work.Waiver{Reason: "Accepted risk after inspection"}}))
	require.NoError(t, err)
	before, err := store.Load("repo", "task")
	require.NoError(t, err)
	for _, choice := range []work.FindingDisposition{&work.PromotedFinding{}, &work.AddressedFinding{Waiver: work.Waiver{Reason: "stale response"}}} {
		_, err = (phaseexec.Runner{Store: raw, RepositoryID: "repo", TaskID: "task"}).CommitHeld(context.Background(), reviewphase.FindingDecisionChange(manual, target, choice))
		require.Error(t, err)
	}
	after, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, before, after)
	_, err = store.SeedManualDecision(manual, taskstate.ReviewStatusPassed)
	require.NoError(t, err)
	reviewedState, err := store.Load("repo", "task")
	require.NoError(t, err)
	phase = reviewedState.Work.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
	assert.Equal(t, work.ReviewPassedWithWaivers, mustReviewOutcome(t, reviewedState.Work.ActiveWorkflow(), review.Address.Phase))
	assert.Equal(t, "main", phase.Scope.Destination.Branch)
	assert.Equal(t, "destination", phase.Scope.Destination.Commit)
	assert.Len(t, reviewedState.Work.ActiveWorkflow().Phases, 3, "manual approval does not select or authorize publication")
	// Establish downstream history as fixture data: Delivery owns actual admission,
	// while this store contract refuses review callbacks after leaving their phase.
	snapshot, err := raw.Load("repo", "task")
	require.NoError(t, err)
	w := snapshot.ActiveWorkflow()
	output, err := w.Output(phase.Scope.Output)
	require.NoError(t, err)
	w.Phases = append(w.Phases, &work.PublicationPhase{Review: work.ReviewRef{Phase: review.Address.Phase}, Input: work.PublicationInput{Code: *output, Provenance: work.InputReviewed}, Destination: *phase.Scope.Destination, Comparison: work.PublicationComparison{Relationship: "reviewed"}, Target: &work.PullRequestTarget{}})
	_, err = raw.Save(snapshot, snapshot.Version)
	require.NoError(t, err)
	downstream, err := store.Load("repo", "task")
	require.NoError(t, err)
	_, err = (phaseexec.Runner{Store: raw, RepositoryID: "repo", TaskID: "task"}).CommitHeld(context.Background(), reviewphase.FindingChange(actor, reviewphase.FindingInput{Classification: work.FindingBlocking, Title: "Late child report", Description: "Wrong owner"}))
	require.Error(t, err)
	refused, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, downstream, refused)
}

func mustReviewOutcome(t *testing.T, w *work.Workflow, phase int) work.ReviewOutcome {
	t.Helper()
	outcome, err := w.ReviewOutcome(work.ReviewRef{Phase: phase})
	require.NoError(t, err)
	return outcome
}
