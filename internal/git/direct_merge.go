package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func refCommit(ctx context.Context, dir string, ref string) (string, error) {
	output, err := runGitContext(ctx, dir, "rev-parse", ref)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w%s", ref, err, gitOutputSuffix(output))
	}
	commit := strings.TrimSpace(output)
	if commit == "" {
		return "", fmt.Errorf("resolve %s: Git returned an empty commit", ref)
	}
	return commit, nil
}

func branchFetchRefspec(branch string) string {
	return "+refs/heads/" + branch + ":refs/remotes/origin/" + branch
}

// VerifyRemoteBranch verifies that branch is a safe branch name and names an
// existing head on origin. It never creates or updates a remote reference.
func VerifyRemoteBranch(ctx context.Context, repoPath string, branch string) error {
	_, err := ObserveRemoteBranch(ctx, repoPath, branch)
	return err
}

// ObserveRemoteBranch returns the actual origin branch commit, not a local guess.
func ObserveRemoteBranch(ctx context.Context, repoPath string, branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", errors.New("integration destination branch is required")
	}
	if err := validateBranchRef(ctx, repoPath, "integration destination branch", branch); err != nil {
		return "", err
	}
	output, err := runGitContext(ctx, repoPath, "ls-remote", "--exit-code", "--heads", "origin", "refs/heads/"+branch)
	if err != nil {
		if gitExitCode(err) == 2 {
			return "", fmt.Errorf("integration destination branch %q does not exist on origin; select an existing remote branch", branch)
		}
		return "", fmt.Errorf("verify integration destination branch %q on origin: %w%s", branch, err, gitOutputSuffix(output))
	}
	if strings.TrimSpace(output) == "" {
		return "", fmt.Errorf("integration destination branch %q does not exist on origin; select an existing remote branch", branch)
	}
	fields := strings.Fields(output)
	if len(fields) != 2 || !validObjectID(fields[0]) || fields[1] != "refs/heads/"+branch {
		return "", fmt.Errorf("origin returned an ambiguous integration destination")
	}
	return fields[0], nil
}

func fetchDirectDestinationCheckpoint(ctx context.Context, dir, destinationBranch, checkpoint string) error {
	if output, err := runGitContext(ctx, dir, "fetch", "origin", branchFetchRefspec(destinationBranch)); err != nil {
		return fmt.Errorf("fetch integration destination origin/%s: %w%s", destinationBranch, err, gitOutputSuffix(output))
	}
	fetched, err := refCommit(ctx, dir, "refs/remotes/origin/"+destinationBranch)
	if err != nil {
		return err
	}
	if fetched != checkpoint {
		return errors.New("destination advanced after sync; reconcile and renew consent")
	}
	return nil
}
