package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// CheckCheckout checks supplied commit/branch expectations and ordinary Git
// status. Empty identity fields impose no expectation. Ignored files and Git's
// configured index, mode and content-conversion behavior remain Git's concern.
// This observation never stages, commits, or discards checkout changes.
func CheckCheckout(ctx context.Context, dir string, expected CheckoutIdentity) error {
	if expected.Commit != "" || expected.Ref != "" {
		head, err := (CandidateOperations{}).CheckoutIdentity(ctx, dir)
		if err != nil {
			return err
		}
		if (expected.Commit != "" && head.Commit != expected.Commit) || (expected.Ref != "" && head.Ref != expected.Ref) {
			return fmt.Errorf("checkout requires HEAD %s at %q, got %s at %q", expected.Commit, expected.Ref, head.Commit, head.Ref)
		}
	}
	dirty, err := HasWorkingTreeChanges(ctx, dir)
	if err != nil {
		return err
	}
	if dirty {
		return fmt.Errorf("checkout %q has uncommitted changes; staged, unstaged and non-ignored untracked output must be settled before continuing", dir)
	}
	return nil
}

// VerifyRevision admits a recorded revision using the shared checkout contract.
// Commit/tree existence and retention are checked separately from cleanliness.
func VerifyRevision(ctx context.Context, dir string, expected CheckoutIdentity, revision Revision) error {
	if !validObjectID(expected.Commit) || revision.Commit != expected.Commit {
		return fmt.Errorf("review requires an identified, retained revision")
	}
	if err := VerifyRetainedRevision(ctx, dir, revision); err != nil {
		return err
	}
	root, err := worktreeRoot(ctx, dir)
	if err != nil {
		return err
	}
	if root != dir {
		return fmt.Errorf("review requires the recorded worktree root, got %q", root)
	}
	return CheckCheckout(ctx, dir, expected)
}

// VerifyRetainedRevision verifies native commit/tree identity and its owned ref
// without requiring HEAD or the checkout to represent that historical revision.
// It does not establish a safe review checkout or authorize any external effect.
func VerifyRetainedRevision(ctx context.Context, dir string, revision Revision) error {
	if !validObjectID(revision.Commit) || !validObjectID(revision.Tree) || !strings.HasPrefix(revision.RetainedRef, "refs/orpheus/") {
		return fmt.Errorf("revision requires native commit/tree identity and an owned retention ref")
	}
	retained, err := optionalRevisionRef(ctx, dir, revision.RetainedRef)
	if err != nil {
		return err
	}
	if retained != revision.Commit {
		return fmt.Errorf("revision retention ref no longer identifies the recorded commit")
	}
	tree, err := revisionTree(ctx, dir, revision.Commit)
	if err != nil {
		return err
	}
	if tree != revision.Tree {
		return fmt.Errorf("revision tree differs from the recorded commit tree")
	}
	return nil
}

// requireNoGitOperation protects commit/history rewriting from incorporating an
// unfinished operation into Orpheus output. Observation and retained recovery do
// not need this check. Native Git handles index locking itself.
func requireNoGitOperation(ctx context.Context, dir string) error {
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer"} {
		output, err := runGitContext(ctx, dir, "rev-parse", "--path-format=absolute", "--git-path", name)
		if err != nil {
			return fmt.Errorf("inspect unfinished Git operation: %w", err)
		}
		_, err = os.Lstat(strings.TrimSpace(output))
		if err == nil {
			return fmt.Errorf("revision requires settled Git state; %s exists", name)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect unfinished Git operation %s: %w", name, err)
		}
	}
	return nil
}
