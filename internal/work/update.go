package work

import (
	"errors"
	"fmt"
	"reflect"
)

// ValidateSuccessor checks a proposed update against an independent prior snapshot.
// It preserves addresses and recorded facts while permitting tail work to finish,
// new attempts/contracts/workflows and finding decisions to append, supplemental
// telemetry to fill, and cleanup observations after closure.
// Storage still owns expected-version atomic saves; this method neither saves nor
// detects mutations already made through pointers shared with the prior snapshot.
func (s *State) ValidateSuccessor(previous *State) error {
	if err := previous.Validate(); err != nil {
		return fmt.Errorf("previous state: %w", err)
	}
	if err := s.Validate(); err != nil {
		return err
	}
	g := changeGuard{}
	g.same(previous.RepositoryID, s.RepositoryID, "repository identity")
	g.same(previous.TaskID, s.TaskID, "task identity")
	if len(s.Workflows) < len(previous.Workflows) {
		return fmt.Errorf("workflows are append-only")
	}
	for i := range previous.Workflows {
		g.workflow(&previous.Workflows[i], &s.Workflows[i], i == len(previous.Workflows)-1 && !previous.Workflows[i].closed())
	}
	return errors.Join(g.errors...)
}

type changeGuard struct{ errors []error }

func (g *changeGuard) same(before, after any, label string) bool {
	if reflect.DeepEqual(before, after) {
		return true
	}
	g.errors = append(g.errors, fmt.Errorf("%s cannot be rewritten", label))
	return false
}

func (g *changeGuard) assigned(before, after any, label string) {
	if before == nil {
		return
	}
	value := reflect.ValueOf(before)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return
	}
	g.same(before, after, label)
}

func (g *changeGuard) prefix(before, after any, label string) {
	old, current := reflect.ValueOf(before), reflect.ValueOf(after)
	if old.Len() <= current.Len() && (old.Len() == 0 || reflect.DeepEqual(old.Interface(), current.Slice(0, old.Len()).Interface())) {
		return
	}
	g.errors = append(g.errors, fmt.Errorf("%s is append-only", label))
}

func (g *changeGuard) workflow(before, after *Workflow, active bool) {
	g.prefix(before.ReviewContracts, after.ReviewContracts, "review contracts")
	if !active {
		g.same(len(before.Phases), len(after.Phases), "inactive workflow history")
		g.same(len(before.ReviewContracts), len(after.ReviewContracts), "inactive workflow contracts")
	}
	if len(after.Phases) < len(before.Phases) {
		g.errors = append(g.errors, fmt.Errorf("phases are append-only"))
		return
	}
	for i, phase := range before.Phases {
		mutable := active && i == len(before.Phases)-1
		if final, ok := phase.(*FinalizationPhase); ok {
			if next, sameKind := after.Phases[i].(*FinalizationPhase); sameKind {
				g.finalization(final, next, mutable)
				continue
			}
		}
		if !mutable {
			g.historicalPhase(phase, after.Phases[i])
			continue
		}
		g.phase(phase, after.Phases[i])
	}
}

func (g *changeGuard) historicalPhase(before, after Phase) {
	if !g.same(reflect.TypeOf(before), reflect.TypeOf(after), "historical phase kind") {
		return
	}
	switch before := before.(type) {
	case *ImplementationPhase:
		next := after.(*ImplementationPhase)
		oldBody, newBody := *before, *next
		oldBody.Execution, newBody.Execution = AgentExecution{}, AgentExecution{}
		g.same(oldBody, newBody, "historical phase")
		g.execution(before.Execution, next.Execution)
	case *ReviewPhase:
		next := after.(*ReviewPhase)
		oldBody, newBody := *before, *next
		oldBody.Steps, newBody.Steps = nil, nil
		g.same(oldBody, newBody, "historical review")
		if !g.same(len(before.Steps), len(next.Steps), "historical review attempts") {
			return
		}
		for i, step := range before.Steps {
			g.step(step, next.Steps[i], false)
		}
	case *ExternalReviewPhase:
		g.externalReview(before, after.(*ExternalReviewPhase), false)
	default:
		g.same(before, after, "historical phase")
	}
}

func (g *changeGuard) phase(before, after Phase) {
	if !g.same(reflect.TypeOf(before), reflect.TypeOf(after), "phase kind") {
		return
	}
	switch before := before.(type) {
	case *PreparationPhase:
		next := after.(*PreparationPhase)
		g.assigned(before.Context, next.Context, "prepared context")
		g.assigned(before.EstablishedAt, next.EstablishedAt, "preparation time")
	case *ImplementationPhase:
		next := after.(*ImplementationPhase)
		g.same(before.Input, next.Input, "implementation input")
		g.same(before.TargetFindings, next.TargetFindings, "repair targets")
		g.execution(before.Execution, next.Execution)
		g.assigned(before.Completion, next.Completion, "completion handoff")
		if before.Completion == nil && next.Completion != nil && !running(&before.Execution) {
			g.same(before.Completion, next.Completion, "terminal execution handoff")
		}
		g.assigned(before.Output, next.Output, "captured output")
	case *ReviewPhase:
		g.review(before, after.(*ReviewPhase))
	case *PublicationPhase:
		next := after.(*PublicationPhase)
		g.same(before.Plan, next.Plan, "publication intent")
		g.assigned(before.Consent, next.Consent, "publication authorization")
		g.assigned(before.Delivery, next.Delivery, "observed delivery")
	case *ExternalReviewPhase:
		g.externalReview(before, after.(*ExternalReviewPhase), true)
	}
}

func (g *changeGuard) externalReview(before, after *ExternalReviewPhase, active bool) {
	g.same(before.Publication, after.Publication, "external publication")
	if len(after.BranchSyncs) < len(before.BranchSyncs) {
		g.errors = append(g.errors, fmt.Errorf("branch syncs are append-only"))
		return
	}
	if !active {
		g.same(len(before.BranchSyncs), len(after.BranchSyncs), "historical branch syncs")
	}
	for i := range before.BranchSyncs {
		g.branchSync(&before.BranchSyncs[i], &after.BranchSyncs[i], active && i == len(before.BranchSyncs)-1 && !before.BranchSyncs[i].settled())
	}
}

func (g *changeGuard) execution(before, after AgentExecution) {
	old, current := before, after
	old.Process, current.Process = nil, nil
	old.Session, current.Session = nil, nil
	old.Usage, current.Usage, old.Cost, current.Cost = nil, nil, nil, nil
	old.UsageCapture, current.UsageCapture = UsageCapture{}, UsageCapture{}
	old.Duration, current.Duration = 0, 0
	old.ChildPID, current.ChildPID, old.SupervisorPID, current.SupervisorPID = 0, 0, 0, 0
	g.same(old, current, "execution launch identity")
	if before.ChildPID != 0 {
		g.same(before.ChildPID, after.ChildPID, "native child PID")
	}
	if before.SupervisorPID != 0 {
		g.same(before.SupervisorPID, after.SupervisorPID, "native supervisor PID")
	}
	g.assigned(before.Session, after.Session, "native session")
	g.assigned(before.Usage, after.Usage, "captured usage")
	g.assigned(before.Cost, after.Cost, "captured cost")
	if before.Duration != 0 {
		g.same(before.Duration, after.Duration, "measured duration")
	}
	if before.UsageCapture.Status == UsageCaptured {
		g.same(before.UsageCapture, after.UsageCapture, "captured usage diagnostics")
	}
	if !running(&before) {
		g.same(before.Process, after.Process, "terminal process result")
		return
	}
	if _, impossible := after.Process.(*StartFailedProcess); impossible {
		g.same(before.Process, after.Process, "started process")
	}
}

func (g *changeGuard) review(before, after *ReviewPhase) {
	g.same(before.Contract, after.Contract, "review contract selection")
	g.same(before.Scope, after.Scope, "review scope")
	g.same(before.StartedAt, after.StartedAt, "review entry time")
	if len(after.Steps) < len(before.Steps) {
		g.errors = append(g.errors, fmt.Errorf("review attempts are append-only"))
		return
	}
	for i, step := range before.Steps {
		g.step(step, after.Steps[i], i == len(before.Steps)-1)
	}
}

func (g *changeGuard) step(before, after ReviewStep, active bool) {
	if !g.same(reflect.TypeOf(before), reflect.TypeOf(after), "review attempt kind") {
		return
	}
	oldSlot, _ := reviewSlot(before)
	newSlot, _ := reviewSlot(after)
	g.same(oldSlot, newSlot, "review slot")
	switch before := before.(type) {
	case *CheckStep:
		next := after.(*CheckStep)
		if active {
			g.assigned(before.Result, next.Result, "check result")
			g.assigned(before.Waiver, next.Waiver, "check waiver")
		} else {
			g.same(before, next, "historical review attempt")
		}
	case *AgentReviewStep:
		next := after.(*AgentReviewStep)
		g.execution(before.Execution, next.Execution)
		g.report(before.Report, next.Report, active && running(&before.Execution))
	case *ManualReviewStep:
		g.assessment(before.Decision, after.(*ManualReviewStep).Decision, active)
	}
}

func (g *changeGuard) assessment(before, after ManualAssessment, active bool) {
	if before == nil && active {
		return
	}
	old, ok := before.(*RequestedChanges)
	if !ok || !g.same(reflect.TypeOf(before), reflect.TypeOf(after), "manual assessment kind") {
		g.same(before, after, "manual assessment")
		return
	}
	next := after.(*RequestedChanges)
	g.same(old.At, next.At, "manual assessment time")
	g.findings(old.Findings, next.Findings)
	g.same(len(old.Findings), len(next.Findings), "manual assessment findings")
}

func (g *changeGuard) report(before, after *ReviewReport, active bool) {
	if before == nil {
		if after != nil && !active {
			g.same(before, after, "terminal reviewer handoff")
		}
		return
	}
	if after == nil {
		g.same(before, after, "review report")
		return
	}
	if before.Evidence != "" {
		g.same(before.Evidence, after.Evidence, "report evidence")
	}
	g.findings(before.Findings, after.Findings)
	g.prefix(before.Verifications, after.Verifications, "local verifications")
	if !active {
		g.same(len(before.Findings), len(after.Findings), "terminal report findings")
		g.same(before.Verifications, after.Verifications, "terminal report verifications")
		g.same(before.Evidence, after.Evidence, "terminal report evidence")
	}
}

func (g *changeGuard) findings(before, after []Finding) {
	if len(after) < len(before) {
		g.errors = append(g.errors, fmt.Errorf("findings are append-only"))
		return
	}
	for i, old := range before {
		current := after[i]
		old.Dispositions, current.Dispositions = nil, nil
		g.same(old, current, "reported finding")
		g.prefix(before[i].Dispositions, after[i].Dispositions, "finding disposition history")
	}
}

func (g *changeGuard) finalization(before, after *FinalizationPhase, mutable bool) {
	g.same(before.Acceptance, after.Acceptance, "delivery acceptance")
	if mutable {
		g.assigned(before.Closure, after.Closure, "source closure")
	} else {
		g.same(before.Closure, after.Closure, "source closure")
	}
	g.prefix(before.Cleanup, after.Cleanup, "cleanup observations")
}
