//go:build integration

package cli_test

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/cli"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowMutatingCheckOrManualCommandStopsBeforeDecisions(t *testing.T) {
	for _, kind := range []string{review.KindCheck, review.KindManual} {
		t.Run(kind, func(t *testing.T) {
			fixture := newReviewWorkflowFixture(t, "op-mutation", "Review commands", "Keep the output.")
			fixture.reviewers()
			fixture.pipelines("standard", map[string][]map[string]any{"standard": {
				{"kind": kind, "name": "mutator", "command": "mutation"},
				{"kind": "agent_review", "name": "must-not-launch"},
			}})
			calls := 0
			fixture.options.Dependencies.ReviewEffects.RunCommand = func(review.CommandOptions) (*int, error) {
				calls++
				fixture.candidate.contents = "command mutation"
				code := 0
				if kind == review.KindCheck {
					code = 1
					return &code, checkExit(code)
				}
				return &code, nil
			}
			// Confirm the manual command (if any), then preserve the checkout.
			_, stderr, err := fixture.runError("\nq\n", "task", "run", "op-mutation")
			require.ErrorContains(t, err, "changes preserved")
			assert.NotContains(t, stderr, "Decision for finding")
			assert.NotContains(t, stderr, "Review action")
			assert.Equal(t, 1, calls)
			assert.Zero(t, fixture.candidate.restorations)
			assert.Equal(t, "command mutation", fixture.candidate.contents)
			assert.Empty(t, fixture.agent.launches)
			loaded, item := fixture.loadFinalTask("op-mutation")
			require.Len(t, loaded.Reviews, 1)
			assert.Equal(t, taskstate.ReviewStatusFailed, loaded.Reviews[0].Status)
			require.Len(t, loaded.Reviews[0].Steps, 1)
			phase := loaded.Work.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
			require.NotNil(t, phase.CheckoutRecovery)
			assert.Nil(t, phase.CheckoutRecovery.RestoredAt)
			if manual, ok := phase.Steps[0].(*work.ManualReviewStep); ok {
				assert.Nil(t, manual.Decision)
			}
			assertTaskNotPublished(t, loaded, item)
		})
	}
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
