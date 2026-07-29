package git

import (
	"context"
	"fmt"
	"strings"

	"github.com/hea3ven/orpheus/internal/task"
)

// MergeTaskBranchIntoDefault safely refreshes a repository's registered default
// branch and creates a merge commit for taskBranch. It never pushes; callers
// record the merge before separately pushing the default branch.
func MergeTaskBranchIntoDefault(ctx context.Context, repo task.Repository, taskBranch string) (string, error) {
	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	taskBranch = strings.TrimSpace(taskBranch)
	if defaultBranch == "" {
		return "", fmt.Errorf("repo %q has no registered default branch", repo.ID)
	}
	if taskBranch == "" {
		return "", fmt.Errorf("task branch is required")
	}
	dirty, err := HasWorkingTreeChanges(ctx, repo.Path)
	if err != nil {
		return "", fmt.Errorf("inspect repository root before direct merge: %w", err)
	}
	if dirty {
		return "", fmt.Errorf("repository root %q has uncommitted changes; commit, stash, or discard them before direct merge", repo.Path)
	}
	if output, err := runGitContext(ctx, repo.Path, "fetch", "origin", defaultBranch); err != nil {
		return "", fmt.Errorf("fetch default branch origin/%s: %w%s", defaultBranch, err, gitOutputSuffix(output))
	}
	if err := checkoutDefaultBranch(ctx, repo.Path, defaultBranch); err != nil {
		return "", err
	}
	// A successful merge can precede its state write. Recover only an exact
	// no-ff merge against the fetched default baseline; merely containing the
	// task branch could otherwise publish unrelated local commits.
	mergeCommit, recovered, err := expectedDirectMerge(ctx, repo.Path, defaultBranch, taskBranch)
	if err != nil {
		return "", err
	}
	if recovered {
		return mergeCommit, nil
	}
	if err := fastForwardFromOrigin(ctx, repo.Path, defaultBranch); err != nil {
		return "", err
	}

	output, err := runGitContext(ctx, repo.Path, "merge", "--no-ff", "--no-edit", taskBranch)
	if err != nil {
		abortOutput, abortErr := runGitContext(ctx, repo.Path, "merge", "--abort")
		if abortErr != nil {
			return "", fmt.Errorf("%w: merge task branch %q into default branch %q: %w%s; additionally failed to abort merge: %w%s", ErrMergeConflict, taskBranch, defaultBranch, err, gitOutputSuffix(output), abortErr, gitOutputSuffix(abortOutput))
		}
		return "", fmt.Errorf("%w: merge task branch %q into default branch %q conflicts; default was not pushed. Resolve the conflict outside Orpheus, then retry: %w%s", ErrMergeConflict, taskBranch, defaultBranch, err, gitOutputSuffix(output))
	}
	mergeCommit, recovered, err = expectedDirectMerge(ctx, repo.Path, defaultBranch, taskBranch)
	if err != nil {
		return "", err
	}
	if !recovered {
		return "", fmt.Errorf("merge task branch %q into default branch %q did not create the expected no-ff merge; default was not pushed. Inspect the local default branch and recover it outside Orpheus before retrying", taskBranch, defaultBranch)
	}
	return mergeCommit, nil
}

// ValidateRecordedDirectMerge verifies that the durable merge checkpoint is
// still safe to publish. It returns true when origin already contains the
// checkpoint, which recovers a push that succeeded before its state write.
func ValidateRecordedDirectMerge(ctx context.Context, repo task.Repository, mergeCommit string) (bool, error) {
	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	mergeCommit = strings.TrimSpace(mergeCommit)
	if defaultBranch == "" {
		return false, fmt.Errorf("repo %q has no registered default branch", repo.ID)
	}
	if mergeCommit == "" {
		return false, fmt.Errorf("recorded direct merge commit is required")
	}
	if output, err := runGitContext(ctx, repo.Path, "fetch", "origin", defaultBranch); err != nil {
		return false, fmt.Errorf("fetch default branch origin/%s before direct-merge push: %w%s", defaultBranch, err, gitOutputSuffix(output))
	}

	remoteRef := "refs/remotes/origin/" + defaultBranch
	remoteContainsMerge, err := branchContainsRef(ctx, repo.Path, remoteRef, mergeCommit)
	if err != nil {
		return false, fmt.Errorf("inspect origin/%s for recorded direct merge %s: %w", defaultBranch, mergeCommit, err)
	}
	if remoteContainsMerge {
		return true, nil
	}

	localCommit, err := refCommit(ctx, repo.Path, "refs/heads/"+defaultBranch)
	if err != nil {
		return false, fmt.Errorf("inspect local default branch %q before direct-merge push: %w", defaultBranch, err)
	}
	if localCommit != mergeCommit {
		return false, fmt.Errorf("cannot retry direct merge: local default branch %q is at %s, not recorded merge %s, and origin/%s does not contain that merge; reset the local default branch to %s or publish the recorded merge manually before retrying", defaultBranch, localCommit, mergeCommit, defaultBranch, mergeCommit)
	}
	return false, nil
}

func expectedDirectMerge(ctx context.Context, dir string, defaultBranch string, taskBranch string) (string, bool, error) {
	head, err := HeadCommit(ctx, dir)
	if err != nil {
		return "", false, fmt.Errorf("inspect existing direct merge: %w", err)
	}
	remoteCommit, err := refCommit(ctx, dir, "refs/remotes/origin/"+defaultBranch)
	if err != nil {
		return "", false, fmt.Errorf("inspect fetched default branch origin/%s: %w", defaultBranch, err)
	}
	taskCommit, err := refCommit(ctx, dir, taskBranch)
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
