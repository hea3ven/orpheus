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
	TaskWorktreeLifecycleCreated   TaskWorktreeLifecycle = "created"
	TaskWorktreeLifecycleReused    TaskWorktreeLifecycle = "reused"
	TaskWorktreeLifecycleRecreated TaskWorktreeLifecycle = "recreated"
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

// TaskWorktreeSetupResult is the backend-neutral result of preparing a task worktree.
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

	repoRoot, err := worktreeRoot(ctx, plan.RepoPath)
	if err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf(
			"prepare task worktree %s for repo %s (%s): inspect registered repo root %q: %w",
			plan.TaskID,
			plan.RepoID,
			plan.RepoName,
			plan.RepoPath,
			err,
		)
	}
	if repoRoot != plan.RepoPath {
		return TaskWorktreeSetupResult{}, fmt.Errorf(
			"prepare task worktree %s for repo %s (%s): registered repo path %q resolves to Git root %q; register the repository root before running tasks",
			plan.TaskID,
			plan.RepoID,
			plan.RepoName,
			plan.RepoPath,
			repoRoot,
		)
	}

	if err := validateBranchRef(ctx, repoRoot, "task branch", plan.Branch); err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf("prepare task worktree %s: %w", plan.TaskID, err)
	}
	if err := validateBranchRef(ctx, repoRoot, "default branch", plan.DefaultBranch); err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf("prepare task worktree %s: %w", plan.TaskID, err)
	}

	repoCommonDir, err := gitCommonDir(ctx, repoRoot)
	if err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf(
			"prepare task worktree %s for repo %s (%s): inspect registered repo common Git dir: %w",
			plan.TaskID,
			plan.RepoID,
			plan.RepoName,
			err,
		)
	}

	if err := requireOriginRemote(ctx, repoRoot); err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf(
			"prepare task worktree %s for repo %s (%s): %w",
			plan.TaskID,
			plan.RepoID,
			plan.RepoName,
			err,
		)
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
		if err := validateExistingTaskWorktree(ctx, plan.WorktreePath, plan.Branch, repoCommonDir); err != nil {
			return TaskWorktreeSetupResult{}, fmt.Errorf("prepare task worktree %s: %w", plan.TaskID, err)
		}
		result.Lifecycle = TaskWorktreeLifecycleReused
		return result, nil
	}

	if branchExists {
		if err := addWorktree(ctx, repoRoot, plan.WorktreePath, plan.Branch); err != nil {
			return TaskWorktreeSetupResult{}, fmt.Errorf("prepare task worktree %s: %w", plan.TaskID, err)
		}
		result.Lifecycle = TaskWorktreeLifecycleRecreated
		return result, nil
	}

	if err := fetchDefaultBranch(ctx, repoRoot, plan.DefaultBranch); err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf("prepare task worktree %s: %w", plan.TaskID, err)
	}
	remoteRef := "refs/remotes/origin/" + plan.DefaultBranch
	if err := verifyRef(ctx, repoRoot, remoteRef); err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf("prepare task worktree %s: %w", plan.TaskID, err)
	}
	if err := createBranch(ctx, repoRoot, plan.Branch, remoteRef); err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf("prepare task worktree %s: %w", plan.TaskID, err)
	}
	if err := addWorktree(ctx, repoRoot, plan.WorktreePath, plan.Branch); err != nil {
		return TaskWorktreeSetupResult{}, fmt.Errorf("prepare task worktree %s: %w", plan.TaskID, err)
	}

	result.Lifecycle = TaskWorktreeLifecycleCreated
	return result, nil
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

	repoPath := strings.TrimSpace(opts.RepoPath)
	if repoPath == "" {
		return taskWorktreePlan{}, errors.New("registered repo path is required")
	}
	if !filepath.IsAbs(repoPath) {
		return taskWorktreePlan{}, fmt.Errorf("registered repo path must be absolute, got %q", repoPath)
	}
	repoPath = filepath.Clean(repoPath)

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
	output, err := runGitContext(ctx, repoRoot, "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("registered repository requires an origin remote for deterministic task worktrees: %w%s", err, gitOutputSuffix(output))
	}
	if strings.TrimSpace(output) == "" {
		return errors.New("registered repository requires an origin remote for deterministic task worktrees")
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
