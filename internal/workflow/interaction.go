package workflow

import (
	"context"
	"errors"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/delivery"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/review"
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

// Interaction projects phase-owned requests for the current inspection facade.
type Interaction struct {
	ID       InteractionID
	Actions  []DecisionAction
	Findings []FindingDecisionView
}

type FindingDecisionView struct {
	Ref   work.FindingRef
	Title string
}

// DecisionAction is the presentation label of a phase-owned legal answer.
type DecisionAction string

const (
	DecisionPause     DecisionAction = "pause"
	DecisionAuthorize DecisionAction = "authorize"
	DecisionRevise    DecisionAction = "revise"
)

// PauseResponse is the facade's phase-independent request to leave input pending.
type PauseResponse struct{}

func pendingInteraction(v View, snapshot taskstate.TaskState) (*Interaction, error) {
	if v.Owner == nil || v.Next.Action == TaskRunActionNeedsAttention || v.Next.Action == TaskRunActionCompleted || snapshot.Work == nil || snapshot.Work.ActiveWorkflow() == nil {
		return nil, nil
	}
	interaction := &Interaction{ID: InteractionID{Owner: *v.Owner, Version: v.Next.Version}}
	switch snapshot.Work.ActiveWorkflow().LastPhase().(type) {
	case *work.PublicationPhase:
		switch request := delivery.Pending(*snapshot.Work).(type) {
		case delivery.RecoveryRequest:
			interaction.ID = InteractionID{Owner: request.ID.Owner, Version: request.ID.Version, Kind: InteractionPublicationRecovery}
			interaction.Actions = []DecisionAction{DecisionPause, DecisionAuthorize}
		case delivery.ConsentRequest:
			interaction.ID = InteractionID{Owner: request.ID.Owner, Version: request.ID.Version, Kind: InteractionPublication}
			interaction.Actions = []DecisionAction{DecisionAuthorize, DecisionRevise, DecisionPause}
		default:
			return nil, nil
		}
	case *work.ReviewPhase:
		request, err := review.PendingDecision(*snapshot.Work)
		if err != nil || request == nil {
			return nil, err
		}
		interaction.ID = InteractionID{Owner: request.ID.Owner, Version: request.ID.Version, Kind: InteractionKind(request.ID.Kind)}
		for _, action := range request.Actions {
			interaction.Actions = append(interaction.Actions, DecisionAction(action))
		}
		for _, finding := range request.Findings {
			interaction.Findings = append(interaction.Findings, FindingDecisionView{Ref: finding.Ref, Title: finding.Title})
		}
	default:
		return nil, nil
	}
	return interaction, nil
}

// Respond routes typed phase answers through the existing inspection boundary.
// A response does not authorize a cross-phase transition or further execution.
func (s Controller) Respond(ctx context.Context, id InteractionID, response any) (View, error) {
	if s.BackendFactory == nil {
		return View{}, errors.New("workflow responses require a task source")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if id.Kind == InteractionPublication || id.Kind == InteractionPublicationRecovery {
		return s.respondDelivery(ctx, id, response)
	}
	return s.respondReview(ctx, id, response)
}

func interactionView(item task.Task, snapshot work.State) (View, error) {
	projected, err := taskstate.Project(snapshot)
	if err != nil {
		return View{}, err
	}
	return InspectTask(item, projected)
}

func validateInteractionID(pending *Interaction, id InteractionID) error {
	if pending == nil || !pending.ID.Owner.Equal(id.Owner) || pending.ID.Kind != id.Kind || pending.ID.Version != id.Version {
		return phaseexec.ErrStaleInput
	}
	return nil
}
