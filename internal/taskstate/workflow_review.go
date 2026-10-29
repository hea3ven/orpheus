package taskstate

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/work"
)

func currentReview(snapshot *work.State, ref work.AttemptRef) (*work.Workflow, *work.ReviewPhase, error) {
	w := snapshot.ActiveWorkflow()
	if w == nil || ref.RepositoryID != snapshot.RepositoryID || ref.TaskID != snapshot.TaskID || ref.Workflow != len(snapshot.Workflows)-1 || ref.Phase < 0 || ref.Phase != len(w.Phases)-1 || ref.BranchSync != nil {
		return nil, nil, errors.New("review address does not own the current workflow phase")
	}
	p, ok := w.LastPhase().(*work.ReviewPhase)
	if !ok {
		return nil, nil, errors.New("current phase is not review")
	}
	if ref.Step != nil && (*ref.Step < 0 || *ref.Step != len(p.Steps)-1) {
		return nil, nil, errors.New("review callback came from a superseded step")
	}
	return w, p, nil
}

func contractSlot(w *work.Workflow, p *work.ReviewPhase, name, kind string) (int, error) {
	for slot, required := range w.ReviewContracts[p.Contract].Slots {
		if required.ConfigKey == name {
			if string(required.Kind) != kind {
				return 0, errors.New("review step kind differs from its captured contract")
			}
			return slot, nil
		}
	}
	return 0, fmt.Errorf("step %q is not in the captured review contract; start a fresh review for changed configuration", name)
}

func workFinding(view ReviewFinding) (work.Finding, error) {
	finding, err := normalizeReviewFinding(view)
	if err != nil {
		return work.Finding{}, err
	}
	result := work.Finding{Title: finding.Title, Classification: work.FindingClassification(finding.Type), Description: finding.Description, SuggestedAction: finding.SuggestedAction}
	if finding.Type == FindingTypeBlocking {
		result.Dispositions = []work.FindingDisposition{&work.RepairRequired{}}
	}
	if !finding.TaskProposal.IsZero() {
		result.Proposal = &work.FollowUpProposal{Title: finding.TaskProposal.Title, Description: finding.TaskProposal.Description, AcceptanceCriteria: finding.TaskProposal.AcceptanceCriteria}
	}
	return result, nil
}

func appendObservedStep(w *work.Workflow, p *work.ReviewPhase, opts RecordReviewStepOptions) (bool, error) {
	slot, err := contractSlot(w, p, opts.Name, opts.Kind)
	if err != nil {
		return false, err
	}
	switch opts.Kind {
	case ReviewStepKindCheck:
		step := &work.CheckStep{Slot: slot, StartFailure: opts.StartFailure}
		if opts.ExitCode != nil {
			step.Result = &work.CheckResult{ExitCode: cloneIntPointer(opts.ExitCode), FinishedAt: opts.ObservedAt}
		}
		for _, finding := range opts.Findings {
			reported, err := workFinding(finding)
			if err != nil {
				return false, err
			}
			step.Findings = append(step.Findings, reported)
		}
		p.Steps = append(p.Steps, step)
	case ReviewStepKindAgentReview:
		if opts.Execution == nil {
			return false, errors.New("review agent requires actual execution metadata")
		}
		execution, err := observedExecution(*opts.Execution)
		if err != nil {
			return false, err
		}
		p.Steps = append(p.Steps, &work.AgentReviewStep{Slot: slot, Execution: execution})
	case ReviewStepKindManual:
		var result work.ProcessResult
		if opts.ExitCode != nil {
			result = &work.ExitedProcess{Code: cloneIntPointer(opts.ExitCode), At: opts.ObservedAt}
		}
		if opts.StartFailure != nil {
			result = opts.StartFailure
		}
		if len(p.Steps) > 0 {
			if step, ok := p.Steps[len(p.Steps)-1].(*work.ManualReviewStep); ok && step.Slot == slot && step.Decision == nil && step.CommandResult == nil {
				step.CommandResult = result
				return result != nil, nil
			}
		}
		p.Steps = append(p.Steps, &work.ManualReviewStep{Slot: slot, CommandResult: result})
	default:
		return false, errors.New("unsupported review step kind")
	}
	return true, nil
}

// RecordManualDecision records the actual prompt response at its stable step.
// Empty status is the existing ManualResult convention for explicit approval.
func (s Store) RecordManualDecision(ref work.AttemptRef, status ReviewStatus, publication *work.ReviewedPublication) (ReviewAttempt, error) {
	view, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		if err := snapshot.CheckActiveAttempt(ref); err != nil {
			return false, err
		}
		_, p, err := currentReview(snapshot, ref)
		if err != nil {
			return false, err
		}
		if ref.Step == nil {
			return false, errors.New("manual decision requires its step address")
		}
		step, ok := p.Steps[*ref.Step].(*work.ManualReviewStep)
		if !ok {
			return false, errors.New("decision does not belong to a manual step")
		}
		switch status {
		case "", ReviewStatusPassed:
			step.Decision = &work.ApprovedReview{At: s.nowUTC(), Publication: publication}
		case ReviewStatusBlocked:
			decision := &work.RequestedChanges{At: s.nowUTC()}
			projection, err := Project(*snapshot)
			if err != nil {
				return false, err
			}
			review, err := viewReviewAt(projection, ref)
			if err != nil {
				return false, err
			}
			for _, finding := range review.Findings {
				if finding.Address.Step != *ref.Step && IsOpenBlockingReviewFinding(finding) {
					decision.Targets = append(decision.Targets, *finding.Address)
				}
			}
			step.Decision = decision
		case ReviewStatusAborted:
			step.Decision = &work.AbortedReview{At: s.nowUTC(), Reason: "Operator stopped manual review"}
		default:
			return false, errors.New("manual decision requires an actual approval, requested changes, or abort")
		}
		return true, nil
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, ref)
}

// RecordReadOnlyFailure preserves failed safety verification without replacing a
// child's real successful exit with a fabricated nonzero status or agent finding.
func (s Store) RecordReadOnlyFailure(ref work.AttemptRef, cause error) (ReviewAttempt, error) {
	if cause == nil {
		return ReviewAttempt{}, errors.New("read-only failure requires observed error")
	}
	view, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		_, p, err := currentReview(snapshot, ref)
		if err != nil {
			return false, err
		}
		if ref.Step == nil || *ref.Step < 0 || *ref.Step != len(p.Steps)-1 {
			return false, errors.New("read-only observation requires the current last step")
		}
		failure := work.ReadOnlyFailure{At: s.nowUTC(), Reason: cause.Error()}
		switch step := p.Steps[*ref.Step].(type) {
		case *work.CheckStep:
			step.ReadOnlyFailures = append(step.ReadOnlyFailures, failure)
		case *work.AgentReviewStep:
			step.ReadOnlyFailures = append(step.ReadOnlyFailures, failure)
		case *work.ManualReviewStep:
			step.ReadOnlyFailures = append(step.ReadOnlyFailures, failure)
		}
		return true, nil
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, ref)
}

// PauseReviewForManualAt records entry into an actual manual assessment without
// persisting a cursor or changing task-source blocking.
func (s Store) PauseReviewForManualAt(ref work.AttemptRef, name string) (ReviewAttempt, error) {
	view, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		w, p, err := currentReview(snapshot, ref)
		if err != nil {
			return false, err
		}
		if ref.Step != nil {
			if err := snapshot.CheckActiveAttempt(ref); err != nil {
				return false, err
			}
		}
		slot, err := contractSlot(w, p, name, ReviewStepKindManual)
		if err != nil {
			return false, err
		}
		if len(p.Steps) > 0 {
			if step, ok := p.Steps[len(p.Steps)-1].(*work.ManualReviewStep); ok && step.Slot == slot && step.Decision == nil {
				return false, nil
			}
		}
		p.Steps = append(p.Steps, &work.ManualReviewStep{Slot: slot})
		return true, nil
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, ref)
}

// RecordReviewStepChildPIDAt records the actual child under its original parent
// and step positions, never under a task-wide latest execution selector.
func (s Store) RecordReviewStepChildPIDAt(ref work.AttemptRef, pid int) (ReviewAttempt, error) {
	view, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		if err := snapshot.CheckActiveAttempt(ref); err != nil {
			return false, err
		}
		w, _, err := currentReview(snapshot, ref)
		if err != nil {
			return false, err
		}
		execution, err := w.Execution(work.ExecutionRef{Phase: ref.Phase, Step: ref.Step})
		if err != nil {
			return false, err
		}
		if pid <= 0 || execution.ChildPID != 0 && execution.ChildPID != pid {
			return false, errors.New("reviewer direct-child identity conflicts with its owner")
		}
		if execution.ChildPID == pid {
			return false, nil
		}
		execution.ChildPID = pid
		return true, nil
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, ref)
}

// RecordReviewAdmissionFailure records an actual failed read-only admission,
// without pretending an agent process or check command was started.
func (s Store) RecordReviewAdmissionFailure(ref work.AttemptRef, cause error) (ReviewAttempt, error) {
	if cause == nil {
		return ReviewAttempt{}, errors.New("admission failure requires an observed error")
	}
	view, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		_, phase, err := currentReview(snapshot, ref)
		if err != nil {
			return false, err
		}
		if phase.AdmissionFailure != nil {
			return false, nil
		}
		phase.AdmissionFailure = &work.ReadOnlyFailure{At: s.nowUTC(), Reason: cause.Error()}
		return true, nil
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, ref)
}

// RecordAutomatedDecisionPause retains a real pause/input-loss observation on
// the step which owns the unanswered findings. It does not block the task source.
func (s Store) RecordAutomatedDecisionPause(ref work.AttemptRef, kind work.AutomatedDecisionPauseKind) (ReviewAttempt, error) {
	view, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		_, p, err := currentReview(snapshot, ref)
		if err != nil {
			return false, err
		}
		if ref.Step == nil || *ref.Step < 0 || *ref.Step != len(p.Steps)-1 {
			return false, errors.New("automated decision pause requires the actual last step")
		}
		pause := work.AutomatedDecisionPause{At: s.nowUTC(), Kind: kind}
		switch step := p.Steps[*ref.Step].(type) {
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
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, ref)
}

// RecordRepairBudgetStop records the real invocation's consumed repair budget.
// A later explicit task run may use a fresh budget without rewriting this stop.
func (s Store) RecordRepairBudgetStop(ref work.AttemptRef, used, maximum int) (ReviewAttempt, error) {
	view, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		_, p, err := currentReview(snapshot, ref)
		if err != nil {
			return false, err
		}
		p.RepairBudgetStops = append(p.RepairBudgetStops, work.RepairBudgetStop{At: s.nowUTC(), Used: used, Maximum: maximum})
		return true, nil
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, ref)
}

// CurrentReviewAt checks an invocation's parent address without changing facts.
// Resuming a prompt is not another persisted running-status transition.
func (s Store) CurrentReviewAt(ref work.AttemptRef) (ReviewAttempt, error) {
	view, err := s.Load(ref.RepositoryID, ref.TaskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	_, phase, err := currentReview(view.Work, ref)
	if err != nil {
		return ReviewAttempt{}, err
	}
	if ref.Step != nil && (*ref.Step < 0 || *ref.Step != len(phase.Steps)-1) {
		return ReviewAttempt{}, errors.New("review step has been superseded")
	}
	return viewReviewAt(view, ref)
}

// RecordFindingDecision appends an actual operator decision to the reporting
// owner. A stale parent/step cannot attach its response to a newer workflow.
func (s Store) RecordFindingDecision(actor work.AttemptRef, target work.FindingRef, decision work.FindingDisposition) (ReviewAttempt, error) {
	view, err := s.updateWorkflow(actor.RepositoryID, actor.TaskID, func(snapshot *work.State) (bool, error) {
		w, err := findingDecisionOwner(snapshot, actor, target)
		if err != nil {
			return false, err
		}
		finding, err := w.Finding(target)
		if err != nil {
			return false, err
		}
		if err := validateFindingChoice(w, finding, target, actor, decision); err != nil {
			return false, err
		}
		stamped, err := observedDecision(decision, s.nowUTC())
		if err != nil {
			return false, err
		}
		finding.Dispositions = append(finding.Dispositions, stamped)

		return true, nil
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	owner := actor
	owner.Phase = target.Phase
	owner.Step = nil
	return viewReviewAt(view, owner)
}

func observedDecision(decision work.FindingDisposition, at time.Time) (work.FindingDisposition, error) {
	switch choice := decision.(type) {
	case *work.RepairRequired:
		if choice != nil {
			return &work.RepairRequired{At: at}, nil
		}
	case *work.PromotedFinding:
		if choice != nil {
			return &work.PromotedFinding{At: at}, nil
		}
	case *work.WaivedFinding:
		if choice != nil {
			return &work.WaivedFinding{Waiver: work.Waiver{At: at, Reason: choice.Reason}}, nil
		}
	case *work.DowngradedFinding:
		if choice != nil {
			return &work.DowngradedFinding{Waiver: work.Waiver{At: at, Reason: choice.Reason}}, nil
		}
	case *work.DismissedFinding:
		if choice != nil {
			return &work.DismissedFinding{Waiver: work.Waiver{At: at, Reason: choice.Reason}}, nil
		}
	case *work.AddressedFinding:
		if choice != nil {
			return &work.AddressedFinding{Waiver: work.Waiver{At: at, Reason: choice.Reason}}, nil
		}
	}
	return nil, errors.New("an explicit finding decision is required")
}

// RecordCheckResult settles the pending check attempt using its actual exit or
// explicit startup failure, preserving the earlier attempt on a restart.
func (s Store) RecordCheckResult(ref work.AttemptRef, exitCode *int, startFailure error, findings []ReviewFinding) (ReviewAttempt, error) {
	view, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		if err := snapshot.CheckActiveAttempt(ref); err != nil {
			return false, err
		}
		_, p, err := currentReview(snapshot, ref)
		if err != nil {
			return false, err
		}
		if ref.Step == nil {
			return false, errors.New("check result requires the exact step")
		}
		step, ok := p.Steps[*ref.Step].(*work.CheckStep)
		if !ok {
			return false, errors.New("result does not belong to a check")
		}
		switch {
		case exitCode != nil:
			step.Result = &work.CheckResult{ExitCode: cloneIntPointer(exitCode), FinishedAt: s.nowUTC()}
		case startFailure != nil:
			step.StartFailure = &work.StartFailedProcess{At: s.nowUTC(), Reason: startFailure.Error()}
		default:
			return false, errors.New("check requires an observed exit or start failure")
		}
		for _, finding := range findings {
			observed, err := workFinding(finding)
			if err != nil {
				return false, err
			}
			step.Findings = append(step.Findings, observed)
		}
		return true, nil
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, ref)
}

// RecordReviewFailure retains a real orchestration error, independently of the
// successful/failed native exits of the steps which already ran.
func (s Store) RecordReviewFailure(ref work.AttemptRef, cause error) (ReviewAttempt, error) {
	if cause == nil {
		return ReviewAttempt{}, errors.New("review failure requires an observed cause")
	}
	view, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		_, phase, err := currentReview(snapshot, ref)
		if err != nil {
			return false, err
		}
		if phase.ExecutionFailure != nil {
			return false, nil
		}
		phase.ExecutionFailure = &work.ReviewExecutionFailure{At: s.nowUTC(), Reason: cause.Error()}
		return true, nil
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, ref)
}

// RecordReviewAbort retains an actual operator stop outside an individual manual
// assessment, such as declining continuation after follow-up creation failed.
func (s Store) RecordReviewAbort(ref work.AttemptRef, reason string) (ReviewAttempt, error) {
	view, err := s.updateWorkflow(ref.RepositoryID, ref.TaskID, func(snapshot *work.State) (bool, error) {
		_, phase, err := currentReview(snapshot, ref)
		if err != nil {
			return false, err
		}
		if phase.Abort != nil {
			return false, nil
		}
		phase.Abort = &work.AbortedReview{At: s.nowUTC(), Reason: reason}
		return true, nil
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, ref)
}

func findingDecisionOwner(snapshot *work.State, actor work.AttemptRef, target work.FindingRef) (*work.Workflow, error) {
	w := snapshot.ActiveWorkflow()
	if w == nil || actor.RepositoryID != snapshot.RepositoryID || actor.TaskID != snapshot.TaskID || actor.Workflow != len(snapshot.Workflows)-1 || actor.Phase != len(w.Phases)-1 || actor.BranchSync != nil || target.Phase > actor.Phase {
		return nil, errors.New("finding decision no longer owns the current workflow")
	}
	switch phase := w.LastPhase().(type) {
	case *work.ImplementationPhase:
		if actor.Step != nil || phase.Output == nil {
			return nil, errors.New("pre-review decisions require safely captured implementation output")
		}
	case *work.ReviewPhase:
		if err := reviewDecisionActor(snapshot, actor, target, phase); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("finding decision requires review or captured pre-review work")
	}
	return w, nil
}
func reviewDecisionActor(snapshot *work.State, actor work.AttemptRef, target work.FindingRef, phase *work.ReviewPhase) error {
	if actor.Step == nil {
		if len(phase.Steps) == 0 {
			return nil
		}
		switch step := phase.Steps[len(phase.Steps)-1].(type) {
		case *work.AgentReviewStep:
			if _, running := step.Execution.Process.(*work.RunningProcess); running {
				return errors.New("a live reviewer owns this boundary")
			}
		case *work.CheckStep:
			if step.Result == nil && step.StartFailure == nil {
				return errors.New("a pending check owns this boundary")
			}
		}
		return nil
	}
	if *actor.Step < 0 || *actor.Step != len(phase.Steps)-1 {
		return errors.New("decision came from a superseded step")
	}
	if _, manual := phase.Steps[*actor.Step].(*work.ManualReviewStep); manual {
		return snapshot.CheckActiveAttempt(actor)
	}
	if target.Phase != actor.Phase || target.Step != *actor.Step {
		return errors.New("automated decision is outside its reporting step")
	}
	return nil
}

// RecordFindingCreatedTask appends the actual task-source receipt to its original
// finding. It does not snapshot or own the created task's lifecycle.
func (s Store) RecordFindingCreatedTask(actor work.AttemptRef, target work.FindingRef, taskID string) (ReviewAttempt, error) {
	if strings.TrimSpace(taskID) == "" {
		return ReviewAttempt{}, errors.New("created follow-up requires observed native task identity")
	}
	view, err := s.updateWorkflow(actor.RepositoryID, actor.TaskID, func(snapshot *work.State) (bool, error) {
		w, _, err := currentReview(snapshot, actor)
		if err != nil {
			return false, err
		}
		finding, err := w.Finding(target)
		if err != nil {
			return false, err
		}
		if finding.Classification != work.FindingSeparateTask || finding.Proposal == nil {
			return false, errors.New("created follow-up must belong to a reported separate-task proposal")
		}
		if finding.CreatedTask != nil {
			if finding.CreatedTask.TaskID != taskID {
				return false, errors.New("created task receipt cannot be rewritten")
			}
			return false, nil
		}
		finding.CreatedTask = &work.CreatedFollowUp{TaskID: taskID, ObservedAt: s.nowUTC()}
		return true, nil
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, actor)
}

func validateFindingChoice(w *work.Workflow, finding *work.Finding, target work.FindingRef, actor work.AttemptRef, choice work.FindingDisposition) error {
	source := w.Phases[target.Phase].(*work.ReviewPhase)
	address := actor
	address.Phase = target.Phase
	address.Step = &target.Step
	name := w.ReviewContracts[source.Contract].Slots[stepSlot(source.Steps[target.Step])].ConfigKey
	view := projectFinding(w, finding, target, name, address)
	if view.Superseded || view.NonAuthoritative {
		return errors.New("audit-only findings cannot authorize repair or a live disposition")
	}
	if _, promoted := choice.(*work.PromotedFinding); promoted {
		resolution := ResolveReviewFinding(view)
		if view.Type != FindingTypeAdvisory || (resolution != ReviewFindingResolutionNonBlocking && resolution != ReviewFindingResolutionDowngraded) {
			return errors.New("promotion requires an unresolved advisory opportunity")
		}
		return nil
	}
	if !IsOpenBlockingReviewFinding(view) {
		return errors.New("finding decision requires an unresolved blocking finding")
	}
	return nil
}
