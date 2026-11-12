package git

import (
	"context"
	"fmt"
)

// WorkflowResetRequest identifies an existing task checkout, never a new branch.
type WorkflowResetRequest struct {
	RepoPath, Directory, Branch, DefaultBranch, Ref string
	Target                                          Revision
	// Public commits may otherwise be reachable only through the moving task branch.
	// Private history already has owned refs, or has intentionally been released.
	RetainCommits []string
}

func validateWorkflowReset(ctx context.Context, req WorkflowResetRequest) error {
	common, err := gitCommonDir(ctx, req.RepoPath)
	if err != nil {
		return err
	}
	return validateExistingTaskWorktree(ctx, req.Directory, req.Branch, common)
}

// InspectWorkflowReset observes ordinary Git status without fetching or mutating.
func InspectWorkflowReset(ctx context.Context, req WorkflowResetRequest) (string, error) {
	if err := validateWorkflowReset(ctx, req); err != nil {
		return "", err
	}
	return ShortStatus(ctx, req.Directory)
}

// ResolveWorkflowReset shares initial task preparation's fetch and baseline lookup.
// Preparation persists this target before ApplyWorkflowReset retains and applies it.
func ResolveWorkflowReset(ctx context.Context, req WorkflowResetRequest) (Revision, error) {
	if err := validateWorkflowReset(ctx, req); err != nil {
		return Revision{}, err
	}
	if err := validateBranchRef(ctx, req.RepoPath, "default branch", req.DefaultBranch); err != nil {
		return Revision{}, err
	}
	remote, err := prepareDefaultBranchRef(ctx, req.RepoPath, req.DefaultBranch)
	if err != nil {
		return Revision{}, err
	}
	commit, err := gitRefCommit(ctx, req.RepoPath, remote)
	if err != nil {
		return Revision{}, err
	}
	tree, err := revisionTree(ctx, req.RepoPath, commit)
	if err != nil {
		return Revision{}, err
	}
	return Revision{Commit: commit, Tree: tree, RetainedRef: req.Ref}, nil
}

// ApplyWorkflowReset consumes only a previously recorded target. It never fetches
// or writes a remote. Native Git restores the baseline, including over ignored
// obstructions, and preserves unrelated ignored files during untracked cleanup.
func ApplyWorkflowReset(ctx context.Context, req WorkflowResetRequest) error {
	if err := validateWorkflowReset(ctx, req); err != nil {
		return err
	}
	if err := createRevisionRef(ctx, req.Directory, req.Target.RetainedRef, req.Target.Commit); err != nil {
		return err
	}
	if err := VerifyRetainedRevision(ctx, req.Directory, req.Target); err != nil {
		return err
	}
	for _, commit := range req.RetainCommits {
		if err := createRevisionRef(ctx, req.Directory, req.Ref+"-history/"+commit, commit); err != nil {
			return err
		}
	}
	if _, err := runGitContext(ctx, req.Directory, "reset", "--hard", req.Target.Commit); err != nil {
		return fmt.Errorf("reset task checkout: %w", err)
	}
	if _, err := runGitContext(ctx, req.Directory, "clean", "-fd"); err != nil {
		return fmt.Errorf("clean task checkout: %w", err)
	}
	return CheckCheckout(ctx, req.Directory, CheckoutIdentity{Commit: req.Target.Commit, Ref: "refs/heads/" + req.Branch})
}
