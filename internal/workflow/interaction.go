package workflow

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
)

// InteractionKind identifies a durable unanswered domain decision. Identity
// follows its owning assessment/plan; no terminal session or raw output is used.
type InteractionKind string

const (
	InteractionManualReview        InteractionKind = "manual_review"
	InteractionFindings            InteractionKind = "finding_dispositions"
	InteractionPublication         InteractionKind = "publication_consent"
	InteractionPublicationRecovery InteractionKind = "publication_recovery"
)

// InteractionID binds a response to the exact observed owner and state version.
// Re-inspection after an edit yields a new version of the same pending decision.
type InteractionID struct {
	Owner   work.AttemptRef
	Version work.StateVersion
	Kind    InteractionKind
}

// Interaction supplies legal typed responses without implying a default answer.
type Interaction struct {
	ID       InteractionID
	Actions  []DecisionAction
	Findings []FindingDecisionView
}

// FindingDecisionView identifies a finding rather than an unstable display index.
type FindingDecisionView struct {
	Ref   work.FindingRef
	Title string
}

// DecisionAction identifies an explicit operator response.
type DecisionAction string

const (
	DecisionPause          DecisionAction = "pause"
	DecisionApprove        DecisionAction = "approve"
	DecisionRequestChanges DecisionAction = "request_changes"
	DecisionAbort          DecisionAction = "abort"
	DecisionKeep           DecisionAction = "keep"
	DecisionWaive          DecisionAction = "waive"
	DecisionDowngrade      DecisionAction = "downgrade"
	DecisionAuthorize      DecisionAction = "authorize"
	DecisionRevise         DecisionAction = "revise"
)

// Response is a closed set of decisions. A wrong alternative is refused before
// any state or external effect changes.
type Response interface{ workflowResponse() }

// PauseResponse leaves any pending interaction unanswered.
type PauseResponse struct{}

func (PauseResponse) workflowResponse() {}

// ManualResponse answers an assessment, never a publication decision.
type ManualResponse struct{ Action DecisionAction }

// FindingsResponse answers all selected blockers atomically.
type FindingsResponse struct{ Decisions []FindingDecision }

// FindingDecision is an explicit disposition of one retained finding.
type FindingDecision struct {
	Ref    work.FindingRef
	Action DecisionAction
	Reason string
}

// PublicationResponse authorizes only the displayed immutable intent.
type PublicationResponse struct {
	Action   DecisionAction
	Evidence string
}

func (ManualResponse) workflowResponse()      {}
func (FindingsResponse) workflowResponse()    {}
func (PublicationResponse) workflowResponse() {}

// ErrStaleInteraction refuses responses to replaced owners or changed state.
var ErrStaleInteraction = errors.New("workflow interaction is stale; inspect pending decisions before responding")

func pendingInteraction(v View, snapshot taskstate.TaskState) *Interaction {
	if v.Owner == nil || (v.Next.Action == TaskRunActionNeedsAttention || v.Next.Action == TaskRunActionCompleted) || snapshot.Work == nil || snapshot.Work.ActiveWorkflow() == nil {
		return nil
	}
	interaction := &Interaction{ID: InteractionID{Owner: *v.Owner, Version: v.Next.Version}}
	switch phase := snapshot.Work.ActiveWorkflow().LastPhase().(type) {
	case *work.PublicationPhase:
		if phase.NeedsPublicationRecovery() {
			interaction.ID.Kind = InteractionPublicationRecovery
			interaction.Actions = []DecisionAction{DecisionPause, DecisionAuthorize}
			return interaction
		}
		if phase.Consent != nil {
			return nil
		}
		interaction.ID.Kind = InteractionPublication
		interaction.Actions = []DecisionAction{DecisionAuthorize, DecisionRevise, DecisionPause}
	case *work.ReviewPhase:
		if !pendingReviewInteraction(interaction, phase, snapshot) {
			return nil
		}
	default:
		return nil
	}
	return interaction
}

func pendingReviewInteraction(interaction *Interaction, phase *work.ReviewPhase, snapshot taskstate.TaskState) bool {
	if len(phase.Steps) == 0 || phase.Abort != nil || phase.ExecutionFailure != nil || phase.AdmissionFailure != nil {
		return false
	}
	latest, ok := taskstate.LatestReview(snapshot)
	if !ok {
		return false
	}
	if step, manual := phase.Steps[len(phase.Steps)-1].(*work.ManualReviewStep); manual {
		if step.Decision != nil || len(step.ReadOnlyFailures) > 0 || manualCommandFailed(step.CommandResult) {
			return false
		}
		interaction.ID.Kind = InteractionManualReview
		interaction.Actions = []DecisionAction{DecisionPause, DecisionAbort}
		action := DecisionApprove
		if taskstate.ReviewHasOpenBlockers(latest) {
			action = DecisionRequestChanges
		}
		interaction.Actions = append(interaction.Actions, action)
		return true
	}
	if latest.Status != taskstate.ReviewStatusWaitingForAutomatedDecision {
		return false
	}
	interaction.ID.Kind = InteractionFindings
	interaction.Actions = []DecisionAction{DecisionKeep, DecisionWaive, DecisionDowngrade, DecisionPause}
	for _, finding := range latest.Findings {
		if finding.Address != nil && finding.Address.Phase == interaction.ID.Owner.Phase && finding.Address.Step == *interaction.ID.Owner.Step && taskstate.IsOpenBlockingReviewFinding(finding) {
			interaction.Findings = append(interaction.Findings, FindingDecisionView{Ref: *finding.Address, Title: finding.Title})
		}
	}
	return len(interaction.Findings) > 0
}

// Respond validates a complete response under mutation protection and saves it
// once at the inspected version. Selection changes can reserve fresh review or replace
// intent, but never execute review or launch publication effects.
// Pause and cancellation leave the exact pending decision intact.
func (s Controller) Respond(ctx context.Context, id InteractionID, response Response) (View, error) {
	if s.BackendFactory == nil {
		return View{}, errors.New("workflow responses require a task source")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result View
	err := state.WithGlobalMutationLockLogger(ctx, s.Paths, "respond to workflow interaction", s.Logger, func() error {
		var err error
		result, err = s.respondLocked(ctx, id, response)
		return err
	})
	return result, err
}

func (s Controller) respondLocked(ctx context.Context, id InteractionID, response Response) (View, error) {
	if err := ctx.Err(); err != nil {
		return View{}, err
	}
	base, err := s.resolveReviewTask(ctx, id.Owner.TaskID)
	if err != nil {
		return View{}, err
	}
	if base.RepoID() != id.Owner.RepositoryID {
		return View{}, ErrStaleInteraction
	}
	store := s.Delivery.Store
	if store == nil {
		store = workstate.NewStore(s.Paths)
	}
	snapshot, err := store.Load(base.RepoID(), base.TaskID())
	if err != nil {
		return View{}, err
	}
	view, err := interactionView(base.Task, snapshot)
	if err != nil {
		return View{}, err
	}
	if err := s.validateResponseTarget(base, snapshot, view.Pending, id); err != nil {
		return View{}, err
	}
	return s.respondToSnapshot(ctx, base, store, snapshot, view, id, response)
}

func (s Controller) respondToSnapshot(ctx context.Context, base ReviewContext, store WorkStore, snapshot work.State, view View, id InteractionID, response Response) (View, error) {
	if recovery, ok := response.(PublicationRecoveryResponse); ok {
		if id.Kind != InteractionPublicationRecovery {
			return View{}, ErrStaleInteraction
		}
		return s.restorePublication(ctx, base, store, &snapshot, recovery)
	}
	if _, manual := response.(PublicationManualRepairResponse); manual {
		if id.Kind != InteractionPublicationRecovery {
			return View{}, ErrStaleInteraction
		}
		return s.continueRepairedPublication(ctx, base, store, &snapshot)
	}
	var changed bool
	var err error
	if proposal, ok := response.(PublicationFlowResponse); ok && id.Kind == InteractionPublication {
		changed, err = s.changePublicationChoices(ctx, base, &snapshot, id.Owner, proposal)
	} else {
		changed, err = applyResponse(&snapshot, *view.Pending, response, time.Now().UTC())
	}
	if err != nil {
		return View{}, err
	}
	if !changed {
		return view, nil
	}
	if _, revising := response.(PublicationFlowResponse); !revising {
		if id.Kind == InteractionPublication {
			err = s.verifyPublicationInput(ctx, base, snapshot, id.Owner)
		} else {
			err = s.verifyInteractionRevision(ctx, snapshot, id.Owner)
		}
		if err != nil {
			return View{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return View{}, err
	}
	version, err := store.Save(snapshot, snapshot.Version)
	if err != nil {
		return View{}, err
	}
	snapshot.Version = version
	return interactionView(base.Task, snapshot)
}

func interactionView(item task.Task, snapshot work.State) (View, error) {
	projected, err := taskstate.Project(snapshot)
	if err != nil {
		return View{}, err
	}
	return InspectTask(item, projected)
}

func applyResponse(snapshot *work.State, pending Interaction, response Response, at time.Time) (bool, error) {
	switch response := response.(type) {
	case PauseResponse:
		return false, nil
	case ManualResponse:
		if pending.ID.Kind == InteractionManualReview && slices.Contains(pending.Actions, response.Action) {
			return applyManualResponse(snapshot, pending.ID.Owner, response, at)
		}
	case FindingsResponse:
		if pending.ID.Kind == InteractionFindings {
			return applyFindingResponses(snapshot, pending, response, at)
		}
	case PublicationResponse:
		if pending.ID.Kind == InteractionPublication && (response.Action == DecisionAuthorize || response.Action == DecisionPause) {
			if response.Action == DecisionPause {
				return false, nil
			}
			err := snapshot.AuthorizePublication(pending.ID.Owner.Workflow, work.PublicationRef{Phase: pending.ID.Owner.Phase}, at, response.Evidence)
			return err == nil, err
		}
	}
	return false, fmt.Errorf("response does not match pending %s interaction", pending.ID.Kind)
}

func applyManualResponse(snapshot *work.State, owner work.AttemptRef, response ManualResponse, at time.Time) (bool, error) {
	if response.Action == DecisionPause {
		return false, nil
	}
	phase := snapshot.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
	step := phase.Steps[*owner.Step].(*work.ManualReviewStep)
	switch response.Action {
	case DecisionApprove:
		step.Decision = &work.ApprovedReview{At: at}
	case DecisionAbort:
		step.Decision = &work.AbortedReview{At: at, Reason: "Operator stopped manual review"}
	case DecisionRequestChanges:
		decision, err := requestedChanges(snapshot, owner, at)
		if err != nil {
			return false, err
		}
		step.Decision = decision
	}
	return true, nil
}

func requestedChanges(snapshot *work.State, owner work.AttemptRef, at time.Time) (*work.RequestedChanges, error) {
	projected, err := taskstate.Project(*snapshot)
	if err != nil {
		return nil, err
	}
	latest, _ := taskstate.LatestReview(projected)
	decision := &work.RequestedChanges{At: at}
	for _, finding := range latest.Findings {
		if finding.Address != nil && finding.Address.Step != *owner.Step && taskstate.IsOpenBlockingReviewFinding(finding) {
			decision.Targets = append(decision.Targets, *finding.Address)
		}
	}
	return decision, nil
}

func validateFindingResponses(pending Interaction, response FindingsResponse) error {
	if len(response.Decisions) != len(pending.Findings) {
		return errors.New("respond to every pending finding explicitly")
	}
	seen := map[work.FindingRef]bool{}
	for _, decision := range response.Decisions {
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

func applyFindingResponses(snapshot *work.State, pending Interaction, response FindingsResponse, at time.Time) (bool, error) {
	if err := validateFindingResponses(pending, response); err != nil {
		return false, err
	}
	for _, decision := range response.Decisions {
		finding, err := snapshot.ActiveWorkflow().Finding(decision.Ref)
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

func manualCommandFailed(result work.ProcessResult) bool {
	if result == nil {
		return false
	}
	exit, ok := result.(*work.ExitedProcess)
	return !ok || exit.Code == nil || *exit.Code != 0
}

func (s Controller) verifyInteractionRevision(ctx context.Context, snapshot work.State, owner work.AttemptRef) error {
	if phase, ok := snapshot.Workflows[owner.Workflow].Phases[owner.Phase].(*work.PublicationPhase); ok {
		owner.Phase = phase.Review.Phase
		owner.Step = nil
	}
	candidate, err := committedCandidate(taskstate.TaskState{Work: &snapshot}, owner)
	if err != nil {
		return err
	}
	boundary := s.CandidateGit
	if boundary == nil {
		boundary = LocalReviewCandidateGit{}
	}
	return boundary.VerifyRevision(ctx, snapshot.Workflows[owner.Workflow].Context().Directory, candidate.Identity, candidate.Revision)
}
