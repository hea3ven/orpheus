package work

import "time"

// CheckoutRecovery retains an unresolved restoration choice after a checkout
// violation. Restoring never removes the independently retained step failure.
type CheckoutRecovery struct {
	At         time.Time
	Reason     string
	RestoredAt *time.Time
	Failures   []ReadOnlyFailure
}

// ReadOnlyFailure is an actual failed Git-visible checkout verification. It
// does not rewrite a child's observed exit or fabricate a reviewer finding.
// Reason retains the adapter's restoration/refusal advice, including uncertainty.
type ReadOnlyFailure struct {
	At     time.Time
	Reason string
}

func readOnlyFailures(step ReviewStep) []ReadOnlyFailure {
	switch step := step.(type) {
	case *CheckStep:
		if step != nil {
			return step.ReadOnlyFailures
		}
	case *AgentReviewStep:
		if step != nil {
			return step.ReadOnlyFailures
		}
	case *ManualReviewStep:
		if step != nil {
			return step.ReadOnlyFailures
		}
	}
	return nil
}

func (v *validator) readOnlyFailures(step ReviewStep, enteredAt time.Time) {
	previous := enteredAt
	for _, failure := range readOnlyFailures(step) {
		v.text(failure.Reason, "read-only verification failure")
		v.check(!failure.At.IsZero() && !failure.At.Before(previous), "read-only verification failure requires ordered observation time after review entry")
		previous = failure.At
	}
}
