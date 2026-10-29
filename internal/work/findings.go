package work

import (
	"fmt"
	"time"
)

func (f *Finding) blockingAt(cutoff time.Time) bool {
	if f.Classification == FindingBlocking {
		return true
	}
	for _, disposition := range f.Dispositions {
		if promoted, ok := disposition.(*PromotedFinding); ok && promoted != nil && observedBy(promoted.At, cutoff) {
			return true
		}
	}
	return false
}

func (w *Workflow) findingOutput(ref FindingRef) (OutputRef, error) {
	p, err := phaseAs[*ReviewPhase](w, ref.Phase)
	if err != nil || p == nil {
		return OutputRef{}, fmt.Errorf("finding source requires review")
	}
	return p.Scope.Output, nil
}

func (w *Workflow) sameOutput(a, b OutputRef) bool {
	left, err := w.Output(a)
	if err != nil {
		return false
	}
	right, err := w.Output(b)
	if err != nil {
		return false
	}
	return left.CodeIdentity == right.CodeIdentity
}

// RepairPhases derives reverse navigation from the targets owned by each effort.
// A pending repair is retained even before output capture or subsequent verification.
func (w *Workflow) RepairPhases(ref FindingRef) []int {
	var result []int
	for i, phase := range w.Phases {
		p, ok := phase.(*ImplementationPhase)
		if !ok || p == nil {
			continue
		}
		for _, target := range p.TargetFindings {
			if sameFinding(target, ref) {
				result = append(result, i)
				break
			}
		}
	}
	return result
}

// LatestLocalVerification returns explicit effective evidence, not a guess from
// overall review success or PR merge. Later explicit reversal wins.
func (w *Workflow) LatestLocalVerification(ref FindingRef) *FindingVerification {
	if _, err := w.Finding(ref); err != nil {
		return nil
	}
	var latest *FindingVerification
	for i, phase := range w.Phases {
		p, ok := phase.(*ReviewPhase)
		if !ok || p == nil {
			continue
		}
		outcome, err := w.ReviewOutcome(ReviewRef{Phase: i})
		if err != nil {
			continue
		}
		contract, err := element(w.ReviewContracts, p.Contract)
		if err != nil {
			continue
		}
		for _, step := range effectiveSteps(p.Steps, len(contract.Slots)) {
			if !verificationObserved(step) {
				continue
			}
			verifications := stepVerifications(step)
			for n := range verifications {
				if sameFinding(verifications[n].Finding, ref) && (verifications[n].Resolution == FindingUnresolved || passed(outcome)) {
					latest = &verifications[n]
				}
			}
		}
	}
	return latest
}

func verificationObserved(step ReviewStep) bool {
	switch step := step.(type) {
	case *AgentReviewStep:
		return step != nil && step.Report != nil && successful(&step.Execution)
	case *ManualReviewStep:
		if step != nil {
			approval, ok := step.Decision.(*ApprovedReview)
			return ok && approval != nil
		}
	}
	return false
}
