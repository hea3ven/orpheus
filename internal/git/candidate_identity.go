package git

import (
	"context"
	"fmt"
	"strings"
)

// CheckoutIdentity identifies both HEAD's commit and its attachment. A clean
// checkout on a different branch is not the same review input.
type CheckoutIdentity struct {
	Commit string
	Ref    string
}

// CheckoutIdentity observes a checkout's actual commit and symbolic HEAD.
func (ops CandidateOperations) CheckoutIdentity(ctx context.Context, dir string) (CheckoutIdentity, error) {
	head, err := HeadCommit(ctx, dir)
	if err != nil {
		return CheckoutIdentity{}, err
	}
	ref, err := runGitContextLogger(ctx, ops.loggerFor(ctx), dir, "candidate_head_ref", "symbolic-ref", "--quiet", "HEAD")
	if err != nil && gitExitCode(err) != 1 {
		return CheckoutIdentity{}, fmt.Errorf("read candidate HEAD attachment: %w: %s", err, strings.TrimSpace(ref))
	}
	return CheckoutIdentity{Commit: head, Ref: strings.TrimSpace(ref)}, nil
}

// RestoreCheckoutIdentity restores only the captured branch (or detached HEAD),
// without moving a different branch that a reviewer may have selected. Reset
// restores the tracked tree and index; the caller restores its raw file snapshot.
func (ops CandidateOperations) RestoreCheckoutIdentity(ctx context.Context, dir string, identity CheckoutIdentity) error {
	if identity.Commit == "" || (identity.Ref != "" && !strings.HasPrefix(identity.Ref, "refs/heads/")) {
		return fmt.Errorf("invalid captured checkout identity")
	}
	root, err := worktreeRoot(ctx, dir)
	if err != nil {
		return err
	}
	if root != dir {
		return fmt.Errorf("review checkout was redirected from %q to %q; refusing restoration", dir, root)
	}
	if err := restoreReviewHEAD(ctx, dir, identity); err != nil {
		return err
	}
	if output, err := runGitContext(ctx, dir, "reset", "--hard", identity.Commit, "--"); err != nil {
		return fmt.Errorf("restore review checkout: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

func restoreReviewHEAD(ctx context.Context, dir string, identity CheckoutIdentity) error {
	if identity.Ref == "" {
		if _, err := runGitContext(ctx, dir, "update-ref", "--no-deref", "HEAD", identity.Commit); err != nil {
			return fmt.Errorf("restore detached review HEAD: %w", err)
		}
		return nil
	}
	current, err := gitRefCommit(ctx, dir, identity.Ref)
	if err != nil {
		return err
	}
	if _, err := runGitContext(ctx, dir, "update-ref", identity.Ref, identity.Commit, current); err != nil {
		return fmt.Errorf("restore review branch: %w", err)
	}
	if _, err := runGitContext(ctx, dir, "symbolic-ref", "HEAD", identity.Ref); err != nil {
		return fmt.Errorf("restore review HEAD attachment: %w", err)
	}
	return nil
}

// IndexEntries captures stage, object, mode, and assume-unchanged/skip-worktree
// flags. Unlike raw index bytes it ignores Git's incidental stat-cache refreshes.
func (ops CandidateOperations) IndexEntries(ctx context.Context, dir string) ([]byte, error) {
	output, err := runGitContextLogger(ctx, ops.loggerFor(ctx), dir, "candidate_index", "ls-files", "--stage", "-v", "-z")
	if err != nil {
		return nil, fmt.Errorf("read review index entries: %w", err)
	}
	return []byte(output), nil
}

// IndexPath returns this worktree's index, including linked-worktree layouts.
func (ops CandidateOperations) IndexPath(ctx context.Context, dir string) (string, error) {
	output, err := runGitContextLogger(ctx, ops.loggerFor(ctx), dir, "candidate_index_path", "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", fmt.Errorf("locate review index: %w", err)
	}
	return strings.TrimSpace(output), nil
}

// TrackedFiles returns every tracked path, including files hidden from diff by
// index flags or core.filemode=false. The caller compares actual bytes and modes.
func (ops CandidateOperations) TrackedFiles(ctx context.Context, dir string) ([]string, error) {
	output, err := runGitContextLogger(ctx, ops.loggerFor(ctx), dir, "candidate_tracked", "ls-files", "--cached", "-z")
	if err != nil {
		return nil, fmt.Errorf("list tracked review files: %w", err)
	}
	return splitNUL([]byte(output)), nil
}
