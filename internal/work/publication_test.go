package work_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/require"
)

func TestPublicationAuthorizationIsBoundToExactIntentAndOwnerAcrossRetryAndReset(t *testing.T) {
	s := repairHistory(t)
	p := s.Workflows[0].Phases[9].(*work.PublicationPhase)
	ref := work.PublicationRef{Phase: 9}
	require.Nil(t, p.Consent)
	require.NoError(t, s.AuthorizePublication(0, ref, at(100), "Explicitly authorize R3 update"))
	require.Equal(t, at(100), p.Consent.GrantedAt)
	require.Equal(t, "Explicitly authorize R3 update", p.Consent.Evidence)
	first := *p.Consent
	require.NoError(t, s.AuthorizePublication(0, ref, at(101), "Repeated request"))
	require.Equal(t, first, *p.Consent, "unchanged retry retains the first exact-plan grant")
	p.Delivery = updateDelivery()
	require.NoError(t, s.Validate(), "a verified recovered/imported PR needs no invented Operation")
	p.Plan.Content = "Different publication body"
	require.ErrorContains(t, s.Validate(), "exact intent and owner")
	require.Error(t, s.AuthorizePublication(0, ref, at(103), "Cannot silently replace old intent"))

	copied := repairHistory(t)
	other := repairHistory(t)
	copied.Workflows = append(copied.Workflows, other.Workflows[0])
	require.ErrorContains(t, copied.Validate(), "exact intent and owner", "identical indexes/content in another workflow cannot inherit consent")
	fresh := repairHistory(t)
	require.Error(t, fresh.AuthorizePublication(-1, ref, at(100), "Negative workflow is not workflow zero"))
	require.Error(t, fresh.AuthorizePublication(0, work.PublicationRef{Phase: 5}, at(100), "Historical publication"))
	require.Error(t, fresh.AuthorizePublication(0, ref, at(80), "Too early"))
	require.Nil(t, fresh.Workflows[0].Phases[9].(*work.PublicationPhase).Consent)

	t.Run("referenced finding must be waived at the grant", func(t *testing.T) {
		state := blockingReviewerState(t)
		w := &state.Workflows[0]
		review := passedReview(t, 1, 40)
		review.Steps = review.Steps[:2]
		review.Steps[1].(*work.AgentReviewStep).Report.Verifications = []work.FindingVerification{{Finding: localFindingRef(t), Resolution: work.FindingUnresolved, Evidence: "The reported issue remains on this output"}}
		publication := &work.PublicationPhase{Plan: work.PublicationPlan{Review: work.ReviewRef{Phase: 3}, Target: &work.PullRequestTarget{}, Content: "Publish with an explicit finding exception"}}
		w.Phases = append(w.Phases, review, publication)
		finding, err := w.Finding(localFindingRef(t))
		require.NoError(t, err)
		finding.Dispositions = append(finding.Dispositions,
			&work.WaivedFinding{Waiver: work.Waiver{Reason: "Earlier exception", At: at(30)}},
			&work.RepairRequired{At: at(35)},
			&work.WaivedFinding{Waiver: work.Waiver{Reason: "Operator accepts the unresolved issue", At: at(50)}})
		require.NoError(t, state.Validate())
		outcome, err := w.ReviewOutcome(work.ReviewRef{Phase: 3})
		require.NoError(t, err)
		require.Equal(t, work.ReviewPassedWithWaivers, outcome)

		err = state.AuthorizePublication(0, work.PublicationRef{Phase: 4}, at(45), "Grant before the required exception")
		require.Error(t, err, "the passing reviewer process cannot borrow a future finding waiver")
		require.Nil(t, publication.Consent, "a premature grant must not mint consent")
		require.Len(t, finding.Dispositions, 4)
		require.NoError(t, state.AuthorizePublication(0, work.PublicationRef{Phase: 4}, at(50), "Grant after accepting the issue"))
		grant := *publication.Consent
		finding.Dispositions = append(finding.Dispositions, &work.DismissedFinding{Waiver: work.Waiver{Reason: "Later evidence supports dismissal", At: at(60)}})
		require.NoError(t, state.Validate(), "later referenced decisions must not reinterpret an established grant")
		require.Equal(t, grant, *publication.Consent)
	})
}

func TestPublicationRequiresConsentAndIndependentMatchingRemoteEvidence(t *testing.T) {
	cases := []struct {
		name   string
		change func(*work.State, *work.PublicationPhase)
		want   string
	}{
		{"no consent", func(_ *work.State, p *work.PublicationPhase) { p.Consent = nil }, "explicit exact-plan consent"},
		{"unbound grant", func(_ *work.State, p *work.PublicationPhase) { p.Consent.IntentDigest = [32]byte{} }, "exact intent and owner"},
		{"changed reviewed scope", func(s *work.State, _ *work.PublicationPhase) {
			s.Workflows[0].Phases[8].(*work.ReviewPhase).Scope.Requirements = "Changed requirements"
		}, "exact intent and owner"},
		{"changed inherited baseline", func(s *work.State, _ *work.PublicationPhase) {
			s.Workflows[0].Phases[0].(*work.PreparationPhase).Context.Baseline.Commit = "different-baseline"
		}, "exact intent and owner"},
		{"failed latest reviewer", func(s *work.State, _ *work.PublicationPhase) {
			*s.Workflows[0].Phases[8].(*work.ReviewPhase).Steps[2].(*work.AgentReviewStep).Execution.Process.(*work.ExitedProcess).Code = 1
		}, "passed exact review"},
		{"packaging changed tree", func(_ *work.State, p *work.PublicationPhase) {
			p.Delivery.(*work.PullRequestDelivery).Tree = "unreviewed-tree"
		}, "exact reviewed output"},
		{"wrong observed head", func(_ *work.State, p *work.PublicationPhase) { p.Delivery.(*work.PullRequestDelivery).Head = "P1" }, "exact reviewed output"},
		{"wrong previous head", func(_ *work.State, p *work.PublicationPhase) {
			p.Delivery.(*work.PullRequestDelivery).PreviousHead = "foreign-head"
		}, "expected previous public head"},
		{"different native PR", func(_ *work.State, p *work.PublicationPhase) {
			p.Delivery.(*work.PullRequestDelivery).PullRequest.URL = "https://github.example/owner/repo/pull/999"
		}, "intended target"},
		{"local result without observation", func(_ *work.State, p *work.PublicationPhase) { p.Delivery.(*work.PullRequestDelivery).Evidence = "" }, "remote delivery evidence"},
		{"wrong delivery flow", func(_ *work.State, p *work.PublicationPhase) {
			p.Delivery = &work.DirectDelivery{PublicCommit: "P2", Destination: work.CodeIdentity{Commit: "M2", Tree: "T3"}, ObservedAt: at(101), Evidence: "Destination"}
		}, "planned flow"},
		{"known merged PR", func(s *work.State, _ *work.PublicationPhase) {
			pr := nativePR()
			s.Workflows[0].Phases = append(s.Workflows[0].Phases,
				&work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: 9}},
				&work.FinalizationPhase{Acceptance: &work.PRAcceptance{ExternalReview: 10, ObservedAt: at(110), Evidence: "Provider confirmed merge"}},
				&work.PublicationPhase{Plan: work.PublicationPlan{Review: work.ReviewRef{Phase: 8}, Target: &work.PullRequestTarget{Existing: &pr, ExpectedHead: "P2"}, Content: "Cannot update a merged PR"}})
		}, "requires reconciliation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := repairHistory(t)
			require.NoError(t, s.AuthorizePublication(0, work.PublicationRef{Phase: 9}, at(100), "Operator authorizes update"))
			p := s.Workflows[0].Phases[9].(*work.PublicationPhase)
			p.Delivery = updateDelivery()
			tc.change(&s, p)
			require.ErrorContains(t, s.Validate(), tc.want)
		})
	}
}

func TestFinalizationRequiresMergeConfirmationAndPreservesSeparatelyRetryableCleanup(t *testing.T) {
	s := deliveredHistory(t)
	w := &s.Workflows[0]
	external := &work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: 5}}
	merged := &work.PRAcceptance{ExternalReview: 10, ObservedAt: at(110), Evidence: "Provider confirmed the PR merged"}
	w.Phases = append(w.Phases, external, &work.FinalizationPhase{Acceptance: merged})
	require.ErrorContains(t, s.Validate(), "superseded", "finalization must address the current published PR stage")
	external.Publication.Phase = 9
	merged.ObservedAt = at(100)
	require.ErrorContains(t, s.Validate(), "merge confirmation predates delivery")
	merged.ObservedAt, merged.Evidence = at(110), ""
	require.ErrorContains(t, s.Validate(), "PR merge confirmation")
	merged.Evidence = "Provider confirmed the PR merged"
	require.NoError(t, s.Validate(), "the current provider confirms merge without supplying head or merge-commit metadata")
	f := w.Phases[11].(*work.FinalizationPhase)
	f.Closure = &work.TaskClosure{At: at(120), Reason: "pr_merged", Evidence: "Task source closed"}
	f.Cleanup = []work.CleanupObservation{&work.WorktreeRetained{Path: "/fixture/work", At: at(121), Reason: "Uncommitted work remains", Evidence: "Non-forced cleanup refused"}}
	require.NoError(t, s.Validate())
	require.Nil(t, s.ActiveWorkflow(), "closure, not a second status flag, ends active work")
	f.Cleanup = append(f.Cleanup, &work.WorktreeRemoved{Path: "/fixture/another-task", At: at(122), Evidence: "Removed"})
	require.ErrorContains(t, s.Validate(), "exact prepared work directory")
	f.Cleanup[1].(*work.WorktreeRemoved).Path = "/fixture/work"
	require.NoError(t, s.Validate())
	require.Equal(t, "P2", w.Phases[9].(*work.PublicationPhase).Delivery.(*work.PullRequestDelivery).Head)
	require.Equal(t, work.FindingResolved, w.LatestLocalVerification(followupFindingRef(t)).Resolution)
	w.Phases[8].(*work.ReviewPhase).Steps[2].(*work.AgentReviewStep).Report.Verifications = nil
	require.Nil(t, w.LatestLocalVerification(followupFindingRef(t)), "PR merge does not fabricate per-finding local verification")
	f.Acceptance = nil
	require.ErrorContains(t, s.Validate(), "explicit delivery acceptance is required", "recorded source closure cannot replace delivery acceptance")
}

func TestDirectFinalizationNeedsObservedDestinationPushNotJustLocalMerge(t *testing.T) {
	s := reviewingState(t)
	p := &work.PublicationPhase{Plan: work.PublicationPlan{Review: work.ReviewRef{Phase: 2}, Target: &work.DirectMergeTarget{}, Content: "Direct destination delivery"}}
	s.Workflows[0].Phases = append(s.Workflows[0].Phases, p)
	require.NoError(t, s.AuthorizePublication(0, work.PublicationRef{Phase: 3}, at(30), "Authorize destination delivery"))
	s.Workflows[0].Phases = append(s.Workflows[0].Phases, &work.FinalizationPhase{Acceptance: &work.DirectAcceptance{Publication: work.PublicationRef{Phase: 3}}})
	require.ErrorContains(t, s.Validate(), "observed destination push")
	p.Delivery = &work.DirectDelivery{PublicCommit: "P3", Destination: work.CodeIdentity{Commit: "M3", Tree: "T1"}, ObservedAt: at(31), Evidence: "Verified destination ref after push"}
	require.NoError(t, s.Validate(), "direct publication reaches finalization without an external-review phase")
	s.Workflows[0].Phases[4].(*work.FinalizationPhase).Closure = &work.TaskClosure{At: at(40), Reason: "destination_published", Evidence: "Source closure"}
	require.NoError(t, s.Validate())
	p.Delivery.(*work.DirectDelivery).Destination.Tree = "different-tree"
	require.ErrorContains(t, s.Validate(), "preserving reviewed output")
}

func updateDelivery() *work.PullRequestDelivery {
	return &work.PullRequestDelivery{PullRequest: nativePR(), PublicCommit: "P2", Head: "P2", PreviousHead: "P1", Tree: "T3", ObservedAt: at(101), Evidence: "Verified recovered PR update"}
}
func deliveredHistory(t *testing.T) work.State {
	t.Helper()
	s := repairHistory(t)
	require.NoError(t, s.AuthorizePublication(0, work.PublicationRef{Phase: 9}, at(100), "Operator authorizes update"))
	s.Workflows[0].Phases[9].(*work.PublicationPhase).Delivery = updateDelivery()
	return s
}
