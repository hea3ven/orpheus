package review_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckoutRecoveryCancellationNeverImpliesConsent(t *testing.T) {
	for _, duringPrompt := range []bool{false, true} {
		t.Run(map[bool]string{false: "before prompt", true: "while prompting"}[duringPrompt], func(t *testing.T) {
			runner, original, owner := checkoutRecoveryFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if !duringPrompt {
				cancel()
			}
			var output bytes.Buffer
			restored, prompted := false, false
			err := review.ResolveCheckoutRecovery(review.CheckoutRecoveryOptions{
				Context: ctx, Runner: runner, Owner: owner, Output: &output,
				Prompt: func(v review.CheckoutViolation) (bool, error) {
					prompted = true
					assert.Contains(t, output.String(), "fixture-output")
					assert.Contains(t, output.String(), "staged, unstaged, and non-ignored untracked")
					assert.Equal(t, "/fixture/work", v.Workdir)
					cancel()
					return true, nil
				},
				Restore: func(context.Context, string, review.CommittedCandidate) error { restored = true; return nil },
			})
			require.ErrorIs(t, err, context.Canceled)
			assert.Equal(t, duringPrompt, prompted)
			assert.False(t, restored)
			persisted, err := runner.Load(context.Background())
			require.NoError(t, err)
			assert.Equal(t, original, persisted, "cancellation preserves unanswered assessment, findings and failed-step evidence")
		})
	}
}

func TestCheckoutRecoveryRetainsFailedOrInterruptedRestoration(t *testing.T) {
	for _, failure := range []error{io.ErrUnexpectedEOF, context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			runner, original, owner := checkoutRecoveryFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var output bytes.Buffer
			err := review.ResolveCheckoutRecovery(review.CheckoutRecoveryOptions{
				Context: ctx, Runner: runner, Owner: owner, Output: &output,
				Prompt: func(review.CheckoutViolation) (bool, error) { return true, nil },
				Restore: func(_ context.Context, dir string, candidate review.CommittedCandidate) error {
					assert.Equal(t, "/fixture/work", dir)
					assert.Equal(t, "refs/heads/task-branch", candidate.Identity.Ref)
					assert.Equal(t, "fixture-output", candidate.Identity.Commit)
					assert.Equal(t, "fixture-tree", candidate.Revision.Tree)
					if errors.Is(failure, context.Canceled) {
						cancel()
					}
					return failure
				},
			})
			require.ErrorIs(t, err, failure)
			assert.ErrorContains(t, err, "rollback failed or incomplete")
			assert.ErrorContains(t, err, "fixture-output")
			assert.NotContains(t, output.String(), "Checkout restored.")
			persisted, err := runner.Load(context.Background())
			require.NoError(t, err)
			phase := persisted.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
			require.Len(t, phase.CheckoutRecovery.Failures, 1)
			assert.Equal(t, failure.Error(), phase.CheckoutRecovery.Failures[0].Reason)
			assert.Nil(t, phase.CheckoutRecovery.RestoredAt)
			assert.Equal(t, original.Version+1, persisted.Version)
			assert.Equal(t, original.ActiveWorkflow().LastPhase().(*work.ReviewPhase).Steps, phase.Steps)
		})
	}
}

func TestCheckoutRecoveryReceiptFailureStopsWithoutLosingThePendingTarget(t *testing.T) {
	runner, current, owner := checkoutRecoveryFixture(t)
	injected := errors.New("restoration receipt unavailable")
	savedStore := &manualSaveStore{Store: runner.Store, failure: injected}
	runner.Store = savedStore
	restores := 0
	phase := review.CheckoutRecovery{Restore: func(_ context.Context, dir string, candidate review.CommittedCandidate) error {
		restores++
		assert.Equal(t, "/fixture/work", dir)
		assert.Equal(t, "fixture-output", candidate.Revision.Commit)
		return nil
	}}
	selected, err := phase.Next(current, owner)
	require.NoError(t, err)
	request, ok := selected.Input()
	require.True(t, ok)
	assert.Equal(t, current.Version, request.ID.Version)
	op, err := phase.Respond(current, request.ID, review.CheckoutRecoveryAnswer{Restore: true})
	require.NoError(t, err)

	returned, err := runner.Execute(context.Background(), current, op)

	require.ErrorIs(t, err, injected)
	assert.Equal(t, 1, restores, "no generic retry of the external restoration")
	assert.Equal(t, 1, savedStore.saves)
	assert.Equal(t, current, returned, "an unsaved restoration cannot leak into progression")
	persisted, err := runner.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, current, persisted)
	savedStore.failure = nil
	retry := review.CheckoutRecovery{Restore: phase.Restore}
	selected, err = retry.Next(persisted, owner)
	require.NoError(t, err)
	pending, ok := selected.Input()
	require.True(t, ok)
	assert.Equal(t, request.Violation, pending.Violation)
	stale := pending.ID
	stale.Version++
	_, err = retry.Respond(persisted, stale, review.CheckoutRecoveryAnswer{Restore: true})
	require.ErrorIs(t, err, phaseexec.ErrStaleInput)
	assert.Equal(t, 1, restores)
	op, err = retry.Respond(persisted, pending.ID, review.CheckoutRecoveryAnswer{Restore: true})
	require.NoError(t, err)
	committed, err := runner.Execute(context.Background(), persisted, op)
	require.NoError(t, err)
	assert.Equal(t, current.Version+1, committed.Version)
	assert.Equal(t, 2, restores)
	selected, err = retry.Next(committed, owner)
	require.NoError(t, err)
	outcome, ok := selected.Outcome()
	require.True(t, ok)
	assert.True(t, outcome.Restored)
	assert.Equal(t, current.ActiveWorkflow().LastPhase().(*work.ReviewPhase).Steps, committed.ActiveWorkflow().LastPhase().(*work.ReviewPhase).Steps)
	persisted, err = runner.Load(context.Background())
	require.NoError(t, err)
	assert.Equal(t, committed, persisted)
}

func checkoutRecoveryFixture(t *testing.T) (phaseexec.Runner, work.State, work.AttemptRef) {
	t.Helper()
	paths, _, owner := manualSubmissionFixture(t)
	runner := phaseexec.Runner{Paths: paths, Store: workstate.NewStore(paths), RepositoryID: "repo", TaskID: "task"}
	current, err := runner.Commit(context.Background(), review.ReadOnlyFailureChange(owner, errors.New("checkout changed")))
	require.NoError(t, err)
	return runner, current, owner
}
