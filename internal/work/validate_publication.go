package work

import (
	"net/url"
	"time"
)

func (v *validator) publication(p *PublicationPhase) {
	if p == nil {
		v.check(false, "publication is required")
		return
	}
	v.prepared()
	if !v.prior(p.Plan.Review.Phase, "publication review") {
		return
	}
	review, err := phaseAs[*ReviewPhase](v.w, p.Plan.Review.Phase)
	v.check(err == nil && review != nil, "publication requires review occurrence")
	if err != nil || review == nil {
		return
	}
	var cutoff time.Time
	if p.Consent != nil {
		cutoff = p.Consent.GrantedAt
	}
	outcome, err := v.w.reviewOutcome(p.Plan.Review, cutoff)
	v.check(err == nil && passed(outcome), "publication requires passed exact review at authorization: %v", err)
	v.text(p.Plan.Content, "publication content")
	destination, err := v.w.ReviewDestination(p.Plan.Review)
	v.check(err == nil, "publication review destination: %v", err)
	v.destination(destination)
	code, err := v.w.Output(review.Scope.Output)
	v.check(err == nil && code != nil, "Git publication requires retained code output")
	v.target(p.Plan.Target)
	if target, pr := p.Plan.Target.(*PullRequestTarget); pr && target != nil && target.Existing != nil {
		v.unmergedPR(*target.Existing)
	}
	if p.Consent != nil {
		v.check(!p.Consent.GrantedAt.IsZero(), "publication consent requires observed grant time")
		v.text(p.Consent.Evidence, "publication authorization evidence")
	}
	if p.Delivery != nil {
		v.check(p.Consent != nil, "delivery requires explicit exact-plan consent")
		if code != nil {
			v.delivery(p.Plan.Target, p.Delivery, code.Tree)
		}
		if p.Consent != nil {
			v.check(!deliveryTime(p.Delivery).Before(p.Consent.GrantedAt), "delivery observation predates authorization")
		}
	}
}

func (v *validator) target(target PublicationTarget) {
	switch target := target.(type) {
	case *PullRequestTarget:
		if target == nil {
			v.check(false, "publication target is nil")
			return
		}
		v.check((target.Existing == nil) == (target.ExpectedHead == ""), "PR update requires existing target and expected head together")
		if target.Existing != nil {
			v.pullRequest(*target.Existing)
		}
	case *DirectMergeTarget:
		v.check(target != nil, "publication target is nil")
	default:
		v.check(false, "publication target is required")
	}
}

func (v *validator) unmergedPR(pr PullRequestRef) {
	for _, phase := range v.w.Phases[:v.phase] {
		p, ok := phase.(*FinalizationPhase)
		if !ok || p == nil {
			continue
		}
		merged, ok := p.Acceptance.(*PRAcceptance)
		if !ok || merged == nil {
			continue
		}
		delivery, err := v.w.externalDelivery(merged.ExternalReview)
		v.check(err != nil || delivery.PullRequest != pr, "merged PR requires reconciliation, not an update retry")
	}
}

func (v *validator) pullRequest(pr PullRequestRef) {
	parsed, err := url.Parse(pr.URL)
	v.check(err == nil && parsed.Host != "" && (parsed.Scheme == "https" || parsed.Scheme == "http"), "PR requires native HTTP(S) URL")
}

func (v *validator) delivery(target PublicationTarget, delivery Delivery, tree string) {
	switch delivery := delivery.(type) {
	case *PullRequestDelivery:
		if delivery == nil {
			v.check(false, "delivery is nil")
			return
		}
		planned, ok := target.(*PullRequestTarget)
		v.check(ok && planned != nil, "PR delivery disagrees with planned flow")
		v.pullRequest(delivery.PullRequest)
		v.check(delivery.PublicCommit != "" && delivery.Head == delivery.PublicCommit && delivery.Tree == tree, "remote PR observation must preserve exact reviewed output")
		v.check(!delivery.ObservedAt.IsZero(), "remote delivery requires observation time")
		v.text(delivery.Evidence, "remote delivery evidence")
		if ok && planned != nil {
			v.check(planned.Existing == nil || *planned.Existing == delivery.PullRequest, "delivered PR must match intended target")
			v.check(planned.ExpectedHead == delivery.PreviousHead, "delivery must match expected previous public head")
		}
	case *DirectDelivery:
		if delivery == nil {
			v.check(false, "delivery is nil")
			return
		}
		planned, ok := target.(*DirectMergeTarget)
		v.check(ok && planned != nil, "direct delivery disagrees with planned flow")
		v.check(delivery.PublicCommit != "" && delivery.Destination.Commit != "" && delivery.Destination.Tree == tree, "direct delivery requires observed destination push preserving reviewed output")
		v.check(!delivery.ObservedAt.IsZero(), "destination push requires observation time")
		v.text(delivery.Evidence, "destination push evidence")
	default:
		v.check(false, "invalid delivery alternative")
	}
}
