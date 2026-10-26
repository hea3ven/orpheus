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
	assertOutcome(work.ReviewPending)
	agent := r.Steps[3].(*work.AgentReviewStep)
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

func TestReviewFailedCheckNeedsExplicitReasonedWaiverNotManualApproval(t *testing.T) {
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
	check.Waiver = &work.Waiver{Reason: "Explicitly accepted check limitation", At: at(25)}
	outcome, err = w.ReviewOutcome(ref)
	require.NoError(t, err)
	require.Equal(t, work.ReviewPassedWithWaivers, outcome)
	require.Equal(t, 3, *check.Result.ExitCode)
	check.Waiver.Reason = ""
	_, err = w.ReviewOutcome(ref)
	require.ErrorContains(t, err, "nonblank reason")
	check.Waiver.Reason, check.Waiver.At = "Reason provided too early", at(20)
	_, err = w.ReviewOutcome(ref)
	require.ErrorContains(t, err, "predates observed failed check")
	check.Waiver = nil
	check.Result.ExitCode = nil
	_, err = w.ReviewOutcome(ref)
	require.ErrorContains(t, err, "explicit exit code")
	check.Result.ExitCode = exitValue(0)
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

func TestLocalVerificationIsExplicitAndLaterAuthoritativeReversalWins(t *testing.T) {
	s := repairHistory(t)
	w := &s.Workflows[0]
	finding := followupFindingRef(t)
	require.Equal(t, work.FindingResolved, w.LatestLocalVerification(finding).Resolution)
	r := w.Phases[8].(*work.ReviewPhase)
	agent := r.Steps[2].(*work.AgentReviewStep)
	agent.Report.Verifications = nil
	require.Nil(t, w.LatestLocalVerification(finding), "a whole-task pass cannot fabricate a per-finding verification")
	outcome, err := w.ReviewOutcome(work.ReviewRef{Phase: 8})
	require.NoError(t, err)
	require.Equal(t, work.ReviewPassed, outcome)
	w.Phases = w.Phases[:9]
	agent.Report.Verifications = []work.FindingVerification{{Finding: finding, Resolution: work.FindingUnresolved, Evidence: "Explicit verification found the issue remains"}}
	outcome, err = w.ReviewOutcome(work.ReviewRef{Phase: 8})
	require.NoError(t, err)
	require.Equal(t, work.ReviewChangesRequired, outcome, "an explicitly unresolved blocker cannot hide behind successful process exit")
	require.NoError(t, s.Validate())
	require.Equal(t, work.FindingUnresolved, w.LatestLocalVerification(finding).Resolution)
	agent.Execution.Process = &work.ExitedProcess{At: at(85), Code: exitValue(1)}
	require.Nil(t, w.LatestLocalVerification(finding), "a failed process cannot supply authoritative local verification")

	history := repairHistory(t)
	owner := &history.Workflows[0]
	negative := passedReview(t, 7, 110)
	negative.Steps[1].(*work.AgentReviewStep).Report.Verifications = []work.FindingVerification{{Finding: finding, Resolution: work.FindingUnresolved, Evidence: "Later explicit negative assessment"}}
	owner.Phases = append(owner.Phases, negative)
	require.NoError(t, history.Validate())
	require.Equal(t, work.FindingUnresolved, owner.LatestLocalVerification(finding).Resolution, "a later negative must supersede an earlier positive, not select the first success")
	positive := passedReview(t, 7, 120)
	positive.Steps[1].(*work.AgentReviewStep).Report.Verifications = []work.FindingVerification{{Finding: finding, Resolution: work.FindingResolved, Evidence: "Fresh complete positive assessment"}}
	owner.Phases = append(owner.Phases, positive)
	require.NoError(t, history.Validate())
	require.Equal(t, work.FindingResolved, owner.LatestLocalVerification(finding).Resolution)
	*positive.Steps[1].(*work.AgentReviewStep).Execution.Process.(*work.ExitedProcess).Code = 1
	require.NoError(t, history.Validate())
	require.Equal(t, work.FindingUnresolved, owner.LatestLocalVerification(finding).Resolution, "a failed latest process cannot revive an older positive claim")
}
