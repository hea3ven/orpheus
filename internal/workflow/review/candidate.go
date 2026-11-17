package review

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
)

// CaptureCommittedCandidate admits the retained revision and returns a read-only
// Git-visible checkout check. Detection never authorizes restoration.
func CaptureCommittedCandidate(ctx context.Context, workdir string, expected gitmeta.CheckoutIdentity, revision gitmeta.Revision, _ *slog.Logger) (CandidateCheck, error) {
	if err := gitmeta.VerifyRevision(ctx, workdir, expected, revision); err != nil {
		return nil, err
	}
	return func() error {
		inspection, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := gitmeta.CheckCheckout(inspection, workdir, expected); err != nil {
			return fmt.Errorf("review step changed the checkout; changes preserved in %q: %w", workdir, err)
		}
		return nil
	}, nil
}

// RestoreCommittedCandidate discards disclosed Git-visible changes only after the
// caller has obtained explicit operator consent. Other branch refs are untouched.
func RestoreCommittedCandidate(ctx context.Context, workdir string, candidate CommittedCandidate) error {
	if candidate.Identity.Commit != candidate.Revision.Commit {
		return fmt.Errorf("rollback target differs from the retained review revision")
	}
	ops := gitmeta.CandidateOperations{}
	if err := gitmeta.VerifyRetainedRevision(ctx, workdir, candidate.Revision); err != nil {
		return err
	}
	if err := ops.RestoreCheckoutIdentity(ctx, workdir, candidate.Identity); err != nil {
		return err
	}
	return gitmeta.CheckCheckout(ctx, workdir, candidate.Identity)
}

// HasCandidateChanges reports whether the review worktree contains candidate changes.
func HasCandidateChanges(ctx context.Context, workdir string) (bool, error) {
	status, err := gitmeta.CandidateStatus(ctx, workdir)
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(status)) > 0, nil
}
