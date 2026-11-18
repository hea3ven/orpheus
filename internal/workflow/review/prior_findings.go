package review

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// FreshReviewBlocker identifies an unresolved finding that a fresh Review would
// otherwise supersede.
type FreshReviewBlocker struct {
	Index   int
	Finding taskstate.ReviewFinding
}

type FreshReviewBlockerAction string

const (
	FreshReviewBlockerActionKeep              FreshReviewBlockerAction = "keep"
	FreshReviewBlockerActionAddressedManually FreshReviewBlockerAction = "addressed_manually"
	FreshReviewBlockerActionWaive             FreshReviewBlockerAction = "waive"
)

type FreshReviewBlockerDisposition struct {
	FindingIndex int
	Action       FreshReviewBlockerAction
	Reason       string
}

// ErrFreshReviewBlockerDispositionInterrupted preserves the attached frontend's
// unfinished-input boundary. No decisions have been accepted in that case.
var ErrFreshReviewBlockerDispositionInterrupted = errors.New("fresh review blocker disposition interrupted")

type PriorFindingsRequest struct {
	ID       phaseexec.InputID
	Blockers []FreshReviewBlocker
}

type PriorFindingsAnswer struct {
	Dispositions []FreshReviewBlockerDisposition
	Interrupted  bool
}

type PriorFindingsOutcome struct {
	Blocked     bool
	Interrupted bool
}

// PriorFindings preserves the existing per-finding persistence boundary for
// accepted fresh-review dispositions. The complete answer is validated first;
// each following operation records one decision, as before this extraction.
// Saved disposition counts, not an executable cursor, determine what remains.
type PriorFindings struct {
	answered    bool
	interrupted bool
	choices     []priorFindingChoice
}

type priorFindingChoice struct {
	finding     work.FindingRef
	before      int
	disposition work.FindingDisposition
}

const priorFindingsKind = "prior_review_findings"

func (s *PriorFindings) Next(current work.State, owner work.AttemptRef) (phaseexec.Selection[PriorFindingsRequest, PriorFindingsOutcome], error) {
	latest, ok, err := latestPriorReview(current)
	if err != nil {
		return phaseexec.Selection[PriorFindingsRequest, PriorFindingsOutcome]{}, err
	}
	if s.interrupted {
		return phaseexec.Return[PriorFindingsRequest](PriorFindingsOutcome{Interrupted: true}), nil
	}
	if !ok {
		return phaseexec.Return[PriorFindingsRequest](PriorFindingsOutcome{}), nil
	}
	if !s.answered {
		blockers := priorReviewBlockers(latest)
		if len(blockers) == 0 {
			return phaseexec.Return[PriorFindingsRequest](PriorFindingsOutcome{}), nil
		}
		return phaseexec.Await[PriorFindingsRequest, PriorFindingsOutcome](PriorFindingsRequest{ID: phaseexec.InputID{Owner: owner, Version: current.Version, Kind: priorFindingsKind}, Blockers: blockers}), nil
	}
	for _, choice := range s.choices {
		finding, err := current.ActiveWorkflow().Finding(choice.finding)
		if err != nil {
			return phaseexec.Selection[PriorFindingsRequest, PriorFindingsOutcome]{}, err
		}
		if len(finding.Dispositions) == choice.before {
			return phaseexec.Run[PriorFindingsRequest, PriorFindingsOutcome](phaseexec.Operation{Name: "record prior finding decision", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
				changed, err := FindingDecisionChange(owner, choice.finding, choice.disposition)(current)
				return phaseexec.Prepared{Changed: changed}, err
			}}), nil
		}
	}
	return phaseexec.Return[PriorFindingsRequest](PriorFindingsOutcome{Blocked: taskstate.ReviewHasOpenBlockers(latest)}), nil
}

func (s *PriorFindings) Respond(current work.State, id phaseexec.InputID, answer PriorFindingsAnswer) (phaseexec.Operation, error) {
	if id.Kind != priorFindingsKind || s.answered {
		return phaseexec.Operation{}, phaseexec.ErrStaleInput
	}
	latest, ok, err := latestPriorReview(current)
	if err != nil {
		return phaseexec.Operation{}, err
	}
	if !ok {
		return phaseexec.Operation{}, errors.New("prior finding response has no review")
	}
	var choices []priorFindingChoice
	if !answer.Interrupted {
		if err := validateFreshReviewBlockerDispositions(priorReviewBlockers(latest), answer.Dispositions); err != nil {
			return phaseexec.Operation{}, err
		}
		for _, disposition := range answer.Dispositions {
			finding := latest.Findings[disposition.FindingIndex]
			if finding.Address == nil {
				return phaseexec.Operation{}, errors.New("finding has no workflow-owned address")
			}
			var decision work.FindingDisposition
			switch disposition.Action {
			case FreshReviewBlockerActionKeep:
				decision = &work.RepairRequired{}
			case FreshReviewBlockerActionAddressedManually:
				decision = &work.AddressedFinding{Waiver: work.Waiver{Reason: disposition.Reason}}
			case FreshReviewBlockerActionWaive:
				decision = &work.WaivedFinding{Waiver: work.Waiver{Reason: disposition.Reason}}
			}
			retained, err := current.ActiveWorkflow().Finding(*finding.Address)
			if err != nil {
				return phaseexec.Operation{}, err
			}
			choices = append(choices, priorFindingChoice{finding: *finding.Address, before: len(retained.Dispositions), disposition: decision})
		}
	}
	return phaseexec.Operation{Name: "accept prior finding dispositions", Prepare: func(*work.State) (phaseexec.Prepared, error) {
		return phaseexec.Prepared{Run: func(context.Context) phaseexec.Result {
			s.answered, s.interrupted, s.choices = true, answer.Interrupted, choices
			return phaseexec.Result{}
		}}, nil
	}}, nil
}

func latestPriorReview(current work.State) (taskstate.ReviewAttempt, bool, error) {
	view, err := taskstate.Project(current)
	if err != nil {
		return taskstate.ReviewAttempt{}, false, err
	}
	latest, ok := taskstate.LatestReview(view)
	return latest, ok, nil
}

func priorReviewBlockers(latest taskstate.ReviewAttempt) []FreshReviewBlocker {
	var blockers []FreshReviewBlocker
	for _, index := range taskstate.UntargetedBlockingFindingIndexes(latest) {
		blockers = append(blockers, FreshReviewBlocker{Index: index, Finding: latest.Findings[index]})
	}
	return blockers
}

func validateFreshReviewBlockerDispositions(blockers []FreshReviewBlocker, dispositions []FreshReviewBlockerDisposition) error {
	if len(dispositions) != len(blockers) {
		return fmt.Errorf("expected dispositions for %d blockers, got %d", len(blockers), len(dispositions))
	}
	allowed := make(map[int]bool, len(blockers))
	for _, blocker := range blockers {
		allowed[blocker.Index] = true
	}
	seen := make(map[int]bool, len(dispositions))
	for _, disposition := range dispositions {
		if !allowed[disposition.FindingIndex] || seen[disposition.FindingIndex] {
			return fmt.Errorf("disposition for finding %d is invalid", disposition.FindingIndex+1)
		}
		seen[disposition.FindingIndex] = true
		switch disposition.Action {
		case FreshReviewBlockerActionKeep:
		case FreshReviewBlockerActionAddressedManually, FreshReviewBlockerActionWaive:
			if strings.TrimSpace(disposition.Reason) == "" {
				return fmt.Errorf("disposition for finding %d requires a reason", disposition.FindingIndex+1)
			}
		default:
			return fmt.Errorf("disposition for finding %d has unsupported action %q", disposition.FindingIndex+1, disposition.Action)
		}
	}
	return nil
}
