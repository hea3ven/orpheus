package review

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// SeparateTaskCandidate describes a submitted finding that can become a native task.
type SeparateTaskCandidate struct {
	Index   int
	Finding taskstate.ReviewFinding
}

func (c SeparateTaskCandidate) createOptions(owner work.AttemptRef, attempt int) task.CreateOptions {
	proposal := c.Finding.TaskProposal
	provenance := fmt.Sprintf("Discovered during review of %s in repository %s (review attempt %d, finding %d).", owner.TaskID, owner.RepositoryID, attempt, c.Index+1)
	if strings.TrimSpace(c.Finding.Step) != "" {
		provenance += " Review step: " + c.Finding.Step + "."
	}
	return task.CreateOptions{
		Title: proposal.Title, Description: strings.TrimSpace(proposal.Description) + "\n\nProvenance:\n" + provenance,
		AcceptanceCriteria: proposal.AcceptanceCriteria, IssueType: task.IssueTypeTask,
	}
}

// FollowUps owns selection, creation and receipts for submitted separate-task
// proposals. Its selection is invocation-local operator intent, not a progress
// cursor: pending work is always derived from the saved finding receipts.
type FollowUps struct {
	Backend func() (task.CreateMutator, error)
	Created func(SeparateTaskCandidate, task.Task) error

	backend   task.CreateMutator
	selected  bool
	selection []work.FindingRef
	created   *followUpCreation
	failure   *FollowUpCreationFailure
	finished  bool
}

type followUpCreation struct {
	candidate SeparateTaskCandidate
	task      task.Task
}

type FollowUpRequest interface{ followUpRequest() }

type FollowUpSelection struct {
	ID         phaseexec.InputID
	Candidates []SeparateTaskCandidate
}

func (FollowUpSelection) followUpRequest() {}

type FollowUpCreationFailure struct {
	ID        phaseexec.InputID
	Candidate SeparateTaskCandidate
	Err       error
}

func (FollowUpCreationFailure) followUpRequest() {}

type FollowUpAnswer interface{ followUpAnswer() }

type SelectFollowUps struct{ Candidates []SeparateTaskCandidate }

func (SelectFollowUps) followUpAnswer() {}

type ContinueAfterCreationFailure struct{ Proceed bool }

func (ContinueAfterCreationFailure) followUpAnswer() {}

type FollowUpOutcome struct{ Aborted bool }

const followUpSelectionKind = "review_follow_ups"
const followUpFailureKind = "review_follow_up_failure"

func (s *FollowUps) Next(current work.State, owner work.AttemptRef) (phaseexec.Selection[FollowUpRequest, FollowUpOutcome], error) {
	latest, err := reviewAt(current, owner)
	if err != nil {
		return phaseexec.Selection[FollowUpRequest, FollowUpOutcome]{}, err
	}
	if s.finished || latest.Status == taskstate.ReviewStatusAborted {
		return phaseexec.Return[FollowUpRequest](FollowUpOutcome{Aborted: latest.Status == taskstate.ReviewStatusAborted}), nil
	}
	if s.failure != nil {
		request := *s.failure
		request.ID.Version = current.Version
		return phaseexec.Await[FollowUpRequest, FollowUpOutcome](request), nil
	}
	if s.created != nil {
		finding, err := current.ActiveWorkflow().Finding(*s.created.candidate.Finding.Address)
		if err != nil {
			return phaseexec.Selection[FollowUpRequest, FollowUpOutcome]{}, err
		}
		if finding.CreatedTask == nil || finding.CreatedTask.TaskID != s.created.task.ID {
			return phaseexec.Selection[FollowUpRequest, FollowUpOutcome]{}, errors.New("follow-up receipt was not committed")
		}
		return phaseexec.Run[FollowUpRequest, FollowUpOutcome](s.reportCreated(owner)), nil
	}
	candidates := pendingSeparateTaskCandidates(latest)
	if !s.selected {
		if len(candidates) == 0 {
			return phaseexec.Return[FollowUpRequest](FollowUpOutcome{}), nil
		}
		return phaseexec.Await[FollowUpRequest, FollowUpOutcome](FollowUpSelection{ID: phaseexec.InputID{Owner: owner, Version: current.Version, Kind: followUpSelectionKind}, Candidates: candidates}), nil
	}
	for _, ref := range s.selection {
		for _, candidate := range candidates {
			if candidate.Finding.Address != nil && *candidate.Finding.Address == ref {
				return phaseexec.Run[FollowUpRequest, FollowUpOutcome](s.create(owner, latest.Attempt, candidate)), nil
			}
		}
	}
	return phaseexec.Return[FollowUpRequest](FollowUpOutcome{}), nil
}

// Respond translates the existing attached selection and failure conversations
// into operations. It does not create native tasks or save an answer itself.
func (s *FollowUps) Respond(current work.State, id phaseexec.InputID, answer FollowUpAnswer) (phaseexec.Operation, error) {
	latest, err := reviewAt(current, id.Owner)
	if err != nil {
		return phaseexec.Operation{}, err
	}
	switch response := answer.(type) {
	case SelectFollowUps:
		if id.Kind != followUpSelectionKind || s.selected {
			return phaseexec.Operation{}, phaseexec.ErrStaleInput
		}
		selected, err := selectedFollowUpRefs(pendingSeparateTaskCandidates(latest), response.Candidates)
		if err != nil {
			return phaseexec.Operation{}, err
		}
		return phaseexec.Operation{Name: "select review follow-ups", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
			if _, err := reviewAt(*current, id.Owner); err != nil {
				return phaseexec.Prepared{}, err
			}
			return phaseexec.Prepared{Run: func(context.Context) phaseexec.Result {
				s.selected, s.selection = true, selected
				return phaseexec.Result{}
			}}, nil
		}}, nil
	case ContinueAfterCreationFailure:
		if id.Kind != followUpFailureKind || s.failure == nil {
			return phaseexec.Operation{}, phaseexec.ErrStaleInput
		}
		return phaseexec.Operation{Name: "answer follow-up creation failure", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
			if _, err := reviewAt(*current, id.Owner); err != nil {
				return phaseexec.Prepared{}, err
			}
			changed := false
			if !response.Proceed {
				var err error
				changed, err = AbortChange(id.Owner, "Operator declined continuation after follow-up creation failure")(current)
				if err != nil {
					return phaseexec.Prepared{}, err
				}
			}
			return phaseexec.Prepared{Changed: changed, Run: func(context.Context) phaseexec.Result {
				s.finished, s.failure = true, nil
				return phaseexec.Result{}
			}}, nil
		}}, nil
	default:
		return phaseexec.Operation{}, errors.New("unsupported review follow-up answer")
	}
}

func (s *FollowUps) create(owner work.AttemptRef, attempt int, candidate SeparateTaskCandidate) phaseexec.Operation {
	return phaseexec.Operation{Name: "create review follow-up", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		if _, err := reviewAt(*current, owner); err != nil {
			return phaseexec.Prepared{}, err
		}
		return phaseexec.Prepared{Scope: phaseexec.ReleaseForEffect, Run: func(ctx context.Context) phaseexec.Result {
			if s.backend == nil {
				var err error
				s.backend, err = s.Backend()
				if err != nil {
					return phaseexec.Result{Err: fmt.Errorf("task run %s: create follow-up task backend: %w", owner.TaskID, err)}
				}
			}
			created, err := s.backend.Create(ctx, candidate.createOptions(owner, attempt))
			if err != nil {
				s.failure = &FollowUpCreationFailure{ID: phaseexec.InputID{Owner: owner, Kind: followUpFailureKind}, Candidate: candidate, Err: err}
				return phaseexec.Result{}
			}
			s.created = &followUpCreation{candidate: candidate, task: created}
			return phaseexec.Result{Update: CreatedTaskChange(owner, *candidate.Finding.Address, created.ID)}
		}}, nil
	}}
}

func (s *FollowUps) reportCreated(owner work.AttemptRef) phaseexec.Operation {
	return phaseexec.Operation{Name: "report review follow-up", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		if _, err := reviewAt(*current, owner); err != nil {
			return phaseexec.Prepared{}, err
		}
		created := *s.created
		return phaseexec.Prepared{Scope: phaseexec.ReleaseForEffect, Run: func(context.Context) phaseexec.Result {
			if s.Created != nil {
				if err := s.Created(created.candidate, created.task); err != nil {
					return phaseexec.Result{Err: fmt.Errorf("%w: %w", ErrPresentationUnavailable, err)}
				}
			}
			s.created = nil
			return phaseexec.Result{}
		}}, nil
	}}
}

func selectedFollowUpRefs(pending, selected []SeparateTaskCandidate) ([]work.FindingRef, error) {
	byIndex := make(map[int]work.FindingRef, len(pending))
	for _, candidate := range pending {
		byIndex[candidate.Index] = *candidate.Finding.Address
	}
	var refs []work.FindingRef
	for _, candidate := range selected {
		ref, ok := byIndex[candidate.Index]
		if !ok {
			return nil, errors.New("follow-up selection does not identify a pending proposal")
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func pendingSeparateTaskCandidates(latest taskstate.ReviewAttempt) []SeparateTaskCandidate {
	var candidates []SeparateTaskCandidate
	for index, finding := range latest.Findings {
		if finding.Type == taskstate.FindingTypeSeparateTask && strings.TrimSpace(finding.CreatedTaskID) == "" {
			candidates = append(candidates, SeparateTaskCandidate{Index: index, Finding: finding})
		}
	}
	return candidates
}

// CreatedTaskChange records the native receipt without taking ownership of the
// new task's lifecycle. A lost receipt remains a failed operation, not a retry.
func CreatedTaskChange(actor work.AttemptRef, target work.FindingRef, taskID string) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		if strings.TrimSpace(taskID) == "" {
			return false, errors.New("created follow-up requires observed native task identity")
		}
		w, _, err := currentReview(current, actor)
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
		finding.CreatedTask = &work.CreatedFollowUp{TaskID: taskID, ObservedAt: time.Now().UTC()}
		return true, nil
	}
}

// AbortChange records an operator stop outside an individual assessment.
func AbortChange(owner work.AttemptRef, reason string) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		_, phase, err := currentReview(current, owner)
		if err != nil {
			return false, err
		}
		if phase.Abort != nil {
			return false, nil
		}
		phase.Abort = &work.AbortedReview{At: time.Now().UTC(), Reason: reason}
		return true, nil
	}
}

// SelectSeparateTaskCandidates parses the attached frontend's selection string.
func SelectSeparateTaskCandidates(candidates []SeparateTaskCandidate, input string) ([]SeparateTaskCandidate, error) {
	answer := strings.ToLower(strings.TrimSpace(input))
	switch answer {
	case "", "n", "no", "none", "skip":
		return nil, nil
	case "a", "all":
		return candidates, nil
	}
	fields := strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	if len(fields) == 0 {
		return nil, nil
	}
	seen := map[int]bool{}
	selected := make([]SeparateTaskCandidate, 0, len(fields))
	for _, field := range fields {
		number, err := strconv.Atoi(field)
		if err != nil || number < 1 || number > len(candidates) {
			return nil, fmt.Errorf("invalid follow-up task selection %q", strings.TrimSpace(input))
		}
		if seen[number] {
			continue
		}
		seen[number] = true
		selected = append(selected, candidates[number-1])
	}
	return selected, nil
}
