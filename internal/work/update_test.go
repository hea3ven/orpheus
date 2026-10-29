package work_test

import (
	"slices"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/require"
)

func TestSuccessorSettlesOwnedWorkAndAppendsAttemptsWithoutRewritingHistory(t *testing.T) {
	previous, next := runningImplementation(t), runningImplementation(t)
	p := next.Workflows[0].Phases[1].(*work.ImplementationPhase)
	p.Execution.Process = &work.ExitedProcess{At: at(12), Code: exitValue(0)}
	p.Completion = implementation(t, "feat: original feature", "1", 10).Completion
	p.Output = code("1")
	p.Execution.Usage = &work.Usage{InputTokens: 100, CachedInputTokens: 80, TotalTokens: 120}
	p.Execution.Cost = &work.UsageCost{Kind: "reported", Currency: "USD", AmountMicroUSD: 12}
	p.Execution.UsageCapture = work.UsageCapture{Status: work.UsageCaptured}
	p.Execution.Duration = 2 * time.Minute
	require.NoError(t, next.ValidateSuccessor(&previous))
	require.IsType(t, &work.RunningProcess{}, previous.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Process)
	require.Nil(t, previous.Workflows[0].Phases[1].(*work.ImplementationPhase).Completion)
	require.Nil(t, previous.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Usage)

	previous, next = failedImplementation(t), failedImplementation(t)
	retry := implementation(t, "Retry", "2", 20)
	retry.Execution.Process, retry.Completion, retry.Output = &work.RunningProcess{}, nil, nil
	next.Workflows[0].Phases = append(next.Workflows[0].Phases, retry)
	require.NoError(t, next.ValidateSuccessor(&previous))
	require.Equal(t, 1, *next.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Process.(*work.ExitedProcess).Code)
	require.NoError(t, next.CheckActiveAttempt(attempt(t, 0, 2, nil)))
	require.Error(t, next.CheckActiveAttempt(attempt(t, 0, 1, nil)))

	previous, next = reviewingState(t), reviewingState(t)
	oldContract := next.Workflows[0].ReviewContracts[0]
	newSlots := append(slices.Clone(oldContract.Slots), work.ReviewSlot{Kind: work.ReviewCheck, ConfigKey: "extra-lint"})
	next.Workflows[0].ReviewContracts = append(next.Workflows[0].ReviewContracts, work.ReviewContract{PipelineKey: "changed-policy", Slots: newSlots})
	newReview := review(t, 1, 50)
	newReview.Contract = 1
	next.Workflows[0].Phases = append(next.Workflows[0].Phases, newReview)
	require.NoError(t, next.ValidateSuccessor(&previous))
	outcome, err := next.Workflows[0].ReviewOutcome(work.ReviewRef{Phase: 2})
	require.NoError(t, err)
	require.Equal(t, work.ReviewPassed, outcome, "new configuration must not reinterpret historical acceptance")
	outcome, err = next.Workflows[0].ReviewOutcome(work.ReviewRef{Phase: 3})
	require.NoError(t, err)
	require.Equal(t, work.ReviewPending, outcome)
	require.Empty(t, newReview.Steps, "required definitions are not started-work placeholders")

	previous, next = reviewingState(t), reviewingState(t)
	observed := &next.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution
	observed.Usage = &work.Usage{InputTokens: 250, TotalTokens: 300}
	observed.UsageCapture = work.UsageCapture{Status: work.UsageCaptured}
	require.NoError(t, next.ValidateSuccessor(&previous), "safe telemetry capture may finish after later work starts")
	require.Nil(t, previous.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Usage)
	require.Equal(t, "feat: original feature", next.Workflows[0].FeatureCompletion().Summary)

	previous, next = completedReviewerState(t), completedReviewerState(t)
	oldReviewer := previous.Workflows[0].Phases[2].(*work.ReviewPhase).Steps[1].(*work.AgentReviewStep)
	newReviewer := next.Workflows[0].Phases[2].(*work.ReviewPhase).Steps[1].(*work.AgentReviewStep)
	oldReviewer.Execution.Process = &work.RunningProcess{}
	newReviewer.Report.Evidence = "Final reviewer narrative"
	require.NoError(t, next.ValidateSuccessor(&previous), "an active reviewer can fill its initially absent narrative while settling")
	require.Empty(t, oldReviewer.Report.Evidence)
	require.IsType(t, &work.RunningProcess{}, oldReviewer.Execution.Process)
	require.Equal(t, "Final reviewer narrative", newReviewer.Report.Evidence)
}

func TestSuccessorPreservesAddressesAndRecordedFacts(t *testing.T) {
	cases := []struct {
		name   string
		build  func(*testing.T) work.State
		change func(*work.State)
		want   string
	}{
		{"repository identity", reviewingState, func(s *work.State) { s.RepositoryID = "different" }, "repository identity"},
		{"removed workflow", reviewingState, func(s *work.State) { s.Workflows = nil }, "workflows are append-only"},
		{"removed phase", simpleState, func(s *work.State) { s.Workflows[0].Phases = s.Workflows[0].Phases[:1] }, "phases are append-only"},
		{"replaced current phase kind", simpleState, func(s *work.State) { s.Workflows[0].Phases[1] = &work.PreparationPhase{} }, "phase kind"},
		{"rewritten historical handoff", reviewingState, func(s *work.State) {
			s.Workflows[0].Phases[1].(*work.ImplementationPhase).Completion.Description = "Replacement rationale"
		}, "historical phase"},
		{"changed captured contract", reviewingState, func(s *work.State) {
			s.Workflows[0].ReviewContracts[0].Slots = append(s.Workflows[0].ReviewContracts[0].Slots, work.ReviewSlot{Kind: work.ReviewCheck, ConfigKey: "new-required-check"})
		}, "review contracts is append-only"},
		{"changed review scope", reviewingState, func(s *work.State) {
			s.Workflows[0].Phases[2].(*work.ReviewPhase).Scope.Requirements = "Different scope"
		}, "review scope"},
		{"rewritten manual decision", reviewingState, func(s *work.State) {
			s.Workflows[0].Phases[2].(*work.ReviewPhase).Steps[2].(*work.ManualReviewStep).Decision.(*work.ApprovedReview).At = at(29)
		}, "manual assessment"},
		{"reordered retry history", retryReviewState, func(s *work.State) {
			p := s.Workflows[0].Phases[2].(*work.ReviewPhase)
			p.Steps[1], p.Steps[2] = p.Steps[2], p.Steps[1]
		}, "terminal process result"},
		{"rewritten failed exit", failedImplementation, func(s *work.State) {
			*s.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Process.(*work.ExitedProcess).Code = 2
		}, "terminal process result"},
		{"replaced native session", failedImplementation, func(s *work.State) {
			s.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Session.ID = "other-session"
		}, "native session"},
		{"changed launch model", failedImplementation, func(s *work.State) {
			s.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Model = "other-model"
		}, "launch identity"},
		{"rewritten captured usage", capturedUsageState, func(s *work.State) {
			s.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Usage.InputTokens = 999
		}, "captured usage"},
		{"repriced captured cost", capturedUsageState, func(s *work.State) {
			s.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Cost.AmountMicroUSD = 999
		}, "captured cost"},
		{"rewritten finding decision", waivedFindingState, func(s *work.State) {
			finding, err := s.Workflows[0].Finding(localFindingRef(t))
			require.NoError(t, err)
			finding.Dispositions[1].(*work.WaivedFinding).Reason = "Rewritten authorization rationale"
		}, "finding disposition history is append-only"},
		{"removed finding decision", waivedFindingState, func(s *work.State) {
			finding, err := s.Workflows[0].Finding(localFindingRef(t))
			require.NoError(t, err)
			finding.Dispositions = finding.Dispositions[:1]
		}, "finding disposition history is append-only"},
		{"rewritten reported issue", waivedFindingState, func(s *work.State) {
			finding, err := s.Workflows[0].Finding(localFindingRef(t))
			require.NoError(t, err)
			finding.Description = "Different issue than the reviewer reported"
		}, "reported finding"},
		{"late report findings", completedReviewerState, func(s *work.State) {
			p := s.Workflows[0].Phases[2].(*work.ReviewPhase)
			p.Steps[1].(*work.AgentReviewStep).Report.Findings = []work.Finding{blocker("Late child result")}
		}, "terminal report findings"},
		{"late report narrative", completedReviewerState, func(s *work.State) {
			s.Workflows[0].Phases[2].(*work.ReviewPhase).Steps[1].(*work.AgentReviewStep).Report.Evidence = "Late reviewer narrative"
		}, "terminal report evidence"},
		{"late implementation handoff", failedImplementation, func(s *work.State) {
			s.Workflows[0].Phases[1].(*work.ImplementationPhase).Completion = implementation(t, "Late handoff", "1", 10).Completion
		}, "terminal execution handoff"},
		{"changed publication intent", repairHistory, func(s *work.State) {
			s.Workflows[0].Phases[9].(*work.PublicationPhase).Plan.Content = "Different authored publication"
		}, "publication intent"},
		{"rewritten delivered observation", deliveredHistory, func(s *work.State) {
			s.Workflows[0].Phases[9].(*work.PublicationPhase).Delivery.(*work.PullRequestDelivery).ObservedAt = at(102)
		}, "observed delivery"},
		{"removed branch sync", pushedBranchSyncState, func(s *work.State) {
			branchMaintenance(s).BranchSyncs = branchMaintenance(s).BranchSyncs[:1]
		}, "branch syncs are append-only"},
		{"rewritten sync checkpoint", pushedBranchSyncState, func(s *work.State) {
			branchMaintenance(s).BranchSyncs[0].Checkpoint.MergeSource = "different-incoming-commit"
		}, "branch sync checkpoint"},
		{"rewritten settled sync result", pushedBranchSyncState, func(s *work.State) {
			branchMaintenance(s).BranchSyncs[1].LocalCommit.Head = "replacement"
			branchMaintenance(s).BranchSyncs[1].Push.Head = "replacement"
		}, "settled branch sync local commit"},
		{"rewritten push observation", pushedBranchSyncState, func(s *work.State) {
			branchMaintenance(s).BranchSyncs[1].Push.ObservedAt = at(125)
		}, "settled branch sync push"},
		{"rewritten resolver PID", pushedBranchSyncState, func(s *work.State) {
			branchMaintenance(s).BranchSyncs[1].ConflictResolution.Execution.ChildPID = 999
		}, "native child PID"},
		{"rewritten conflict files", pushedBranchSyncState, func(s *work.State) {
			branchMaintenance(s).BranchSyncs[1].ConflictResolution.Files[0] = "different-file.go"
		}, "branch sync conflict files"},
		{"erased conflict resolution", pushedBranchSyncState, func(s *work.State) {
			branchMaintenance(s).BranchSyncs[1].ConflictResolution = nil
		}, "branch sync conflict resolution"},
		{"sync appended to historical phase", finalizedBranchSyncState, func(s *work.State) {
			branchMaintenance(s).BranchSyncs = append(branchMaintenance(s).BranchSyncs, cleanBranchSync("S2", "D4", "S3", 130))
		}, "historical branch syncs"},
		{"old workflow receives new finished work", resetState, func(s *work.State) {
			p := implementation(t, "Stale result", "old", 30)
			p.Execution.Process, p.Completion, p.Output = &work.ExitedProcess{At: at(32), Code: exitValue(1)}, nil, nil
			s.Workflows[0].Phases = append(s.Workflows[0].Phases, p)
		}, "inactive workflow history"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			previous, next := tc.build(t), tc.build(t)
			tc.change(&next)
			require.NoError(t, next.Validate(), "this case must distinguish legal snapshot shape from unsafe historical mutation")
			require.ErrorContains(t, next.ValidateSuccessor(&previous), tc.want)
			require.NoError(t, previous.Validate(), "independent previous evidence remains unchanged")
		})
	}
}

func TestSuccessorRecordsWaiverAndNewIntentWithoutOverwritingTheirSources(t *testing.T) {
	previous, next := completedReviewerState(t), completedReviewerState(t)
	oldCheck := previous.Workflows[0].Phases[2].(*work.ReviewPhase).Steps[0].(*work.CheckStep)
	newCheck := next.Workflows[0].Phases[2].(*work.ReviewPhase).Steps[0].(*work.CheckStep)
	// Use one configured check as the tail attempt; its failed evidence is immutable.
	previous.Workflows[0].Phases[2].(*work.ReviewPhase).Steps = []work.ReviewStep{oldCheck}
	next.Workflows[0].Phases[2].(*work.ReviewPhase).Steps = []work.ReviewStep{newCheck}
	*oldCheck.Result.ExitCode, *newCheck.Result.ExitCode = 4, 4
	newCheck.Waiver = &work.Waiver{Reason: "Accepted check limitation", At: at(25)}
	require.NoError(t, next.ValidateSuccessor(&previous))
	require.Equal(t, 4, *oldCheck.Result.ExitCode)
	require.Nil(t, oldCheck.Waiver)

	cases := []struct {
		name       string
		build      func(*testing.T) work.State
		finding    work.FindingRef
		decision   func() work.FindingDisposition
		wantReview work.ReviewOutcome
	}{
		{"completed reviewer", blockingReviewerState, localFindingRef(t), findingWaiver, work.ReviewPassedWithWaivers},
		{"earlier review attempt", blockingReviewState, localFindingRef(t), func() work.FindingDisposition {
			return &work.DowngradedFinding{Waiver: work.Waiver{Reason: "Operator treats the local issue as advisory", At: at(90)}}
		}, work.ReviewPassedWithWaivers},
		{"earlier review phase", repairHistory, localFindingRef(t), func() work.FindingDisposition {
			return &work.AddressedFinding{Waiver: work.Waiver{Reason: "Operator records an explicit local resolution", At: at(90)}}
		}, work.ReviewPending},
		{"manual assessment", manualFindingState, work.FindingRef{Phase: 2, Step: 2, Finding: 0}, findingWaiver, work.ReviewChangesRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before, after := tc.build(t), tc.build(t)
			original, err := before.Workflows[0].Finding(tc.finding)
			require.NoError(t, err)
			finding, err := after.Workflows[0].Finding(tc.finding)
			require.NoError(t, err)
			finding.Dispositions = append(finding.Dispositions, tc.decision())

			require.NoError(t, after.Validate())
			require.NoError(t, after.ValidateSuccessor(&before), "an explicit decision must not need a fabricated reviewer retry")
			require.Len(t, original.Dispositions, 1)
			require.IsType(t, &work.RepairRequired{}, original.Disposition())
			require.IsType(t, &work.RepairRequired{}, finding.Dispositions[0])
			require.IsType(t, tc.decision(), finding.Disposition())
			require.Equal(t, original.Classification, finding.Classification)
			require.Equal(t, original.Description, finding.Description)
			if tc.wantReview != "" {
				outcome, outcomeErr := after.Workflows[0].ReviewOutcome(work.ReviewRef{Phase: tc.finding.Phase})
				require.NoError(t, outcomeErr)
				require.Equal(t, tc.wantReview, outcome)
			}

			later := tc.build(t)
			latest, err := later.Workflows[0].Finding(tc.finding)
			require.NoError(t, err)
			latest.Dispositions = append(latest.Dispositions, tc.decision(), &work.DismissedFinding{Waiver: work.Waiver{Reason: "Further evidence supports dismissal", At: at(91)}})
			require.NoError(t, later.ValidateSuccessor(&after), "later decisions append instead of rewriting the previous decision")
			require.Len(t, finding.Dispositions, 2)
			require.IsType(t, &work.DismissedFinding{}, latest.Disposition())
		})
	}

	t.Run("decision on the delivered exact review", func(t *testing.T) {
		before, after := publishedWaivedFindingState(t), publishedWaivedFindingState(t)
		publication := after.Workflows[0].Phases[3].(*work.PublicationPhase)
		grant, delivery := *publication.Consent, *publication.Delivery.(*work.PullRequestDelivery)
		finding, err := after.Workflows[0].Finding(localFindingRef(t))
		require.NoError(t, err)
		finding.Dispositions = append(finding.Dispositions, &work.DismissedFinding{Waiver: work.Waiver{Reason: "Later evidence supports dismissal", At: at(40)}})

		require.NoError(t, after.ValidateSuccessor(&before), "a later decision must not move the historical grant or delivery")
		require.Equal(t, grant, *publication.Consent)
		require.Equal(t, delivery, *publication.Delivery.(*work.PullRequestDelivery))
		original, err := before.Workflows[0].Finding(localFindingRef(t))
		require.NoError(t, err)
		require.Len(t, original.Dispositions, 2)
		require.Len(t, finding.Dispositions, 3)
		outcome, err := after.Workflows[0].ReviewOutcome(work.ReviewRef{Phase: 2})
		require.NoError(t, err)
		require.Equal(t, work.ReviewPassedWithWaivers, outcome)

		reopened := publishedWaivedFindingState(t)
		reopenedFinding, err := reopened.Workflows[0].Finding(localFindingRef(t))
		require.NoError(t, err)
		decision := &work.RepairRequired{}
		reopenedFinding.Dispositions = append(reopenedFinding.Dispositions,
			&work.DismissedFinding{Waiver: work.Waiver{Reason: "Later evidence supports dismissal", At: at(40)}}, decision)
		require.ErrorContains(t, reopened.Validate(), "later finding decisions require observation time")
		decision.At = at(45)
		require.NoError(t, reopened.ValidateSuccessor(&after), "reopening changes current progression, not the grant's historical authority")
		outcome, err = reopened.Workflows[0].ReviewOutcome(work.ReviewRef{Phase: 2})
		require.NoError(t, err)
		require.Equal(t, work.ReviewChangesRequired, outcome)
		retained := reopened.Workflows[0].Phases[3].(*work.PublicationPhase)
		require.Equal(t, grant, *retained.Consent)
		require.Equal(t, delivery, *retained.Delivery.(*work.PullRequestDelivery))
		require.Len(t, finding.Dispositions, 3)
	})

	previous, next = repairHistory(t), repairHistory(t)
	p := &work.PublicationPhase{Plan: work.PublicationPlan{Review: work.ReviewRef{Phase: 8}, Target: &work.DirectMergeTarget{}, Content: "A new intended flow"}}
	next.Workflows[0].Phases = append(next.Workflows[0].Phases, p)
	require.NoError(t, next.AuthorizePublication(0, work.PublicationRef{Phase: 10}, at(110), "Explicitly authorize new flow"))
	require.NoError(t, next.ValidateSuccessor(&previous))
	require.Nil(t, next.Workflows[0].Phases[9].(*work.PublicationPhase).Consent)
	require.NotNil(t, p.Consent)
	require.Equal(t, "Original feature narrative with repair context", previous.Workflows[0].Phases[9].(*work.PublicationPhase).Plan.Content)
}

func TestSuccessorAllowsCleanupAfterClosureWithoutRepeatingPublicationOrInheritingIt(t *testing.T) {
	previous, next := closedState(t), closedState(t)
	next.Workflows = append(next.Workflows, freshWorkflow(t))
	old := next.Workflows[0].Phases[11].(*work.FinalizationPhase)
	old.Cleanup = append(old.Cleanup, &work.WorktreeRemoved{Path: "/fixture/work", At: at(122), Evidence: "Safe cleanup completed later"})
	require.NoError(t, next.ValidateSuccessor(&previous))
	require.Len(t, previous.Workflows[0].Phases[11].(*work.FinalizationPhase).Cleanup, 1)
	require.Equal(t, "P2", next.Workflows[0].Phases[9].(*work.PublicationPhase).Delivery.(*work.PullRequestDelivery).Head)
	require.Same(t, &next.Workflows[1], next.ActiveWorkflow())
	require.Empty(t, next.Workflows[1].Phases)
}

func reviewingState(t *testing.T) work.State {
	t.Helper()
	s := simpleState(t)
	s.Workflows[0].Phases = append(s.Workflows[0].Phases, passedReview(t, 1, 20))
	return s
}
func runningImplementation(t *testing.T) work.State {
	t.Helper()
	s := simpleState(t)
	p := s.Workflows[0].Phases[1].(*work.ImplementationPhase)
	p.Execution.Process, p.Completion, p.Output = &work.RunningProcess{}, nil, nil
	return s
}
func failedImplementation(t *testing.T) work.State {
	t.Helper()
	s := simpleState(t)
	p := s.Workflows[0].Phases[1].(*work.ImplementationPhase)
	p.Execution.Process, p.Completion, p.Output = &work.ExitedProcess{At: at(12), Code: exitValue(1)}, nil, nil
	return s
}
func capturedUsageState(t *testing.T) work.State {
	t.Helper()
	s := failedImplementation(t)
	p := s.Workflows[0].Phases[1].(*work.ImplementationPhase)
	p.Execution.Usage = &work.Usage{InputTokens: 10}
	p.Execution.Cost = &work.UsageCost{Kind: "reported", Currency: "USD", AmountMicroUSD: 12}
	p.Execution.UsageCapture = work.UsageCapture{Status: work.UsageCaptured}
	return s
}
func publishedWaivedFindingState(t *testing.T) work.State {
	t.Helper()
	s := blockingReviewState(t)
	finding, err := s.Workflows[0].Finding(localFindingRef(t))
	require.NoError(t, err)
	finding.Dispositions = append(finding.Dispositions, &work.WaivedFinding{Waiver: work.Waiver{Reason: "Operator accepts the reported limitation", At: at(25)}})
	publication := &work.PublicationPhase{Plan: work.PublicationPlan{Review: work.ReviewRef{Phase: 2}, Target: &work.PullRequestTarget{}, Content: "Publish the reviewed feature"}}
	s.Workflows[0].Phases = append(s.Workflows[0].Phases, publication)
	require.NoError(t, s.AuthorizePublication(0, work.PublicationRef{Phase: 3}, at(30), "Explicit publication grant"))
	publication.Delivery = &work.PullRequestDelivery{PullRequest: nativePR(), PublicCommit: "P1", Head: "P1", Tree: "T1", ObservedAt: at(31), Evidence: "Observed exact reviewed delivery"}
	require.NoError(t, s.Validate())
	return s
}
func findingWaiver() work.FindingDisposition {
	return &work.WaivedFinding{Waiver: work.Waiver{Reason: "Operator accepts this limitation", At: at(90)}}
}
func blockingReviewState(t *testing.T) work.State {
	t.Helper()
	s := reviewingState(t)
	s.Workflows[0].Phases[2].(*work.ReviewPhase).Steps[1].(*work.AgentReviewStep).Report.Findings = []work.Finding{blocker("Reported blocker")}
	return s
}
func blockingReviewerState(t *testing.T) work.State {
	t.Helper()
	s := blockingReviewState(t)
	s.Workflows[0].ReviewContracts[0].Slots = s.Workflows[0].ReviewContracts[0].Slots[:2]
	p := s.Workflows[0].Phases[2].(*work.ReviewPhase)
	p.Steps = p.Steps[:2]
	return s
}
func waivedFindingState(t *testing.T) work.State {
	t.Helper()
	s := blockingReviewState(t)
	finding, err := s.Workflows[0].Finding(localFindingRef(t))
	require.NoError(t, err)
	finding.Dispositions = append(finding.Dispositions, findingWaiver())
	return s
}
func manualFindingState(t *testing.T) work.State {
	t.Helper()
	s := reviewingState(t)
	step := s.Workflows[0].Phases[2].(*work.ReviewPhase).Steps[2].(*work.ManualReviewStep)
	step.Findings = []work.Finding{blocker("Manually reported blocker")}
	step.Decision = &work.RequestedChanges{At: at(24)}
	return s
}
func completedReviewerState(t *testing.T) work.State {
	t.Helper()
	s := reviewingState(t)
	p := s.Workflows[0].Phases[2].(*work.ReviewPhase)
	p.Steps = p.Steps[:2]
	return s
}
func retryReviewState(t *testing.T) work.State {
	t.Helper()
	s := reviewingState(t)
	p := s.Workflows[0].Phases[2].(*work.ReviewPhase)
	p.Steps = []work.ReviewStep{p.Steps[0], &work.AgentReviewStep{Slot: 1, Execution: execution(22, 23, 1)}, &work.AgentReviewStep{Slot: 1, Execution: execution(25, 26, 0), Report: &work.ReviewReport{}}, p.Steps[2]}
	return s
}
func resetState(t *testing.T) work.State {
	t.Helper()
	s := failedImplementation(t)
	s.Workflows = append(s.Workflows, freshWorkflow(t))
	return s
}
func closedState(t *testing.T) work.State {
	t.Helper()
	s := deliveredHistory(t)
	s.Workflows[0].Phases = append(s.Workflows[0].Phases,
		&work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: 9}},
		&work.FinalizationPhase{Acceptance: &work.PRAcceptance{ExternalReview: 10, ObservedAt: at(110), Evidence: "Provider confirmed the PR merged"}, Closure: &work.TaskClosure{At: at(120), Reason: "pr_merged", Evidence: "Source closed"}, Cleanup: []work.CleanupObservation{&work.WorktreeRetained{Path: "/fixture/work", At: at(121), Reason: "Dirty worktree", Evidence: "Cleanup refused"}}})
	return s
}
