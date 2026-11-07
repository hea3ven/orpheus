package git

import (
	"context"
	"errors"
)

// verifyTaskBranchPublication recognizes only the recorded local/public mapping,
// not arbitrary divergent branches with coincidentally matching trees.
func verifyTaskBranchPublication(ctx context.Context, plan taskBranchSyncPlan) error {
	publication := plan.Publication
	if err := VerifyRevision(ctx, plan.Worktree, CheckoutIdentity{Commit: publication.Input.Commit, Ref: "refs/heads/" + plan.Branch}, publication.Input); err != nil {
		return err
	}
	if err := VerifyRetainedRevision(ctx, plan.Worktree, publication.Public); err != nil {
		return err
	}
	remote, err := revParse(ctx, plan.Worktree, "refs/remotes/origin/"+plan.Branch)
	if err != nil {
		return err
	}
	if remote != publication.Public.Commit || publication.Input.Tree != publication.Public.Tree {
		return errors.New("remote branch differs from retained publication; reconcile before sync")
	}
	return nil
}

func verifyPublicSyncDestination(ctx context.Context, plan taskBranchSyncPlan) error {
	if plan.Publication == nil {
		return nil
	}
	contains, err := branchContainsRef(ctx, plan.Worktree, plan.Publication.Public.Commit, "refs/remotes/origin/"+plan.DefaultBranch)
	if err != nil {
		return err
	}
	if !contains {
		return errors.New("public branch requires content-changing synchronization; reconcile retained history before sync")
	}
	return nil
}

func unchangedPublicSync(ctx context.Context, plan taskBranchSyncPlan, result TaskBranchSyncResult) (TaskBranchSyncResult, error) {
	if err := verifyPublicSyncDestination(ctx, plan); err != nil {
		return TaskBranchSyncResult{}, err
	}
	result.Status = TaskBranchSyncAlreadyCurrent
	result.PreviousHead, result.Head = plan.Publication.Input.Commit, plan.Publication.Input.Commit
	return result, nil
}
