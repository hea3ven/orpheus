package work

import "time"

func (v *validator) finalization(p *FinalizationPhase) {
	if p == nil {
		v.check(false, "finalization is required")
		return
	}
	prerequisite, err := v.w.FinalizationContext(v.phase)
	v.check(err == nil, "%v", err)
	if err == nil && prerequisite.Outcome != nil {
		v.currentPRPublication(prerequisite.Publication, prerequisite.PullRequest)
	}
	observed := prerequisite.ObservedAt
	if p.ClosureIntentAt != nil {
		v.check(!p.ClosureIntentAt.IsZero() && !p.ClosureIntentAt.Before(observed), "closure intent predates finalization prerequisite")
		observed = *p.ClosureIntentAt
	}
	if p.Closure != nil {
		v.check(p.ClosureIntentAt != nil, "source closure requires recorded intent")
		v.check(!p.Closure.At.IsZero() && !p.Closure.At.Before(observed), "closure requires time after finalization prerequisite and intent")
		v.text(p.Closure.Reason, "closure reason")
	}
	for _, observation := range p.Cleanup {
		v.check(p.Closure != nil, "cleanup requires observed source closure")
		v.cleanup(observation, p.Closure)
	}
}

func (v *validator) currentPRPublication(publication PublicationRef, pr PullRequestRef) {
	for i := publication.Phase + 1; i < v.phase-1; i++ {
		p, ok := v.w.Phases[i].(*PublicationPhase)
		if ok && p != nil {
			if delivery, ok := p.Delivery.(*PullRequestDelivery); ok && delivery != nil && delivery.PullRequest == pr {
				v.check(false, "finalization references a superseded publication")
			}
		}
	}
}

func (v *validator) cleanup(observation CleanupObservation, closure *TaskClosure) {
	var at time.Time
	switch observation := observation.(type) {
	case *WorktreeRemoved:
		if observation == nil {
			v.check(false, "cleanup observation is nil")
			return
		}
		at = observation.At
		v.text(observation.Evidence, "worktree removal evidence")
	case *WorktreeRetained:
		if observation == nil {
			v.check(false, "cleanup observation is nil")
			return
		}
		at = observation.At
		v.text(observation.Reason, "retained worktree reason")
		v.text(observation.Evidence, "retained worktree evidence")
	default:
		v.check(false, "cleanup observation is required")
		return
	}
	v.prepared()
	v.check(!at.IsZero() && (closure == nil || !at.Before(closure.At)), "cleanup observation requires time after closure")
}
