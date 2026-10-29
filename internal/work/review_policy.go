package work

import "time"

// RepairBudgetStop records the policy actually consumed when automatic repair
// stopped. It is not live configuration or a second review outcome/cursor.
type RepairBudgetStop struct {
	At      time.Time
	Used    int
	Maximum int
}

// AutomatedDecisionPauseKind distinguishes an explicit pause from unavailable
// input. Neither kind blocks the task in its authoritative task source.
type AutomatedDecisionPauseKind string

const (
	DecisionPaused           AutomatedDecisionPauseKind = "operator_paused"
	DecisionInputUnavailable AutomatedDecisionPauseKind = "input_unavailable"
)

// AutomatedDecisionPause is an observation owned by the automated step whose
// reported blockers were awaiting disposition, not a generic interaction record.
type AutomatedDecisionPause struct {
	At   time.Time
	Kind AutomatedDecisionPauseKind
}

func decisionPauses(step ReviewStep) []AutomatedDecisionPause {
	switch step := step.(type) {
	case *CheckStep:
		if step != nil {
			return step.DecisionPauses
		}
	case *AgentReviewStep:
		if step != nil {
			return step.DecisionPauses
		}
	}
	return nil
}

func (v *validator) reviewPolicy(p *ReviewPhase) {
	previous := p.StartedAt
	for _, stop := range p.RepairBudgetStops {
		v.check(!stop.At.IsZero() && !stop.At.Before(previous), "repair budget stops require ordered observation times after review entry")
		v.check(stop.Maximum > 0 && stop.Used >= stop.Maximum, "repair budget stop requires actual consumed and maximum attempts")
		previous = stop.At
	}
	for _, step := range p.Steps {
		previous = p.StartedAt
		for _, pause := range decisionPauses(step) {
			v.check(pause.Kind == DecisionPaused || pause.Kind == DecisionInputUnavailable, "unknown automated decision pause kind")
			v.check(!pause.At.IsZero() && !pause.At.Before(previous), "decision pauses require ordered observation times after review entry")
			previous = pause.At
		}
	}
}
