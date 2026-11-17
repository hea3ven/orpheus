package review

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// CheckoutRecoveryOptions supplies the existing attached conversation and its
// runner. A missing prompt, cancellation, EOF, or false answer preserves changes.
type CheckoutRecoveryOptions struct {
	Context context.Context
	Owner   work.AttemptRef
	Runner  phaseexec.Runner
	Prompt  func(CheckoutViolation) (bool, error)
	Restore func(context.Context, string, CommittedCandidate) error
	Output  io.Writer
}

// ResolveCheckoutRecovery drives the phase-owned recovery boundary. It always
// stops the invocation when recovery is required, even after successful rollback.
func ResolveCheckoutRecovery(opts CheckoutRecoveryOptions) error {
	current, err := opts.Runner.Load(context.WithoutCancel(opts.Context))
	if err != nil {
		return err
	}
	phase := CheckoutRecovery{Restore: opts.Restore}
	selected, err := phase.Next(current, opts.Owner)
	if err != nil {
		return err
	}
	request, required := selected.Input()
	if !required {
		return nil
	}
	v := request.Violation
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
	// This is the existing response boundary after operator interaction, not a
	// reload between an operation's final save and its next selection.
	current, err = opts.Runner.Load(opts.Context)
	if err != nil {
		return errors.Join(advice, err)
	}
	op, err := phase.Respond(current, request.ID, CheckoutRecoveryAnswer{Restore: confirmed})
	if err != nil {
		return errors.Join(advice, err)
	}
	current, executeErr := opts.Runner.Execute(opts.Context, current, op)
	var outputErr error
	if phase.restored && opts.Output != nil {
		// Report the actual checkout result even if its receipt failed, as in
		// the existing conversation. This does not authorize progression.
		_, outputErr = fmt.Fprintln(opts.Output, "Checkout restored. The review step remains failed; no approval or publication was authorized.")
	}
	if executeErr != nil {
		return errors.Join(advice, executeErr, outputErr)
	}
	_, err = phase.Next(current, opts.Owner)
	return errors.Join(advice, err, outputErr)
}
