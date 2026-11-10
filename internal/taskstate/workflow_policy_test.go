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
	handoff := taskstate.CompleteRunOptions{CommitSubject: "feat: original intent", CommitBody: "Requested behavior", PRDescription: "Original PR content", TechnicalExplanation: "Why"}
	live, err := store.CompleteImplementation(run.Address, handoff)
	require.NoError(t, err)
	assert.Equal(t, taskstate.RunStatusRunning, live.Status)
	assert.Nil(t, live.Execution.FinishedAt)
	assert.Empty(t, live.Completion.Commit, "handoff cannot authorize capture or a native exit")
	first, err := store.Load("repo", "task")
	require.NoError(t, err)
	_, err = store.CompleteImplementation(run.Address, taskstate.CompleteRunOptions{CommitSubject: "attempt to replace", CommitBody: "Different", PRDescription: "Different"})
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

func TestImplementationResumptionKeepsExactSourceWithinCurrentWorkflow(t *testing.T) {
	history := projectionHistory(t)
	current := projectionHistory(t).Workflows[0]
	current.Phases = current.Phases[:2]
	current.ReviewContracts = nil
	current.Phases[1].(*work.ImplementationPhase).Execution.Session = &work.Session{ID: "current-session", LogPath: "/fixture/current-session.jsonl"}
	history.Workflows = append(history.Workflows, current)
	store, _ := newPolicyStore(t, history)
	before, err := store.Load("repo", "task")
	require.NoError(t, err)
	source := before.Runs[len(before.Runs)-1].Address
	historical := before.Runs[0].Address
	foreign := source
	foreign.TaskID = "another-task"
	for _, tc := range []struct {
		name   string
		source *work.AttemptRef
	}{
		{name: "missing exact source"},
		{name: "older workflow with matching attempt", source: &historical},
		{name: "another task", source: &foreign},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.StartRun("repo", "task", taskstate.StartRunOptions{Agent: "repairer", Harness: "pi", Launch: &taskstate.AgentLaunch{Mode: taskstate.AgentLaunchResumed, SourceRunAttempt: 1, SourceExecution: tc.source}})
			require.ErrorContains(t, err, "owning workflow")
			after, err := store.Load("repo", "task")
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
	baseline := &taskstate.AgentUsage{TotalTokens: 150}
	run, err := store.StartRun("repo", "task", taskstate.StartRunOptions{Agent: "repairer", Harness: "pi", Launch: &taskstate.AgentLaunch{Mode: taskstate.AgentLaunchResumed, SourceRunAttempt: 999, SourceExecution: &source, UsageBaseline: baseline}})
	require.NoError(t, err)
	assert.Equal(t, &source, run.Execution.Launch.SourceExecution)
	assert.Equal(t, 1, run.Execution.Launch.SourceRunAttempt, "display attempt is derived from exact provenance, never used to select it")
	assert.Equal(t, "current-session", run.Execution.Launch.SourceSession.ID)
	assert.Equal(t, baseline, run.Execution.Launch.UsageBaseline)
	after, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, before.Runs, after.Runs[:len(before.Runs)], "resumption retains historical handoffs, process and usage facts")
}

func TestReviewPolicyRetainsOwnedFindingsWithoutSelectingPublication(t *testing.T) {
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
	blocked, err := store.RecordManualDecision(manual, taskstate.ReviewStatusBlocked)
	require.NoError(t, err)
	assert.Equal(t, taskstate.ReviewStatusBlocked, blocked.Status)
	assert.Equal(t, taskstate.FindingTypeAdvisory, blocked.Findings[0].ReportedType)
	assert.Equal(t, taskstate.FindingTypeBlocking, blocked.Findings[0].Type)
	_, err = store.PreparePublication(review.Address, work.PublicationPhase{Review: work.ReviewRef{Phase: review.Address.Phase}, Target: &work.PullRequestTarget{}})
	require.Error(t, err)
	loaded, err := store.Load("repo", "task")
	require.NoError(t, err)
	phase := loaded.Work.Workflows[0].Phases[2].(*work.ReviewPhase)
	assert.Equal(t, []work.FindingRef{target}, phase.Steps[1].(*work.ManualReviewStep).Decision.(*work.RequestedChanges).Targets)
	assert.Equal(t, "main", phase.Scope.Destination.Branch, "review destination remains unchanged")
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
	_, err = store.RecordManualDecision(manual, taskstate.ReviewStatusPassed)
	require.NoError(t, err)
	reviewedState, err := store.Load("repo", "task")
	require.NoError(t, err)
	w := reviewedState.Work.ActiveWorkflow()
	output, err := w.Output(w.LastPhase().(*work.ReviewPhase).Scope.Output)
	require.NoError(t, err)
	publication, err := store.PreparePublication(review.Address, work.PublicationPhase{Review: work.ReviewRef{Phase: review.Address.Phase}, Input: work.PublicationInput{Code: *output, Provenance: work.InputReviewed}, Destination: *w.LastPhase().(*work.ReviewPhase).Scope.Destination, Comparison: work.PublicationComparison{Relationship: "reviewed"}, Target: &work.PullRequestTarget{}})
	require.NoError(t, err)
	snapshot, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, 3, publication.Phase)
	p := snapshot.Work.Workflows[0].Phases[3].(*work.PublicationPhase)
	assert.IsType(t, &work.PullRequestTarget{}, p.Target)
	assert.Nil(t, p.Consent, "manual approval never selects or authorizes delivery")
	assert.Nil(t, p.Delivery)
	destination, err := snapshot.Work.Workflows[0].ReviewDestination(p.Review)
	require.NoError(t, err)
	assert.Equal(t, "main", destination.Branch)
	assert.Equal(t, "destination", destination.Commit)
	assert.Equal(t, "main", snapshot.Work.Workflows[0].Phases[2].(*work.ReviewPhase).Scope.Destination.Branch, "review scope remains immutable and visible")
	_, err = store.RecordReviewFinding("repo", "task", review.Attempt, taskstate.ReviewFinding{Actor: &actor, Type: taskstate.FindingTypeBlocking, Title: "Late child report", Description: "Wrong owner"})
	require.Error(t, err)
	refused, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, snapshot, refused)
}
