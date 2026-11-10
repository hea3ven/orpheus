package git

import (
	"context"
	"fmt"
)

func pushExactCommit(ctx context.Context, dir, branch, commit string) error {
	if err := validateBranchRef(ctx, dir, "publication branch", branch); err != nil {
		return err
	}
	if !validObjectID(commit) {
		return fmt.Errorf("publication push requires an exact commit")
	}
	output, err := runGitContext(ctx, dir, "push", "--no-follow-tags", "origin", commit+":refs/heads/"+branch)
	if err != nil {
		return fmt.Errorf("push authorized commit to origin/%s: %w%s", branch, err, gitOutputSuffix(output))
	}
	return nil
}
