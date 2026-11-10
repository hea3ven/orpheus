package git

import (
	"context"
	"fmt"
	"strings"
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

func verifyDirectMergeCode(ctx context.Context, dir, commit, parent string, output Revision) error {
	parents, err := runGitContext(ctx, dir, "show", "--no-patch", "--format=%P", commit)
	if err != nil {
		return err
	}
	fields := strings.Fields(parents)
	if len(fields) != 2 || fields[0] != parent || fields[1] != output.Commit {
		return fmt.Errorf("integration parents differ from reviewed destination and output; destination was not pushed")
	}
	tree, err := revisionTree(ctx, dir, commit)
	if err != nil {
		return err
	}
	if tree != output.Tree {
		return fmt.Errorf("integration tree differs from reviewed output; destination was not pushed")
	}
	return nil
}
