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
	for i, advisory := range p.AdvisoryFindings {
		v.findingRef(advisory, "advisory input")
		for _, earlier := range p.AdvisoryFindings[:i] {
			v.check(!sameFinding(earlier, advisory), "duplicate advisory input")
		}
		for _, target := range p.TargetFindings {
			v.check(!sameFinding(target, advisory), "required repair cannot also be an advisory opportunity")
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

// CheckOutputCapture requires the live last implementation's own handoff and
// observed successful process exit. A callback while its agent is running may
// record completion, but cannot authorize Git capture. Already captured output
// may be reverified idempotently by its owner without changing history.
func (s *State) CheckOutputCapture(ref AttemptRef) error {
	if err := s.Validate(); err != nil {
		return err
	}
	w := s.ActiveWorkflow()
	if w == nil || ref.RepositoryID != s.RepositoryID || ref.TaskID != s.TaskID || ref.Workflow != len(s.Workflows)-1 || ref.Phase != len(w.Phases)-1 || ref.Phase < 0 || ref.Step != nil || ref.BranchSync != nil {
		return fmt.Errorf("capture attempt does not own the active last implementation")
	}
	p, ok := w.LastPhase().(*ImplementationPhase)
	if !ok || p == nil {
		return fmt.Errorf("capture attempt does not own implementation")
	}
	return captureProblem(p)
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
	if f.SuggestedAction != "" {
		v.text(f.SuggestedAction, "finding suggested action")
	}
	if proposal := f.Proposal; proposal != nil {
		v.check(f.Classification == FindingSeparateTask, "follow-up proposal requires a separate-task finding")
		v.text(proposal.Title, "follow-up title")
		v.text(proposal.Description, "follow-up description")
		v.text(proposal.AcceptanceCriteria, "follow-up acceptance criteria")
	}
	if created := f.CreatedTask; created != nil {
		v.check(f.Proposal != nil, "created follow-up requires its authored proposal")
		v.text(created.TaskID, "created follow-up task identity")
		v.check(!created.ObservedAt.IsZero(), "created follow-up requires observation time")
	}
	var latest time.Time
	for i, disposition := range f.Dispositions {
		v.disposition(disposition)
		if _, promoted := disposition.(*PromotedFinding); promoted {
			v.check(f.Classification == FindingAdvisory, "promotion requires an originally advisory finding")
		}
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
	case *PromotedFinding:
		v.check(disposition != nil && !disposition.At.IsZero(), "promotion requires observed time")
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
