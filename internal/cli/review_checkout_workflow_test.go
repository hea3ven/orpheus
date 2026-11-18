//go:build integration

package cli_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/cli"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowMutatingCheckOrManualCommandStopsBeforeDecisions(t *testing.T) {
	for _, kind := range []string{review.KindCheck, review.KindManual} {
		t.Run(kind, func(t *testing.T) {
			const movedCommit = "cccccccccccccccccccccccccccccccccccccccc"
			fixture := reviewFixtureWithOperatorCommit(t)
			if kind == review.KindManual {
				fixture.reviewers(semanticAgentOutcome{})
			} else {
				fixture.reviewers()
			}
			fixture.pipelines("standard", map[string][]map[string]any{"standard": {
				{"kind": kind, "name": "mutator", "command": "mutation"},
				{"kind": "agent_review", "name": "after-mutation-check"},
			}})
			calls := 0
			fixture.options.Dependencies.ReviewEffects.RunCommand = func(review.CommandOptions) (*int, error) {
				calls++
				if calls == 1 {
					fixture.candidate.contents = "command mutation"
					moveReviewFixtureHead(fixture, movedCommit)
				}
				code := 0
				if kind == review.KindCheck {
					code = 1
					return &code, checkExit(code)
				}
				return &code, nil
			}
			// Confirm the manual command (if any), then preserve the checkout.
			_, stderr, err := fixture.runError("\nq\n", "task", "run", "op-selected")
			require.ErrorContains(t, err, "changes preserved")
			assert.NotContains(t, stderr, "Decision for finding")
			assert.NotContains(t, stderr, "Review action")
			assert.Equal(t, 1, calls)
			assert.Zero(t, fixture.candidate.restorations)
			assert.Equal(t, "command mutation", fixture.candidate.contents)
			assert.Empty(t, fixture.agent.launches)
			phase := assertSelectedReviewStops(t, fixture)
			require.NotNil(t, phase.CheckoutRecovery)
			assert.Nil(t, phase.CheckoutRecovery.RestoredAt)
			switch step := phase.Steps[0].(type) {
			case *work.ManualReviewStep:
				assert.Equal(t, movedCommit, fixture.git.heads[taskWorkflowRepoRoot])
				assert.Nil(t, step.Decision)
				assert.Len(t, step.ReadOnlyFailures, 1, "the final safety boundary records the violation once")
				assert.Equal(t, 0, *step.CommandResult.(*work.ExitedProcess).Code)
			case *work.CheckStep:
				assert.Equal(t, movedCommit, fixture.git.heads[taskWorkflowRepoRoot])
				assert.Len(t, step.ReadOnlyFailures, 1, "an internal check must not duplicate the final safety receipt")
				assert.Equal(t, 1, *step.Result.ExitCode)
				require.Len(t, step.Findings, 1)
				assert.Equal(t, "Check \"mutator\" failed", step.Findings[0].Title)

				// The committed mutation must not be adopted as an operator fix.
				_, retryOutput, err := fixture.runError("q\n", "task", "run", "op-selected")
				require.ErrorContains(t, err, "changes preserved")
				assert.Contains(t, retryOutput, "restoration target refs/heads/orpheus/op-selected at "+selectedReviewCommit)
				assert.NotContains(t, retryOutput, "Selected review commit:")
				assert.Equal(t, phase, assertSelectedReviewStops(t, fixture))
				assert.Equal(t, movedCommit, fixture.git.heads[taskWorkflowRepoRoot])
				assert.Equal(t, 1, calls)
				assert.Zero(t, fixture.candidate.restorations)
			}
			if kind == review.KindManual {
				assertFreshManualReviewAfterRollback(t, fixture)
				assert.Equal(t, 2, calls)
			}
		})
	}
}

func assertFreshManualReviewAfterRollback(t *testing.T, f *reviewWorkflowFixture) {
	t.Helper()
	_, _, err := f.runError("r\n", "task", "run", "op-selected")
	require.ErrorContains(t, err, "review remains failed")
	restored := assertSelectedReviewStops(t, f)
	require.NotNil(t, restored.CheckoutRecovery.RestoredAt)
	assert.Equal(t, 1, f.candidate.restorations)
	assert.Equal(t, selectedReviewCommit, f.git.heads[taskWorkflowRepoRoot])
	assert.Equal(t, "reviewed\n", f.candidate.contents)

	f.run("\na\n", "task", "run", "op-selected")

	retried, _ := f.loadFinalTask("op-selected")
	require.Len(t, retried.Reviews, 2)
	assert.Equal(t, taskstate.ReviewStatusFailed, retried.Reviews[0].Status)
	assert.Equal(t, restored, retried.Work.ActiveWorkflow().Phases[retried.Reviews[0].Address.Phase])
	assert.Equal(t, taskstate.ReviewStatusPassed, retried.Reviews[1].Status)
	assert.Len(t, f.agent.launches, 1)
	assertUnconsentedPublication(t, retried)
	assert.Equal(t, selectedReviewCommit, retried.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase).Input.Code.Commit)
}

func TestIntegrationWorkflowSelectedRevisionStopsOnCommitMove(t *testing.T) {
	const movedCommit = "cccccccccccccccccccccccccccccccccccccccc"
	for _, tc := range []struct {
		name, input, finalHead string
		onEntry, interrupted   bool
	}{
		{name: "explicit rollback", input: "r\n", finalHead: selectedReviewCommit},
		{name: "quit", input: "q\n", finalHead: movedCommit},
		{name: "next step entry", finalHead: movedCommit, onEntry: true},
		{name: "next step after interruption", finalHead: movedCommit, onEntry: true, interrupted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := reviewFixtureWithOperatorCommit(t)
			f.pipelines("standard", map[string][]map[string]any{"standard": {
				{"kind": "check", "name": "first", "command": "first"},
				{"kind": "check", "name": "next", "command": "next"},
			}})
			f.options.Dependencies.ReviewEffects.RunCommand = func(opts review.CommandOptions) (*int, error) {
				f.checkCalls = append(f.checkCalls, opts.Step.Name)
				if !tc.onEntry {
					moveReviewFixtureHead(f, movedCommit)
				}
				return fixtureExitCode(0), nil
			}
			if tc.onEntry {
				f.options.Dependencies.ReviewEffects.CaptureCandidate = func(ctx context.Context, dir string, candidate review.CommittedCandidate, logger *slog.Logger, attrs ...slog.Attr) (review.CandidateCheck, error) {
					if f.candidate.snapshots == 1 {
						if tc.interrupted {
							// Model a hard stop, not a returned pipeline error that would fail the review.
							panic("interrupted before second step admission")
						}
						moveReviewFixtureHead(f, movedCommit)
					}
					return f.candidate.capture(ctx, dir, candidate, logger, attrs...)
				}
			}
			if tc.interrupted {
				require.PanicsWithValue(t, "interrupted before second step admission", func() {
					_, _, _ = f.runError("", "task", "run", "op-selected")
				})
				f.options.Dependencies.ReviewEffects.CaptureCandidate = f.candidate.capture
				paused, _ := f.loadFinalTask("op-selected")
				require.Len(t, paused.Reviews, 1)
				require.Equal(t, taskstate.ReviewStatusRunning, paused.Reviews[0].Status)
				require.Len(t, paused.Reviews[0].Steps, 1)
				require.Equal(t, fixtureExitCode(0), paused.Reviews[0].Steps[0].ExitCode)
				moveReviewFixtureHead(f, movedCommit)
			}

			_, stderr, err := f.runError(tc.input, "task", "run", "op-selected")

			assert.Equal(t, []string{"first"}, f.checkCalls)
			assert.Equal(t, tc.finalHead, f.git.heads[taskWorkflowRepoRoot])
			phase := assertSelectedReviewStops(t, f)
			if tc.interrupted {
				require.ErrorContains(t, err, "candidate is not at the reviewed commit")
				require.NotNil(t, phase.AdmissionFailure)
				assert.Nil(t, phase.CheckoutRecovery, "entry refusal must not offer destructive recovery")
				assert.Zero(t, f.candidate.restorations)

				moveReviewFixtureHead(f, selectedReviewCommit)
				f.run("", "task", "run", "op-selected")
				retried, _ := f.loadFinalTask("op-selected")
				require.Len(t, retried.Reviews, 2)
				assert.Equal(t, taskstate.ReviewStatusFailed, retried.Reviews[0].Status)
				assert.Len(t, retried.Reviews[0].Steps, 1)
				assert.Equal(t, taskstate.ReviewStatusPassed, retried.Reviews[1].Status)
				assert.Equal(t, []string{"first", "first", "next"}, f.checkCalls)
				return
			}
			if tc.onEntry {
				require.ErrorContains(t, err, "admit committed review revision")
				assert.NotNil(t, phase.AdmissionFailure)
				assert.Zero(t, f.candidate.restorations)
				return
			}
			require.ErrorContains(t, err, "changes preserved")
			assert.Contains(t, stderr, "restoration target refs/heads/orpheus/op-selected at "+selectedReviewCommit)
			require.NotNil(t, phase.CheckoutRecovery)
			if tc.finalHead == selectedReviewCommit {
				assert.NotNil(t, phase.CheckoutRecovery.RestoredAt)
				assert.Equal(t, 1, f.candidate.restorations)
			} else {
				assert.Nil(t, phase.CheckoutRecovery.RestoredAt)
				assert.Zero(t, f.candidate.restorations)
			}
		})
	}
}

func TestIntegrationWorkflowManualReviewCanRestartAfterResumeAdmissionFails(t *testing.T) {
	const movedCommit = "cccccccccccccccccccccccccccccccccccccccc"
	f := reviewFixtureWithOperatorCommit(t)
	f.pipelines("standard", map[string][]map[string]any{"standard": {
		{"kind": "manual", "name": "inspect"},
	}})
	f.run("q\n", "task", "run", "op-selected")
	paused, _ := f.loadFinalTask("op-selected")
	require.Len(t, paused.Reviews, 1)
	require.Equal(t, taskstate.ReviewStatusWaitingForManual, paused.Reviews[0].Status)
	moveReviewFixtureHead(f, movedCommit)

	_, _, err := f.runError("a\n", "task", "run", "op-selected")

	require.ErrorContains(t, err, "candidate is not at the reviewed commit")
	failed := assertSelectedReviewStops(t, f)
	require.NotNil(t, failed.AdmissionFailure)
	require.Nil(t, failed.Steps[0].(*work.ManualReviewStep).Decision)
	assert.Equal(t, movedCommit, f.git.heads[taskWorkflowRepoRoot])
	assert.Zero(t, f.candidate.restorations)

	moveReviewFixtureHead(f, selectedReviewCommit)
	f.run("a\n", "task", "run", "op-selected")

	retried, _ := f.loadFinalTask("op-selected")
	require.Len(t, retried.Reviews, 2)
	assert.Equal(t, taskstate.ReviewStatusFailed, retried.Reviews[0].Status)
	assert.Equal(t, failed, retried.Work.ActiveWorkflow().Phases[retried.Reviews[0].Address.Phase])
	assert.Equal(t, taskstate.ReviewStatusPassed, retried.Reviews[1].Status)
	assertUnconsentedPublication(t, retried)
}

func TestIntegrationWorkflowCanceledRecoveryKeepsChoiceForContinuation(t *testing.T) {
	fixture := newReviewWorkflowFixture(t, "op-cancel", "Review cancellation", "Keep output.")
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "check", "name": "mutator", "command": "mutation"}}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture.options.Dependencies.ReviewEffects.RunCommand = func(review.CommandOptions) (*int, error) {
		fixture.candidate.contents = "canceled mutation"
		cancel()
		return nil, context.Canceled
	}
	command := cli.NewRootCommandWithOptions(fixture.options)
	command.SetIn(strings.NewReader("r\n"))
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetContext(ctx)
	command.SetArgs([]string{"task", "run", "op-cancel"})
	err := command.Execute()
	require.ErrorIs(t, err, context.Canceled)
	loaded, item := fixture.loadFinalTask("op-cancel")
	phase := loaded.Work.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
	require.NotNil(t, phase.CheckoutRecovery)
	assert.Nil(t, phase.CheckoutRecovery.RestoredAt)
	assert.Equal(t, "canceled mutation", fixture.candidate.contents)
	assert.Zero(t, fixture.candidate.restorations)
	assertTaskNotPublished(t, loaded, item)
	_, stderr, err := fixture.runError("r\n", "task", "run", "op-cancel")
	require.ErrorContains(t, err, "review remains failed")
	assert.Contains(t, stderr, "Checkout restored.")
	assert.Equal(t, 1, fixture.candidate.snapshots)
}
