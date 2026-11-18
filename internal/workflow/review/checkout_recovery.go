package review

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// CheckoutViolation explains the retained restoration target and violation.
type CheckoutViolation struct {
	Workdir   string
	Candidate CommittedCandidate
	Reason    string
}

const checkoutRecoveryInputKind = "review_checkout_recovery"

// CheckoutRecoveryRequest discloses the exact retained rollback target.
type CheckoutRecoveryRequest struct {
	ID        phaseexec.InputID
	Violation CheckoutViolation
}

// CheckoutRecoveryAnswer grants restoration explicitly; declining preserves the
// checkout and the unresolved recovery record.
type CheckoutRecoveryAnswer struct{ Restore bool }

type CheckoutRecoveryOutcome struct{ Restored bool }

// CheckoutRecovery owns read-only recovery selection and confirmed restoration.
// The failed review evidence is never replaced by a successful rollback.
type CheckoutRecovery struct {
	Restore func(context.Context, string, CommittedCandidate) error

	answered bool
	restored bool // Actual effect observation, not permission to advance after a failed save.
}

func (s *CheckoutRecovery) Next(current work.State, owner work.AttemptRef) (phaseexec.Selection[CheckoutRecoveryRequest, CheckoutRecoveryOutcome], error) {
	w, phase, err := currentReview(&current, owner)
	if err != nil {
		return phaseexec.Selection[CheckoutRecoveryRequest, CheckoutRecoveryOutcome]{}, err
	}
	recovery := phase.CheckoutRecovery
	if recovery == nil || recovery.RestoredAt != nil || s.answered {
		return phaseexec.Return[CheckoutRecoveryRequest](CheckoutRecoveryOutcome{Restored: recovery != nil && recovery.RestoredAt != nil}), nil
	}
	candidate, err := CommittedInput(taskstate.TaskState{Work: &current}, owner)
	if err != nil {
		return phaseexec.Selection[CheckoutRecoveryRequest, CheckoutRecoveryOutcome]{}, err
	}
	request := CheckoutRecoveryRequest{
		ID:        phaseexec.InputID{Owner: owner, Version: current.Version, Kind: checkoutRecoveryInputKind},
		Violation: CheckoutViolation{Workdir: w.Context().Directory, Candidate: candidate, Reason: recovery.Reason},
	}
	return phaseexec.Await[CheckoutRecoveryRequest, CheckoutRecoveryOutcome](request), nil
}

func (s *CheckoutRecovery) Respond(current work.State, id phaseexec.InputID, answer CheckoutRecoveryAnswer) (phaseexec.Operation, error) {
	request, err := s.pending(current, id)
	if err != nil {
		return phaseexec.Operation{}, err
	}
	return phaseexec.Operation{Name: "resolve review checkout recovery", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		if _, err := s.pending(*current, id); err != nil {
			return phaseexec.Prepared{}, err
		}
		return phaseexec.Prepared{Scope: phaseexec.ReleaseForEffect, Run: func(ctx context.Context) phaseexec.Result {
			if err := ctx.Err(); err != nil {
				return phaseexec.Result{Err: err}
			}
			s.answered = true
			if !answer.Restore {
				return phaseexec.Result{}
			}
			restore := s.Restore
			if restore == nil {
				restore = RestoreCommittedCandidate
			}
			v := request.Violation
			restoreErr := restore(ctx, v.Workdir, v.Candidate)
			s.restored = restoreErr == nil
			var effectErr error
			if restoreErr != nil {
				effectErr = fmt.Errorf("rollback failed or incomplete; manual recovery required in %q to branch %s at commit %s: %w", v.Workdir, v.Candidate.Identity.Ref, v.Candidate.Identity.Commit, restoreErr)
			}
			return phaseexec.Result{Update: checkoutRestorationChange(id.Owner, restoreErr, time.Now().UTC()), Err: effectErr}
		}}, nil
	}}, nil
}

func (s *CheckoutRecovery) pending(current work.State, id phaseexec.InputID) (CheckoutRecoveryRequest, error) {
	selected, err := s.Next(current, id.Owner)
	if err != nil {
		return CheckoutRecoveryRequest{}, err
	}
	request, ok := selected.Input()
	if !ok || request.ID.Version != id.Version || request.ID.Kind != id.Kind || !request.ID.Owner.Equal(id.Owner) {
		return CheckoutRecoveryRequest{}, phaseexec.ErrStaleInput
	}
	return request, nil
}

func checkoutRestorationChange(owner work.AttemptRef, cause error, at time.Time) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		_, phase, err := currentReview(current, owner)
		if err != nil {
			return false, err
		}
		recovery := phase.CheckoutRecovery
		if recovery == nil || recovery.RestoredAt != nil {
			return false, errors.New("no unresolved checkout restoration")
		}
		if cause != nil {
			recovery.Failures = append(recovery.Failures, work.ReadOnlyFailure{At: at, Reason: cause.Error()})
		} else {
			recovery.RestoredAt = &at
		}
		return true, nil
	}
}
