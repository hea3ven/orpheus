package work

import "time"

func manualCommandFailed(result ProcessResult) bool {
	if result == nil {
		return false
	}
	exit, ok := result.(*ExitedProcess)
	return !ok || exit == nil || exit.Code == nil || *exit.Code != 0
}

func manualCommandTime(result ProcessResult) time.Time {
	switch result := result.(type) {
	case *ExitedProcess:
		if result != nil {
			return result.At
		}
	case *StartFailedProcess:
		if result != nil {
			return result.At
		}
	}
	return time.Time{}
}

func (v *validator) manualCommand(result ProcessResult) {
	switch result := result.(type) {
	case nil:
	case *ExitedProcess:
		v.check(result != nil && result.Code != nil && !result.At.IsZero(), "manual command requires explicit exit code and observation time")
	case *StartFailedProcess:
		v.check(result != nil && result.Reason != "" && !result.At.IsZero(), "manual command start failure requires reason and observation time")
	default:
		v.check(false, "manual command requires an observed exit or start failure")
	}
}

func manualStepTime(step *ManualReviewStep) time.Time {
	if step == nil {
		return time.Time{}
	}
	switch decision := step.Decision.(type) {
	case *ApprovedReview:
		if decision != nil {
			return decision.At
		}
	case *RequestedChanges:
		if decision != nil {
			return decision.At
		}
	case *AbortedReview:
		if decision != nil {
			return decision.At
		}
	}
	return manualCommandTime(step.CommandResult)
}
