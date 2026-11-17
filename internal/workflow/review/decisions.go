package review

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

const FindingsInputKind = "finding_dispositions"

// DecisionAction is a legal answer to a pending Review assessment.
type DecisionAction string

const (
	DecisionPause          DecisionAction = "pause"
	DecisionApprove        DecisionAction = "approve"
	DecisionRequestChanges DecisionAction = "request_changes"
	DecisionAbort          DecisionAction = "abort"
	DecisionKeep           DecisionAction = "keep"
	DecisionWaive          DecisionAction = "waive"
	DecisionDowngrade      DecisionAction = "downgrade"
)

// DecisionRequest is reconstructed from the unanswered assessment and findings.
// It does not reserve a new step or persist a queue of operator questions.
type DecisionRequest struct {
	ID       phaseexec.InputID
	Actions  []DecisionAction
	Findings []FindingDecisionView
}

// FindingDecisionView identifies a retained finding rather than a display index.
type FindingDecisionView struct {
	Ref   work.FindingRef
	Title string
}

// Answer is an explicit Review answer, distinct from publication consent.
type Answer interface{ reviewAnswer() }

// PauseAnswer leaves the assessment unanswered and discards unsubmitted edits.
type PauseAnswer struct{}

func (PauseAnswer) reviewAnswer()      {}
func (ManualSubmission) reviewAnswer() {}

// FindingsAnswer answers all selected automated blockers atomically.
type FindingsAnswer struct{ Decisions []FindingDecision }

func (FindingsAnswer) reviewAnswer() {}

// FindingDecision is one explicit disposition of a retained automated finding.
type FindingDecision struct {
	Ref    work.FindingRef
	Action DecisionAction
	Reason string
}

// PendingDecision describes only the current Review's legal answers. Workflow
// decides whether this phase may run; Review owns the unanswered boundary.
func PendingDecision(current work.State) (*DecisionRequest, error) {
	w := current.ActiveWorkflow()
	if w == nil {
		return nil, nil
	}
	phase, ok := w.LastPhase().(*work.ReviewPhase)
	if !ok || len(phase.Steps) == 0 || phase.Abort != nil || phase.ExecutionFailure != nil || phase.AdmissionFailure != nil {
		return nil, nil
	}
	projected, err := taskstate.Project(current)
	if err != nil {
		return nil, err
	}
	latest, ok := taskstate.LatestReview(projected)
	if !ok {
		return nil, nil
	}
	owner := latest.Steps[len(latest.Steps)-1].Address
	request := &DecisionRequest{ID: phaseexec.InputID{Owner: owner, Version: current.Version}}
	if step, manual := phase.Steps[len(phase.Steps)-1].(*work.ManualReviewStep); manual {
		if step.Decision != nil || len(step.ReadOnlyFailures) > 0 || manualCommandFailed(step.CommandResult) {
			return nil, nil
		}
		request.ID.Kind = ManualInputKind
		request.Actions = []DecisionAction{DecisionPause, DecisionAbort}
		action := DecisionApprove
		if taskstate.ReviewHasOpenBlockers(latest) {
			action = DecisionRequestChanges
		}
		request.Actions = append(request.Actions, action)
		return request, nil
	}
	if latest.Status != taskstate.ReviewStatusWaitingForAutomatedDecision {
		return nil, nil
	}
	request.ID.Kind = FindingsInputKind
	request.Actions = []DecisionAction{DecisionKeep, DecisionWaive, DecisionDowngrade, DecisionPause}
	for _, finding := range latest.Findings {
		if finding.Address != nil && finding.Address.Phase == owner.Phase && finding.Address.Step == *owner.Step && taskstate.IsOpenBlockingReviewFinding(finding) {
			request.Findings = append(request.Findings, FindingDecisionView{Ref: *finding.Address, Title: finding.Title})
		}
	}
	if len(request.Findings) == 0 {
		return nil, nil
	}
	return request, nil
}

func validateDecisionInput(current work.State, id phaseexec.InputID) (*DecisionRequest, error) {
	pending, err := PendingDecision(current)
	if err != nil {
		return nil, err
	}
	if pending == nil || !pending.ID.Owner.Equal(id.Owner) || pending.ID.Kind != id.Kind || pending.ID.Version != id.Version {
		return nil, phaseexec.ErrStaleInput
	}
	return pending, nil
}

func validateFindingAnswers(pending DecisionRequest, answer FindingsAnswer) error {
	if len(answer.Decisions) != len(pending.Findings) {
		return errors.New("respond to every pending finding explicitly")
	}
	seen := map[work.FindingRef]bool{}
	for _, decision := range answer.Decisions {
		if seen[decision.Ref] || !slices.ContainsFunc(pending.Findings, func(f FindingDecisionView) bool { return f.Ref == decision.Ref }) {
			return errors.New("response identifies an unrelated or duplicate finding")
		}
		seen[decision.Ref] = true
		if decision.Action != DecisionKeep && decision.Action != DecisionWaive && decision.Action != DecisionDowngrade {
			return errors.New("invalid finding disposition")
		}
		if decision.Action != DecisionKeep && strings.TrimSpace(decision.Reason) == "" {
			return errors.New("finding disposition requires a reason")
		}
	}
	return nil
}

func applyFindingAnswers(current *work.State, pending DecisionRequest, answer FindingsAnswer, at time.Time) (bool, error) {
	if err := validateFindingAnswers(pending, answer); err != nil {
		return false, err
	}
	for _, decision := range answer.Decisions {
		finding, err := current.ActiveWorkflow().Finding(decision.Ref)
		if err != nil {
			return false, err
		}
		var disposition work.FindingDisposition = &work.RepairRequired{At: at}
		switch decision.Action {
		case DecisionWaive:
			disposition = &work.WaivedFinding{Waiver: work.Waiver{At: at, Reason: decision.Reason}}
		case DecisionDowngrade:
			disposition = &work.DowngradedFinding{Waiver: work.Waiver{At: at, Reason: decision.Reason}}
		}
		finding.Dispositions = append(finding.Dispositions, disposition)
	}
	return true, nil
}

// Decision translates the pipeline's manual result convention into an explicit
// assessment decision. Empty status is the existing explicit-approval result.
func (result ManualResult) Decision() (ManualDecision, error) {
	switch result.Status {
	case "", taskstate.ReviewStatusPassed:
		return ManualApprove, nil
	case taskstate.ReviewStatusBlocked:
		return ManualRequestChanges, nil
	case taskstate.ReviewStatusAborted:
		return ManualAbort, nil
	default:
		return "", fmt.Errorf("%w: invalid manual response", ErrInvalidExecution)
	}
}

// FindingAnswers resolves presentation indexes to their stable retained owners.
func FindingAnswers(latest taskstate.ReviewAttempt, decisions []AutomatedBlockerDecision) (FindingsAnswer, error) {
	var answer FindingsAnswer
	for _, decision := range decisions {
		if decision.FindingIndex < 0 || decision.FindingIndex >= len(latest.Findings) || latest.Findings[decision.FindingIndex].Address == nil {
			return FindingsAnswer{}, fmt.Errorf("%w: response does not identify a retained finding", ErrInvalidExecution)
		}
		answer.Decisions = append(answer.Decisions, FindingDecision{Ref: *latest.Findings[decision.FindingIndex].Address, Action: DecisionAction(decision.Action), Reason: decision.Reason})
	}
	return answer, nil
}

func (s Assessments) validateConfiguration(current work.State) error {
	projected, err := taskstate.Project(current)
	if err != nil {
		return err
	}
	latest, ok := taskstate.LatestReview(projected)
	if !ok {
		return phaseexec.ErrStaleInput
	}
	pipeline, err := ResolvePausedTaskReviewPipeline(s.Paths, s.Repository, latest, "")
	if err != nil {
		return err
	}
	_, err = ContinuationIndex(pipeline, latest)
	return err
}

// Respond validates typed answers without saving state. Preparation revalidates
// identity, and the runner commits one complete submission after the current
// configuration and retained revision have been checked.
func (s Assessments) Respond(current work.State, id phaseexec.InputID, answer Answer) (phaseexec.Operation, error) {
	pending, err := validateDecisionInput(current, id)
	if err != nil {
		return phaseexec.Operation{}, err
	}
	switch answer := answer.(type) {
	case ManualSubmission:
		if id.Kind != ManualInputKind {
			break
		}
		return s.manualResponse(current, id, answer)
	case PauseAnswer:
		return s.decisionOperation(id, "pause review assessment", true, nil), nil
	case FindingsAnswer:
		if id.Kind != FindingsInputKind {
			break
		}
		if err := validateFindingAnswers(*pending, answer); err != nil {
			return phaseexec.Operation{}, err
		}
		answer.Decisions = slices.Clone(answer.Decisions)
		return s.decisionOperation(id, "submit finding dispositions", false, func(current *work.State, pending DecisionRequest, at time.Time) (bool, error) {
			return applyFindingAnswers(current, pending, answer, at)
		}), nil
	}
	return phaseexec.Operation{}, fmt.Errorf("response does not match pending %s interaction", id.Kind)
}

func (s Assessments) decisionOperation(id phaseexec.InputID, name string, pause bool, apply func(*work.State, DecisionRequest, time.Time) (bool, error)) phaseexec.Operation {
	return phaseexec.Operation{Name: name, Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		if _, err := validateDecisionInput(*current, id); err != nil {
			return phaseexec.Prepared{}, err
		}
		frozen := *current
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			if err := s.validateConfiguration(frozen); err != nil {
				return phaseexec.Result{Err: err}
			}
			if pause {
				return phaseexec.Result{}
			}
			candidate, err := CommittedInput(taskstate.TaskState{Work: &frozen}, id.Owner)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			boundary := s.Git
			if boundary == nil {
				boundary = LocalReviewCandidateGit{}
			}
			if err := boundary.VerifyRevision(ctx, frozen.Workflows[id.Owner.Workflow].Context().Directory, candidate.Identity, candidate.Revision); err != nil {
				return phaseexec.Result{Err: err}
			}
			if err := ctx.Err(); err != nil {
				return phaseexec.Result{Err: err}
			}
			at := time.Now().UTC()
			return phaseexec.Result{Update: func(current *work.State) (bool, error) {
				pending, err := validateDecisionInput(*current, id)
				if err != nil {
					return false, err
				}
				return apply(current, *pending, at)
			}}
		}}, nil
	}}
}
