package work_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/require"
)

func TestReviewOutcomeRequiresTheWholeContractAndLatestOwnedEvidence(t *testing.T) {
	s := simpleState(t)
	w := &s.Workflows[0]
	r := review(t, 1, 20)
	w.Phases = append(w.Phases, r)
	ref := work.ReviewRef{Phase: 2}
	assertOutcome := func(want work.ReviewOutcome) {
		t.Helper()
		outcome, err := w.ReviewOutcome(ref)
		require.NoError(t, err)
		require.Equal(t, want, outcome)
		require.NoError(t, s.Validate())
	}
	assertOutcome(work.ReviewPending)
	r.Steps = []work.ReviewStep{&work.CheckStep{Slot: 0, Result: &work.CheckResult{ExitCode: exitValue(0), FinishedAt: at(21)}}}
	assertOutcome(work.ReviewPending)
	r.Steps = append(r.Steps, &work.AgentReviewStep{Slot: 1, Execution: execution(22, 23, 1)}, &work.ManualReviewStep{Slot: 2, Decision: &work.ApprovedReview{At: at(24)}})
	assertOutcome(work.ReviewFailed)
	r.Steps = append(r.Steps, &work.AgentReviewStep{Slot: 1, Execution: execution(25, 26, 0)})
	assertOutcome(work.ReviewPassed)
	agent := r.Steps[3].(*work.AgentReviewStep)
	require.Nil(t, agent.Report, "successful no-findings review must not invent a report")
	agent.Report = &work.ReviewReport{Findings: []work.Finding{blocker("Required change")}}
	assertOutcome(work.ReviewChangesRequired)
	agent.Report.Findings[0].Dispositions = append(agent.Report.Findings[0].Dispositions, &work.WaivedFinding{Waiver: work.Waiver{Reason: " ", At: at(27)}})
	_, err := w.ReviewOutcome(ref)
	require.ErrorContains(t, err, "nonblank reason")
	agent.Report.Findings[0].Dispositions[1] = &work.WaivedFinding{Waiver: work.Waiver{Reason: "Not applicable to this exact scope", At: at(27)}}
	assertOutcome(work.ReviewPassedWithWaivers)
	r.Steps = append(r.Steps, &work.AgentReviewStep{Slot: 1, Execution: execution(28, 29, 1)})
	assertOutcome(work.ReviewFailed)
	require.Equal(t, 1, *r.Steps[1].(*work.AgentReviewStep).Execution.Process.(*work.ExitedProcess).Code)
	r.Steps = append(r.Steps, &work.AgentReviewStep{Slot: 1, Execution: execution(30, 31, 0), Report: &work.ReviewReport{}})
	assertOutcome(work.ReviewPassed)
	require.Equal(t, 1, *r.Steps[4].(*work.AgentReviewStep).Execution.Process.(*work.ExitedProcess).Code, "a retry must not erase the failed attempt")
	require.Equal(t, "security-review", w.ReviewContracts[0].Slots[1].ConfigKey)
}

func TestReviewFailedCheckCanBeAcceptedThroughFindingWaiverNotManualApproval(t *testing.T) {
	s := simpleState(t)
	w := &s.Workflows[0]
	r := passedReview(t, 1, 20)
	w.Phases = append(w.Phases, r)
	ref := work.ReviewRef{Phase: 2}
	check := r.Steps[0].(*work.CheckStep)
	*check.Result.ExitCode = 3
	outcome, err := w.ReviewOutcome(ref)
	require.NoError(t, err)
	require.Equal(t, work.ReviewFailed, outcome)
	check.Findings = []work.Finding{blocker("Reported check failure")}
	outcome, err = w.ReviewOutcome(ref)
	require.NoError(t, err)
	require.Equal(t, work.ReviewFailed, outcome)
	waiver := work.Waiver{Reason: "Accepted the reported check finding", At: at(25)}
	check.Findings[0].Dispositions = append(check.Findings[0].Dispositions, &work.WaivedFinding{Waiver: waiver})
	outcome, err = w.ReviewOutcome(ref)
	require.NoError(t, err)
	require.Equal(t, work.ReviewPassedWithWaivers, outcome)
	finding, err := w.Finding(work.FindingRef{Phase: 2, Step: 0, Finding: 0})
	require.NoError(t, err)
	require.Equal(t, "Reported check failure", finding.Title)
	require.Equal(t, waiver, finding.Dispositions[1].(*work.WaivedFinding).Waiver)
	require.Equal(t, 3, *check.Result.ExitCode)
	check.Result.ExitCode = nil
	_, err = w.ReviewOutcome(ref)
	require.ErrorContains(t, err, "explicit exit code")
	check.Result.ExitCode = exitValue(0)
	require.ErrorContains(t, s.Validate(), "check findings require an observed failed check")
	check.Findings = nil
	r.Steps[2].(*work.ManualReviewStep).Decision = nil
	outcome, err = w.ReviewOutcome(ref)
	require.NoError(t, err)
	require.Equal(t, work.ReviewPending, outcome)
	r.Steps[2].(*work.ManualReviewStep).Decision = &work.AbortedReview{At: at(26), Reason: "operator stopped review"}
	outcome, err = w.ReviewOutcome(ref)
	require.NoError(t, err)
	require.Equal(t, work.ReviewPending, outcome)
	r.Contract = -1
	_, err = w.ReviewOutcome(ref)
	require.Error(t, err, "a negative contract must not select contract zero")
	r.Contract = 0
	r.Steps[0].(*work.CheckStep).Slot = 1
	_, err = w.ReviewOutcome(ref)
	require.ErrorContains(t, err, "kind disagrees")
	r.Steps[0].(*work.CheckStep).Slot = -1
	_, err = w.ReviewOutcome(ref)
	require.ErrorContains(t, err, "required-slot index")
	r.Steps[0].(*work.CheckStep).Slot = 0
	outcome, err = w.ReviewOutcome(ref)
	require.NoError(t, err, "contract and slot zero are ordinary required positions")
	require.Equal(t, work.ReviewPending, outcome)
}

func TestManualCommandInterruptionsRequireObservedExitAndAssessmentOrdering(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*work.ManualReviewStep)
		want   string
	}{
		{"missing native exit", func(step *work.ManualReviewStep) { step.CommandInterruptions[0].Code = nil }, "manual command requires explicit exit code"},
		{"interruption before review", func(step *work.ManualReviewStep) { step.CommandInterruptions[0].At = at(19) }, "manual command interruption predates review entry"},
		{"approval before interrupted exit", func(step *work.ManualReviewStep) { step.Decision = &work.ApprovedReview{At: at(23)} }, "manual assessment predates its command result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := interruptedManualState(t)
			require.NoError(t, snapshot.Validate())
			step := snapshot.Workflows[0].Phases[2].(*work.ReviewPhase).Steps[2].(*work.ManualReviewStep)

			tc.change(step)

			require.ErrorContains(t, snapshot.Validate(), tc.want)
		})
	}
}
