package work

import (
	"fmt"
	"time"
)

// FinalizationContext describes the prerequisite at one finalization occurrence.
// It is computed from the preceding phase and is not part of the stored model.
type FinalizationContext struct {
	Publication PublicationRef
	Outcome     *PROutcome
	PullRequest PullRequestRef
	ObservedAt  time.Time
}

// CloseReason is the source closure reason for this prerequisite.
func (c FinalizationContext) CloseReason() string {
	if c.Outcome != nil {
		return CloseReasonPRMerged
	}
	return "destination_published"
}

// CloseReasonPRMerged identifies closure after a recorded pull request is merged.
const CloseReasonPRMerged = "pr_merged"

// FinalizationContext resolves the prerequisite in the workflow containing phase.
func (w *Workflow) FinalizationContext(phase int) (FinalizationContext, error) {
	final, err := phaseAs[*FinalizationPhase](w, phase)
	if err != nil || final == nil || phase == 0 {
		return FinalizationContext{}, fmt.Errorf("finalization requires a preceding phase")
	}
	switch p := w.Phases[phase-1].(type) {
	case *ExternalReviewPhase:
		if p == nil || p.Outcome == nil || p.Outcome.State != PRMerged {
			return FinalizationContext{}, fmt.Errorf("finalization requires recorded merge outcome")
		}
		if p.Publication.Phase >= phase-1 {
			return FinalizationContext{}, fmt.Errorf("merged PR requires earlier publication")
		}
		delivery, err := w.externalDelivery(phase - 1)
		if err != nil {
			return FinalizationContext{}, err
		}
		outcome := *p.Outcome
		return FinalizationContext{Publication: p.Publication, Outcome: &outcome, PullRequest: delivery.PullRequest, ObservedAt: outcome.ObservedAt}, nil
	case *PublicationPhase:
		if p != nil {
			if delivery, ok := p.Delivery.(*DirectDelivery); ok && delivery != nil {
				return FinalizationContext{Publication: PublicationRef{Phase: phase - 1}, ObservedAt: delivery.ObservedAt}, nil
			}
		}
		return FinalizationContext{}, fmt.Errorf("finalization requires observed destination push")
	default:
		return FinalizationContext{}, fmt.Errorf("finalization must follow external review or direct publication")
	}
}
