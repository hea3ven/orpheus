package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// ClosedTaskWorktreeOutcome classifies a safe closed-task worktree inspection
// or removal attempt.
type ClosedTaskWorktreeOutcome string

const (
	ClosedTaskWorktreeClean   ClosedTaskWorktreeOutcome = "clean"
	ClosedTaskWorktreeAbsent  ClosedTaskWorktreeOutcome = "already_absent"
	ClosedTaskWorktreeDirty   ClosedTaskWorktreeOutcome = "dirty"
	ClosedTaskWorktreeUnsafe  ClosedTaskWorktreeOutcome = "unsafe"
	ClosedTaskWorktreeRemoved ClosedTaskWorktreeOutcome = "removed"
	ClosedTaskWorktreeFailed  ClosedTaskWorktreeOutcome = "failed"
)

// ClosedTaskWorktreeOptions identifies one deterministic task worktree.
// It deliberately reuses TaskWorktreeOptions so Git derives the path rather
// than trusting a caller-supplied directory.
type ClosedTaskWorktreeOptions = TaskWorktreeOptions

// ClosedTaskWorktreeInspection reports whether a deterministic worktree can
// be removed without losing changes.
type ClosedTaskWorktreeInspection struct {
	Outcome  ClosedTaskWorktreeOutcome
	Worktree string
	Reason   string
}

// ClosedTaskWorktreeRemoval reports one non-forced worktree removal attempt.
type ClosedTaskWorktreeRemoval struct {
	Outcome  ClosedTaskWorktreeOutcome
	Worktree string
	Reason   string
}

// LocalClosedTaskWorktreeGit delegates closed-task cleanup to the local Git
// executable.
type LocalClosedTaskWorktreeGit struct{}

// InspectClosedTaskWorktree validates registered-repository ownership,
// deterministic identity, and working tree cleanliness without mutating Git.
func (LocalClosedTaskWorktreeGit) InspectClosedTaskWorktree(
	ctx context.Context,
	opts ClosedTaskWorktreeOptions,
) ClosedTaskWorktreeInspection {
	return InspectClosedTaskWorktree(ctx, opts)
}

// RemoveClosedTaskWorktree validates the same conditions immediately before
// asking Git to remove the worktree without force.
func (LocalClosedTaskWorktreeGit) RemoveClosedTaskWorktree(
	ctx context.Context,
	opts ClosedTaskWorktreeOptions,
) ClosedTaskWorktreeRemoval {
	return RemoveClosedTaskWorktree(ctx, opts)
}

// InspectClosedTaskWorktree validates a deterministic worktree before a
// closed-task cleanup attempt. It never removes a path.
func InspectClosedTaskWorktree(ctx context.Context, opts ClosedTaskWorktreeOptions) ClosedTaskWorktreeInspection {
	if ctx == nil {
		ctx = context.Background()
	}
	plan, err := newTaskWorktreePlan(opts)
	if err != nil {
		return closedTaskWorktreeUnsafe("resolve deterministic worktree", "", err)
	}
	inspection := ClosedTaskWorktreeInspection{Worktree: plan.WorktreePath}

	repoRoot, err := worktreeRoot(ctx, plan.RepoPath)
	if err != nil {
		return closedTaskWorktreeUnsafe("inspect registered repository root", plan.WorktreePath, err)
	}
	if repoRoot != plan.RepoPath {
		return closedTaskWorktreeUnsafe(
			"verify registered repository root",
			plan.WorktreePath,
			fmt.Errorf("registered path %q resolves to Git root %q", plan.RepoPath, repoRoot),
		)
	}
	expectedCommonDir, err := gitCommonDir(ctx, repoRoot)
	if err != nil {
		return closedTaskWorktreeUnsafe("inspect registered repository ownership", plan.WorktreePath, err)
	}

	exists, err := deterministicPathExists(plan.WorktreePath)
	if err != nil {
		return closedTaskWorktreeUnsafe("inspect deterministic worktree path", plan.WorktreePath, err)
	}
	if !exists {
		return absentClosedTaskWorktreeInspection(ctx, repoRoot, plan.WorktreePath)
	}
	if err := validateExistingTaskWorktree(ctx, plan.WorktreePath, plan.Branch, expectedCommonDir); err != nil {
		return closedTaskWorktreeUnsafe("verify deterministic worktree identity", plan.WorktreePath, err)
	}
	locked, lockReason, err := closedTaskWorktreeLock(ctx, repoRoot, plan.WorktreePath)
	if err != nil {
		return closedTaskWorktreeUnsafe("inspect deterministic worktree lock", plan.WorktreePath, err)
	}
	if locked {
		reason := "Git marks the deterministic worktree as locked"
		if lockReason != "" {
			reason += ": " + lockReason
		}
		return ClosedTaskWorktreeInspection{Outcome: ClosedTaskWorktreeUnsafe, Worktree: plan.WorktreePath, Reason: reason}
	}

	output, err := runGitContext(ctx, plan.WorktreePath, "status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching")
	if err != nil {
		return closedTaskWorktreeUnsafe("inspect deterministic worktree cleanliness", plan.WorktreePath, fmt.Errorf("%w%s", err, gitOutputSuffix(output)))
	}
	if strings.TrimSpace(output) != "" {
		inspection.Outcome = ClosedTaskWorktreeDirty
		inspection.Reason = "has tracked, untracked, or ignored files; commit, stash, or remove them before running `orpheus doctor --fix`"
		return inspection
	}
	inspection.Outcome = ClosedTaskWorktreeClean
	return inspection
}

// RemoveClosedTaskWorktree removes a clean deterministic worktree without
// force. It re-runs all validation immediately before the mutating command.
func RemoveClosedTaskWorktree(ctx context.Context, opts ClosedTaskWorktreeOptions) ClosedTaskWorktreeRemoval {
	if ctx == nil {
		ctx = context.Background()
	}
	inspection := InspectClosedTaskWorktree(ctx, opts)
	removal := ClosedTaskWorktreeRemoval(inspection)
	if inspection.Outcome != ClosedTaskWorktreeClean {
		return removal
	}

	output, err := runGitContext(ctx, inspection.Worktree, "worktree", "remove", inspection.Worktree)
	if err == nil {
		removal.Outcome = ClosedTaskWorktreeRemoved
		return removal
	}
	removal.Outcome = ClosedTaskWorktreeFailed
	removal.Reason = fmt.Sprintf("remove deterministic worktree without force: %v%s", err, gitOutputSuffix(output))
	return removal
}

func absentClosedTaskWorktreeInspection(ctx context.Context, repoRoot string, worktreePath string) ClosedTaskWorktreeInspection {
	registered, _, _, err := closedTaskWorktreeRegistration(ctx, repoRoot, worktreePath)
	if err != nil {
		return closedTaskWorktreeUnsafe("inspect deterministic worktree registration", worktreePath, err)
	}
	if registered {
		return ClosedTaskWorktreeInspection{
			Outcome:  ClosedTaskWorktreeFailed,
			Worktree: worktreePath,
			Reason:   "Git still registers the absent deterministic worktree after incomplete removal; inspect and repair the Git worktree registration manually",
		}
	}
	return ClosedTaskWorktreeInspection{Outcome: ClosedTaskWorktreeAbsent, Worktree: worktreePath}
}

func closedTaskWorktreeLock(ctx context.Context, repoRoot string, worktreePath string) (bool, string, error) {
	registered, locked, reason, err := closedTaskWorktreeRegistration(ctx, repoRoot, worktreePath)
	if err != nil {
		return false, "", err
	}
	if !registered {
		return false, "", fmt.Errorf("deterministic worktree %q is not registered", worktreePath)
	}
	return locked, reason, nil
}

func closedTaskWorktreeRegistration(ctx context.Context, repoRoot string, worktreePath string) (bool, bool, string, error) {
	output, err := runGitContext(ctx, repoRoot, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return false, false, "", fmt.Errorf("list registered worktrees: %w%s", err, gitOutputSuffix(output))
	}
	locked, reason, registered := closedTaskWorktreeLockFromPorcelain(output, worktreePath)
	return registered, locked, reason, nil
}

func closedTaskWorktreeLockFromPorcelain(output string, worktreePath string) (bool, string, bool) {
	expectedPath := filepath.Clean(worktreePath)
	var currentPath string
	var lockReason string
	locked := false
	for _, field := range strings.Split(output, "\x00") {
		if field == "" {
			if currentPath != "" && filepath.Clean(currentPath) == expectedPath {
				return locked, lockReason, true
			}
			currentPath = ""
			lockReason = ""
			locked = false
			continue
		}
		if path, ok := strings.CutPrefix(field, "worktree "); ok {
			currentPath = path
			continue
		}
		if field == "locked" {
			locked = true
			continue
		}
		if reason, ok := strings.CutPrefix(field, "locked "); ok {
			locked = true
			lockReason = strings.TrimSpace(reason)
		}
	}
	if currentPath != "" && filepath.Clean(currentPath) == expectedPath {
		return locked, lockReason, true
	}
	return false, "", false
}

func closedTaskWorktreeUnsafe(operation string, worktree string, err error) ClosedTaskWorktreeInspection {
	return ClosedTaskWorktreeInspection{
		Outcome:  ClosedTaskWorktreeUnsafe,
		Worktree: worktree,
		Reason:   fmt.Sprintf("%s: %v", operation, err),
	}
}
