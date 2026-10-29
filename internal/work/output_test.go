package work_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/require"
)

func TestCaptureKeepsHandoffSeparateFromSafeCodeAndUnchangedOutput(t *testing.T) {
	s := simpleState(t)
	w := &s.Workflows[0]
	p := w.Phases[1].(*work.ImplementationPhase)
	p.Execution.Process = &work.RunningProcess{}
	p.Output = nil
	capture := work.AttemptRef{RepositoryID: s.RepositoryID, TaskID: s.TaskID, Workflow: 0, Phase: 1}
	require.NoError(t, s.Validate(), "a handoff can arrive before process exit")
	require.ErrorContains(t, s.CheckOutputCapture(capture), "successful safe execution boundary")
	p.Output = code("1")
	require.ErrorContains(t, s.Validate(), "safe execution boundary")
	_, err := w.Output(work.OutputRef{Phase: 1})
	require.Error(t, err, "unsafe output cannot become review authority through the resolver")
	p.Execution.Process = &work.ExitedProcess{At: at(12)}
	require.ErrorContains(t, s.Validate(), "explicit code")
	_, err = w.Output(work.OutputRef{Phase: 1})
	require.Error(t, err, "missing exit evidence must not become an implicit zero exit")
	p.Execution.Process = &work.ExitedProcess{Code: exitValue(0)}
	require.ErrorContains(t, s.Validate(), "observed finish time")
	p.Execution.Process = &work.ExitedProcess{At: at(12), Code: exitValue(1)}
	require.ErrorContains(t, s.Validate(), "safe execution boundary")
	p.Execution.Process = &work.ExitedProcess{At: at(12), Code: exitValue(0)}
	require.NoError(t, s.Validate())
	require.NoError(t, s.CheckOutputCapture(capture))
	captured, err := w.Output(work.OutputRef{Phase: 1})
	require.NoError(t, err)
	require.Equal(t, "C1", captured.Commit)
	require.Same(t, p.Output, captured)

	unchanged := implementation(t, "No change needed", "unused", 20)
	unchanged.Input = outputRef(t, 1)
	unchanged.Output = &work.ReusedOutput{Input: *outputRef(t, 1)}
	w.Phases = append(w.Phases, unchanged)
	require.NoError(t, s.Validate())
	require.ErrorContains(t, s.CheckOutputCapture(capture), "active last implementation", "an earlier successful effort cannot recapture after repair starts")
	capture.Phase = 2
	require.NoError(t, s.CheckOutputCapture(capture))
	foreign := capture
	foreign.TaskID = "another task"
	require.ErrorContains(t, s.CheckOutputCapture(foreign), "active last implementation")
	selected, ok := w.LatestOutput()
	require.True(t, ok)
	require.Equal(t, 2, selected.Phase)
	reused, err := w.Output(selected)
	require.NoError(t, err)
	require.Same(t, captured, reused, "unchanged output is the original owned value, not a copied producer")
	require.Equal(t, "feat: original feature", w.FeatureCompletion().Summary)
	require.Equal(t, "No change needed", unchanged.Completion.Summary)
	unchanged.Completion = nil
	require.ErrorContains(t, s.Validate(), "own completion")
	_, err = w.Output(selected)
	require.Error(t, err)
	unchanged.Completion = &work.Completion{Summary: "No change needed", Description: "Owned rationale", DetailedDescription: "No new output", CompletedAt: at(21)}
	unchanged.Execution.Process = &work.InterruptedProcess{At: at(22), Reason: "supervision disappeared"}
	require.ErrorContains(t, s.Validate(), "safe execution boundary")
	unchanged.Execution.Process = &work.ExitedProcess{At: at(22), Code: exitValue(0)}
	unchanged.Output.(*work.ReusedOutput).Input = *outputRef(t, 0)
	require.ErrorContains(t, s.Validate(), "exact input")
	unchanged.Input, unchanged.Output = outputRef(t, 2), &work.ReusedOutput{Input: *outputRef(t, 2)}
	require.ErrorContains(t, s.Validate(), "earlier input")
}

func TestReviewAndConsentCannotPredateSafeOutputCapture(t *testing.T) {
	cases := []struct {
		name  string
		reuse bool
	}{
		{name: "code"},
		{name: "unchanged input", reuse: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := simpleState(t)
			w := &s.Workflows[0]
			output, earlyEntry, safeEntry := 1, 1, 12
			if tc.reuse {
				effort := implementation(t, "Explicit no-change assessment", "unused", 20)
				effort.Input, effort.Output = outputRef(t, 1), &work.ReusedOutput{Input: *outputRef(t, 1)}
				w.Phases = append(w.Phases, effort)
				output, earlyEntry, safeEntry = 2, 15, 22
			}
			reviewIndex := len(w.Phases)
			w.Phases = append(w.Phases, passedReview(t, output, earlyEntry))
			publication := &work.PublicationPhase{Plan: work.PublicationPlan{Review: work.ReviewRef{Phase: reviewIndex}, Target: &work.PullRequestTarget{}, Content: "Publish reviewed work"}}
			publicationIndex := len(w.Phases)
			w.Phases = append(w.Phases, publication)

			require.ErrorContains(t, s.Validate(), "safe capture boundary")
			_, err := w.ReviewOutcome(work.ReviewRef{Phase: reviewIndex})
			require.ErrorContains(t, err, "safe capture boundary")
			err = s.AuthorizePublication(0, work.PublicationRef{Phase: publicationIndex}, at(earlyEntry+5), "Premature authorization")
			require.ErrorContains(t, err, "safe capture boundary")
			require.Nil(t, publication.Consent, "invalid historical approval must not mint consent")

			w.Phases[reviewIndex] = passedReview(t, output, safeEntry)
			require.NoError(t, s.Validate(), "review may begin at the observed safe boundary")
			outcome, err := w.ReviewOutcome(work.ReviewRef{Phase: reviewIndex})
			require.NoError(t, err)
			require.Equal(t, work.ReviewPassed, outcome)
			require.NoError(t, s.AuthorizePublication(0, work.PublicationRef{Phase: publicationIndex}, at(safeEntry+5), "Authorize after actual review"))
			require.NotNil(t, publication.Consent)
		})
	}
}

func TestCompletionAndReviewKeepRequiredAndOptionalTextDistinct(t *testing.T) {
	s := simpleState(t)
	w := &s.Workflows[0]
	completion := w.Phases[1].(*work.ImplementationPhase).Completion
	completion.TechnicalExplanation = ""
	require.NoError(t, s.Validate(), "optional technical explanation may be absent")
	for _, blank := range []string{"", " \n\t"} {
		completion.DetailedDescription = blank
		require.ErrorContains(t, s.Validate(), "completion detailed description requires nonblank text")
		_, err := w.Output(work.OutputRef{Phase: 1})
		require.Error(t, err, "empty required handoff text cannot authorize output capture")
	}
	completion.DetailedDescription = "\n# Retained completion\n"
	completion.TechnicalExplanation = " \n"
	require.ErrorContains(t, s.Validate(), "technical explanation requires nonblank text")
	completion.TechnicalExplanation = ""
	r := passedReview(t, 1, 20)
	w.Phases = append(w.Phases, r)
	report := r.Steps[1].(*work.AgentReviewStep).Report
	require.Empty(t, report.Evidence)
	require.NoError(t, s.Validate(), "a findings report need not supply an extra narrative")
	require.Equal(t, "\n# Retained completion\n", completion.DetailedDescription, "validation preserves the actual text rather than normalizing it")
	report.Evidence = " \n"
	require.ErrorContains(t, s.Validate(), "review report requires nonblank text")
	report.Evidence = "Reviewer narrative"
	r.Scope.Requirements = ""
	require.ErrorContains(t, s.Validate(), "review requirements requires nonblank text")
}
