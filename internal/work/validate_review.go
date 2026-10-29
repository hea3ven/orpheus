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
	v.reviewPolicy(p)
	v.check(!p.StartedAt.IsZero(), "review requires observed entry time")
	if p.Abort != nil {
		v.text(p.Abort.Reason, "operator review stop")
		v.check(!p.Abort.At.IsZero() && !p.Abort.At.Before(p.StartedAt), "operator review stop requires observation time after entry")
	}
	if failure := p.ExecutionFailure; failure != nil {
		v.text(failure.Reason, "review execution failure")
		v.check(!failure.At.IsZero() && !failure.At.Before(p.StartedAt), "review execution failure requires observation time after entry")
	}
	if failure := p.AdmissionFailure; failure != nil {
		v.text(failure.Reason, "read-only admission failure")
		v.check(!failure.At.IsZero() && !failure.At.Before(p.StartedAt), "read-only admission failure requires observation time after review entry")
	}
	for i, step := range p.Steps {
		v.step = i
		slot, kind := reviewSlot(step)
		if err == nil {
			required, slotErr := element(contract.Slots, slot)
			v.check(slotErr == nil, "review attempt requires an in-range required-slot index")
			v.check(slotErr != nil || required.Kind == kind, "review attempt kind disagrees with required slot")
		}
		v.reviewStep(step, v.phase == len(v.w.Phases)-1 && i == len(p.Steps)-1)
		v.readOnlyFailures(step, p.StartedAt)
		if agent, ok := step.(*AgentReviewStep); ok && agent != nil {
			v.check(!agent.Execution.StartedAt.Before(p.StartedAt), "agent execution predates review entry")
		}
		at := stepTime(step)
		v.check(at.IsZero() || !at.Before(p.StartedAt), "review attempt result predates review entry")
		if manual, ok := step.(*ManualReviewStep); ok && manual != nil {
			commandAt := manualCommandTime(manual.CommandResult)
			v.check(commandAt.IsZero() || !commandAt.Before(p.StartedAt), "manual command result predates review entry")
		}
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
		v.checkStep(step, tail)
	case *AgentReviewStep:
		if step == nil {
			v.check(false, "agent review step is nil")
			return
		}
		v.execution(&step.Execution, v.live && tail)
		if failure := step.ExecutionFailure; failure != nil {
			v.text(failure.Reason, "review execution failure")
			v.check(!failure.At.IsZero() && !failure.At.Before(step.Execution.StartedAt), "review execution failure requires observation time after start")
		}
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
		v.manualStep(step, tail)
	default:
		v.check(false, "review attempt is required")
	}
}

func (v *validator) checkStep(step *CheckStep, tail bool) {
	if step == nil {
		v.check(false, "check step is nil")
		return
	}
	v.check(step.Result != nil || step.StartFailure != nil || tail, "pending check must belong to the last review attempt in the last phase")
	if failure := step.StartFailure; failure != nil {
		v.check(step.Result == nil, "check start failure cannot also claim an exit")
		v.text(failure.Reason, "check start failure reason")
		v.check(!failure.At.IsZero(), "check start failure requires observation time")
	}
	if step.Result != nil {
		v.check(step.Result.ExitCode != nil && !step.Result.FinishedAt.IsZero(), "check requires explicit exit code and observed result time")
	}
	if len(step.Findings) > 0 {
		v.check(step.Result != nil && step.Result.ExitCode != nil && *step.Result.ExitCode != 0, "check findings require an observed failed check")
		for _, finding := range step.Findings {
			v.finding(finding)
		}
	}
	if step.Waiver != nil {
		v.check(step.Result != nil && step.Result.ExitCode != nil && *step.Result.ExitCode != 0, "waiver requires a failed check result")
		v.waiver(*step.Waiver)
		if step.Result != nil {
			v.check(!step.Waiver.At.Before(step.Result.FinishedAt), "waiver predates observed failed check")
		}
	}
}

func (v *validator) manualStep(step *ManualReviewStep, tail bool) {
	if step == nil {
		v.check(false, "manual review step is nil")
		return
	}
	failedCommand := manualCommandFailed(step.CommandResult)
	v.check(step.Decision != nil || failedCommand || tail, "unanswered manual assessment must belong to the last review attempt in the last phase")
	v.manualCommand(step.CommandResult)
	commandAt := manualCommandTime(step.CommandResult)
	v.check(commandAt.IsZero() || !stepTime(step).Before(commandAt), "manual assessment predates its command result")
	for _, finding := range step.Findings {
		v.finding(finding)
	}
	v.manual(step.Decision)
	switch decision := step.Decision.(type) {
	case *ApprovedReview:
		if decision != nil {
			v.check(!failedCommand, "manual approval cannot override its failed command")
			v.check(findingOutcome(step.Findings, decision.At) != StepChangesRequired, "manual approval cannot accept unresolved required findings")
		}
	case *RequestedChanges:
		if decision != nil {
			v.check(len(step.Findings) > 0 || len(decision.Targets) > 0, "requested changes require owned findings or explicit earlier targets")
			for _, target := range decision.Targets {
				v.manualTarget(target)
			}
		}
	}
}

func (v *validator) manual(assessment ManualAssessment) {
	switch assessment := assessment.(type) {
	case nil:
	case *ApprovedReview:
		v.check(assessment != nil && !assessment.At.IsZero(), "manual approval requires observed time")
		if assessment != nil {
			if assessment.Publication != nil {
				v.destination(&assessment.Publication.Destination)
				v.target(assessment.Publication.Target)
			}
			for _, verification := range assessment.Verifications {
				v.check(verification.Resolution == FindingResolved, "manual approval cannot claim an unresolved finding")
			}
		}
	case *RequestedChanges:
		if assessment == nil {
			v.check(false, "manual assessment is nil")
			return
		}
		v.check(!assessment.At.IsZero(), "requested changes require observed time")
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

func (v *validator) manualTarget(ref FindingRef) {
	prior := ref.Phase >= 0 && ref.Phase < v.phase || ref.Phase == v.phase && ref.Step >= 0 && ref.Step < v.step
	v.check(prior, "manual requested-change target must precede its assessment")
	if !prior {
		return
	}
	finding, err := v.w.Finding(ref)
	v.check(err == nil, "manual requested-change target: %v", err)
	if finding != nil {
		v.finding(*finding)
	}
}
