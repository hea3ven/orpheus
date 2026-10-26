package work

import (
	"strings"
	"time"
)

func (v *validator) contract(contract ReviewContract) {
	v.check(strings.TrimSpace(contract.PipelineKey) != "" && len(contract.Slots) > 0, "review contract requires pipeline key and required slots")
	seen := make(map[string]bool)
	for _, slot := range contract.Slots {
		v.check(slot.Kind == ReviewCheck || slot.Kind == ReviewAgent || slot.Kind == ReviewManual, "invalid required review kind")
		v.check(strings.TrimSpace(slot.ConfigKey) != "" && !seen[slot.ConfigKey], "review configuration keys must be nonblank and unique")
		seen[slot.ConfigKey] = true
	}
}

func (v *validator) review(p *ReviewPhase) {
	if p == nil {
		v.check(false, "review is required")
		return
	}
	v.prepared()
	v.sync = -1
	contract, err := element(v.w.ReviewContracts, p.Contract)
	v.check(err == nil, "review requires captured contract: %v", err)
	if err == nil {
		v.contract(*contract)
	}
	v.scope(p.Scope, p.StartedAt)
	v.check(!p.StartedAt.IsZero(), "review requires observed entry time")
	for i, step := range p.Steps {
		v.step = i
		slot, kind := reviewSlot(step)
		if err == nil {
			required, slotErr := element(contract.Slots, slot)
			v.check(slotErr == nil, "review attempt requires an in-range required-slot index")
			v.check(slotErr != nil || required.Kind == kind, "review attempt kind disagrees with required slot")
		}
		v.reviewStep(step, v.phase == len(v.w.Phases)-1 && i == len(p.Steps)-1)
		if agent, ok := step.(*AgentReviewStep); ok && agent != nil {
			v.check(!agent.Execution.StartedAt.Before(p.StartedAt), "agent execution predates review entry")
		}
		at := stepTime(step)
		v.check(at.IsZero() || !at.Before(p.StartedAt), "review attempt result predates review entry")
		for _, verification := range stepVerifications(step) {
			v.verification(p.Scope.Output, verification)
		}
	}
}

func (v *validator) scope(scope ReviewScope, enteredAt time.Time) {
	v.text(scope.Requirements, "review requirements")
	v.reviewOutput(scope.Output, enteredAt, "review output")
	if scope.DeltaFrom != nil {
		v.reviewOutput(*scope.DeltaFrom, enteredAt, "review delta")
	}
	v.destination(scope.Destination)
}

func (v *validator) reviewOutput(ref OutputRef, enteredAt time.Time, label string) {
	if !v.prior(ref.Phase, label) {
		return
	}
	_, boundary, err := v.w.resolveOutput(ref)
	v.check(err == nil, "%s: %v", label, err)
	v.check(err != nil || !enteredAt.Before(boundary), "review entry predates %s safe capture boundary", label)
}

func (v *validator) reviewStep(step ReviewStep, tail bool) {
	switch step := step.(type) {
	case *CheckStep:
		if step == nil {
			v.check(false, "check step is nil")
			return
		}
		v.check(step.Result != nil || tail, "pending check must belong to the last review attempt in the last phase")
		if step.Result != nil {
			v.check(step.Result.ExitCode != nil && !step.Result.FinishedAt.IsZero(), "check requires explicit exit code and observed result time")
		}
		if step.Waiver != nil {
			v.check(step.Result != nil && step.Result.ExitCode != nil && *step.Result.ExitCode != 0, "waiver requires a failed check result")
			v.waiver(*step.Waiver)
			if step.Result != nil {
				v.check(!step.Waiver.At.Before(step.Result.FinishedAt), "waiver predates observed failed check")
			}
		}
	case *AgentReviewStep:
		if step == nil {
			v.check(false, "agent review step is nil")
			return
		}
		v.execution(&step.Execution, v.live && tail)
		if step.Report != nil {
			_, notStarted := step.Execution.Process.(*StartFailedProcess)
			v.check(!notStarted, "start-failed reviewer cannot own a report")
			if step.Report.Evidence != "" {
				v.text(step.Report.Evidence, "review report")
			}
			for _, finding := range step.Report.Findings {
				v.finding(finding)
			}
		}
	case *ManualReviewStep:
		if step == nil {
			v.check(false, "manual review step is nil")
			return
		}
		v.check(step.Decision != nil || tail, "unanswered manual assessment must belong to the last review attempt in the last phase")
		v.manual(step.Decision)
	default:
		v.check(false, "review attempt is required")
	}
}

func (v *validator) manual(assessment ManualAssessment) {
	switch assessment := assessment.(type) {
	case nil:
	case *ApprovedReview:
		v.check(assessment != nil && !assessment.At.IsZero(), "manual approval requires observed time")
		if assessment != nil {
			for _, verification := range assessment.Verifications {
				v.check(verification.Resolution == FindingResolved, "manual approval cannot claim an unresolved finding")
			}
		}
	case *RequestedChanges:
		if assessment == nil {
			v.check(false, "manual assessment is nil")
			return
		}
		v.check(!assessment.At.IsZero() && len(assessment.Findings) > 0, "requested changes require time and findings")
		for _, finding := range assessment.Findings {
			v.finding(finding)
		}
	case *AbortedReview:
		v.check(assessment != nil && assessment.Reason != "" && !assessment.At.IsZero(), "aborted review requires reason and time")
	default:
		v.check(false, "invalid manual assessment")
	}
}

func (v *validator) verification(output OutputRef, verification FindingVerification) {
	finding := v.findingRef(verification.Finding, "finding verification")
	v.check(verification.Resolution == FindingResolved || verification.Resolution == FindingUnresolved, "invalid finding resolution")
	v.text(verification.Evidence, "finding verification")
	if finding == nil {
		return
	}
	v.finding(*finding)
	source, err := v.w.findingOutput(verification.Finding)
	v.check(err == nil, "finding source output: %v", err)
	matches := err == nil && v.w.sameOutput(source, output)
	for i, phase := range v.w.Phases[:v.phase] {
		p, ok := phase.(*ImplementationPhase)
		if !ok || p == nil || p.Output == nil {
			continue
		}
		for _, target := range p.TargetFindings {
			if sameFinding(target, verification.Finding) && v.w.sameOutput(OutputRef{Phase: i}, output) {
				matches = true
			}
		}
	}
	v.check(matches, "finding verification must evaluate source or targeted repaired output")
}
