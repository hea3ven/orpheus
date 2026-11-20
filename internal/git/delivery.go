package git

import (
	"context"
	"errors"
)

// PublishedRevision observes the actual origin branch. Its tree is read from the
// named native object; unavailable objects fail instead of substituting HEAD.
func PublishedRevision(ctx context.Context, dir, branch string) (Revision, error) {
	commit, err := ObserveRemoteBranch(ctx, dir, branch)
	if err != nil {
		return Revision{}, err
	}
	tree, err := revisionTree(ctx, dir, commit)
	if err != nil {
		return Revision{}, err
	}
	return Revision{Commit: commit, Tree: tree}, nil
}

func verifyDirectIntegration(ctx context.Context, dir, destination string, source, result Revision) error {
	if !validObjectID(source.Commit) || !validObjectID(source.Tree) || !validObjectID(destination) {
		return errors.New("integration requires exact synchronized commit, tree, and destination checkpoint")
	}
	if result.Commit != source.Commit || result.Tree != source.Tree {
		return errors.New("integration result differs from the synchronized task commit or tree")
	}
	tree, err := revisionTree(ctx, dir, source.Commit)
	if err != nil {
		return err
	}
	if tree != source.Tree {
		return errors.New("integration tree differs from the synchronized task tree")
	}
	contains, err := branchContainsRef(ctx, dir, source.Commit, destination)
	if err != nil {
		return err
	}
	if !contains {
		return errors.New("synchronized task commit does not contain the recorded destination; reconcile synchronization before delivery")
	}
	return nil
}
