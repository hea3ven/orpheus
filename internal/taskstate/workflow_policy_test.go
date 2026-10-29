package taskstate_test

import (
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

func newPolicyStore(t *testing.T, initial work.State) (taskstate.Store, workstate.Store) {
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
	return taskstate.NewStoreWithClock(paths, func() time.Time { at = at.Add(time.Second); return at }), raw
}

func TestImplementationPolicyKeepsLiveHandoffSeparateFromObservedExitAndRefusesStaleOwners(t *testing.T) {
	store, raw := newPolicyStore(t, work.State{})
	prepared := &work.WorkContext{Directory: "/fixture/work", TaskBranch: "task", CheckpointRef: "refs/orpheus/baseline", Baseline: work.CodeIdentity{Commit: "B", Tree: "BT"}}
	run, err := store.StartRun("repo", "task", taskstate.StartRunOptions{Context: prepared, Agent: "implementer", Harness: "pi", Command: "fixture-agent", Args: []string{" -- prompt\nwith newlines"}, SessionName: "Original launch label", SupervisorPID: 123})
	require.NoError(t, err)
	assert.Equal(t, work.AttemptRef{RepositoryID: "repo", TaskID: "task", Phase: 1}, run.Address)
	before, err := store.Load("repo", "task")
	require.NoError(t, err)
	_, err = store.StartRun("repo", "task", taskstate.StartRunOptions{Agent: "another"})
	require.ErrorIs(t, err, taskstate.ErrActiveRun)
	after, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, before, after)
	_, err = store.RecordRunChildPIDAt(run.Address, 456)
	require.NoError(t, err)
	handoff := taskstate.CompleteRunOptions{Summary: "feat: original intent", Description: "Requested behavior", DetailedDescription: "Original PR content", TechnicalExplanation: "Why"}
	live, err := store.CompleteImplementation(run.Address, handoff)
	require.NoError(t, err)
	assert.Equal(t, taskstate.RunStatusRunning, live.Status)
	assert.Nil(t, live.Execution.FinishedAt)
	assert.Empty(t, live.Completion.Commit, "handoff cannot authorize capture or a native exit")
	first, err := store.Load("repo", "task")
	require.NoError(t, err)
	_, err = store.CompleteImplementation(run.Address, taskstate.CompleteRunOptions{Summary: "attempt to replace", Description: "Different", DetailedDescription: "Different"})
	require.NoError(t, err)
	repeated, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, first, repeated, "first handoff is authoritative and repeated calls do not append an independent event")
	_, err = store.FinishObservedRun(run.Address, nil, 456)
	require.Error(t, err)
	zero := 0
	exit := &work.ExitedProcess{At: live.Completion.CompletedAt.Add(time.Second), Code: &zero}
	_, err = store.FinishObservedRun(run.Address, exit, 999)
	require.Error(t, err)
	preserved, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, first, preserved)
	finished, err := store.FinishObservedRun(run.Address, exit, 456)
	require.NoError(t, err)
	assert.Equal(t, taskstate.RunStatusSucceeded, finished.Status)
	assert.Equal(t, 456, finished.Execution.ChildPID)
	assert.Equal(t, "Original launch label", finished.Execution.SessionName)
	captureAt := exit.At.Add(time.Second)
	_, err = store.RecordRunUsageAt(run.Address, taskstate.RecordRunUsageOptions{Session: &taskstate.AgentSession{ID: "native", LogPath: "/fixture/session.jsonl"}, Usage: &taskstate.AgentUsage{TotalTokens: 50}, UsageCost: &taskstate.AgentUsageCost{Kind: "reported", Currency: "USD", AmountMicroUSD: 0}, UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, CapturedAt: &captureAt}, Model: "native-model"})
	require.NoError(t, err)
	historical, err := store.Load("repo", "task")
	require.NoError(t, err)
	require.NotNil(t, historical.Runs[0].Execution.UsageCost)
	assert.Zero(t, historical.Runs[0].Execution.UsageCost.AmountMicroUSD, "known zero differs from missing cost")
	assert.Equal(t, "native-model", historical.Runs[0].Execution.Model)
	_, err = store.RecordRunUsageAt(run.Address, taskstate.RecordRunUsageOptions{Usage: &taskstate.AgentUsage{TotalTokens: 999}})
	require.Error(t, err)
	_, err = store.StartReviewWithOptions("repo", "task", taskstate.StartReviewOptions{Pipeline: "manual", Contract: work.ReviewContract{PipelineKey: "manual", Slots: []work.ReviewSlot{{Kind: work.ReviewManual, ConfigKey: "approval"}}}, Scope: projectionScope(1)})
	require.Error(t, err, "even a successful process with handoff is not captured output")
	refused, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, historical, refused)
	// Explicit reset is fixture setup: the application never converts or resets
	// operator history implicitly. Its equal indexes must not reuse an old token.
	snapshot, err := raw.Load("repo", "task")
	require.NoError(t, err)
	contextCopy := *prepared
	established := captureAt.Add(time.Second)
	snapshot.Workflows = append(snapshot.Workflows, work.Workflow{Phases: []work.Phase{&work.PreparationPhase{Context: &contextCopy, EstablishedAt: &established}, &work.ImplementationPhase{Execution: work.AgentExecution{StartedAt: established, Process: &work.RunningProcess{}}}}})
	_, err = raw.Save(snapshot, snapshot.Version)
	require.NoError(t, err)
	before, err = store.Load("repo", "task")
	require.NoError(t, err)
	_, err = store.RecordRunChildPIDAt(run.Address, 777)
	require.Error(t, err)
	_, err = store.CompleteImplementation(run.Address, handoff)
	require.Error(t, err)
	after, err = store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestReviewPolicyRetainsOwnedFindingsAndOnlyApprovedPublicationChoices(t *testing.T) {
	history := projectionHistory(t)
	history.Workflows[0].Phases = history.Workflows[0].Phases[:2]
	history.Workflows[0].ReviewContracts = nil
	store, _ := newPolicyStore(t, history)
	contract := work.ReviewContract{PipelineKey: "review", Slots: []work.ReviewSlot{{Kind: work.ReviewAgent, ConfigKey: "ai"}, {Kind: work.ReviewManual, ConfigKey: "approval"}}}
	review, err := store.StartReviewWithOptions("repo", "task", taskstate.StartReviewOptions{Pipeline: "review", Contract: contract, Scope: projectionScope(1)})
	require.NoError(t, err)
	execution := taskstate.AgentExecution{Agent: "reviewer", Harness: "pi", Command: "fixture-reviewer", StartedAt: review.StartedAt, Process: &work.RunningProcess{}}
	review, err = store.RecordReviewStep("repo", "task", review.Attempt, taskstate.RecordReviewStepOptions{Address: review.Address, Kind: taskstate.ReviewStepKindAgentReview, Name: "ai", Execution: &execution})
	require.NoError(t, err)
	actor := review.Steps[0].Address
	_, err = store.RecordReviewFinding("repo", "task", review.Attempt, taskstate.ReviewFinding{Actor: &actor, Type: taskstate.FindingTypeAdvisory, Title: "Original advisory", Description: "Optional improvement", Step: "ai"})
	require.NoError(t, err)
	zero := 0
	review, err = store.FinishReviewStepExecution("repo", "task", review.Attempt, "ai", taskstate.FinishReviewStepExecutionOptions{Address: actor, Process: &work.ExitedProcess{At: review.StartedAt.Add(time.Second), Code: &zero}})
	require.NoError(t, err)
	require.NotNil(t, review.Findings[0].Address)
	target := *review.Findings[0].Address
	pending, err := store.PauseReviewForManualAt(review.Address, "approval")
	require.NoError(t, err)
	manual := pending.Steps[1].Address
	_, err = store.RecordFindingDecision(manual, target, &work.PromotedFinding{})
	require.NoError(t, err)
	tentative := &work.ReviewedPublication{Destination: work.Destination{Remote: "origin", Branch: "discard-me", Commit: "D2"}, Target: &work.DirectMergeTarget{}}
	blocked, err := store.RecordManualDecision(manual, taskstate.ReviewStatusBlocked, tentative)
	require.NoError(t, err)
	assert.Equal(t, taskstate.ReviewStatusBlocked, blocked.Status)
	assert.Equal(t, taskstate.FindingTypeAdvisory, blocked.Findings[0].ReportedType)
	assert.Equal(t, taskstate.FindingTypeBlocking, blocked.Findings[0].Type)
	_, err = store.PreparePublication(review.Address, &work.PullRequestTarget{}, "Title\n\nBody")
	require.Error(t, err)
	loaded, err := store.Load("repo", "task")
	require.NoError(t, err)
	phase := loaded.Work.Workflows[0].Phases[2].(*work.ReviewPhase)
	assert.Equal(t, []work.FindingRef{target}, phase.Steps[1].(*work.ManualReviewStep).Decision.(*work.RequestedChanges).Targets)
	assert.Equal(t, "main", phase.Scope.Destination.Branch, "blocked tentative destination is not stored")
	pending, err = store.PauseReviewForManualAt(review.Address, "approval")
	require.NoError(t, err)
	manual = pending.Steps[2].Address
	_, err = store.RecordFindingDecision(manual, target, &work.WaivedFinding{Waiver: work.Waiver{Reason: "Accepted risk after inspection"}})
	require.NoError(t, err)
	before, err := store.Load("repo", "task")
	require.NoError(t, err)
	for _, choice := range []work.FindingDisposition{&work.PromotedFinding{}, &work.AddressedFinding{Waiver: work.Waiver{Reason: "stale response"}}} {
		_, err = store.RecordFindingDecision(manual, target, choice)
		require.Error(t, err)
	}
	after, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, before, after)
	selected := &work.ReviewedPublication{Destination: work.Destination{Remote: "origin", Branch: "release/next", Commit: "D3"}, Target: &work.DirectMergeTarget{}}
	_, err = store.RecordManualDecision(manual, taskstate.ReviewStatusPassed, selected)
	require.NoError(t, err)
	publication, err := store.PreparePublication(review.Address, &work.PullRequestTarget{}, "Original title\n\nOriginal PR content")
	require.NoError(t, err)
	snapshot, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, 3, publication.Phase)
	p := snapshot.Work.Workflows[0].Phases[3].(*work.PublicationPhase)
	assert.IsType(t, &work.DirectMergeTarget{}, p.Plan.Target)
	assert.Nil(t, p.Consent, "manual approval chooses intent, never delivery authorization")
	assert.Nil(t, p.Delivery)
	destination, err := snapshot.Work.Workflows[0].ReviewDestination(p.Plan.Review)
	require.NoError(t, err)
	assert.Equal(t, "release/next", destination.Branch)
	assert.Equal(t, "D3", destination.Commit)
	assert.Equal(t, "main", snapshot.Work.Workflows[0].Phases[2].(*work.ReviewPhase).Scope.Destination.Branch, "automated scope remains immutable and visible")
	_, err = store.RecordReviewFinding("repo", "task", review.Attempt, taskstate.ReviewFinding{Actor: &actor, Type: taskstate.FindingTypeBlocking, Title: "Late child report", Description: "Wrong owner"})
	require.Error(t, err)
	refused, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, snapshot, refused)
}
