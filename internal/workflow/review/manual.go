package review

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// FindingInput contains authored content, not receipts, history or addresses.
type FindingInput struct {
	Classification  work.FindingClassification
	Title           string
	Description     string
	SuggestedAction string
	Proposal        *work.FollowUpProposal
}

// FindingInputFromView copies only editable finding content.
func FindingInputFromView(f taskstate.ReviewFinding) FindingInput {
	input := FindingInput{Classification: work.FindingClassification(f.Type), Title: f.Title, Description: f.Description, SuggestedAction: f.SuggestedAction}
	if !f.TaskProposal.IsZero() {
		input.Proposal = &work.FollowUpProposal{Title: f.TaskProposal.Title, Description: f.TaskProposal.Description, AcceptanceCriteria: f.TaskProposal.AcceptanceCriteria}
	}
	return input
}

func (f FindingInput) normalized() (FindingInput, taskstate.ReviewFinding, error) {
	view := taskstate.ReviewFinding{Type: taskstate.FindingType(f.Classification), Title: f.Title, Description: f.Description, SuggestedAction: f.SuggestedAction}
	if f.Proposal != nil {
		view.TaskProposal = taskstate.ReviewTaskProposal{Title: f.Proposal.Title, Description: f.Proposal.Description, AcceptanceCriteria: f.Proposal.AcceptanceCriteria}
	}
	normalized, err := taskstate.NormalizeReviewFinding(view)
	return FindingInputFromView(normalized), normalized, err
}

type ManualDecision string

const (
	ManualApprove        ManualDecision = "approve"
	ManualRequestChanges ManualDecision = "request_changes"
	ManualAbort          ManualDecision = "abort"
)

type ManualRequest struct {
	ID     phaseexec.InputID
	Review taskstate.ReviewAttempt
	Step   string
}

type ManualSubmission struct {
	Findings []FindingInput
	Promote  []work.FindingRef
	Decision ManualDecision
}

// ManualDraft is an invocation-local editing session. It never calls a store;
// abandoning it leaves the pending assessment and all persisted findings intact.
type ManualDraft struct {
	step     string
	preview  taskstate.ReviewAttempt
	findings []FindingInput
	promote  []work.FindingRef
}

func NewManualDraft(request ManualRequest) *ManualDraft {
	base := taskstate.ReviewAttempt{Findings: slices.Clone(request.Review.Findings)}
	for i, finding := range base.Findings {
		base.Findings[i].NonAuthoritative = taskstate.AuditOnlyReviewFinding(request.Review, finding)
	}
	return &ManualDraft{step: request.Step, preview: copyManualView(base)}
}

func copyManualView(view taskstate.ReviewAttempt) taskstate.ReviewAttempt {
	view.Findings = slices.Clone(view.Findings)
	for i := range view.Findings {
		if ref := view.Findings[i].Address; ref != nil {
			copied := *ref
			view.Findings[i].Address = &copied
		}
		// Draft presentation uses content and retained references, not recording
		// actors, mutable disposition objects or task-creation timestamps.
		view.Findings[i].Actor = nil
		view.Findings[i].Decision = nil
		view.Findings[i].CreatedTaskAt = nil
	}
	return view
}

func (d *ManualDraft) Add(input FindingInput) error {
	normalized, view, err := input.normalized()
	if err != nil {
		return err
	}
	view.Step = d.step
	d.findings = append(d.findings, normalized)
	// New edits have no durable FindingRef until the complete submission commits.
	d.preview.Findings = append(d.preview.Findings, view)
	return nil
}

func (d *ManualDraft) Promote(ref work.FindingRef) error {
	if slices.Contains(d.promote, ref) {
		return errors.New("advisory is already staged for promotion")
	}
	for i, finding := range d.preview.Findings {
		if finding.Address == nil || *finding.Address != ref {
			continue
		}
		if finding.Step == d.step || !taskstate.IsOpenAdvisoryReviewFinding(finding) {
			return errors.New("manual promotion requires an unresolved prior advisory")
		}
		d.preview.Findings[i].Type = taskstate.FindingTypeBlocking
		d.preview.Findings[i].DowngradeReason = ""
		d.promote = append(d.promote, ref)
		return nil
	}
	return errors.New("manual promotion requires an owned advisory")
}

type ManualDraftView struct {
	Findings                []taskstate.ReviewFinding
	HasOpenBlockers         bool
	HasPromotableAdvisories bool
}

func (d *ManualDraft) View() ManualDraftView {
	view := ManualDraftView{Findings: copyManualView(d.preview).Findings, HasOpenBlockers: taskstate.ReviewHasOpenBlockers(d.preview)}
	for _, finding := range view.Findings {
		if finding.Step != d.step && taskstate.IsOpenAdvisoryReviewFinding(finding) {
			view.HasPromotableAdvisories = true
			break
		}
	}
	return view
}

func (d *ManualDraft) Submit(decision ManualDecision) (ManualSubmission, error) {
	blockers := taskstate.ReviewHasOpenBlockers(d.preview)
	switch decision {
	case ManualAbort:
	case ManualApprove:
		if blockers {
			return ManualSubmission{}, errors.New("manual approval cannot accept unresolved required findings")
		}
	case ManualRequestChanges:
		if !blockers {
			return ManualSubmission{}, errors.New("requested changes require unresolved required findings")
		}
	default:
		return ManualSubmission{}, fmt.Errorf("invalid manual decision %q", decision)
	}
	submission := ManualSubmission{Decision: decision, Promote: slices.Clone(d.promote)}
	for _, finding := range d.findings {
		normalized, _, err := finding.normalized()
		if err != nil {
			return ManualSubmission{}, err
		}
		submission.Findings = append(submission.Findings, normalized)
	}
	return submission, nil
}

// applyManualSubmission changes only the candidate already validated by Respond.
// The runner discards it on any validation or persistence failure.
func applyManualSubmission(snapshot *work.State, owner work.AttemptRef, submission ManualSubmission, at time.Time) (bool, error) {
	phase := snapshot.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
	step := phase.Steps[*owner.Step].(*work.ManualReviewStep)
	accepted, err := acceptedManualSubmission(*snapshot, phase, step, submission)
	if err != nil {
		return false, err
	}
	if err := appendManualEdits(snapshot, step, accepted, at); err != nil {
		return false, err
	}
	decision, err := submittedManualDecision(*snapshot, owner, accepted.Decision, at)
	if err != nil {
		return false, err
	}
	step.Decision = decision
	return true, nil
}

func acceptedManualSubmission(snapshot work.State, phase *work.ReviewPhase, step *work.ManualReviewStep, submission ManualSubmission) (ManualSubmission, error) {
	projected, err := taskstate.Project(snapshot)
	if err != nil {
		return ManualSubmission{}, err
	}
	current, ok := taskstate.LatestReview(projected)
	if !ok {
		return ManualSubmission{}, errors.New("manual assessment has no review")
	}
	name := snapshot.ActiveWorkflow().ReviewContracts[phase.Contract].Slots[step.Slot].ConfigKey
	draft := NewManualDraft(ManualRequest{Review: current, Step: name})
	for _, input := range submission.Findings {
		if err := draft.Add(input); err != nil {
			return ManualSubmission{}, err
		}
	}
	for _, ref := range submission.Promote {
		if err := draft.Promote(ref); err != nil {
			return ManualSubmission{}, err
		}
	}
	return draft.Submit(submission.Decision)
}

func appendManualEdits(snapshot *work.State, step *work.ManualReviewStep, submission ManualSubmission, at time.Time) error {
	for _, input := range submission.Findings {
		finding := work.Finding{Title: input.Title, Classification: input.Classification, Description: input.Description, SuggestedAction: input.SuggestedAction, Proposal: input.Proposal}
		if input.Classification == work.FindingBlocking {
			finding.Dispositions = []work.FindingDisposition{&work.RepairRequired{At: at}}
		}
		step.Findings = append(step.Findings, finding)
	}
	for _, ref := range submission.Promote {
		finding, err := snapshot.ActiveWorkflow().Finding(ref)
		if err != nil {
			return err
		}
		finding.Dispositions = append(finding.Dispositions, &work.PromotedFinding{At: at})
	}
	return nil
}

func submittedManualDecision(snapshot work.State, owner work.AttemptRef, decision ManualDecision, at time.Time) (work.ManualAssessment, error) {
	switch decision {
	case ManualApprove:
		return &work.ApprovedReview{At: at}, nil
	case ManualAbort:
		return &work.AbortedReview{At: at, Reason: "Operator stopped manual review"}, nil
	case ManualRequestChanges:
		projected, err := taskstate.Project(snapshot)
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
	default:
		return nil, fmt.Errorf("invalid manual decision %q", decision)
	}
}

func manualCommandFailed(result work.ProcessResult) bool {
	if result == nil {
		return false
	}
	exit, ok := result.(*work.ExitedProcess)
	return !ok || exit.Code == nil || *exit.Code != 0
}
