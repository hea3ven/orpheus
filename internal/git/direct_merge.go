package git

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hea3ven/orpheus/internal/task"
)

// MergeTaskBranchIntoDefault safely refreshes the registered default branch.
// Deprecated callers should prefer MergeTaskBranchIntoDestination.
func MergeTaskBranchIntoDefault(ctx context.Context, repo task.Repository, taskBranch string) (string, error) {
	return MergeTaskBranchIntoDestination(ctx, repo, repo.DefaultBranch, taskBranch)
}

// MergeTaskBranchIntoDestination safely refreshes destinationBranch and creates
// a merge commit for taskBranch. It never pushes; callers record the merge
// before separately pushing the destination branch.
func MergeTaskBranchIntoDestination(ctx context.Context, repo task.Repository, destinationBranch string, taskBranch string) (string, error) {
	destinationBranch = strings.TrimSpace(destinationBranch)
	taskBranch = strings.TrimSpace(taskBranch)
	if destinationBranch == "" {
		return "", fmt.Errorf("repo %q has no integration destination branch", repo.ID)
	}
	if taskBranch == "" {
		return "", fmt.Errorf("task branch is required")
	}
	if err := validateBranchRef(ctx, repo.Path, "integration destination branch", destinationBranch); err != nil {
		return "", err
	}
	if err := validateBranchRef(ctx, repo.Path, "task branch", taskBranch); err != nil {
		return "", err
	}
	if destinationBranch == taskBranch {
		return "", fmt.Errorf("integration destination branch %q is the task branch; select a different destination before direct merge", destinationBranch)
	}
	dirty, err := HasWorkingTreeChanges(ctx, repo.Path)
	if err != nil {
		return "", fmt.Errorf("inspect repository root before direct merge: %w", err)
	}
	if dirty {
		return "", fmt.Errorf("repository root %q has uncommitted changes; commit, stash, or discard them before direct merge", repo.Path)
	}
	if output, err := runGitContext(ctx, repo.Path, "fetch", "origin", branchFetchRefspec(destinationBranch)); err != nil {
		return "", fmt.Errorf("fetch integration destination origin/%s: %w%s", destinationBranch, err, gitOutputSuffix(output))
	}
	if err := checkoutDefaultBranch(ctx, repo.Path, destinationBranch); err != nil {
		return "", err
	}
	// A successful merge can precede its state write. Recover only an exact
	// no-ff merge against the fetched default baseline; merely containing the
	// task branch could otherwise publish unrelated local commits.
	mergeCommit, recovered, err := expectedDirectMerge(ctx, repo.Path, destinationBranch, taskBranch)
	if err != nil {
		return "", err
	}
	if recovered {
		return mergeCommit, nil
	}
	if err := fastForwardFromOrigin(ctx, repo.Path, destinationBranch); err != nil {
		return "", err
	}

	output, err := runGitContext(ctx, repo.Path, "merge", "--no-ff", "--no-edit", "refs/heads/"+taskBranch)
	if err != nil {
		abortOutput, abortErr := runGitContext(ctx, repo.Path, "merge", "--abort")
		if abortErr != nil {
			return "", fmt.Errorf("%w: merge task branch %q into integration destination %q: %w%s; additionally failed to abort merge: %w%s", ErrMergeConflict, taskBranch, destinationBranch, err, gitOutputSuffix(output), abortErr, gitOutputSuffix(abortOutput))
		}
		return "", fmt.Errorf("%w: merge task branch %q into integration destination %q conflicts; destination was not pushed. Resolve the conflict outside Orpheus, then retry: %w%s", ErrMergeConflict, taskBranch, destinationBranch, err, gitOutputSuffix(output))
	}
	mergeCommit, recovered, err = expectedDirectMerge(ctx, repo.Path, destinationBranch, taskBranch)
	if err != nil {
		return "", err
	}
	if !recovered {
		return "", fmt.Errorf("merge task branch %q into integration destination %q did not create the expected no-ff merge; destination was not pushed. Inspect the local destination branch and recover it outside Orpheus before retrying", taskBranch, destinationBranch)
	}
	return mergeCommit, nil
}

// ValidateRecordedDirectMerge verifies a merge against the registered default branch.
// Deprecated callers should prefer ValidateRecordedDirectMergeIntoDestination.
func ValidateRecordedDirectMerge(ctx context.Context, repo task.Repository, mergeCommit string) (bool, error) {
	return ValidateRecordedDirectMergeIntoDestination(ctx, repo, repo.DefaultBranch, mergeCommit)
}

// ValidateRecordedDirectMergeIntoDestination verifies that the durable merge
// checkpoint is still safe to publish to destinationBranch. It returns true
// when origin already contains the checkpoint, recovering a push that
// succeeded before its state write.
func ValidateRecordedDirectMergeIntoDestination(ctx context.Context, repo task.Repository, destinationBranch string, mergeCommit string) (bool, error) {
	destinationBranch = strings.TrimSpace(destinationBranch)
	mergeCommit = strings.TrimSpace(mergeCommit)
	if destinationBranch == "" {
		return false, fmt.Errorf("repo %q has no integration destination branch", repo.ID)
	}
	if mergeCommit == "" {
		return false, fmt.Errorf("recorded direct merge commit is required")
	}
	if err := validateBranchRef(ctx, repo.Path, "integration destination branch", destinationBranch); err != nil {
		return false, err
	}
	if output, err := runGitContext(ctx, repo.Path, "fetch", "origin", branchFetchRefspec(destinationBranch)); err != nil {
		return false, fmt.Errorf("fetch integration destination origin/%s before direct-merge push: %w%s", destinationBranch, err, gitOutputSuffix(output))
	}

	remoteRef := "refs/remotes/origin/" + destinationBranch
	remoteContainsMerge, err := branchContainsRef(ctx, repo.Path, remoteRef, mergeCommit)
	if err != nil {
		return false, fmt.Errorf("inspect origin/%s for recorded direct merge %s: %w", destinationBranch, mergeCommit, err)
	}
	if remoteContainsMerge {
		return true, nil
	}

	localCommit, err := refCommit(ctx, repo.Path, "refs/heads/"+destinationBranch)
	if err != nil {
		return false, fmt.Errorf("inspect local integration destination %q before direct-merge push: %w", destinationBranch, err)
	}
	if localCommit != mergeCommit {
		return false, fmt.Errorf("cannot retry direct merge: local integration destination %q is at %s, not recorded merge %s, and origin/%s does not contain that merge; reset the local destination to %s or publish the recorded merge manually before retrying", destinationBranch, localCommit, mergeCommit, destinationBranch, mergeCommit)
	}
	return false, nil
}

func expectedDirectMerge(ctx context.Context, dir string, destinationBranch string, taskBranch string) (string, bool, error) {
	head, err := HeadCommit(ctx, dir)
	if err != nil {
		return "", false, fmt.Errorf("inspect existing direct merge: %w", err)
	}
	remoteCommit, err := refCommit(ctx, dir, "refs/remotes/origin/"+destinationBranch)
	if err != nil {
		return "", false, fmt.Errorf("inspect fetched integration destination origin/%s: %w", destinationBranch, err)
	}
	taskCommit, err := refCommit(ctx, dir, "refs/heads/"+taskBranch)
	if err != nil {
		return "", false, fmt.Errorf("inspect task branch %q: %w", taskBranch, err)
	}
	output, err := runGitContext(ctx, dir, "show", "--no-patch", "--format=%P", "HEAD")
	if err != nil {
		return "", false, fmt.Errorf("inspect existing direct merge parents: %w%s", err, gitOutputSuffix(output))
	}
	parents := strings.Fields(output)
	if len(parents) != 2 || parents[1] != taskCommit {
		return "", false, nil
	}
	if head == remoteCommit || parents[0] == remoteCommit {
		return head, true, nil
	}
	return "", false, nil
}

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
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return errors.New("integration destination branch is required")
	}
	if err := validateBranchRef(ctx, repoPath, "integration destination branch", branch); err != nil {
		return err
	}
	output, err := runGitContext(ctx, repoPath, "ls-remote", "--exit-code", "--heads", "origin", "refs/heads/"+branch)
	if err != nil {
		if gitExitCode(err) == 2 {
			return fmt.Errorf("integration destination branch %q does not exist on origin; select an existing remote branch", branch)
		}
		return fmt.Errorf("verify integration destination branch %q on origin: %w%s", branch, err, gitOutputSuffix(output))
	}
	if strings.TrimSpace(output) == "" {
		return fmt.Errorf("integration destination branch %q does not exist on origin; select an existing remote branch", branch)
	}
	return nil
}
