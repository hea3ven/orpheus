package review

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/hea3ven/orpheus/internal/work"
)

// CheckoutViolation explains the retained restoration target and violation.
type CheckoutViolation struct {
	Workdir   string
	Candidate CommittedCandidate
	Reason    string
}

// CheckoutRecoveryOptions supplies presentation and the confirmed restoration
// effect. A missing prompt, cancellation, EOF, or false answer preserves changes.
type CheckoutRecoveryOptions struct {
	Context context.Context
	Owner   work.AttemptRef
	Store   interface {
		RecordCheckoutRestoration(work.AttemptRef, error) error
	}
	Violation CheckoutViolation
	Prompt    func(CheckoutViolation) (bool, error)
	Restore   func(context.Context, string, CommittedCandidate) error
	Output    io.Writer
}

// ResolveCheckoutRecovery always stops this invocation, even after successful
// restoration. It never changes the failed step's evidence or grants approval.
func ResolveCheckoutRecovery(opts CheckoutRecoveryOptions) error {
	v := opts.Violation
	advice := fmt.Errorf("review checkout violation: %s; restoration target %s at %s in %q; review remains failed", v.Reason, v.Candidate.Identity.Ref, v.Candidate.Identity.Commit, v.Workdir)
	if opts.Output != nil {
		if _, err := fmt.Fprintf(opts.Output, "%v\nRoll back discards staged, unstaged, and non-ignored untracked changes in this task checkout. Ignored files and unrelated branches are preserved. Quit without restoring preserves changes for inspection.\n", advice); err != nil {
			return errors.Join(advice, err)
		}
	}
	if opts.Context.Err() != nil {
		return errors.Join(advice, opts.Context.Err())
	}
	if opts.Prompt == nil {
		return errors.Join(advice, ErrInteractionRequired)
	}
	confirmed, err := opts.Prompt(v)
	if err != nil {
		return errors.Join(advice, err)
	}
	if !confirmed {
		return advice
	}
	if opts.Context.Err() != nil {
		return errors.Join(advice, opts.Context.Err())
	}
	restore := opts.Restore
	if restore == nil {
		restore = RestoreCommittedCandidate
	}
	restoreErr := restore(opts.Context, v.Workdir, v.Candidate)
	recordErr := opts.Store.RecordCheckoutRestoration(opts.Owner, restoreErr)
	if restoreErr != nil {
		return errors.Join(advice, fmt.Errorf("rollback failed or incomplete; manual recovery required in %q to branch %s at commit %s: %w", v.Workdir, v.Candidate.Identity.Ref, v.Candidate.Identity.Commit, restoreErr), recordErr)
	}
	if opts.Output != nil {
		_, err = fmt.Fprintln(opts.Output, "Checkout restored. The review step remains failed; no approval or publication was authorized.")
	}
	return errors.Join(advice, recordErr, err)
}
