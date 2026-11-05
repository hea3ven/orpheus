package git

import (
	"context"
	"fmt"
	"strings"
)

// PushPublicationInput pins the remote source to the authorized commit rather
// than a mutable branch. Ordinary hooks and non-fast-forward protection apply.
func PushPublicationInput(ctx context.Context, dir, branch string, input Revision) error {
	if err := VerifyRevision(ctx, dir, CheckoutIdentity{Commit: input.Commit, Ref: "refs/heads/" + branch}, input); err != nil {
		return err
	}
	if err := pushExactCommit(ctx, dir, branch, input.Commit); err != nil {
		return err
	}
	output, err := runGitContext(ctx, dir, "branch", "--set-upstream-to=origin/"+branch, "--", branch)
	if err != nil {
		return fmt.Errorf("set publication branch upstream after push: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

func pushExactCommit(ctx context.Context, dir, branch, commit string) error {
	if err := validateBranchRef(ctx, dir, "publication branch", branch); err != nil {
		return err
	}
	if !validObjectID(commit) {
		return fmt.Errorf("publication push requires an exact commit")
	}
	output, err := runGitContext(ctx, dir, "push", "origin", commit+":refs/heads/"+branch)
	if err != nil {
		return fmt.Errorf("push authorized commit to origin/%s: %w%s", branch, err, gitOutputSuffix(output))
	}
	return nil
}
