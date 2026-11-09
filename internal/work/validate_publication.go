package work

import (
	"net/url"
	"strings"
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
	if ref := p.Plan.FeatureCompletion; ref != nil && v.prior(ref.Phase, "publication feature completion") {
		effort, err := phaseAs[*ImplementationPhase](v.w, ref.Phase)
		v.check(err == nil && effort != nil && effort.Completion == v.w.FeatureCompletion(), "publication narrative must select original feature completion")
	}
	v.fixedDestination(&p.Plan.Destination)
	code, err := v.w.Output(review.Scope.Output)
	v.check(err == nil && code != nil, "Git publication requires retained code output")
	v.publicationInput(p.Plan.Input, code)
	v.text(p.Plan.Comparison.Relationship, "publication input relationship")
	v.check((p.Plan.Comparison.Relationship == "reviewed") == (p.Plan.Input.Provenance == InputReviewed), "publication comparison cannot claim review approval for operator input")
	v.target(p.Plan.Target)
	if p.Consent != nil {
		v.check(!p.Consent.GrantedAt.IsZero(), "publication consent requires observed grant time")
		v.text(p.Consent.Evidence, "publication authorization evidence")
	}
	v.publicationOperation(p)
	v.privateHistoryRelease(p)
	if p.Delivery != nil {
		v.check(p.Consent != nil, "delivery requires explicit exact-plan consent")
		if code != nil {
			v.delivery(p.Plan.Target, p.Delivery, p.Plan.Input.Code.Tree)
		}
		if p.Consent != nil {
			v.check(!deliveryTime(p.Delivery).Before(p.Consent.GrantedAt), "delivery observation predates authorization")
		}
	}
}

func (v *validator) privateHistoryRelease(p *PublicationPhase) {
	if p.PrivateRefsReleasedAt != nil {
		d, ok := p.Delivery.(*PullRequestDelivery)
		v.check(ok && d != nil, "private history release requires observed PR delivery")
		if ok && d != nil {
			v.check(!p.PrivateRefsReleasedAt.Before(d.ObservedAt), "private history released before delivery")
		}
	}
}

func (v *validator) publicationOperation(p *PublicationPhase) {
	op := p.Operation
	if op == nil {
		return
	}
	v.check(p.Consent != nil, "publication operation requires exact-plan consent")
	v.check(!op.StartedAt.IsZero() && strings.HasPrefix(op.Ref, "refs/orpheus/") && op.Parent != "", "publication operation requires time, owned ref and parent")
	if p.Consent != nil {
		v.check(!op.StartedAt.Before(p.Consent.GrantedAt), "publication operation predates consent")
	}
	target, ok := p.Plan.Target.(*PullRequestTarget)
	v.check(ok && target != nil, "public packaging requires PR target")
	prepared := v.w.Context()
	if prepared == nil {
		return
	}
	v.check(op.Parent == prepared.Baseline.Commit, "public packaging must use the recorded task baseline")

	if op.Public != nil {
		v.code(*op.Public)
		// The task branch retains public output; private refs may be released.
	}
	if op.PRCreationStartedAt != nil {
		v.check(op.Public != nil && !op.PRCreationStartedAt.Before(op.StartedAt), "PR creation intent requires recorded public output and ordered time")
	}
	if d, ok := p.Delivery.(*PullRequestDelivery); ok && d != nil {
		v.check(op.Public != nil && op.Public.Commit == d.PublicCommit && op.Public.Tree == d.Tree, "delivery must match packaged public commit")
	}
}

func (v *validator) target(target PublicationTarget) {
	switch target := target.(type) {
	case *PullRequestTarget:
		if target == nil {
			v.check(false, "publication target is nil")
			return
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
		v.check(err != nil || delivery.PullRequest != pr, "merged PR requires reconciliation before further external review")
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
		v.check(delivery.PublicCommit != "" && delivery.Head == delivery.PublicCommit && delivery.Tree == tree, "remote PR observation must preserve exact authorized input")
		v.check(!delivery.ObservedAt.IsZero(), "remote delivery requires observation time")
		v.text(delivery.Evidence, "remote delivery evidence")
	case *DirectDelivery:
		if delivery == nil {
			v.check(false, "delivery is nil")
			return
		}
		planned, ok := target.(*DirectMergeTarget)
		v.check(ok && planned != nil, "direct delivery disagrees with planned flow")
		v.check(delivery.PublicCommit != "" && delivery.Destination.Commit != "" && delivery.Destination.Tree == tree, "direct delivery requires observed destination push preserving authorized input")
		v.check(!delivery.ObservedAt.IsZero(), "destination push requires observation time")
		v.text(delivery.Evidence, "destination push evidence")
	default:
		v.check(false, "invalid delivery alternative")
	}
}

func (v *validator) publicationInput(input PublicationInput, reviewed *CodeOutput) {
	v.code(input.Code.CodeIdentity)
	v.check(strings.HasPrefix(input.Code.RetainedRef, "refs/"), "publication input requires an owned retention ref")
	v.check(input.Provenance == InputReviewed || input.Provenance == InputOperator, "publication input requires honest provenance")
	if reviewed != nil {
		matches := input.Code.CodeIdentity == reviewed.CodeIdentity
		v.check(matches == (input.Provenance == InputReviewed), "operator-selected publication input cannot claim review approval")
	}
}
