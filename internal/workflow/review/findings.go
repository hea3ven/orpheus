package review

import (
	"errors"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// FindingChange records an automated reviewer's report independently of its
// parent process. Manual edits belong to a complete ManualSubmission instead.
func FindingChange(owner work.AttemptRef, input FindingInput) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		if err := current.CheckActiveAttempt(owner); err != nil {
			return false, err
		}
		_, phase, err := currentReview(current, owner)
		if err != nil {
			return false, err
		}
		if owner.Step == nil {
			return false, errors.New("finding requires its exact live reporting actor address")
		}
		step, ok := phase.Steps[*owner.Step].(*work.AgentReviewStep)
		if !ok {
			return false, errors.New("automated findings require the active reviewer; submit manual findings with their assessment")
		}
		_, view, err := input.normalized()
		if err != nil {
			return false, err
		}
		finding := reportedFinding(view)
		if step.Report == nil {
			step.Report = &work.ReviewReport{}
		}
		step.Report.Findings = append(step.Report.Findings, finding)
		return true, nil
	}
}
