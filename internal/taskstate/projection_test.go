package taskstate_test

import (
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectFollowsWorkflowOwnershipWithoutReplacingFeatureIntentWithRepairOrSync(t *testing.T) {
	snapshot := projectionHistory(t)
	view, err := taskstate.Project(snapshot)
	require.NoError(t, err)
	assert.Equal(t, 0, view.CurrentWorkflow)
	assert.Equal(t, "baseline", view.BaselineCommit)
	assert.Equal(t, "/fixture/task", view.WorkDirectory.Path)
	assert.Equal(t, "task-branch", view.GitFacts.Branch)
	require.Len(t, view.Runs, 3)
	assert.Equal(t, "R3", view.Runs[2].Completion.Commit)
	assert.Equal(t, "P2", taskstate.FinalizationFacts(view).Commit, "local output and published output are independent")
	assert.Equal(t, projectionAt(41), *taskstate.FinalizationFacts(view).PushedAt)
	handoffs, err := taskstate.CompletionRunsForReview(view)
	require.NoError(t, err)
	assert.Equal(t, "feat: feature intent", handoffs.Original.Completion.CommitSubject)
	assert.Equal(t, "fix: latest repair", handoffs.Latest.Completion.CommitSubject)
	assert.Equal(t, 1, handoffs.Original.Address.Phase)
	assert.Equal(t, 7, handoffs.Latest.Address.Phase)
	require.NotNil(t, view.Runs[1].Execution.Launch)
	assert.Equal(t, taskstate.AgentLaunchResumed, view.Runs[1].Execution.Launch.Mode)
	assert.Equal(t, "native-feature", view.Runs[1].Execution.Launch.SourceSession.ID)
	assert.Equal(t, &view.Runs[0].Address, view.Runs[1].Execution.Launch.SourceExecution)
	assert.Equal(t, 500, view.Runs[1].Execution.Launch.UsageBaseline.TotalTokens)
	assert.Equal(t, int64(17), *view.Runs[1].Execution.Launch.CostBaseline)
	assert.Equal(t, "Incremental counters unavailable", view.Runs[1].Execution.UsageCapture.Reason)
	assert.Nil(t, view.Runs[1].Execution.Usage)
	assert.Equal(t, []int{0}, view.Runs[1].ReviewFollowUp.FindingIndexes)
	assert.Equal(t, 1, view.Runs[1].ReviewFollowUp.ReviewAttempt)
	require.Len(t, view.Reviews, 2)
	finding := view.Reviews[0].Findings[0]
	assert.Equal(t, &work.FindingRef{Phase: 2, Step: 0, Finding: 0}, finding.Address)
	assert.Equal(t, 2, finding.TargetedByRunAttempt)
	assert.Equal(t, "Use explicit bounds", finding.SuggestedAction)
	assert.Equal(t, taskstate.ReviewStatusPassed, view.Reviews[1].Status, "successful no-findings reviewer exit is sufficient; do not fabricate an extra report")
	assert.Nil(t, snapshot.Workflows[0].Phases[4].(*work.ReviewPhase).Steps[0].(*work.AgentReviewStep).Report)
	var resolverEvents []taskstate.Event
	for _, event := range view.Events {
		if event.Type == taskstate.EventSyncConflictStarted || event.Type == taskstate.EventSyncConflictFinished {
			resolverEvents = append(resolverEvents, event)
		}
	}
	require.Len(t, resolverEvents, 2)
	assert.Equal(t, taskstate.AgentExecutionPurposeSyncConflictResolution, resolverEvents[1].Execution.Purpose)
	assert.Equal(t, 700, resolverEvents[1].Execution.Usage.TotalTokens)
	assert.Equal(t, int64(29), resolverEvents[1].Execution.UsageCost.AmountMicroUSD)
	assert.Equal(t, "reported", resolverEvents[1].Execution.UsageCost.Kind)
	assert.Nil(t, view.ActiveSyncConflict)
	view.Runs[0].Execution.Args[0] = "changed view argument"
	assert.Equal(t, "--native-name", snapshot.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Args[0], "projection must not alias command argument storage")

	fresh := work.Workflow{Phases: []work.Phase{projectionPreparation(), projectionImplementation("feat: another workflow", "R9", 60)}}
	snapshot.Workflows = append(snapshot.Workflows, fresh)
	view, err = taskstate.Project(snapshot)
	require.NoError(t, err)
	latest, ok := taskstate.LatestRun(view)
	require.True(t, ok)
	assert.Equal(t, 1, latest.Attempt, "display numbering restarts and must not become task-wide authority")
	assert.Equal(t, 1, latest.Address.Workflow)
	assert.Equal(t, "R9", latest.Completion.Commit)
	handoffs, err = taskstate.CompletionRunsForReview(view)
	require.NoError(t, err)
	assert.Equal(t, "feat: another workflow", handoffs.Original.Completion.CommitSubject)
	_, ok = taskstate.LatestReview(view)
	assert.False(t, ok, "a current workflow without review cannot borrow an earlier workflow's approval")
	assert.Empty(t, taskstate.FinalizationFacts(view).Commit, "new local work does not inherit old publication")

	current := snapshot.Workflows[1].Phases[1].(*work.ImplementationPhase)
	current.Output = nil
	view, err = taskstate.Project(snapshot)
	require.NoError(t, err)
	_, err = taskstate.CompletionRunsForReview(view)
	require.ErrorContains(t, err, "safely captured output", "a handoff and successful exit alone are not retained feature identity")
	current.Completion = nil
	view, err = taskstate.Project(snapshot)
	require.NoError(t, err)
	_, err = taskstate.CompletionRunsForReview(view)
	require.ErrorContains(t, err, "completion is required", "a newer incomplete effort cannot borrow historical rationale")

	snapshot.Workflows = append(snapshot.Workflows, work.Workflow{})
	view, err = taskstate.Project(snapshot)
	require.NoError(t, err)
	_, ok = taskstate.LatestRun(view)
	assert.False(t, ok, "empty current work must not fall back to older efforts")
	_, err = taskstate.CompletionRunsForReview(view)
	require.Error(t, err)
	assert.True(t, view.GitFacts.IsZero())
	assert.True(t, view.WorkDirectory.IsZero())
}

func TestProjectFinalizationFollowsItsOwnPredecessorAcrossWorkflows(t *testing.T) {
	snapshot, err := testutil.DeliveredPRWork("repo", "task", "/fixture/old", "old-branch", "https://example.test/pull/1")
	require.NoError(t, err)
	old := &snapshot.Workflows[0]
	external := old.Phases[4].(*work.ExternalReviewPhase)
	// Put a newer, unrelated delivery between the owning publication and
	// external review. Neither it nor a later workflow owns this closure.
	other := *old.Phases[3].(*work.PublicationPhase)
	other.Consent = nil
	other.Delivery = nil
	old.Phases = append(old.Phases[:4], &other)
	require.NoError(t, snapshot.AuthorizePublication(0, work.PublicationRef{Phase: 4}, projectionAt(40), "Explicit authorization for the other PR"))
	other.Operation = &work.PublicationOperation{StartedAt: projectionAt(40), Ref: "refs/orpheus/public", Public: &work.CodeIdentity{Commit: "other-public", Tree: "fixture-tree"}}
	other.Delivery = &work.PullRequestDelivery{PullRequest: work.PullRequestRef{URL: "https://example.test/pull/2"}, ObservedAt: projectionAt(41)}
	external.Outcome = &work.PROutcome{State: work.PRMerged, ObservedAt: projectionAt(42)}
	intent := projectionAt(43)
	final := &work.FinalizationPhase{ClosureIntentAt: &intent}
	old.Phases = append(old.Phases, external, final)

	pending, err := taskstate.Project(snapshot)
	require.NoError(t, err)
	assert.Equal(t, "fixture-output", pending.Finalization.Commit, "older delivery uses selected input, not a later package")
	assert.Nil(t, pending.Finalization.ClosedAt)
	final.Closure = &work.TaskClosure{At: projectionAt(44), Reason: "pr_merged"}
	final.Cleanup = []work.CleanupObservation{&work.WorktreeRemoved{At: projectionAt(45), Evidence: "Native removal completed"}}
	newer, err := testutil.DeliveredPRWork("repo", "task", "/fixture/new", "new-branch", "https://example.test/pull/3")
	require.NoError(t, err)
	next := &newer.Workflows[0]
	nextPublication := next.Phases[3].(*work.PublicationPhase)
	nextDelivery := nextPublication.Delivery
	nextPublication.Consent, nextPublication.Delivery = nil, nil
	snapshot.Workflows = append(snapshot.Workflows, *next)
	snapshot.Workflows[1].Phases = next.Phases[:4]
	require.NoError(t, snapshot.AuthorizePublication(1, work.PublicationRef{Phase: 3}, projectionAt(60), "Authorize the new workflow"))
	nextPublication.Delivery = nextDelivery
	nextDelivery.(*work.PullRequestDelivery).ObservedAt = projectionAt(61)
	snapshot.Workflows[1].Phases = next.Phases

	view, err := taskstate.Project(snapshot)
	require.NoError(t, err)
	prerequisite, err := view.Work.Workflows[0].FinalizationContext(6)
	require.NoError(t, err)
	assert.Equal(t, work.PublicationRef{Phase: 3}, prerequisite.Publication)
	assert.Equal(t, projectionAt(42), prerequisite.ObservedAt)
	assert.Equal(t, "pr_merged", prerequisite.CloseReason())
	assert.Equal(t, "/fixture/new", view.WorkDirectory.Path)
	assert.Nil(t, view.Finalization.ClosedAt, "the current workflow does not inherit closure")

	var closed, removed []taskstate.Event
	for _, event := range view.Events {
		switch event.Type {
		case taskstate.EventTaskClosed:
			closed = append(closed, event)
		case taskstate.EventWorktreeRemoved:
			removed = append(removed, event)
		}
	}
	require.Len(t, closed, 1)
	assert.Equal(t, work.AttemptRef{RepositoryID: "repo", TaskID: "task", Workflow: 0, Phase: 6}, closed[0].Address)
	assert.Equal(t, "https://example.test/pull/1", closed[0].PRURL)
	assert.Equal(t, "merged", closed[0].ObservedPRState)
	assert.Equal(t, "pr_merged", closed[0].CloseReason)
	assert.Equal(t, projectionAt(44), closed[0].At)
	require.Len(t, removed, 1)
	assert.Equal(t, closed[0].Address, removed[0].Address)
	assert.Equal(t, "/fixture/old", removed[0].Worktree)
	assert.Equal(t, projectionAt(45), removed[0].At)
}

func TestProjectPreservesPendingManualFindingsAndUnresolvedBranchMaintenance(t *testing.T) {
	snapshot := projectionHistory(t)
	w := &snapshot.Workflows[0]
	w.Phases = w.Phases[:7]
	sync := &w.Phases[6].(*work.ExternalReviewPhase).BranchSyncs[0]
	sync.LocalCommit, sync.Push = nil, nil
	sync.ConflictResolution.Execution.Process = &work.InterruptedProcess{At: projectionAt(45), Reason: "Supervisor disappeared", Trigger: "doctor"}
	view, err := taskstate.Project(snapshot)
	require.NoError(t, err)
	require.NotNil(t, view.ActiveSyncConflict)
	assert.Equal(t, "0/6/0", view.ActiveSyncConflict.ID, "recovery identity is the stable containment address, not a generated operation ID")
	assert.Equal(t, "main", view.ActiveSyncConflict.DefaultBranch)
	assert.Equal(t, "P2", view.ActiveSyncConflict.Checkpoint.RemoteHead)
	assert.Equal(t, taskstate.RunStatusInterrupted, view.ActiveSyncConflict.Execution.Status)
	require.NotNil(t, view.ActiveSyncConflict.Execution.FinishedAt)
	assert.Equal(t, projectionAt(45), *view.ActiveSyncConflict.Execution.FinishedAt)
	assert.Zero(t, view.ActiveSyncConflict.Execution.DurationMillis, "an interruption observation is not an exit")
	assert.Equal(t, "Supervisor disappeared", view.ActiveSyncConflict.Execution.InterruptionReason)
	assert.Equal(t, "doctor", view.ActiveSyncConflict.Execution.InterruptionTrigger)
	assert.Equal(t, []string{"app.go"}, view.ActiveSyncConflict.ConflictFiles)

	snapshot = projectionHistory(t)
	w = &snapshot.Workflows[0]
	manual := &work.ManualReviewStep{Slot: 0, Findings: []work.Finding{
		{Title: "Retained advisory", Classification: work.FindingAdvisory, Description: "Original classification", Dispositions: []work.FindingDisposition{&work.PromotedFinding{At: projectionAt(81)}}},
		{Title: "Separate task", Classification: work.FindingSeparateTask, Description: "A follow-up", Proposal: &work.FollowUpProposal{Title: "Extract helper", Description: "Keep this separate", AcceptanceCriteria: "Helper has focused tests"}, CreatedTask: &work.CreatedFollowUp{TaskID: "op-followup", ObservedAt: projectionAt(81)}},
	}}
	w.ReviewContracts = append(w.ReviewContracts, work.ReviewContract{PipelineKey: "manual", Slots: []work.ReviewSlot{{Kind: work.ReviewManual, ConfigKey: "operator"}}})
	w.Phases = append(w.Phases, &work.ReviewPhase{Contract: 2, StartedAt: projectionAt(80), Scope: projectionScope(7), Steps: []work.ReviewStep{manual}})
	view, err = taskstate.Project(snapshot)
	require.NoError(t, err)
	latest, ok := taskstate.LatestReview(view)
	require.True(t, ok)
	assert.Equal(t, taskstate.ReviewStatusWaitingForManual, latest.Status)
	assert.True(t, taskstate.ReviewHasOpenBlockers(latest), "pending manual findings must remain visible and prevent approval")
	require.Len(t, latest.Findings, 2)
	assert.Equal(t, taskstate.FindingTypeAdvisory, latest.Findings[0].ReportedType)
	assert.Equal(t, taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	assert.IsType(t, &work.PromotedFinding{}, latest.Findings[0].Decision)
	assert.Equal(t, "Extract helper", latest.Findings[1].TaskProposal.Title)
	assert.Equal(t, "Helper has focused tests", latest.Findings[1].TaskProposal.AcceptanceCriteria)
	assert.Equal(t, "op-followup", latest.Findings[1].CreatedTaskID)
	assert.Equal(t, projectionAt(81), *latest.Findings[1].CreatedTaskAt)
	assert.Equal(t, &work.FindingRef{Phase: 8, Step: 0, Finding: 1}, latest.Findings[1].Address)

	manual.Findings = manual.Findings[1:]
	view, err = taskstate.Project(snapshot)
	require.NoError(t, err)
	latest, ok = taskstate.LatestReview(view)
	require.True(t, ok)
	assert.Equal(t, taskstate.ReviewStatusWaitingForManual, latest.Status)
	manual.Decision = &work.AbortedReview{At: projectionAt(82), Reason: "Operator stopped"}
	view, err = taskstate.Project(snapshot)
	require.NoError(t, err)
	latest, ok = taskstate.LatestReview(view)
	require.True(t, ok)
	assert.Equal(t, taskstate.ReviewStatusAborted, latest.Status)
	manual.Slot = -1
	invalid, err := taskstate.Project(snapshot)
	require.Error(t, err)
	assert.Nil(t, invalid.Work, "invalid snapshots must not become partial operator views")
	assert.Empty(t, invalid.Runs)
}

func projectionAt(minute int) time.Time {
	return time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC)
}

func projectionPreparation() *work.PreparationPhase {
	at := projectionAt(0)
	return &work.PreparationPhase{Context: &work.WorkContext{Directory: "/fixture/task", TaskBranch: "task-branch", CheckpointRef: "refs/orpheus/baseline", Baseline: work.CodeIdentity{Commit: "baseline", Tree: "baseline-tree"}}, EstablishedAt: &at}
}

func projectionImplementation(summary, commit string, minute int) *work.ImplementationPhase {
	zero := 0
	return &work.ImplementationPhase{
		Execution:  work.AgentExecution{Profile: "implementer", Harness: "pi", Command: "pi", Args: []string{"--native-name", "Feature session"}, Session: &work.Session{ID: "native-feature"}, StartedAt: projectionAt(minute), Process: &work.ExitedProcess{At: projectionAt(minute + 2), Code: &zero}, Launch: &work.FreshExecution{}, Usage: &work.Usage{InputTokens: 400, OutputTokens: 100, TotalTokens: 500}, Cost: &work.UsageCost{Kind: "reported", Currency: "USD", AmountMicroUSD: 17}},
		Completion: &work.Completion{CommitSubject: summary, CommitBody: "Owned rationale", CompletedAt: projectionAt(minute + 1)},
		Output:     &work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: commit, Tree: "tree-" + commit}, RetainedRef: "refs/orpheus/" + commit},
	}
}

func projectionScope(phase int) work.ReviewScope {
	return work.ReviewScope{Requirements: "Implement the feature safely", Output: work.OutputRef{Phase: phase}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "destination"}}
}

func projectionHistory(t *testing.T) work.State {
	t.Helper()
	one, zero := 1, 0
	firstReview := &work.ReviewPhase{StartedAt: projectionAt(10), Scope: projectionScope(1), Steps: []work.ReviewStep{&work.CheckStep{Slot: 0, Result: &work.CheckResult{ExitCode: &one, FinishedAt: projectionAt(11)}, Findings: []work.Finding{{Title: "Unsafe bounds", Classification: work.FindingBlocking, Description: "The bounds check failed", SuggestedAction: "Use explicit bounds", Dispositions: []work.FindingDisposition{&work.RepairRequired{At: projectionAt(12)}}}}}}}
	repair := projectionImplementation("fix: repair bounds", "R2", 20)
	repair.Input, repair.TargetFindings = &work.OutputRef{Phase: 1}, []work.FindingRef{{Phase: 2, Step: 0, Finding: 0}}
	cost := int64(17)
	repair.Execution.Launch = &work.ResumedExecution{From: work.ExecutionRef{Phase: 1}, UsageBaseline: &work.Usage{TotalTokens: 500}, CostBaselineMicroUSD: &cost}
	repair.Execution.Usage = nil
	repair.Execution.UsageCapture = work.UsageCapture{Status: work.UsageUnknown, Reason: "Incremental counters unavailable"}
	secondReview := &work.ReviewPhase{Contract: 1, StartedAt: projectionAt(30), Scope: projectionScope(3), Steps: []work.ReviewStep{&work.AgentReviewStep{Slot: 0, Execution: work.AgentExecution{Profile: "reviewer", StartedAt: projectionAt(31), Process: &work.ExitedProcess{At: projectionAt(32), Code: &zero}}}}}
	pub := &work.PublicationPhase{Review: work.ReviewRef{Phase: 4}, Target: &work.PullRequestTarget{}}
	w := work.Workflow{ReviewContracts: []work.ReviewContract{
		{PipelineKey: "check", Slots: []work.ReviewSlot{{Kind: work.ReviewCheck, ConfigKey: "bounds"}}},
		{PipelineKey: "review", Slots: []work.ReviewSlot{{Kind: work.ReviewAgent, ConfigKey: "reviewer"}}},
	}, Phases: []work.Phase{projectionPreparation(), projectionImplementation("feat: feature intent", "R1", 1), firstReview, repair, secondReview, pub}}
	snapshot := work.State{Version: 9, RepositoryID: "repo", TaskID: "task", Workflows: []work.Workflow{w}}
	testutil.SeedPublicationInputs(&snapshot)
	require.NoError(t, snapshot.AuthorizePublication(0, work.PublicationRef{Phase: 5}, projectionAt(40), "Explicit pre-existing operator grant in this fixture"))
	pub.Operation = &work.PublicationOperation{StartedAt: projectionAt(40), Ref: "refs/orpheus/public", Public: &work.CodeIdentity{Commit: "P2", Tree: pub.Input.Code.Tree}}
	pub.Delivery = &work.PullRequestDelivery{PullRequest: work.PullRequestRef{URL: "https://example.test/pull/1"}, ObservedAt: projectionAt(41)}
	sync := work.BranchSync{StartedAt: projectionAt(42), Checkpoint: work.BranchSyncCheckpoint{LocalHead: "P2", RemoteHead: "P2", MergeSource: "D2"}, ConflictResolution: &work.ConflictResolution{Files: []string{"app.go"}, Execution: work.AgentExecution{Profile: "resolver", Harness: "pi", StartedAt: projectionAt(43), Process: &work.ExitedProcess{At: projectionAt(44), Code: &zero}, Usage: &work.Usage{TotalTokens: 700}, Cost: &work.UsageCost{Kind: "reported", Currency: "USD", AmountMicroUSD: 29}}}, LocalCommit: &work.BranchSyncCommit{Head: "S1", ObservedAt: projectionAt(45)}, Push: &work.BranchSyncPush{ObservedAt: projectionAt(46)}}
	latest := projectionImplementation("fix: latest repair", "R3", 50)
	latest.Input = &work.OutputRef{Phase: 3}
	snapshot.Workflows[0].Phases = append(snapshot.Workflows[0].Phases, &work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: 5}, BranchSyncs: []work.BranchSync{sync}}, latest)
	require.NoError(t, snapshot.Validate())
	return snapshot
}
