package review

import (
	"errors"
	"time"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// FailureChange retains an observed orchestration failure separately from the
// native results and findings of steps that already ran.
func FailureChange(owner work.AttemptRef, cause error) phaseexec.Change {
	at := time.Now().UTC()
	return func(current *work.State) (bool, error) {
		if cause == nil {
			return false, errors.New("review failure requires an observed cause")
		}
		_, phase, err := currentReview(current, owner)
		if err != nil {
			return false, err
		}
		if phase.ExecutionFailure != nil {
			return false, nil
		}
		phase.ExecutionFailure = &work.ReviewExecutionFailure{At: at, Reason: cause.Error()}
		return true, nil
	}
}
