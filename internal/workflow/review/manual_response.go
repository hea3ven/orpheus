package review

import (
	"context"
	"errors"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

const ManualInputKind = "manual_review"

// Assessments owns typed Review decisions. CLI supplies terminal editing and
// the shared runner supplies persistence; neither interprets the answer.
type Assessments struct {
	Paths      state.Paths
	Repository task.Repository
	Git        interface {
		VerifyRevision(context.Context, string, gitmeta.CheckoutIdentity, gitmeta.Revision) error
	}
}

func (s Assessments) manualResponse(current work.State, id phaseexec.InputID, answer ManualSubmission) (phaseexec.Operation, error) {
	if err := validateManualInput(current, id); err != nil {
		return phaseexec.Operation{}, err
	}
	// Normalize into independent content before retaining the answer in a closure.
	submission := ManualSubmission{Decision: answer.Decision, Promote: append([]work.FindingRef(nil), answer.Promote...)}
	for _, input := range answer.Findings {
		normalized, _, err := input.normalized()
		if err != nil {
			return phaseexec.Operation{}, err
		}
		submission.Findings = append(submission.Findings, normalized)
	}
	return s.decisionOperation(id, "submit manual assessment", false, func(current *work.State, _ DecisionRequest, at time.Time) (bool, error) {
		if err := validateManualInput(*current, id); err != nil {
			return false, err
		}
		return applyManualSubmission(current, id.Owner, submission, at)
	}), nil
}

func validateManualInput(current work.State, id phaseexec.InputID) error {
	if id.Kind != ManualInputKind || current.Version != id.Version {
		return phaseexec.ErrStaleInput
	}
	if err := current.CheckActiveAttempt(id.Owner); err != nil {
		return errors.Join(phaseexec.ErrStaleInput, err)
	}
	p, ok := current.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
	if !ok || id.Owner.Step == nil || p.Abort != nil || p.ExecutionFailure != nil || p.AdmissionFailure != nil {
		return phaseexec.ErrStaleInput
	}
	step, ok := p.Steps[*id.Owner.Step].(*work.ManualReviewStep)
	if !ok || step.Decision != nil || len(step.ReadOnlyFailures) > 0 || manualCommandFailed(step.CommandResult) {
		return phaseexec.ErrStaleInput
	}
	return nil
}
