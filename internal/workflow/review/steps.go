package review

import (
	"errors"
	"fmt"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// StepObservation describes an actual reservation or command observation, not
// an executable plan. Findings and executions retain their existing projections.
type StepObservation struct {
	ObservedAt   time.Time
	Address      work.AttemptRef
	StartFailure *work.StartFailedProcess
	Findings     []taskstate.ReviewFinding
	Kind         string
	Name         string
	Execution    *taskstate.AgentExecution
	ExitCode     *int
}

func contractSlot(w *work.Workflow, p *work.ReviewPhase, name, kind string) (int, error) {
	slots := w.ReviewContracts[p.Contract].Slots
	for slot := len(slots) - 1; slot >= 0; slot-- {
		if slots[slot].ConfigKey == name && string(slots[slot].Kind) == kind {
			return slot, nil
		}
	}
	return 0, fmt.Errorf("step %q is not in the captured review contract; start a fresh review for changed configuration", name)
}

func observedFinding(view taskstate.ReviewFinding) (work.Finding, error) {
	finding, err := taskstate.NormalizeReviewFinding(view)
	if err != nil {
		return work.Finding{}, err
	}
	return reportedFinding(finding), nil
}

func reportedFinding(finding taskstate.ReviewFinding) work.Finding {
	result := work.Finding{Title: finding.Title, Classification: work.FindingClassification(finding.Type), Description: finding.Description, SuggestedAction: finding.SuggestedAction}
	if finding.Type == taskstate.FindingTypeBlocking {
		result.Dispositions = []work.FindingDisposition{&work.RepairRequired{}}
	}
	if !finding.TaskProposal.IsZero() {
		result.Proposal = &work.FollowUpProposal{Title: finding.TaskProposal.Title, Description: finding.TaskProposal.Description, AcceptanceCriteria: finding.TaskProposal.AcceptanceCriteria}
	}
	return result
}

// StepChange reserves a check/reviewer or retains the first manual command
// observation. The runner saves reservation before invoking the command.
func StepChange(attempt int, observed StepObservation) phaseexec.Change {
	if observed.ObservedAt.IsZero() {
		observed.ObservedAt = time.Now().UTC()
	}
	return func(current *work.State) (bool, error) {
		w, p, err := currentReview(current, observed.Address)
		if err != nil {
			return false, err
		}
		if observed.Address.Step != nil {
			if observed.Kind != KindManual {
				return false, errors.New("step-owned results cannot start another review attempt")
			}
			if err := current.CheckActiveAttempt(observed.Address); err != nil {
				return false, err
			}
		}
		projected, err := taskstate.Project(*current)
		if err != nil {
			return false, err
		}
		latest, ok := taskstate.LatestReview(projected)
		if !ok || latest.Attempt != attempt {
			return false, errors.New("review display number differs from its owner")
		}
		slot, err := contractSlot(w, p, observed.Name, observed.Kind)
		if err != nil {
			return false, err
		}
		return appendObservedStep(p, slot, observed)
	}
}

func appendObservedStep(p *work.ReviewPhase, slot int, observed StepObservation) (bool, error) {
	switch observed.Kind {
	case KindCheck:
		step := &work.CheckStep{Slot: slot, StartFailure: observed.StartFailure}
		if observed.ExitCode != nil {
			code := *observed.ExitCode
			step.Result = &work.CheckResult{ExitCode: &code, FinishedAt: observed.ObservedAt}
		}
		for _, finding := range observed.Findings {
			reported, err := observedFinding(finding)
			if err != nil {
				return false, err
			}
			step.Findings = append(step.Findings, reported)
		}
		p.Steps = append(p.Steps, step)
	case KindAgentReview:
		if observed.Execution == nil {
			return false, errors.New("review agent requires actual execution metadata")
		}
		execution, err := taskstate.ObservedExecution(*observed.Execution)
		if err != nil {
			return false, err
		}
		p.Steps = append(p.Steps, &work.AgentReviewStep{Slot: slot, Execution: execution})
	case KindManual:
		return recordManualCommand(p, slot, observed)
	default:
		return false, errors.New("unsupported review step kind")
	}
	return true, nil
}

func recordManualCommand(p *work.ReviewPhase, slot int, observed StepObservation) (bool, error) {
	var result work.ProcessResult
	if observed.ExitCode != nil {
		code := *observed.ExitCode
		result = &work.ExitedProcess{Code: &code, At: observed.ObservedAt}
	}
	if observed.StartFailure != nil {
		result = observed.StartFailure
	}
	if len(p.Steps) > 0 {
		if step, ok := p.Steps[len(p.Steps)-1].(*work.ManualReviewStep); ok && step.Slot == slot && step.Decision == nil && step.CommandResult == nil {
			step.CommandResult = result
			return result != nil, nil
		}
	}
	p.Steps = append(p.Steps, &work.ManualReviewStep{Slot: slot, CommandResult: result})
	return true, nil
}

func manualEntryChange(owner work.AttemptRef, name string) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		w, p, err := currentReview(current, owner)
		if err != nil {
			return false, err
		}
		if owner.Step != nil {
			if err := current.CheckActiveAttempt(owner); err != nil {
				return false, err
			}
		}
		slot, err := contractSlot(w, p, name, KindManual)
		if err != nil {
			return false, err
		}
		if len(p.Steps) > 0 {
			if step, ok := p.Steps[len(p.Steps)-1].(*work.ManualReviewStep); ok && step.Slot == slot && step.Decision == nil {
				return false, nil
			}
		}
		p.Steps = append(p.Steps, &work.ManualReviewStep{Slot: slot, InspectionPending: new(false)})
		return true, nil
	}
}

func checkResultChange(owner work.AttemptRef, exitCode *int, startFailure error, findings []taskstate.ReviewFinding) phaseexec.Change {
	at := time.Now().UTC()
	return func(current *work.State) (bool, error) {
		if err := current.CheckActiveAttempt(owner); err != nil {
			return false, err
		}
		_, p, err := currentReview(current, owner)
		if err != nil {
			return false, err
		}
		if owner.Step == nil {
			return false, errors.New("check result requires the exact step")
		}
		step, ok := p.Steps[*owner.Step].(*work.CheckStep)
		if !ok {
			return false, errors.New("result does not belong to a check")
		}
		switch {
		case exitCode != nil:
			code := *exitCode
			step.Result = &work.CheckResult{ExitCode: &code, FinishedAt: at}
		case startFailure != nil:
			step.StartFailure = &work.StartFailedProcess{At: at, Reason: startFailure.Error()}
		default:
			return false, errors.New("check requires an observed exit or start failure")
		}
		for _, finding := range findings {
			observed, err := observedFinding(finding)
			if err != nil {
				return false, err
			}
			step.Findings = append(step.Findings, observed)
		}
		return true, nil
	}
}

func automatedPauseChange(owner work.AttemptRef, kind work.AutomatedDecisionPauseKind) phaseexec.Change {
	at := time.Now().UTC()
	return func(current *work.State) (bool, error) {
		_, p, err := currentReview(current, owner)
		if err != nil {
			return false, err
		}
		if owner.Step == nil || *owner.Step != len(p.Steps)-1 {
			return false, errors.New("automated decision pause requires the actual last step")
		}
		pause := work.AutomatedDecisionPause{At: at, Kind: kind}
		switch step := p.Steps[*owner.Step].(type) {
		case *work.CheckStep:
			if step.Result == nil {
				return false, errors.New("pending check cannot request a finding disposition")
			}
			step.DecisionPauses = append(step.DecisionPauses, pause)
		case *work.AgentReviewStep:
			if _, running := step.Execution.Process.(*work.RunningProcess); running {
				return false, errors.New("active reviewer cannot request a final finding disposition")
			}
			step.DecisionPauses = append(step.DecisionPauses, pause)
		default:
			return false, errors.New("decision pause does not belong to an automated step")
		}
		return true, nil
	}
}

func manualInterruptionChange(owner work.AttemptRef, exitCode int) phaseexec.Change {
	at := time.Now().UTC()
	return func(current *work.State) (bool, error) {
		if err := current.CheckActiveAttempt(owner); err != nil {
			return false, err
		}
		_, p, err := currentReview(current, owner)
		if err != nil {
			return false, err
		}
		if owner.Step == nil {
			return false, errors.New("manual command interruption requires its step address")
		}
		step, ok := p.Steps[*owner.Step].(*work.ManualReviewStep)
		if !ok {
			return false, errors.New("interrupted command does not belong to a manual assessment")
		}
		step.CommandInterruptions = append(step.CommandInterruptions, work.ExitedProcess{At: at, Code: &exitCode})
		return true, nil
	}
}
