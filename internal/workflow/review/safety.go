package review

import (
	"errors"
	"time"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// ReadOnlyFailureChange preserves failed safety verification without replacing
// a child's real native exit or manufacturing an agent finding.
func ReadOnlyFailureChange(owner work.AttemptRef, cause error) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		if cause == nil {
			return false, errors.New("read-only failure requires observed error")
		}
		_, phase, err := currentReview(current, owner)
		if err != nil {
			return false, err
		}
		if owner.Step == nil || *owner.Step < 0 || *owner.Step != len(phase.Steps)-1 {
			return false, errors.New("read-only observation requires the current last step")
		}
		failure := work.ReadOnlyFailure{At: time.Now().UTC(), Reason: cause.Error()}
		if phase.CheckoutRecovery == nil {
			phase.CheckoutRecovery = &work.CheckoutRecovery{At: failure.At, Reason: failure.Reason}
		}
		switch step := phase.Steps[*owner.Step].(type) {
		case *work.CheckStep:
			step.ReadOnlyFailures = append(step.ReadOnlyFailures, failure)
		case *work.AgentReviewStep:
			step.ReadOnlyFailures = append(step.ReadOnlyFailures, failure)
		case *work.ManualReviewStep:
			step.ReadOnlyFailures = append(step.ReadOnlyFailures, failure)
		}
		return true, nil
	}
}

// AdmissionFailureChange records an actual failed checkout admission without
// pretending an agent process or check command was started.
func AdmissionFailureChange(owner work.AttemptRef, cause error) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		if cause == nil {
			return false, errors.New("admission failure requires an observed error")
		}
		_, phase, err := currentReview(current, owner)
		if err != nil {
			return false, err
		}
		if phase.AdmissionFailure != nil {
			return false, nil
		}
		phase.AdmissionFailure = &work.ReadOnlyFailure{At: time.Now().UTC(), Reason: cause.Error()}
		return true, nil
	}
}
