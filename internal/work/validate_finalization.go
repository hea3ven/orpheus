package work

import (
	"strings"
	"time"
)

func (v *validator) finalization(p *FinalizationPhase) {
	if p == nil {
		v.check(false, "finalization is required")
		return
	}
	observed := v.acceptance(p.Acceptance)
	if p.Closure != nil {
		v.check(!p.Closure.At.IsZero() && !p.Closure.At.Before(observed), "closure requires time after delivery acceptance")
		v.check(strings.TrimSpace(p.Closure.Reason) != "", "closure requires reason")
		v.text(p.Closure.Evidence, "source closure evidence")
	}
	for _, observation := range p.Cleanup {
		v.check(p.Closure != nil, "cleanup requires observed source closure")
		v.cleanup(observation, p.Closure)
	}
}

func (v *validator) acceptance(acceptance Acceptance) time.Time {
	switch acceptance := acceptance.(type) {
	case *PRAcceptance:
		if acceptance == nil {
			v.check(false, "acceptance is nil")
			return time.Time{}
		}
		return v.prAcceptance(acceptance)
	case *DirectAcceptance:
		if acceptance == nil {
			v.check(false, "acceptance is nil")
			return time.Time{}
		}
		if !v.prior(acceptance.Publication.Phase, "direct acceptance") {
			return time.Time{}
		}
		publication, err := phaseAs[*PublicationPhase](v.w, acceptance.Publication.Phase)
		v.check(err == nil && publication != nil, "direct acceptance requires publication")
		if err != nil || publication == nil {
			return time.Time{}
		}
		delivery, ok := publication.Delivery.(*DirectDelivery)
		v.check(ok && delivery != nil, "direct acceptance requires observed destination push")
		if !ok || delivery == nil {
			return time.Time{}
		}
		return delivery.ObservedAt
	default:
		v.check(false, "explicit delivery acceptance is required")
		return time.Time{}
	}
}

func (v *validator) prAcceptance(accepted *PRAcceptance) time.Time {
	if !v.prior(accepted.ExternalReview, "PR acceptance") {
		return time.Time{}
	}
	external, err := phaseAs[*ExternalReviewPhase](v.w, accepted.ExternalReview)
	v.check(err == nil && external != nil, "PR acceptance requires external review")
	if err != nil || external == nil {
		return time.Time{}
	}
	delivery, err := v.w.externalDelivery(accepted.ExternalReview)
	v.check(err == nil, "PR acceptance requires delivered PR: %v", err)
	if err != nil {
		return time.Time{}
	}
	boundary := delivery.ObservedAt
	for i := range external.BranchSyncs {
		sync := &external.BranchSyncs[i]
		v.check(sync.settled(), "PR acceptance requires settled branch sync")
		if sync.settledAt().After(boundary) {
			boundary = sync.settledAt()
		}
	}
	v.check(!accepted.ObservedAt.IsZero() && !accepted.ObservedAt.Before(boundary), "PR merge confirmation predates delivery or branch synchronization")
	v.text(accepted.Evidence, "PR merge confirmation")
	v.currentPRPublication(accepted.ExternalReview, external.Publication, delivery.PullRequest)
	return accepted.ObservedAt
}

func (v *validator) currentPRPublication(external int, publication PublicationRef, pr PullRequestRef) {
	for i := publication.Phase + 1; i < v.phase; i++ {
		switch p := v.w.Phases[i].(type) {
		case *PublicationPhase:
			if p != nil {
				if delivery, ok := p.Delivery.(*PullRequestDelivery); ok && delivery != nil && delivery.PullRequest == pr {
					v.check(false, "PR acceptance references a superseded publication")
				}
			}
		case *ExternalReviewPhase:
			if i > external && p != nil {
				if delivery, err := v.w.externalDelivery(i); err == nil && delivery.PullRequest == pr {
					v.check(false, "PR acceptance references a superseded external review")
				}
			}
		}
	}
}

func (v *validator) cleanup(observation CleanupObservation, closure *TaskClosure) {
	var path string
	var at time.Time
	switch observation := observation.(type) {
	case *WorktreeRemoved:
		if observation == nil {
			v.check(false, "cleanup observation is nil")
			return
		}
		path, at = observation.Path, observation.At
		v.text(observation.Evidence, "worktree removal evidence")
	case *WorktreeRetained:
		if observation == nil {
			v.check(false, "cleanup observation is nil")
			return
		}
		path, at = observation.Path, observation.At
		v.check(strings.TrimSpace(observation.Reason) != "", "retained worktree requires reason")
		v.text(observation.Evidence, "retained worktree evidence")
	default:
		v.check(false, "cleanup observation is required")
		return
	}
	context := v.w.Context()
	v.check(context != nil && context.Directory == path, "cleanup must observe the exact prepared work directory")
	v.check(!at.IsZero() && (closure == nil || !at.Before(closure.At)), "cleanup observation requires time after closure")
}
