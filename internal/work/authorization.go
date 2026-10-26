package work

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

// AuthorizePublication records direct authorization of the current immutable plan.
// Repeats keep the first grant. The intent digest prevents copying that grant to a
// changed plan, another occurrence, another task, or a workflow after reset.
// This pure domain update neither prompts nor performs publication effects.
func (s *State) AuthorizePublication(workflow int, ref PublicationRef, at time.Time, evidence string) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if workflow < 0 || workflow != len(s.Workflows)-1 || s.ActiveWorkflow() == nil {
		return fmt.Errorf("authorization requires active workflow")
	}
	w := &s.Workflows[workflow]
	if ref.Phase < 0 || ref.Phase != len(w.Phases)-1 {
		return fmt.Errorf("authorization requires current publication occurrence")
	}
	p, err := phaseAs[*PublicationPhase](w, ref.Phase)
	if err != nil || p == nil {
		return fmt.Errorf("authorization requires publication")
	}
	if p.Consent != nil {
		return nil
	}
	c := &PublicationConsent{GrantedAt: at, Evidence: evidence}
	v := validator{}
	v.text(evidence, "publication authorization evidence")
	outcome, err := w.reviewOutcome(p.Plan.Review, at)
	v.check(!at.IsZero() && err == nil && passed(outcome), "authorization must follow completed review at grant time")
	if err := v.err(); err != nil {
		return err
	}
	c.IntentDigest, err = s.intentDigest(workflow, ref.Phase, p, c)
	if err != nil {
		return err
	}
	p.Consent = c
	return nil
}

// authorizationMaterial is transient digest input, not another stored work model.
type authorizationMaterial struct {
	RepositoryID, TaskID                            string
	Workflow, Publication, Review, Output, Contract int
	DeltaPhase                                      *int
	Baseline                                        CodeIdentity
	TaskBranch                                      string
	Requirements                                    string
	Destination                                     *Destination
	Code                                            *CodeOutput
	Delta                                           *CodeOutput
	Policy                                          ReviewContract
	Flow                                            string
	Target                                          PublicationTarget
	Content                                         string
	GrantedAt                                       time.Time
	Evidence                                        string
}

// intentDigest binds actual output/scope and owner, not just coincident indexes.
func (s *State) intentDigest(wi, pi int, p *PublicationPhase, c *PublicationConsent) ([32]byte, error) {
	var zero [32]byte
	basis, err := s.authorizationMaterial(wi, pi, p, c)
	if err != nil {
		return zero, err
	}
	encoded, err := json.Marshal(basis)
	if err != nil {
		return zero, fmt.Errorf("encode publication authorization: %w", err)
	}
	return sha256.Sum256(encoded), nil
}

func (s *State) authorizationMaterial(wi, pi int, p *PublicationPhase, c *PublicationConsent) (authorizationMaterial, error) {
	var zero authorizationMaterial
	w := &s.Workflows[wi]
	r, err := phaseAs[*ReviewPhase](w, p.Plan.Review.Phase)
	if err != nil || r == nil {
		return zero, fmt.Errorf("authorization review is missing")
	}
	contract, err := element(w.ReviewContracts, r.Contract)
	if err != nil {
		return zero, err
	}
	output, err := w.Output(r.Scope.Output)
	if err != nil {
		return zero, err
	}
	var delta *CodeOutput
	var deltaPhase *int
	if r.Scope.DeltaFrom != nil {
		delta, err = w.Output(*r.Scope.DeltaFrom)
		if err != nil {
			return zero, err
		}
		deltaPhase = &r.Scope.DeltaFrom.Phase
	}
	context := w.Context()
	if context == nil {
		return zero, fmt.Errorf("authorization requires preparation context")
	}
	flow := "pull-request"
	if _, direct := p.Plan.Target.(*DirectMergeTarget); direct {
		flow = "direct-merge"
	}
	return authorizationMaterial{
		RepositoryID: s.RepositoryID, TaskID: s.TaskID,
		Workflow: wi, Publication: pi, Review: p.Plan.Review.Phase,
		Output: r.Scope.Output.Phase, Contract: r.Contract,
		DeltaPhase: deltaPhase,
		Baseline:   context.Baseline, TaskBranch: context.TaskBranch,
		Requirements: r.Scope.Requirements, Destination: r.Scope.Destination,
		Code: output, Delta: delta, Policy: *contract,
		Flow: flow, Target: p.Plan.Target, Content: p.Plan.Content,
		GrantedAt: c.GrantedAt.UTC(), Evidence: c.Evidence,
	}, nil
}

func (s *State) validateConsent(wi, pi int, p *PublicationPhase) error {
	if p.Consent == nil {
		return nil
	}
	digest, err := s.intentDigest(wi, pi, p, p.Consent)
	if err != nil {
		return err
	}
	if digest != p.Consent.IntentDigest || p.Consent.IntentDigest == [32]byte{} {
		return fmt.Errorf("publication consent does not authorize this exact intent and owner")
	}
	return nil
}

func stepTime(step ReviewStep) time.Time {
	var at time.Time
	switch step := step.(type) {
	case *CheckStep:
		if step != nil && step.Result != nil {
			at = step.Result.FinishedAt
		}
		if step != nil && step.Waiver != nil && step.Waiver.At.After(at) {
			at = step.Waiver.At
		}
	case *AgentReviewStep:
		if step != nil {
			if exit, ok := step.Execution.Process.(*ExitedProcess); ok && exit != nil {
				at = exit.At
			}
		}
	case *ManualReviewStep:
		if step != nil {
			switch assessment := step.Decision.(type) {
			case *ApprovedReview:
				if assessment != nil {
					at = assessment.At
				}
			case *RequestedChanges:
				if assessment != nil {
					at = assessment.At
				}
			}
		}
	}
	return at
}

func dispositionTime(disposition FindingDisposition) time.Time {
	switch disposition := disposition.(type) {
	case *RepairRequired:
		if disposition != nil {
			return disposition.At
		}
	case *WaivedFinding:
		if disposition != nil {
			return disposition.At
		}
	case *DismissedFinding:
		if disposition != nil {
			return disposition.At
		}
	case *DowngradedFinding:
		if disposition != nil {
			return disposition.At
		}
	case *AddressedFinding:
		if disposition != nil {
			return disposition.At
		}
	}
	return time.Time{}
}
