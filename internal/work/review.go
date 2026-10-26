package work

import (
	"fmt"
	"time"
)

// ReviewStepKind describes a configurable requirement, not attempt identity.
type ReviewStepKind string

const (
	ReviewCheck  ReviewStepKind = "check"
	ReviewAgent  ReviewStepKind = "agent_review"
	ReviewManual ReviewStepKind = "manual"
)

// ReviewSlot retains a real customizable configuration key once in the contract.
type ReviewSlot struct {
	Kind      ReviewStepKind
	ConfigKey string
}

// ReviewContract is the immutable captured set of requirements for an occurrence.
// Appending another contract permits changed configuration without rewriting history.
type ReviewContract struct {
	PipelineKey string
	Slots       []ReviewSlot
}

// ReviewReport is an agent's explicit handoff, distinct from successful process exit.
type ReviewReport struct {
	Evidence      string
	Findings      []Finding
	Verifications []FindingVerification
}

// ReviewOutcome is a computed projection, never a stored field on review/step.
type ReviewOutcome string

const (
	ReviewPending           ReviewOutcome = "pending"
	ReviewFailed            ReviewOutcome = "failed"
	ReviewChangesRequired   ReviewOutcome = "changes_required"
	ReviewPassed            ReviewOutcome = "passed"
	ReviewPassedWithWaivers ReviewOutcome = "passed_with_waivers"
)

// StepOutcome distinguishes an effective waiver from a successful check exit.
type StepOutcome string

const (
	StepPending         StepOutcome = "pending"
	StepFailed          StepOutcome = "failed"
	StepChangesRequired StepOutcome = "changes_required"
	StepPassed          StepOutcome = "passed"
	StepWaived          StepOutcome = "waived"
)

// ReviewOutcome validates the occurrence and derives its requirements' last attempts.
// A manual approval, successful prefix, or earlier successful retry cannot override
// a missing requirement, failed process, or unresolved blocker.
func (w *Workflow) ReviewOutcome(ref ReviewRef) (ReviewOutcome, error) {
	return w.reviewOutcome(ref, time.Time{})
}

// reviewOutcome uses the same authority rules for current progression and grants.
// A zero cutoff includes all recorded decisions; a grant includes only its past.
func (w *Workflow) reviewOutcome(ref ReviewRef, cutoff time.Time) (ReviewOutcome, error) {
	p, err := phaseAs[*ReviewPhase](w, ref.Phase)
	if err != nil || p == nil {
		return "", fmt.Errorf("review reference requires review")
	}
	v := validator{w: w, phase: ref.Phase, live: ref.Phase == len(w.Phases)-1 && !w.closed()}
	v.review(p)
	if err := v.err(); err != nil {
		return "", err
	}
	contract, err := element(w.ReviewContracts, p.Contract)
	if err != nil {
		return "", err
	}
	return aggregateReview(w, p.Steps, len(contract.Slots), cutoff), nil
}

func effectiveSteps(steps []ReviewStep, required int) []ReviewStep {
	latest := make([]ReviewStep, required)
	for _, step := range steps {
		slot, _ := reviewSlot(step)
		latest[slot] = step
	}
	return latest
}

func aggregateReview(w *Workflow, steps []ReviewStep, required int, cutoff time.Time) ReviewOutcome {
	outcomes := make([]StepOutcome, required)
	for i, step := range effectiveSteps(steps, required) {
		outcomes[i] = stepOutcome(w, step, cutoff)
	}
	for _, pair := range []struct {
		step   StepOutcome
		review ReviewOutcome
	}{{StepFailed, ReviewFailed}, {StepChangesRequired, ReviewChangesRequired}, {StepPending, ReviewPending}, {StepWaived, ReviewPassedWithWaivers}} {
		for _, outcome := range outcomes {
			if outcome == pair.step {
				return pair.review
			}
		}
	}
	return ReviewPassed
}

func reviewSlot(step ReviewStep) (int, ReviewStepKind) {
	switch step := step.(type) {
	case *CheckStep:
		if step != nil {
			return step.Slot, ReviewCheck
		}
	case *AgentReviewStep:
		if step != nil {
			return step.Slot, ReviewAgent
		}
	case *ManualReviewStep:
		if step != nil {
			return step.Slot, ReviewManual
		}
	}
	return 0, ""
}

func stepOutcome(w *Workflow, step ReviewStep, cutoff time.Time) StepOutcome {
	if !observedBy(stepTime(step), cutoff) {
		return StepPending
	}
	switch step := step.(type) {
	case *CheckStep:
		if step == nil || step.Result == nil {
			return StepPending
		}
		if *step.Result.ExitCode == 0 {
			return StepPassed
		}
		if step.Waiver != nil {
			return StepWaived
		}
		return StepFailed
	case *AgentReviewStep:
		return agentReviewOutcome(w, step, cutoff)
	case *ManualReviewStep:
		if step == nil {
			return StepPending
		}
		switch decision := step.Decision.(type) {
		case *ApprovedReview:
			if decision != nil {
				return StepPassed
			}
		case *RequestedChanges:
			if decision != nil {
				return StepChangesRequired
			}
		}
	}
	return StepPending
}

func agentReviewOutcome(w *Workflow, step *AgentReviewStep, cutoff time.Time) StepOutcome {
	if step == nil {
		return StepPending
	}
	if running(&step.Execution) {
		return StepPending
	}
	if !successful(&step.Execution) {
		return StepFailed
	}
	if step.Report == nil {
		return StepPending
	}
	outcome := findingOutcome(step.Report.Findings, cutoff)
	for _, verification := range step.Report.Verifications {
		if verification.Resolution != FindingUnresolved {
			continue
		}
		finding, err := w.Finding(verification.Finding)
		if err != nil {
			return StepChangesRequired
		}
		referenced := findingOutcome([]Finding{*finding}, cutoff)
		if referenced == StepChangesRequired {
			return StepChangesRequired
		}
		if referenced == StepWaived && outcome == StepPassed {
			outcome = StepWaived
		}
	}
	return outcome
}

func findingOutcome(findings []Finding, cutoff time.Time) StepOutcome {
	outcome := StepPassed
	for _, finding := range findings {
		if finding.Classification != FindingBlocking {
			continue
		}
		switch finding.dispositionAt(cutoff).(type) {
		case *WaivedFinding, *DismissedFinding, *DowngradedFinding, *AddressedFinding:
			outcome = StepWaived
		default:
			return StepChangesRequired
		}
	}
	return outcome
}

func stepFindings(step ReviewStep) []Finding {
	switch step := step.(type) {
	case *AgentReviewStep:
		if step != nil && step.Report != nil {
			return step.Report.Findings
		}
	case *ManualReviewStep:
		if step != nil {
			if decision, ok := step.Decision.(*RequestedChanges); ok && decision != nil {
				return decision.Findings
			}
		}
	}
	return nil
}

func stepVerifications(step ReviewStep) []FindingVerification {
	switch step := step.(type) {
	case *AgentReviewStep:
		if step != nil && step.Report != nil {
			return step.Report.Verifications
		}
	case *ManualReviewStep:
		if step != nil {
			if decision, ok := step.Decision.(*ApprovedReview); ok && decision != nil {
				return decision.Verifications
			}
		}
	}
	return nil
}

// observedBy treats a zero cutoff as the current projection, not a grant time.
func observedBy(at, cutoff time.Time) bool {
	return cutoff.IsZero() || !at.After(cutoff)
}

func passed(outcome ReviewOutcome) bool {
	return outcome == ReviewPassed || outcome == ReviewPassedWithWaivers
}
