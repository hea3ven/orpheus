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
	if !v.prior(p.Review.Phase, "publication review") {
		return
	}
	review, err := phaseAs[*ReviewPhase](v.w, p.Review.Phase)
	v.check(err == nil && review != nil, "publication requires review occurrence")
	if err != nil || review == nil {
		return
	}
	var cutoff time.Time
	if p.Consent != nil {
		cutoff = p.Consent.GrantedAt
	}
	outcome, err := v.w.reviewOutcome(p.Review, cutoff)
	v.check(err == nil && passed(outcome), "publication requires passed exact review at authorization: %v", err)
	v.check(v.w.Publication != nil, "publication content is required")
	v.fixedDestination(&p.Destination)
	code, err := v.w.Output(review.Scope.Output)
	v.check(err == nil && code != nil, "Git publication requires retained code output")
	v.publicationInput(p.Input, code)
	if p.Input.Provenance == InputSynchronized {
		_, direct := p.Target.(*DirectMergeTarget)
		v.check(direct && p.PreparedFrom != nil, "synchronized input requires direct public continuation")
	}
	v.text(p.Comparison.Relationship, "publication input relationship")
	v.check((p.Comparison.Relationship == "reviewed") == (p.Input.Provenance == InputReviewed), "publication comparison cannot claim review approval for operator input")
	v.target(p.Target)
	if p.Consent != nil {
		v.check(!p.Consent.GrantedAt.IsZero(), "publication consent requires observed grant time")
		v.text(p.Consent.Evidence, "publication authorization evidence")
	}
	v.publicationOperation(p)
	v.privateHistoryRelease(p)
	v.directPublication(p)
	v.publicationDelivery(p)
}

func (v *validator) privateHistoryRelease(p *PublicationPhase) {
	if p.PrivateRefsReleasedAt == nil {
		return
	}
	if _, pr := p.Target.(*PullRequestTarget); pr {
		d, ok := p.Delivery.(*PullRequestDelivery)
		v.check(ok && d != nil, "private history release requires observed PR delivery")
		if ok && d != nil {
			v.check(!p.PrivateRefsReleasedAt.Before(d.ObservedAt), "private history released before delivery")
		}
		return
	}
	v.check(p.Operation != nil && p.Operation.Public != nil, "private history release requires recorded public preparation")
	if p.Operation != nil {
		v.check(!p.PrivateRefsReleasedAt.Before(p.Operation.StartedAt), "private history release predates preparation")
	}
}

func (v *validator) directPublication(p *PublicationPhase) {
	if p.PreparedFrom == nil && p.DirectSync == nil && p.SyncFrom == nil && p.Integration == nil {
		return
	}
	_, direct := p.Target.(*DirectMergeTarget)
	v.check(direct, "direct checkpoints require direct target")
	prepared := v.w.PublicPreparation(p)
	if p.PreparedFrom != nil {
		v.check(p.PreparedFrom.Phase >= 0 && p.PreparedFrom.Phase < v.phase, "public continuation requires earlier preparation")
		v.check(p.Operation == nil, "public continuation cannot repackage")
	}
	v.check(prepared != nil && prepared.Operation != nil && prepared.Operation.Public != nil, "direct continuation requires durable public preparation")
	if prepared != nil && prepared.Operation != nil {
		v.check(prepared.Review == p.Review, "public continuation preserves review evidence")
	}
	if p.SyncFrom != nil {
		v.check(p.SyncFrom.Phase >= 0 && p.SyncFrom.Phase < v.phase && p.DirectSync == nil, "sync continuation requires earlier owned sync")
		sync := v.w.PublicSync(p)
		v.check(sync != nil && sync.Result != nil, "sync continuation requires recorded public result")
	}
	v.directSync(p.DirectSync, prepared)
	v.directIntegration(p, prepared)
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
		_, pr := p.Target.(*PullRequestTarget)
		v.check(pr && op.Public != nil && !op.PRCreationStartedAt.Before(op.StartedAt), "PR creation intent requires recorded public output and ordered time")
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
	v.check(input.Provenance == InputReviewed || input.Provenance == InputOperator || input.Provenance == InputSynchronized, "publication input requires honest provenance")
	if reviewed != nil {
		matches := input.Code.CodeIdentity == reviewed.CodeIdentity
		v.check(matches == (input.Provenance == InputReviewed), "operator-selected publication input cannot claim review approval")
	}
}

func (v *validator) publicationDelivery(p *PublicationPhase) {
	if p.Delivery != nil {
		v.check(p.Consent != nil, "delivery requires explicit exact-plan consent")
		if _, direct := p.Target.(*DirectMergeTarget); direct {
			v.check(p.Integration != nil && p.Integration.Result != nil && p.Integration.InvalidatedAt == nil, "direct delivery requires recorded integration")
		}
		if p.Input.Code.Tree != "" {
			v.delivery(p.Target, p.Delivery, p.Input.Code.Tree)
		}
		if p.Consent != nil {
			v.check(!deliveryTime(p.Delivery).Before(p.Consent.GrantedAt), "delivery observation predates authorization")
		}
	}
}

func (v *validator) directSync(sync *DirectPublicationSync, prepared *PublicationPhase) {
	if sync == nil {
		return
	}
	v.check(!sync.StartedAt.IsZero(), "direct sync requires intent time")
	v.code(sync.Source)
	v.fixedDestination(&sync.Destination)
	v.check(prepared != nil && prepared.PrivateRefsReleasedAt != nil, "direct sync requires private ref release")
	if prepared != nil && prepared.PrivateRefsReleasedAt != nil {
		v.check(!sync.StartedAt.Before(*prepared.PrivateRefsReleasedAt), "sync predates private ref release")
	}
	if sync.Execution != nil {
		v.execution(sync.Execution, v.live && v.phase == len(v.w.Phases)-1)
	}
	if sync.Result != nil {
		v.code(*sync.Result)
		if sync.Execution != nil {
			v.check(successful(sync.Execution), "recorded sync output requires successful resolver exit")
		}
	}

}

func (v *validator) directIntegration(p, prepared *PublicationPhase) {
	if integration := p.Integration; integration != nil {
		v.check(!integration.StartedAt.IsZero() && p.Consent != nil, "integration requires consent and intent time")
		if p.Consent != nil {
			v.check(!integration.StartedAt.Before(p.Consent.GrantedAt), "integration predates consent")
		}
		sync := v.w.PublicSync(p)
		v.check(sync != nil && sync.Result != nil && *sync.Result == integration.Source && sync.Destination == integration.Destination, "integration requires recorded synchronized source")
		v.check(integration.Destination == p.Destination && (integration.Source == p.Input.Code.CodeIdentity || (prepared != nil && prepared.Operation != nil && prepared.Operation.Public != nil && integration.Source == *prepared.Operation.Public && p.Input == prepared.Input)), "integration must match exact consent")
		if integration.Result != nil {
			v.code(*integration.Result)
			v.check(integration.Result.Tree == integration.Source.Tree, "integration preserves authorized sync result")
		}
	}
	if d, ok := p.Delivery.(*DirectDelivery); ok && d != nil && p.Integration != nil {
		v.check(p.Integration.Result != nil && *p.Integration.Result == d.Destination, "delivery requires recorded integration")
		if prepared != nil && prepared.Operation != nil && prepared.Operation.Public != nil {
			v.check(d.PublicCommit == prepared.Operation.Public.Commit, "delivery identifies original prepared public commit")
		}
	}
}
