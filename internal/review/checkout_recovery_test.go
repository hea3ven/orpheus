package review_test

import (
	"bytes"
	"context"
	"io"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckoutRecoveryCancellationNeverImpliesConsent(t *testing.T) {
	for _, duringPrompt := range []bool{false, true} {
		t.Run(map[bool]string{false: "before prompt", true: "while prompting"}[duringPrompt], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if !duringPrompt {
				cancel()
			}
			var output bytes.Buffer
			restored, prompted := false, false
			store := &recoveryRecorder{}
			err := review.ResolveCheckoutRecovery(review.CheckoutRecoveryOptions{
				Context: ctx, Store: store, Output: &output,
				Violation: review.CheckoutViolation{Workdir: "/fixture/task", Reason: "checkout changed", Candidate: review.CommittedCandidate{Identity: gitmeta.CheckoutIdentity{Commit: "recorded-commit", Ref: "refs/heads/task"}}},
				Prompt: func(review.CheckoutViolation) (bool, error) {
					prompted = true
					assert.Contains(t, output.String(), "recorded-commit")
					assert.Contains(t, output.String(), "staged, unstaged, and non-ignored untracked")
					cancel()
					return true, nil
				},
				Restore: func(context.Context, string, review.CommittedCandidate) error { restored = true; return nil },
			})
			require.ErrorIs(t, err, context.Canceled)
			assert.Equal(t, duringPrompt, prompted)
			assert.False(t, restored)
			assert.Zero(t, store.records)
		})
	}
}

func TestCheckoutRecoveryRetainsFailedOrInterruptedRestoration(t *testing.T) {
	for _, failure := range []error{io.ErrUnexpectedEOF, context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			var output bytes.Buffer
			store := &recoveryRecorder{}
			err := review.ResolveCheckoutRecovery(review.CheckoutRecoveryOptions{
				Context: context.Background(), Store: store, Output: &output,
				Violation: review.CheckoutViolation{Workdir: "/fixture/task", Reason: "checkout changed", Candidate: review.CommittedCandidate{Identity: gitmeta.CheckoutIdentity{Commit: "recorded-commit", Ref: "refs/heads/task"}}},
				Prompt:    func(review.CheckoutViolation) (bool, error) { return true, nil },
				Restore:   func(context.Context, string, review.CommittedCandidate) error { return failure },
			})
			require.ErrorIs(t, err, failure)
			assert.ErrorContains(t, err, "rollback failed or incomplete")
			assert.ErrorContains(t, err, "recorded-commit")
			assert.NotContains(t, output.String(), "Checkout restored.")
			assert.Equal(t, 1, store.records)
			assert.ErrorIs(t, store.cause, failure)
		})
	}
}

type recoveryRecorder struct {
	records int
	cause   error
}

func (s *recoveryRecorder) RecordCheckoutRestoration(_ work.AttemptRef, cause error) error {
	s.records++
	s.cause = cause
	return nil
}
