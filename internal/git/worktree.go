package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hea3ven/orpheus/internal/state"
)

const taskWorktreeBranchPrefix = "orpheus/"

type TaskWorktreeLifecycle string

const (
	TaskWorktreeLifecycleCreated           TaskWorktreeLifecycle = "created"
	TaskWorktreeLifecycleReused            TaskWorktreeLifecycle = "reused"
	TaskWorktreeLifecycleRecreated         TaskWorktreeLifecycle = "recreated"
	TaskWorktreeLifecycleTaskBranchCreated TaskWorktreeLifecycle = "task_branch_created"
)

// TaskWorktreeOptions describes the deterministic Git context for one task run.
type TaskWorktreeOptions struct {
	RepoID        string
	RepoName      string
	RepoPath      string
	DefaultBranch string
	TaskID        string
	Paths         state.Paths
}

// RepoRootOptions describes a repo-root/default-branch task run target.
type RepoRootOptions struct {
	RepoID        string
	RepoName      string
	RepoPath      string
	DefaultBranch string
}

// TaskWorktreeSetupResult is the backend-neutral result of preparing a task execution target.
type TaskWorktreeSetupResult struct {
	Branch       string
	WorktreePath string
	Lifecycle    TaskWorktreeLifecycle
}

type taskWorktreePlan struct {
	RepoID        string
	RepoName      string
	RepoPath      string
	DefaultBranch string
	TaskID        string
	Branch        string
	WorktreePath  string
}

type repoRootPlan struct {
	RepoID        string
	RepoName      string
	RepoPath      string
	DefaultBranch string
}

// ExpectedTaskWorktree returns the deterministic branch and worktree path for a task without mutating Git.
func ExpectedTaskWorktree(opts TaskWorktreeOptions) (TaskWorktreeSetupResult, error) {
	plan, err := newTaskWorktreePlan(opts)
	if err != nil {
		return TaskWorktreeSetupResult{}, err
	}
	return TaskWorktreeSetupResult{
		Branch:       plan.Branch,
		WorktreePath: plan.WorktreePath,
	}, nil
}

// ExpectedRepoRoot returns the repo-root/default-branch target without mutating Git.
func ExpectedRepoRoot(opts RepoRootOptions) (TaskWorktreeSetupResult, error) {
	plan, err := newRepoRootPlan(opts)
	if err != nil {
		return TaskWorktreeSetupResult{}, err
	}
	return TaskWorktreeSetupResult{
		Branch:       plan.DefaultBranch,
		WorktreePath: plan.RepoPath,
		Lifecycle:    TaskWorktreeLifecycleReused,
	}, nil
}

// ExpectedRepoRootTaskBranch returns the repo-root/task-branch target without mutating Git.
func ExpectedRepoRootTaskBranch(opts TaskWorktreeOptions) (TaskWorktreeSetupResult, error) {
	plan, err := newTaskWorktreePlan(opts)
	if err != nil {
		return TaskWorktreeSetupResult{}, err
	}
	return TaskWorktreeSetupResult{
		Branch:       plan.Branch,
		WorktreePath: plan.RepoPath,
		Lifecycle:    TaskWorktreeLifecycleReused,
	}, nil
}

// SetupTaskWorktree prepares or reuses the deterministic branch and worktree for a task.
//
// The setup is intentionally conservative: it creates the expected branch only when it
// does not already exist, reuses an existing matching worktree, and refuses existing
// paths or Git state that do not match the deterministic task plan.
func SetupTaskWorktree(ctx context.Context, opts TaskWorktreeOptions) (TaskWorktreeSetupResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	plan, err := newTaskWorktreePlan(opts)
	if err != nil {
		return TaskWorktreeSetupResult{}, err
	}
	result := TaskWorktreeSetupResult{
		Branch:       plan.Branch,
		WorktreePath: plan.WorktreePath,
	}

	repoRoot, repoCommonDir, err := validateTaskWorktreeRepo(ctx, plan)
	if err != nil {
		return TaskWorktreeSetupResult{}, err
	}

	branchExists, err := localBranchExists(ctx, repoRoot, plan.Branch)
	if err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf("prepare task worktree %s: %w", plan.TaskID, err)
	}

	pathExists, err := deterministicPathExists(plan.WorktreePath)
	if err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf("prepare task worktree %s: %w", plan.TaskID, err)
	}
	if pathExists {
		return reuseTaskWorktree(ctx, plan, repoCommonDir, result)
	}

	if branchExists {
		if err := recreateTaskWorktree(ctx, repoRoot, plan); err != nil {
			return TaskWorktreeSetupResult{}, fmt.Errorf("prepare task worktree %s: %w", plan.TaskID, err)
		}
		result.Lifecycle = TaskWorktreeLifecycleRecreated
		return result, nil
	}

	if err := createTaskWorktree(ctx, repoRoot, plan); err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf("prepare task worktree %s: %w", plan.TaskID, err)
	}

	result.Lifecycle = TaskWorktreeLifecycleCreated
	return result, nil
}

// SetupRepoRoot prepares the registered repo root on the registered default branch.
//
// It refuses to mutate the checkout until the repo root is clean, then fetches the
// default branch from origin, switches to the local default branch, and fast-forwards
// from origin using --ff-only. No task branch or deterministic task worktree is
// created for this mode.
func SetupRepoRoot(ctx context.Context, opts RepoRootOptions) (TaskWorktreeSetupResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	plan, err := newRepoRootPlan(opts)
	if err != nil {
		return TaskWorktreeSetupResult{}, err
	}
	result := TaskWorktreeSetupResult{
		Branch:       plan.DefaultBranch,
		WorktreePath: plan.RepoPath,
		Lifecycle:    TaskWorktreeLifecycleReused,
	}

	repoRoot, err := validateRepoRootRun(ctx, plan)
	if err != nil {
		return TaskWorktreeSetupResult{}, err
	}
	if _, err := prepareDefaultBranchRef(ctx, repoRoot, plan.DefaultBranch); err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf("prepare repo-root task run: %w", err)
	}
	if err := ensureRepoRootOnDefaultBranch(ctx, repoRoot, plan.DefaultBranch); err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf("prepare repo-root task run: %w", err)
	}
	if err := syncCleanRepoRootWithOrigin(ctx, repoRoot, plan.DefaultBranch); err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf("prepare repo-root task run: %w", err)
	}

	return result, nil
}

// SetupRepoRootTaskBranch prepares the registered repo root on the deterministic task branch.
//
// It refuses to mutate the checkout until the repo root is clean, creates the
// task branch from origin/default when needed, and switches the registered repo
// root onto that branch. No deterministic task worktree is created for this mode.
func SetupRepoRootTaskBranch(ctx context.Context, opts TaskWorktreeOptions) (TaskWorktreeSetupResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	plan, err := newTaskWorktreePlan(opts)
	if err != nil {
		return TaskWorktreeSetupResult{}, err
	}
	result := TaskWorktreeSetupResult{
		Branch:       plan.Branch,
		WorktreePath: plan.RepoPath,
		Lifecycle:    TaskWorktreeLifecycleReused,
	}

	repoRoot, err := validateRepoRootTaskBranchRun(ctx, plan)
	if err != nil {
		return TaskWorktreeSetupResult{}, err
	}
	remoteRef, err := prepareDefaultBranchRef(ctx, repoRoot, plan.DefaultBranch)
	if err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf("prepare repo-root task branch run %s: %w", plan.TaskID, err)
	}
	branchCreated, err := ensureRepoRootOnTaskBranch(ctx, repoRoot, plan.Branch, remoteRef)
	if err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf("prepare repo-root task branch run %s: %w", plan.TaskID, err)
	}
	if err := requireCleanRepoRootFor(ctx, repoRoot, "with --repo-root"); err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf("prepare repo-root task branch run %s: %w", plan.TaskID, err)
	}
	if branchCreated {
		result.Lifecycle = TaskWorktreeLifecycleTaskBranchCreated
	}

	return result, nil
}

func validateTaskWorktreeRepo(ctx context.Context, plan taskWorktreePlan) (string, string, error) {
	repoRoot, err := worktreeRoot(ctx, plan.RepoPath)
	if err != nil {
		return "", "", fmt.Errorf(
			"prepare task worktree %s for repo %s (%s): inspect registered repo root %q: %w",
			plan.TaskID,
			plan.RepoID,
			plan.RepoName,
			plan.RepoPath,
			err,
		)
	}
	if repoRoot != plan.RepoPath {
		return "", "", fmt.Errorf(
			"prepare task worktree %s for repo %s (%s): registered repo path %q resolves to Git root %q; register the repository root before running tasks",
			plan.TaskID,
			plan.RepoID,
			plan.RepoName,
			plan.RepoPath,
			repoRoot,
		)
	}

	if err := validateBranchRef(ctx, repoRoot, "task branch", plan.Branch); err != nil {
		return "", "", fmt.Errorf("prepare task worktree %s: %w", plan.TaskID, err)
	}
	if err := validateBranchRef(ctx, repoRoot, "default branch", plan.DefaultBranch); err != nil {
		return "", "", fmt.Errorf("prepare task worktree %s: %w", plan.TaskID, err)
	}

	repoCommonDir, err := gitCommonDir(ctx, repoRoot)
	if err != nil {
		return "", "", fmt.Errorf(
			"prepare task worktree %s for repo %s (%s): inspect registered repo common Git dir: %w",
			plan.TaskID,
			plan.RepoID,
			plan.RepoName,
			err,
		)
	}
	if err := requireOriginRemote(ctx, repoRoot); err != nil {
		return "", "", fmt.Errorf(
			"prepare task worktree %s for repo %s (%s): %w",
			plan.TaskID,
			plan.RepoID,
			plan.RepoName,
			err,
		)
	}
	return repoRoot, repoCommonDir, nil
}

func reuseTaskWorktree(
	ctx context.Context,
	plan taskWorktreePlan,
	repoCommonDir string,
	result TaskWorktreeSetupResult,
) (TaskWorktreeSetupResult, error) {
	if err := validateExistingTaskWorktree(ctx, plan.WorktreePath, plan.Branch, repoCommonDir); err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf("prepare task worktree %s: %w", plan.TaskID, err)
	}
	result.Lifecycle = TaskWorktreeLifecycleReused
	return result, nil
}

func recreateTaskWorktree(ctx context.Context, repoRoot string, plan taskWorktreePlan) error {
	return addWorktree(ctx, repoRoot, plan.WorktreePath, plan.Branch)
}

func createTaskWorktree(ctx context.Context, repoRoot string, plan taskWorktreePlan) error {
	remoteRef, err := prepareDefaultBranchRef(ctx, repoRoot, plan.DefaultBranch)
	if err != nil {
		return err
	}
	if err := createBranch(ctx, repoRoot, plan.Branch, remoteRef); err != nil {
		return err
	}
	return addWorktree(ctx, repoRoot, plan.WorktreePath, plan.Branch)
}

func prepareDefaultBranchRef(ctx context.Context, repoRoot string, defaultBranch string) (string, error) {
	if err := fetchDefaultBranch(ctx, repoRoot, defaultBranch); err != nil {
		return "", err
	}

	remoteRef := "refs/remotes/origin/" + defaultBranch
	if err := verifyRef(ctx, repoRoot, remoteRef); err != nil {
		return "", err
	}
	return remoteRef, nil
}

func validateRepoRootRun(ctx context.Context, plan repoRootPlan) (string, error) {
	repoRoot, err := worktreeRoot(ctx, plan.RepoPath)
	if err != nil {
		return "", fmt.Errorf(
			"prepare repo-root task run for repo %s (%s): inspect registered repo root %q: %w",
			plan.RepoID,
			plan.RepoName,
			plan.RepoPath,
			err,
		)
	}
	if repoRoot != plan.RepoPath {
		return "", fmt.Errorf(
			"prepare repo-root task run for repo %s (%s): registered repo path %q resolves to Git root %q; register the repository root before running tasks",
			plan.RepoID,
			plan.RepoName,
			plan.RepoPath,
			repoRoot,
		)
	}

	if err := validateBranchRef(ctx, repoRoot, "default branch", plan.DefaultBranch); err != nil {
		return "", fmt.Errorf("prepare repo-root task run: %w", err)
	}
	if err := requireOriginRemoteFor(ctx, repoRoot, "repo-root task runs"); err != nil {
		return "", fmt.Errorf(
			"prepare repo-root task run for repo %s (%s): %w",
			plan.RepoID,
			plan.RepoName,
			err,
		)
	}
	if err := requireCleanRepoRoot(ctx, repoRoot); err != nil {
		return "", fmt.Errorf(
			"prepare repo-root task run for repo %s (%s): %w",
			plan.RepoID,
			plan.RepoName,
			err,
		)
	}
	return repoRoot, nil
}

func validateRepoRootTaskBranchRun(ctx context.Context, plan taskWorktreePlan) (string, error) {
	repoRoot, err := worktreeRoot(ctx, plan.RepoPath)
	if err != nil {
		return "", fmt.Errorf(
			"prepare repo-root task branch run %s for repo %s (%s): inspect registered repo root %q: %w",
			plan.TaskID,
			plan.RepoID,
			plan.RepoName,
			plan.RepoPath,
			err,
		)
	}
	if repoRoot != plan.RepoPath {
		return "", fmt.Errorf(
			"prepare repo-root task branch run %s for repo %s (%s): registered repo path %q resolves to Git root %q; register the repository root before running tasks",
			plan.TaskID,
			plan.RepoID,
			plan.RepoName,
			plan.RepoPath,
			repoRoot,
		)
	}

	if err := validateBranchRef(ctx, repoRoot, "task branch", plan.Branch); err != nil {
		return "", fmt.Errorf("prepare repo-root task branch run %s: %w", plan.TaskID, err)
	}
	if err := validateBranchRef(ctx, repoRoot, "default branch", plan.DefaultBranch); err != nil {
		return "", fmt.Errorf("prepare repo-root task branch run %s: %w", plan.TaskID, err)
	}
	if err := requireOriginRemoteFor(ctx, repoRoot, "repo-root task branch runs"); err != nil {
		return "", fmt.Errorf(
			"prepare repo-root task branch run %s for repo %s (%s): %w",
			plan.TaskID,
			plan.RepoID,
			plan.RepoName,
			err,
		)
	}
	if err := requireCleanRepoRootFor(ctx, repoRoot, "with --repo-root"); err != nil {
		return "", fmt.Errorf(
			"prepare repo-root task branch run %s for repo %s (%s): %w",
			plan.TaskID,
			plan.RepoID,
			plan.RepoName,
			err,
		)
	}
	return repoRoot, nil
}

func ensureRepoRootOnDefaultBranch(ctx context.Context, repoRoot string, defaultBranch string) error {
	current, currentErr := currentBranchAt(ctx, repoRoot)
	if currentErr != nil || current != defaultBranch {
		if err := checkoutDefaultBranch(ctx, repoRoot, defaultBranch); err != nil {
			if currentErr != nil {
				return fmt.Errorf(
					"switch to default branch %q (current branch could not be read: %w): %w",
					defaultBranch,
					currentErr,
					err,
				)
			}
			return fmt.Errorf("switch to default branch %q: %w", defaultBranch, err)
		}
	}

	current, err := currentBranchAt(ctx, repoRoot)
	if err != nil {
		return fmt.Errorf("verify default branch checkout: %w", err)
	}
	if current != defaultBranch {
		return fmt.Errorf(
			"repo root is on branch %q after checkout; expected default branch %q",
			current,
			defaultBranch,
		)
	}
	return nil
}

func ensureRepoRootOnTaskBranch(ctx context.Context, repoRoot string, branch string, startPoint string) (bool, error) {
	current, currentErr := currentBranchAt(ctx, repoRoot)
	if currentErr == nil && current == branch {
		return false, nil
	}

	branchExists, err := localBranchExists(ctx, repoRoot, branch)
	if err != nil {
		return false, err
	}
	branchCreated := false
	if !branchExists {
		if err := createBranch(ctx, repoRoot, branch, startPoint); err != nil {
			return false, err
		}
		branchCreated = true
	}

	if err := checkoutTaskBranch(ctx, repoRoot, branch); err != nil {
		if currentErr != nil {
			return false, fmt.Errorf(
				"switch to task branch %q (current branch could not be read: %w): %w",
				branch,
				currentErr,
				err,
			)
		}
		return false, fmt.Errorf("switch to task branch %q: %w", branch, err)
	}

	current, err = currentBranchAt(ctx, repoRoot)
	if err != nil {
		return false, fmt.Errorf("verify task branch checkout: %w", err)
	}
	if current != branch {
		return false, fmt.Errorf(
			"repo root is on branch %q after checkout; expected task branch %q",
			current,
			branch,
		)
	}
	return branchCreated, nil
}

func syncCleanRepoRootWithOrigin(ctx context.Context, repoRoot string, defaultBranch string) error {
	if err := requireCleanRepoRoot(ctx, repoRoot); err != nil {
		return err
	}
	if err := fastForwardFromOrigin(ctx, repoRoot, defaultBranch); err != nil {
		return err
	}
	return requireCleanRepoRoot(ctx, repoRoot)
}

func newTaskWorktreePlan(opts TaskWorktreeOptions) (taskWorktreePlan, error) {
	repoID, err := cleanPathComponent("repo id", opts.RepoID)
	if err != nil {
		return taskWorktreePlan{}, err
	}
	taskID, err := cleanPathComponent("task id", opts.TaskID)
	if err != nil {
		return taskWorktreePlan{}, err
	}

	repoPath, err := normalizeRegisteredRepoPath(opts.RepoPath)
	if err != nil {
		return taskWorktreePlan{}, err
	}

	defaultBranch := strings.TrimSpace(opts.DefaultBranch)
	if defaultBranch == "" {
		return taskWorktreePlan{}, fmt.Errorf("repo %q has no default branch; register it again or edit the repo registry before running tasks", repoID)
	}

	worktreePath, err := opts.Paths.DataPath(filepath.Join("repos", repoID, "worktrees", taskID))
	if err != nil {
		return taskWorktreePlan{}, fmt.Errorf("resolve deterministic task worktree path: %w", err)
	}

	return taskWorktreePlan{
		RepoID:        repoID,
		RepoName:      strings.TrimSpace(opts.RepoName),
		RepoPath:      repoPath,
		DefaultBranch: defaultBranch,
		TaskID:        taskID,
		Branch:        taskWorktreeBranchPrefix + taskID,
		WorktreePath:  worktreePath,
	}, nil
}

func newRepoRootPlan(opts RepoRootOptions) (repoRootPlan, error) {
	repoID, err := cleanPathComponent("repo id", opts.RepoID)
	if err != nil {
		return repoRootPlan{}, err
	}

	repoPath, err := normalizeRegisteredRepoPath(opts.RepoPath)
	if err != nil {
		return repoRootPlan{}, err
	}

	defaultBranch := strings.TrimSpace(opts.DefaultBranch)
	if defaultBranch == "" {
		return repoRootPlan{}, fmt.Errorf("repo %q has no default branch; register it again or edit the repo registry before running tasks", repoID)
	}

	return repoRootPlan{
		RepoID:        repoID,
		RepoName:      strings.TrimSpace(opts.RepoName),
		RepoPath:      repoPath,
		DefaultBranch: defaultBranch,
	}, nil
}

func normalizeRegisteredRepoPath(repoPath string) (string, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return "", errors.New("registered repo path is required")
	}
	if !filepath.IsAbs(repoPath) {
		return "", fmt.Errorf("registered repo path must be absolute, got %q", repoPath)
	}
	return filepath.Clean(repoPath), nil
}

func cleanPathComponent(label string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	if value == "." || value == ".." || strings.ContainsAny(value, `/\\`) || filepath.VolumeName(value) != "" {
		return "", fmt.Errorf("%s %q cannot be used in deterministic task worktree path", label, value)
	}
	return value, nil
}

func validateBranchRef(ctx context.Context, dir string, label string, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return fmt.Errorf("%s is required", label)
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("%s %q is not a valid Git branch name", label, branch)
	}

	ref := "refs/heads/" + branch
	output, err := runGitContext(ctx, dir, "check-ref-format", ref)
	if err != nil {
		return fmt.Errorf("%s %q is not a valid Git branch name: %w%s", label, branch, err, gitOutputSuffix(output))
	}
	return nil
}

func requireOriginRemote(ctx context.Context, repoRoot string) error {
	return requireOriginRemoteFor(ctx, repoRoot, "deterministic task worktrees")
}

func requireOriginRemoteFor(ctx context.Context, repoRoot string, purpose string) error {
	purpose = strings.TrimSpace(purpose)
	if purpose == "" {
		purpose = "task runs"
	}

	output, err := runGitContext(ctx, repoRoot, "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("registered repository requires an origin remote for %s: %w%s", purpose, err, gitOutputSuffix(output))
	}
	if strings.TrimSpace(output) == "" {
		return fmt.Errorf("registered repository requires an origin remote for %s", purpose)
	}
	return nil
}

func localBranchExists(ctx context.Context, repoRoot string, branch string) (bool, error) {
	output, err := runGitContext(ctx, repoRoot, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	if gitExitCode(err) == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check local branch %q: %w%s", branch, err, gitOutputSuffix(output))
}

func deterministicPathExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect deterministic worktree path %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("deterministic worktree path %q exists as a symlink; refusing to use ambiguous path", path)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("deterministic worktree path %q exists but is not a directory", path)
	}
	return true, nil
}

func validateExistingTaskWorktree(ctx context.Context, path string, expectedBranch string, expectedCommonDir string) error {
	root, err := worktreeRoot(ctx, path)
	if err != nil {
		return fmt.Errorf("deterministic worktree path %q exists but is not a Git worktree: %w", path, err)
	}
	if root != filepath.Clean(path) {
		return fmt.Errorf("deterministic worktree path %q resolves to Git root %q; expected the path itself to be a task worktree root", path, root)
	}

	branch, err := currentBranchAt(ctx, path)
	if err != nil {
		return fmt.Errorf("deterministic worktree path %q is not on expected branch %q: %w", path, expectedBranch, err)
	}
	if branch != expectedBranch {
		return fmt.Errorf("deterministic worktree path %q is on branch %q; expected %q", path, branch, expectedBranch)
	}

	commonDir, err := gitCommonDir(ctx, path)
	if err != nil {
		return fmt.Errorf("inspect deterministic worktree path %q common Git dir: %w", path, err)
	}
	if commonDir != expectedCommonDir {
		return fmt.Errorf("deterministic worktree path %q points at Git common dir %q; expected %q", path, commonDir, expectedCommonDir)
	}
	return nil
}

func fetchDefaultBranch(ctx context.Context, repoRoot string, defaultBranch string) error {
	output, err := runGitContext(ctx, repoRoot, "fetch", "origin", defaultBranch)
	if err != nil {
		return fmt.Errorf("fetch origin/%s: %w%s", defaultBranch, err, gitOutputSuffix(output))
	}
	return nil
}

func requireCleanRepoRoot(ctx context.Context, repoRoot string) error {
	return requireCleanRepoRootFor(ctx, repoRoot, "with --main")
}

func requireCleanRepoRootFor(ctx context.Context, repoRoot string, usage string) error {
	output, err := runGitContext(ctx, repoRoot, "status", "--porcelain=v1")
	if err != nil {
		return fmt.Errorf("inspect repo-root working tree cleanliness: %w%s", err, gitOutputSuffix(output))
	}
	if strings.TrimSpace(output) != "" {
		usage = strings.TrimSpace(usage)
		if usage == "" {
			usage = "a repo-root task"
		}
		return fmt.Errorf("repo root %q has uncommitted changes; commit, stash, or discard them before running %s", repoRoot, usage)
	}
	return nil
}

func checkoutDefaultBranch(ctx context.Context, repoRoot string, defaultBranch string) error {
	branchExists, err := localBranchExists(ctx, repoRoot, defaultBranch)
	if err != nil {
		return err
	}

	if branchExists {
		output, err := runGitContext(ctx, repoRoot, "checkout", defaultBranch)
		if err != nil {
			return fmt.Errorf("checkout default branch %q: %w%s", defaultBranch, err, gitOutputSuffix(output))
		}
		return nil
	}

	remoteBranch := "origin/" + defaultBranch
	output, err := runGitContext(ctx, repoRoot, "checkout", "--track", "-b", defaultBranch, remoteBranch)
	if err != nil {
		return fmt.Errorf("checkout default branch %q from %s: %w%s", defaultBranch, remoteBranch, err, gitOutputSuffix(output))
	}
	return nil
}

func checkoutTaskBranch(ctx context.Context, repoRoot string, branch string) error {
	output, err := runGitContext(ctx, repoRoot, "checkout", branch)
	if err != nil {
		return fmt.Errorf("checkout task branch %q: %w%s", branch, err, gitOutputSuffix(output))
	}
	return nil
}

func fastForwardFromOrigin(ctx context.Context, repoRoot string, defaultBranch string) error {
	remoteRef := "refs/remotes/origin/" + defaultBranch
	output, err := runGitContext(ctx, repoRoot, "merge-base", "--is-ancestor", "HEAD", remoteRef)
	if err != nil {
		if gitExitCode(err) == 1 {
			return fmt.Errorf(
				"fast-forward default branch %q from origin/%s: local branch is ahead of or divergent from origin/%s",
				defaultBranch,
				defaultBranch,
				defaultBranch,
			)
		}
		return fmt.Errorf("fast-forward default branch %q from origin/%s: inspect ancestry: %w%s", defaultBranch, defaultBranch, err, gitOutputSuffix(output))
	}

	output, err = runGitContext(ctx, repoRoot, "merge", "--ff-only", remoteRef)
	if err != nil {
		return fmt.Errorf("fast-forward default branch %q from origin/%s: %w%s", defaultBranch, defaultBranch, err, gitOutputSuffix(output))
	}
	return nil
}

// CurrentBranch returns the current branch for dir without mutating the repository.
func CurrentBranch(ctx context.Context, dir string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return currentBranchAt(ctx, dir)
}

// HasWorkingTreeChanges reports whether dir has tracked, staged, or untracked changes.
func HasWorkingTreeChanges(ctx context.Context, dir string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	output, err := runGitContext(ctx, dir, "status", "--porcelain=v1")
	if err != nil {
		return false, fmt.Errorf("inspect working tree changes: %w%s", err, gitOutputSuffix(output))
	}
	return strings.TrimSpace(output) != "", nil
}

// StageAll stages tracked modifications, tracked deletions, and untracked
// non-ignored files in dir using Git's normal ignore rules.
func StageAll(ctx context.Context, dir string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	output, err := runGitContext(ctx, dir, "add", "--all")
	if err != nil {
		return fmt.Errorf("stage changes: %w%s", err, gitOutputSuffix(output))
	}
	return nil
}

// Commit creates a commit from message and returns the resulting HEAD SHA.
func Commit(ctx context.Context, dir string, message string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(message) == "" {
		return "", errors.New("commit message is required")
	}

	output, err := runGitContextWithInput(ctx, dir, message, "commit", "--file", "-")
	if err != nil {
		return "", fmt.Errorf("commit changes: %w%s", err, gitOutputSuffix(output))
	}

	commit, err := HeadCommit(ctx, dir)
	if err != nil {
		return "", fmt.Errorf("read HEAD after commit: %w", err)
	}
	return commit, nil
}

// HeadCommit returns the current HEAD commit SHA without mutating the repository.
func HeadCommit(ctx context.Context, dir string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	output, err := runGitContext(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("read HEAD commit: %w%s", err, gitOutputSuffix(output))
	}
	commit := strings.TrimSpace(output)
	if commit == "" {
		return "", errors.New("read HEAD commit: git returned an empty commit")
	}
	return commit, nil
}

// PushDefaultBranch pushes branch to origin.
func PushDefaultBranch(ctx context.Context, dir string, branch string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return errors.New("default branch is required")
	}

	output, err := runGitContext(ctx, dir, "push", "origin", branch)
	if err != nil {
		return fmt.Errorf("push default branch %q to origin: %w%s", branch, err, gitOutputSuffix(output))
	}
	return nil
}

// PushTaskBranch pushes a task branch to origin.
func PushTaskBranch(ctx context.Context, dir string, branch string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return errors.New("task branch is required")
	}

	output, err := runGitContext(ctx, dir, "push", "-u", "origin", branch)
	if err != nil {
		return fmt.Errorf("push task branch %q to origin: %w%s", branch, err, gitOutputSuffix(output))
	}
	return nil
}

func runGitContextWithInput(ctx context.Context, dir string, input string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.Stdin = strings.NewReader(input)

	var stdout strings.Builder
	var stderr strings.Builder
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()
	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" && !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		output += stderr.String()
	}
	return output, err
}

func verifyRef(ctx context.Context, repoRoot string, ref string) error {
	output, err := runGitContext(ctx, repoRoot, "show-ref", "--verify", "--quiet", ref)
	if err == nil {
		return nil
	}
	return fmt.Errorf("verify fetched ref %q: %w%s", ref, err, gitOutputSuffix(output))
}

func createBranch(ctx context.Context, repoRoot string, branch string, startPoint string) error {
	output, err := runGitContext(ctx, repoRoot, "branch", branch, startPoint)
	if err != nil {
		return fmt.Errorf("create task branch %q from %q: %w%s", branch, startPoint, err, gitOutputSuffix(output))
	}
	return nil
}

func addWorktree(ctx context.Context, repoRoot string, path string, branch string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create deterministic worktree parent for %q: %w", path, err)
	}

	output, err := runGitContext(ctx, repoRoot, "worktree", "add", path, branch)
	if err != nil {
		return fmt.Errorf("add deterministic worktree %q for branch %q: %w%s", path, branch, err, gitOutputSuffix(output))
	}
	return nil
}

func worktreeRoot(ctx context.Context, inputPath string) (string, error) {
	output, err := runGitContext(ctx, inputPath, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("read Git worktree root: %w%s", err, gitOutputSuffix(output))
	}

	root := strings.TrimSpace(output)
	if root == "" {
		return "", errors.New("read Git worktree root: git returned an empty repository root")
	}
	if !filepath.IsAbs(root) {
		root = filepath.Join(inputPath, root)
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("normalize Git worktree root %q: %w", root, err)
	}
	return filepath.Clean(absoluteRoot), nil
}

func gitCommonDir(ctx context.Context, dir string) (string, error) {
	output, err := runGitContext(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("read Git common dir: %w%s", err, gitOutputSuffix(output))
	}

	commonDir := strings.TrimSpace(output)
	if commonDir == "" {
		return "", errors.New("read Git common dir: git returned an empty path")
	}
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(dir, commonDir)
	}
	absoluteCommonDir, err := filepath.Abs(commonDir)
	if err != nil {
		return "", fmt.Errorf("normalize Git common dir %q: %w", commonDir, err)
	}
	return filepath.Clean(absoluteCommonDir), nil
}

func currentBranchAt(ctx context.Context, dir string) (string, error) {
	output, err := runGitContext(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("read current branch: %w%s", err, gitOutputSuffix(output))
	}

	branch := strings.TrimSpace(output)
	if branch == "" {
		return "", errors.New("read current branch: git returned an empty branch")
	}
	return branch, nil
}

func gitOutputSuffix(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	return ": " + output
}

func gitExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
