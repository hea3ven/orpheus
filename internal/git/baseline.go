package git

import (
	"context"
	"fmt"
	"strings"
)

// PrepareBaseline retains the actual prepared starting commit, or verifies the
// already recorded workflow baseline without replacing it with a later HEAD.
// It never creates a commit or preserves uncommitted work as revision evidence.
func PrepareBaseline(ctx context.Context, dir, branch, ref string, recorded *Revision) (Revision, error) {
	if !strings.HasPrefix(ref, "refs/orpheus/") {
		return Revision{}, fmt.Errorf("baseline requires an owned retention ref")
	}
	if _, err := runGitContext(ctx, dir, "check-ref-format", ref); err != nil {
		return Revision{}, err
	}
	root, err := worktreeRoot(ctx, dir)
	if err != nil {
		return Revision{}, err
	}
	if root != dir {
		return Revision{}, fmt.Errorf("baseline requires the exact prepared checkout root")
	}
	head, err := (CandidateOperations{}).CheckoutIdentity(ctx, dir)
	if err != nil {
		return Revision{}, err
	}
	if head.Ref != "refs/heads/"+branch {
		return Revision{}, fmt.Errorf("baseline checkout is attached to %q, expected task branch %q", head.Ref, branch)
	}
	if err := requireNoGitOperation(ctx, dir); err != nil {
		return Revision{}, err
	}
	if recorded != nil {
		if recorded.RetainedRef != ref {
			return Revision{}, fmt.Errorf("baseline retention does not match its workflow owner")
		}
		if err := VerifyRetainedRevision(ctx, dir, *recorded); err != nil {
			return Revision{}, err
		}
		return *recorded, nil
	}
	tree, err := revisionTree(ctx, dir, head.Commit)
	if err != nil {
		return Revision{}, err
	}
	if err := createRevisionRef(ctx, dir, ref, head.Commit); err != nil {
		return Revision{}, err
	}
	result := Revision{Commit: head.Commit, Tree: tree, RetainedRef: ref}
	if err := VerifyRetainedRevision(ctx, dir, result); err != nil {
		return Revision{}, err
	}
	return result, nil
}
