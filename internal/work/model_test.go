package work_test

import (
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/require"
)

func TestStateDerivesPositionAndRejectsStaleChildResultsAcrossAdvanceAndReset(t *testing.T) {
	s := simpleState(t)
	w := &s.Workflows[0]
	effort := w.Phases[1].(*work.ImplementationPhase)
	effort.Execution.Process = &work.RunningProcess{}
	effort.Output = nil
	token := attempt(t, 0, 1, nil)
	require.NoError(t, s.Validate())
	require.NoError(t, s.CheckActiveAttempt(token))
	require.Equal(t, effort, w.LastPhase())

	effort.Execution.Process = &work.ExitedProcess{At: at(12), Code: exitValue(0)}
	effort.Output = code("1")
	require.NoError(t, s.Validate())
	require.ErrorContains(t, s.CheckActiveAttempt(token), "active last work")
	w.Phases = append(w.Phases, review(t, 1, 20))
	r := w.LastPhase().(*work.ReviewPhase)
	require.Empty(t, r.Steps)
	require.Error(t, s.CheckActiveAttempt(token), "an empty last review must not fall back to implementation")
	r.Steps = []work.ReviewStep{&work.CheckStep{Slot: 0}}
	zero := 0
	current := attempt(t, 0, 2, &zero)
	require.NoError(t, s.CheckActiveAttempt(current), "explicit zero is a real step address")
	require.Error(t, s.CheckActiveAttempt(attempt(t, 0, 2, nil)), "nil must not default to review step zero")
	for _, invalid := range []int{-1, 1} {
		require.Error(t, s.CheckActiveAttempt(attempt(t, 0, 2, &invalid)), "an optional index still requires a valid position when present")
	}

	r.Steps[0].(*work.CheckStep).Result = &work.CheckResult{ExitCode: exitValue(0), FinishedAt: at(21)}
	r.Steps = append(r.Steps, &work.AgentReviewStep{Slot: 1, Execution: execution(22, 23, 0)})
	agent := r.Steps[1].(*work.AgentReviewStep)
	agent.Execution.Process = &work.RunningProcess{}
	one := 1
	current = attempt(t, 0, 2, &one)
	require.NoError(t, s.CheckActiveAttempt(current))
	agent.Execution.Process = &work.InterruptedProcess{At: at(23), Reason: "supervisor lost", Trigger: "resume"}
	require.Error(t, s.CheckActiveAttempt(current))
	r.Steps = append(r.Steps, &work.AgentReviewStep{Slot: 1, Execution: execution(24, 25, 0)})
	retry := r.Steps[2].(*work.AgentReviewStep)
	retry.Execution.Process = &work.RunningProcess{}
	two := 2
	require.Error(t, s.CheckActiveAttempt(current))
	require.NoError(t, s.CheckActiveAttempt(attempt(t, 0, 2, &two)))
	retry.Execution.Process = &work.ExitedProcess{At: at(25), Code: exitValue(0)}
	retry.Report = &work.ReviewReport{}
	r.Steps = append(r.Steps, &work.ManualReviewStep{Slot: 2})
	three := 3
	require.NoError(t, s.CheckActiveAttempt(attempt(t, 0, 2, &three)))
	r.Steps[3].(*work.ManualReviewStep).Decision = &work.ApprovedReview{At: at(26)}
	require.Error(t, s.CheckActiveAttempt(attempt(t, 0, 2, &three)))
	outcome, err := w.ReviewOutcome(work.ReviewRef{Phase: 2})
	require.NoError(t, err)
	require.Equal(t, work.ReviewPassed, outcome)

	s.Workflows = append(s.Workflows, freshWorkflow(t))
	require.NoError(t, s.Validate())
	require.Same(t, &s.Workflows[1], s.ActiveWorkflow())
	require.Nil(t, s.ActiveWorkflow().LastPhase())
	require.Error(t, s.CheckActiveAttempt(token))
	newWorkflow := &s.Workflows[1]
	newWorkflow.Phases = []work.Phase{preparation("B2"), implementation(t, "new feature", "N", 40)}
	newWorkflow.Phases[1].(*work.ImplementationPhase).Execution.Process = &work.RunningProcess{}
	newWorkflow.Phases[1].(*work.ImplementationPhase).Output = nil
	require.NoError(t, s.Validate())
	require.Error(t, s.CheckActiveAttempt(token), "same phase index cannot hide a different workflow owner")
	require.NoError(t, s.CheckActiveAttempt(attempt(t, 1, 1, nil)))
	wrongTask := attempt(t, 1, 1, nil)
	wrongTask.TaskID = "another-task"
	require.Error(t, s.CheckActiveAttempt(wrongTask))
	require.Error(t, s.CheckActiveAttempt(attempt(t, -1, 1, nil)), "a negative workflow cannot identify workflow zero")
	require.Error(t, s.CheckActiveAttempt(attempt(t, 1, -1, nil)), "a negative phase cannot identify phase zero")

	newEffort := newWorkflow.Phases[1].(*work.ImplementationPhase)
	newEffort.Execution.Process = &work.StartFailedProcess{At: at(42), Reason: "agent executable unavailable"}
	orphaned := newEffort.Completion
	newEffort.Execution.Session, newEffort.Completion = nil, nil
	require.NoError(t, s.Validate())
	require.Error(t, s.CheckActiveAttempt(attempt(t, 1, 1, nil)))
	require.Nil(t, newWorkflow.FeatureCompletion())
	_, captured := newWorkflow.LatestOutput()
	require.False(t, captured)
	newEffort.Completion = orphaned
	require.ErrorContains(t, s.Validate(), "start-failed execution cannot own")
	newEffort.Completion = nil
	resume := implementation(t, "Cannot resume a child that never started", "unused", 50)
	resume.Execution.Process, resume.Completion, resume.Output = &work.RunningProcess{}, nil, nil
	resume.Execution.Launch = &work.ResumedExecution{From: work.ExecutionRef{Phase: 1}}
	newWorkflow.Phases = append(newWorkflow.Phases, resume)
	require.ErrorContains(t, s.Validate(), "successful source and native session")

	gates := []struct {
		name string
		step func(*testing.T) work.ReviewStep
	}{
		{"pending check", func(t *testing.T) work.ReviewStep {
			t.Helper()
			return &work.CheckStep{Slot: 0}
		}},
		{"unanswered manual assessment", func(t *testing.T) work.ReviewStep {
			t.Helper()
			return &work.ManualReviewStep{Slot: 2}
		}},
	}
	for _, gate := range gates {
		t.Run(gate.name, func(t *testing.T) {
			pending := func() work.State {
				state := simpleState(t)
				review := review(t, 1, 20)
				review.Steps = []work.ReviewStep{gate.step(t)}
				state.Workflows[0].Phases = append(state.Workflows[0].Phases, review)
				return state
			}
			before := pending()
			token := attempt(t, 0, 2, indexPointer(0))
			require.NoError(t, before.CheckActiveAttempt(token))

			afterStep := pending()
			review := afterStep.Workflows[0].Phases[2].(*work.ReviewPhase)
			review.Steps = append(review.Steps, &work.CheckStep{Slot: 0, Result: &work.CheckResult{ExitCode: exitValue(0), FinishedAt: at(25)}})
			require.ErrorContains(t, afterStep.Validate(), "last review attempt")
			require.Error(t, afterStep.ValidateSuccessor(&before), "advancing a slot must not strand its unanswered gate")

			afterPhase := pending()
			afterPhase.Workflows[0].Phases = append(afterPhase.Workflows[0].Phases, implementation(t, "Later work cannot bypass the gate", "2", 30))
			require.ErrorContains(t, afterPhase.Validate(), "last review attempt")
			require.Error(t, afterPhase.ValidateSuccessor(&before), "advancing a phase must not strand its unanswered gate")
			require.NoError(t, before.CheckActiveAttempt(token), "refused proposals leave the original gate answerable")

			afterReset := pending()
			afterReset.Workflows = append(afterReset.Workflows, freshWorkflow(t))
			require.NoError(t, afterReset.ValidateSuccessor(&before), "reset retains the old workflow's pending tail as history")
			require.Error(t, afterReset.CheckActiveAttempt(token), "a superseded gate cannot receive results")
			require.Len(t, afterReset.Workflows[0].Phases[2].(*work.ReviewPhase).Steps, 1)
		})
	}
}

func TestStateRetainsRepairOutputAndPublicationProvenanceWithoutRegistries(t *testing.T) {
	s := repairHistory(t)
	w := &s.Workflows[0]
	require.NoError(t, s.Validate())
	local, ok := w.LatestOutput()
	require.True(t, ok)
	require.Equal(t, 7, local.Phase)
	output, err := w.Output(local)
	require.NoError(t, err)
	require.Equal(t, "C3", output.Commit)
	require.Equal(t, "B", w.Context().Baseline.Commit)
	require.Equal(t, "D", w.Phases[8].(*work.ReviewPhase).Scope.Destination.Commit)
	require.Equal(t, 3, w.Phases[8].(*work.ReviewPhase).Scope.DeltaFrom.Phase)
	published := w.Phases[5].(*work.PublicationPhase)
	publishedReview := w.Phases[4].(*work.ReviewPhase)
	publishedOutput, err := w.Output(publishedReview.Scope.Output)
	require.NoError(t, err)
	require.Equal(t, "C2", publishedOutput.Commit)
	require.Equal(t, "P1", published.Delivery.(*work.PullRequestDelivery).Head)
	require.Equal(t, "feat: original feature", w.FeatureCompletion().Summary)
	require.Equal(t, "fix: repair retained local finding", w.Phases[7].(*work.ImplementationPhase).Completion.Summary)
	localFinding := localFindingRef(t)
	followup := followupFindingRef(t)
	require.Equal(t, []int{3}, w.RepairPhases(localFinding))
	require.Equal(t, []int{7}, w.RepairPhases(followup))
	require.Equal(t, work.FindingResolved, w.LatestLocalVerification(followup).Resolution)
	require.Empty(t, w.Phases[6].(*work.ExternalReviewPhase).BranchSyncs, "waiting for PR merge does not fabricate synchronization work")
	require.NoError(t, s.AuthorizePublication(0, work.PublicationRef{Phase: 9}, at(100), "publish this exact update"))
	require.NoError(t, s.Validate(), "local repair verification does not claim the PR branch already contains that repair")
}

func TestStateRejectsInvalidStructuralOwnershipAndReferences(t *testing.T) {
	cases := []struct {
		name   string
		change func(*work.State)
		want   string
	}{
		{"negative review index", func(s *work.State) {
			s.Workflows[0].Phases[9].(*work.PublicationPhase).Plan.Review.Phase = -1
		}, "earlier phase"},
		{"wrong phase kind", func(s *work.State) { s.Workflows[0].Phases[9].(*work.PublicationPhase).Plan.Review.Phase = 7 }, "requires review"},
		{"out of range review", func(s *work.State) { s.Workflows[0].Phases[9].(*work.PublicationPhase).Plan.Review.Phase = 99 }, "earlier phase"},
		{"future repair target", func(s *work.State) {
			s.Workflows[0].Phases[3].(*work.ImplementationPhase).TargetFindings = []work.FindingRef{followupFindingRef(t)}
		}, "earlier phase"},
		{"target is not a reporting phase", func(s *work.State) {
			s.Workflows[0].Phases[7].(*work.ImplementationPhase).TargetFindings[0].Phase = 5
		}, "requires review"},
		{"target has negative finding index", func(s *work.State) {
			s.Workflows[0].Phases[7].(*work.ImplementationPhase).TargetFindings[0].Finding = -1
		}, "out-of-range index"},
		{"target has out-of-range finding index", func(s *work.State) {
			s.Workflows[0].Phases[7].(*work.ImplementationPhase).TargetFindings[0].Finding = 99
		}, "out-of-range index"},
		{"duplicate targets", func(s *work.State) {
			p := s.Workflows[0].Phases[7].(*work.ImplementationPhase)
			p.TargetFindings = append(p.TargetFindings, p.TargetFindings[0])
		}, "duplicate repair target"},
		{"running earlier workflow", func(s *work.State) {
			s.Workflows[0].Phases[7].(*work.ImplementationPhase).Execution.Process = &work.RunningProcess{}
		}, "active last effort"},
		{"unretained code", func(s *work.State) {
			s.Workflows[0].Phases[7].(*work.ImplementationPhase).Output.(*work.CodeOutput).RetainedRef = "C3"
		}, "owned retention ref"},
		{"resumed source points to a manual step", func(s *work.State) {
			p := s.Workflows[0].Phases[7].(*work.ImplementationPhase)
			p.Execution.Launch.(*work.ResumedExecution).From = work.ExecutionRef{Phase: 4, Step: indexPointer(2)}
		}, "agent review"},
		{"resumed future source", func(s *work.State) {
			s.Workflows[0].Phases[7].(*work.ImplementationPhase).Execution.Launch.(*work.ResumedExecution).From.Phase = 7
		}, "earlier source"},
		{"mismatched session", func(s *work.State) {
			s.Workflows[0].Phases[7].(*work.ImplementationPhase).Execution.Session.ID = "foreign-session"
		}, "native session must match"},
		{"unknown usage without reason", func(s *work.State) {
			s.Workflows[0].Phases[7].(*work.ImplementationPhase).Execution.UsageCapture.Reason = ""
		}, "requires a reason"},
		{"negative cost", func(s *work.State) {
			s.Workflows[0].Phases[7].(*work.ImplementationPhase).Execution.Cost = &work.UsageCost{AmountMicroUSD: -1}
		}, "cost cannot be negative"},
		{"review borrows a process started before its scope", func(s *work.State) {
			s.Workflows[0].Phases[8].(*work.ReviewPhase).Steps[2].(*work.AgentReviewStep).Execution.StartedAt = at(10)
		}, "predates review entry"},
		{"wrong verification scope", func(s *work.State) {
			p := s.Workflows[0].Phases[8].(*work.ReviewPhase)
			p.Steps[2].(*work.AgentReviewStep).Report.Verifications[0].Finding = localFindingRef(t)
		}, "source or targeted repaired output"},
		{"nil typed phase", func(s *work.State) { s.Workflows[0].Phases[9] = (*work.PublicationPhase)(nil) }, "publication is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := repairHistory(t)
			tc.change(&s)
			require.ErrorContains(t, s.Validate(), tc.want)
		})
	}
	s := repairHistory(t)
	require.NoError(t, s.Validate())
	_, err := s.Workflows[0].Finding(work.FindingRef{Phase: 6, Step: 0, Finding: 0})
	require.Error(t, err, "branch maintenance cannot be addressed as a task-review finding")
}

func at(minute int) time.Time  { return time.Date(2026, time.January, 1, 0, minute, 0, 0, time.UTC) }
func exitValue(value int) *int { return &value }
func indexPointer(n int) *int  { return &n }
func outputRef(t *testing.T, n int) *work.OutputRef {
	t.Helper()
	return &work.OutputRef{Phase: n}
}
func attempt(t *testing.T, workflow, phase int, step *int) work.AttemptRef {
	t.Helper()
	return work.AttemptRef{RepositoryID: "repo", TaskID: "task", Workflow: workflow, Phase: phase, Step: step}
}
func preparation(baseline string) *work.PreparationPhase {
	observed := at(0)
	return &work.PreparationPhase{EstablishedAt: &observed, Context: &work.WorkContext{Directory: "/fixture/work", TaskBranch: "task-branch", CheckpointRef: "refs/owned/checkpoint", Baseline: work.CodeIdentity{Commit: baseline, Tree: "tree-" + baseline}}}
}
func freshWorkflow(t *testing.T) work.Workflow {
	t.Helper()
	return work.Workflow{ReviewContracts: []work.ReviewContract{{PipelineKey: "custom", Slots: []work.ReviewSlot{{Kind: work.ReviewCheck, ConfigKey: "tests"}, {Kind: work.ReviewAgent, ConfigKey: "security-review"}, {Kind: work.ReviewManual, ConfigKey: "operator"}}}}}
}
func execution(start, end, exit int) work.AgentExecution {
	return work.AgentExecution{Profile: "implementer", Harness: "pi", Model: "example-model", StartedAt: at(start), Process: &work.ExitedProcess{At: at(end), Code: exitValue(exit)}, Session: &work.Session{ID: "native-session", LogPath: "/fixture/session.jsonl"}}
}
func code(n string) *work.CodeOutput {
	return &work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: "C" + n, Tree: "T" + n}, RetainedRef: "refs/owned/C" + n}
}
func implementation(t *testing.T, summary, output string, start int) *work.ImplementationPhase {
	t.Helper()
	return &work.ImplementationPhase{Execution: execution(start, start+2, 0), Completion: &work.Completion{Summary: summary, Description: "Owned effort rationale", DetailedDescription: "Detailed handoff for " + summary, TechnicalExplanation: "Technical explanation", CompletedAt: at(start + 1)}, Output: code(output)}
}
func review(t *testing.T, output, start int) *work.ReviewPhase {
	t.Helper()
	return &work.ReviewPhase{Contract: 0, StartedAt: at(start), Scope: work.ReviewScope{Requirements: "Task requirements", Output: work.OutputRef{Phase: output}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "D"}}}
}
func passedReview(t *testing.T, output, start int) *work.ReviewPhase {
	t.Helper()
	p := review(t, output, start)
	p.Steps = []work.ReviewStep{&work.CheckStep{Slot: 0, Result: &work.CheckResult{ExitCode: exitValue(0), FinishedAt: at(start + 1)}}, &work.AgentReviewStep{Slot: 1, Execution: execution(start+2, start+3, 0), Report: &work.ReviewReport{}}, &work.ManualReviewStep{Slot: 2, Decision: &work.ApprovedReview{At: at(start + 4)}}}
	return p
}
func simpleState(t *testing.T) work.State {
	t.Helper()
	w := freshWorkflow(t)
	w.Phases = []work.Phase{preparation("B"), implementation(t, "feat: original feature", "1", 10)}
	return work.State{Version: 17, RepositoryID: "repo", TaskID: "task", Workflows: []work.Workflow{w}}
}
func localFindingRef(t *testing.T) work.FindingRef {
	t.Helper()
	return work.FindingRef{Phase: 2, Step: 1, Finding: 0}
}
func followupFindingRef(t *testing.T) work.FindingRef {
	t.Helper()
	return work.FindingRef{Phase: 4, Step: 1, Finding: 0}
}
func blocker(title string) work.Finding {
	return work.Finding{Title: title, Classification: work.FindingBlocking, Description: "Issue details", Dispositions: []work.FindingDisposition{&work.RepairRequired{}}}
}
func nativePR() work.PullRequestRef {
	return work.PullRequestRef{URL: "https://github.example/owner/repo/pull/123"}
}

func repairHistory(t *testing.T) work.State {
	t.Helper()
	s := simpleState(t)
	w := &s.Workflows[0]
	first := review(t, 1, 20)
	first.Steps = []work.ReviewStep{&work.CheckStep{Slot: 0, Result: &work.CheckResult{ExitCode: exitValue(0), FinishedAt: at(21)}}, &work.AgentReviewStep{Slot: 1, Execution: execution(22, 23, 0), Report: &work.ReviewReport{Findings: []work.Finding{blocker("Local issue")}}}}
	w.Phases = append(w.Phases, first)
	local := implementation(t, "fix: repair local finding", "2", 30)
	local.Input, local.TargetFindings = outputRef(t, 1), []work.FindingRef{localFindingRef(t)}
	w.Phases = append(w.Phases, local)
	second := passedReview(t, 3, 40)
	second.Scope.DeltaFrom = outputRef(t, 1)
	second.Steps[1].(*work.AgentReviewStep).Report.Findings = []work.Finding{blocker("Follow-up local issue")}
	second.Steps[1].(*work.AgentReviewStep).Report.Findings[0].Dispositions = append(second.Steps[1].(*work.AgentReviewStep).Report.Findings[0].Dispositions,
		&work.WaivedFinding{Waiver: work.Waiver{Reason: "Accepted for the initial delivery", At: at(45)}})
	second.Steps[1].(*work.AgentReviewStep).Report.Verifications = []work.FindingVerification{{Finding: localFindingRef(t), Resolution: work.FindingResolved, Evidence: "Local issue verified"}}
	w.Phases = append(w.Phases, second, &work.PublicationPhase{Plan: work.PublicationPlan{Review: work.ReviewRef{Phase: 4}, Target: &work.PullRequestTarget{}, Content: "Original feature publication narrative"}})
	require.NoError(t, s.AuthorizePublication(0, work.PublicationRef{Phase: 5}, at(50), "Operator authorizes this plan"))
	w.Phases[5].(*work.PublicationPhase).Delivery = &work.PullRequestDelivery{PullRequest: nativePR(), PublicCommit: "P1", Head: "P1", Tree: "T2", ObservedAt: at(51), Evidence: "Verified PR delivery"}
	w.Phases = append(w.Phases, &work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: 5}})
	second.Steps[1].(*work.AgentReviewStep).Report.Findings[0].Dispositions = append(second.Steps[1].(*work.AgentReviewStep).Report.Findings[0].Dispositions, &work.RepairRequired{At: at(65)})
	followup := implementation(t, "fix: repair retained local finding", "3", 70)
	followup.Input, followup.TargetFindings = outputRef(t, 3), []work.FindingRef{followupFindingRef(t)}
	followup.Execution.Launch = &work.ResumedExecution{From: work.ExecutionRef{Phase: 3}, UsageBaseline: &work.Usage{InputTokens: 100}}
	followup.Execution.UsageCapture = work.UsageCapture{Status: work.UsageUnknown, Reason: "resumed counters unavailable"}
	w.Phases = append(w.Phases, followup)
	third := passedReview(t, 7, 80)
	third.Scope.DeltaFrom = outputRef(t, 3)
	failed := &work.AgentReviewStep{Slot: 1, Execution: execution(82, 83, 1)}
	selected := third.Steps[1].(*work.AgentReviewStep)
	selected.Execution = execution(84, 85, 0)
	selected.Report.Verifications = []work.FindingVerification{{Finding: followupFindingRef(t), Resolution: work.FindingResolved, Evidence: "Retained local issue verified"}}
	third.Steps = []work.ReviewStep{third.Steps[0], failed, selected, &work.ManualReviewStep{Slot: 2, Decision: &work.ApprovedReview{At: at(86)}}}
	pr := nativePR()
	w.Phases = append(w.Phases, third, &work.PublicationPhase{Plan: work.PublicationPlan{Review: work.ReviewRef{Phase: 8}, Target: &work.PullRequestTarget{Existing: &pr, ExpectedHead: "P1"}, Content: "Original feature narrative with repair context"}})
	return s
}
