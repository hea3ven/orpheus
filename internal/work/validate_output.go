package work

import (
	"fmt"
	"strings"
	"time"
)

func (v *validator) implementation(p *ImplementationPhase) {
	if p == nil {
		v.check(false, "implementation is required")
		return
	}
	v.prepared()
	v.execution(&p.Execution, v.live && v.phase == len(v.w.Phases)-1)
	if p.Input != nil {
		v.outputRef(*p.Input, "implementation input")
	}
	for i, target := range p.TargetFindings {
		v.findingRef(target, "repair target")
		for _, earlier := range p.TargetFindings[:i] {
			v.check(!sameFinding(earlier, target), "duplicate repair target")
		}
	}
	if p.Completion != nil {
		_, notStarted := p.Execution.Process.(*StartFailedProcess)
		v.check(!notStarted, "start-failed execution cannot own a completion handoff")
		v.completion(p.Completion)
		v.check(!p.Completion.CompletedAt.Before(p.Execution.StartedAt), "completion predates its execution")
	}
	if p.Output != nil {
		v.check(captureProblem(p) == nil, "output capture requires its own completion and successful safe execution boundary")
		_, err := v.w.Output(OutputRef{Phase: v.phase})
		v.check(err == nil, "invalid owned output: %v", err)
	}
}

func captureProblem(p *ImplementationPhase) error {
	v := validator{}
	v.check(p.Completion != nil, "output requires its own completion, including unchanged output")
	v.check(successful(&p.Execution), "output requires observed successful safe execution boundary")
	if p.Completion != nil {
		v.completion(p.Completion)
		v.check(!p.Completion.CompletedAt.Before(p.Execution.StartedAt), "completion predates execution")
		if exit, ok := p.Execution.Process.(*ExitedProcess); ok && exit != nil {
			v.check(!p.Completion.CompletedAt.After(exit.At), "completion cannot postdate observed process exit")
		}
	}
	return v.err()
}

func (v *validator) completion(c *Completion) {
	v.check(strings.TrimSpace(c.Summary) != "" && strings.TrimSpace(c.Description) != "", "completion requires summary and description")
	v.check(!c.CompletedAt.IsZero(), "completion requires handoff time")
	v.text(c.DetailedDescription, "completion detailed description")
	if c.TechnicalExplanation != "" {
		v.text(c.TechnicalExplanation, "technical explanation")
	}
}

func validateCodeOutput(output *CodeOutput) error {
	if output == nil {
		return fmt.Errorf("code output is required")
	}
	v := validator{}
	v.code(output.CodeIdentity)
	v.check(strings.HasPrefix(output.RetainedRef, "refs/"), "code output requires an owned retention ref")
	return v.err()
}

func (v *validator) outputRef(ref OutputRef, label string) {
	if !v.prior(ref.Phase, label) {
		return
	}
	_, err := v.w.Output(ref)
	v.check(err == nil, "%s: %v", label, err)
}

func (v *validator) findingRef(ref FindingRef, label string) *Finding {
	if !v.prior(ref.Phase, label) {
		return nil
	}
	finding, err := v.w.Finding(ref)
	v.check(err == nil, "%s: %v", label, err)
	return finding
}

func sameFinding(a, b FindingRef) bool {
	return a == b
}

func (v *validator) finding(f Finding) {
	v.check(strings.TrimSpace(f.Title) != "", "finding title is required")
	v.check(f.Classification == FindingBlocking || f.Classification == FindingAdvisory || f.Classification == FindingSeparateTask, "invalid finding classification")
	v.text(f.Description, "finding description")
	var latest time.Time
	for i, disposition := range f.Dispositions {
		v.disposition(disposition)
		at := dispositionTime(disposition)
		v.check(i == 0 || !at.IsZero(), "later finding decisions require observation time")
		if !at.IsZero() {
			v.check(!at.Before(latest), "finding disposition predates earlier decision")
			latest = at
		}
	}
}

func (v *validator) disposition(disposition FindingDisposition) {
	switch disposition := disposition.(type) {
	case nil:
		v.check(false, "finding disposition is required")
	case *RepairRequired:
		v.check(disposition != nil, "finding disposition is nil")
	case *WaivedFinding:
		if disposition == nil {
			v.check(false, "finding disposition is nil")
			return
		}
		v.waiver(disposition.Waiver)
	case *DismissedFinding:
		if disposition == nil {
			v.check(false, "finding disposition is nil")
			return
		}
		v.waiver(disposition.Waiver)
	case *DowngradedFinding:
		if disposition == nil {
			v.check(false, "finding disposition is nil")
			return
		}
		v.waiver(disposition.Waiver)
	case *AddressedFinding:
		if disposition == nil {
			v.check(false, "finding disposition is nil")
			return
		}
		v.waiver(disposition.Waiver)
	default:
		v.check(false, "invalid finding disposition")
	}
}

func (v *validator) waiver(waiver Waiver) {
	v.check(strings.TrimSpace(waiver.Reason) != "" && !waiver.At.IsZero(), "waiver requires nonblank reason and observation time")
}
