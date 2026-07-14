package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	AllowDirty    bool
}

// RepoRootOptions describes a repo-root/default-branch task run target.
type RepoRootOptions struct {
	RepoID        string
	RepoName      string
	RepoPath      string
	DefaultBranch string
	AllowDirty    bool
}

// TaskWorktreeSetupResult is the backend-neutral result of preparing a task execution target.
type TaskWorktreeSetupResult struct {
	Branch       string
	WorktreePath string
	Lifecycle    TaskWorktreeLifecycle
}

// TaskBranchSyncStatus describes whether a PR branch changed during sync.
type TaskBranchSyncStatus string

const (
	// TaskBranchSyncAlreadyCurrent means the task branch already contains origin/default.
	TaskBranchSyncAlreadyCurrent TaskBranchSyncStatus = "already_current"

	// TaskBranchSyncPushed means the task branch already contains origin/default and was pushed.
	TaskBranchSyncPushed TaskBranchSyncStatus = "pushed"

	// TaskBranchSyncUpdated means origin/default was merged and the task branch was pushed.
	TaskBranchSyncUpdated TaskBranchSyncStatus = "updated"

	// TaskBranchSyncConflicted means origin/default was merged into the task branch
	// and left conflicts for an agent to resolve.
	TaskBranchSyncConflicted TaskBranchSyncStatus = "conflicted"
)

// TaskBranchSyncOptions describes a PR branch update from the registered default branch.
type TaskBranchSyncOptions struct {
	RepoPath      string
	DefaultBranch string
	Branch        string
	Worktree      string
}

// TaskBranchSyncResult reports the local branch state after sync.
type TaskBranchSyncResult struct {
	Status        TaskBranchSyncStatus
	Branch        string
	DefaultBranch string
	PreviousHead  string
	Head          string
	ConflictFiles []string
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

type taskBranchSyncPlan struct {
	RepoPath      string
	DefaultBranch string
	Branch        string
	Worktree      string
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

	repoRoot, err := validateRepoRootRun(ctx, plan, opts.AllowDirty)
	if err != nil {
		return TaskWorktreeSetupResult{}, err
	}
	if opts.AllowDirty {
		if err := requireCurrentBranch(ctx, repoRoot, plan.DefaultBranch, "default branch"); err != nil {
			return TaskWorktreeSetupResult{}, fmt.Errorf("prepare repo-root task run: %w", err)
		}
		return result, nil
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

	repoRoot, err := validateRepoRootTaskBranchRun(ctx, plan, opts.AllowDirty)
	if err != nil {
		return TaskWorktreeSetupResult{}, err
	}
	if opts.AllowDirty {
		if err := requireCurrentBranch(ctx, repoRoot, plan.Branch, "task branch"); err != nil {
			return TaskWorktreeSetupResult{}, fmt.Errorf("prepare repo-root task branch run %s: %w", plan.TaskID, err)
		}
		return result, nil
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

// SyncTaskBranchWithDefault fetches origin branches, merges default into a clean task branch, and pushes the branch.
func SyncTaskBranchWithDefault(ctx context.Context, opts TaskBranchSyncOptions) (TaskBranchSyncResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	plan, err := newTaskBranchSyncPlan(opts)
	if err != nil {
		return TaskBranchSyncResult{}, err
	}
	result := newTaskBranchSyncResult(plan)

	if err := prepareTaskBranchSync(ctx, plan); err != nil {
		return TaskBranchSyncResult{}, err
	}
	if _, err := prepareDefaultBranchRef(ctx, plan.Worktree, plan.DefaultBranch); err != nil {
		return TaskBranchSyncResult{}, err
	}

	return syncFetchedTaskBranchWithDefault(ctx, plan, result)
}

// BeginTaskBranchConflictResolution fetches origin branches and attempts to
// merge origin/default into a task branch, leaving merge conflicts in the
// worktree when the merge cannot be completed automatically.
func BeginTaskBranchConflictResolution(
	ctx context.Context,
	opts TaskBranchSyncOptions,
) (TaskBranchSyncResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	plan, err := newTaskBranchSyncPlan(opts)
	if err != nil {
		return TaskBranchSyncResult{}, err
	}
	result := newTaskBranchSyncResult(plan)

	if err := prepareTaskBranchSync(ctx, plan); err != nil {
		return TaskBranchSyncResult{}, err
	}
	if _, err := prepareDefaultBranchRef(ctx, plan.Worktree, plan.DefaultBranch); err != nil {
		return TaskBranchSyncResult{}, err
	}
	return beginFetchedTaskBranchConflictResolution(ctx, plan, result)
}

// CompleteTaskBranchConflictResolution verifies an in-progress conflicted merge
// is resolved, creates the merge commit, and pushes the task branch.
func CompleteTaskBranchConflictResolution(
	ctx context.Context,
	opts TaskBranchSyncOptions,
	conflictFiles []string,
) (TaskBranchSyncResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	plan, err := newTaskBranchSyncPlan(opts)
	if err != nil {
		return TaskBranchSyncResult{}, err
	}
	result := newTaskBranchSyncResult(plan)

	if err := validateTaskBranchSyncCheckout(ctx, plan); err != nil {
		return TaskBranchSyncResult{}, err
	}
	if err := requireMergeInProgress(ctx, plan.Worktree); err != nil {
		return TaskBranchSyncResult{}, err
	}
	resolutionState, err := mergeResolutionState(ctx, plan.Worktree, conflictFiles)
	if err != nil {
		return TaskBranchSyncResult{}, err
	}
	unresolved, err := unmergedFiles(ctx, plan.Worktree)
	if err != nil {
		return TaskBranchSyncResult{}, err
	}
	if len(unresolved) > 0 {
		result.Status = TaskBranchSyncConflicted
		result.ConflictFiles = unresolved
		return result, fmt.Errorf(
			"complete conflict resolution for task branch %q: unresolved merge conflicts remain: %s",
			plan.Branch,
			strings.Join(unresolved, ", "),
		)
	}
	if err := stageResolvedConflictFiles(ctx, plan.Worktree, conflictFiles); err != nil {
		return TaskBranchSyncResult{}, err
	}
	if err := requireExpectedConflictResolutionChanges(ctx, plan.Worktree, resolutionState); err != nil {
		return TaskBranchSyncResult{}, err
	}
	if err := rejectConflictMarkers(plan.Worktree, conflictFiles); err != nil {
		return TaskBranchSyncResult{}, err
	}
	head, err := commitAndPushResolvedMerge(ctx, plan.Worktree, plan.Branch)
	if err != nil {
		return TaskBranchSyncResult{}, err
	}
	result.Status = TaskBranchSyncUpdated
	result.Head = head
	return result, nil
}

func newTaskBranchSyncResult(plan taskBranchSyncPlan) TaskBranchSyncResult {
	return TaskBranchSyncResult{
		Branch:        plan.Branch,
		DefaultBranch: plan.DefaultBranch,
		ConflictFiles: []string{},
	}
}

func prepareTaskBranchSync(ctx context.Context, plan taskBranchSyncPlan) error {
	if err := validateTaskBranchSyncCheckout(ctx, plan); err != nil {
		return err
	}
	if err := requireCleanRepoRootFor(ctx, plan.Worktree, "task sync branch update"); err != nil {
		return err
	}
	taskBranchFetched, err := fetchTaskBranch(ctx, plan.Worktree, plan.Branch)
	if err != nil {
		return err
	}
	if !taskBranchFetched {
		return nil
	}
	if err := fastForwardTaskBranchFromOrigin(ctx, plan.Worktree, plan.Branch); err != nil {
		return err
	}
	return requireCleanRepoRootFor(ctx, plan.Worktree, "task sync branch update")
}

func syncFetchedTaskBranchWithDefault(
	ctx context.Context,
	plan taskBranchSyncPlan,
	result TaskBranchSyncResult,
) (TaskBranchSyncResult, error) {
	previousHead, err := HeadCommit(ctx, plan.Worktree)
	if err != nil {
		return TaskBranchSyncResult{}, err
	}
	result.PreviousHead = previousHead

	remoteRef := "refs/remotes/origin/" + plan.DefaultBranch
	containsDefault, err := branchContainsRef(ctx, plan.Worktree, "HEAD", remoteRef)
	if err != nil {
		return TaskBranchSyncResult{}, err
	}
	if containsDefault {
		pushed, err := pushTaskBranchIfRemoteBehind(ctx, plan.Worktree, plan.Branch, previousHead)
		if err != nil {
			return TaskBranchSyncResult{}, err
		}
		result.Status = TaskBranchSyncAlreadyCurrent
		if pushed {
			result.Status = TaskBranchSyncPushed
		}
		result.Head = previousHead
		return result, nil
	}

	if err := verifyMergeWouldBeClean(ctx, plan.Worktree, remoteRef); err != nil {
		return TaskBranchSyncResult{}, err
	}
	if err := mergeDefaultIntoTaskBranch(ctx, plan.Worktree, plan.DefaultBranch, remoteRef); err != nil {
		return TaskBranchSyncResult{}, err
	}
	if err := requireCleanRepoRootFor(ctx, plan.Worktree, "task sync branch update"); err != nil {
		return TaskBranchSyncResult{}, err
	}
	if err := PushTaskBranch(ctx, plan.Worktree, plan.Branch); err != nil {
		return TaskBranchSyncResult{}, err
	}

	head, err := HeadCommit(ctx, plan.Worktree)
	if err != nil {
		return TaskBranchSyncResult{}, err
	}
	result.Status = TaskBranchSyncUpdated
	result.Head = head
	return result, nil
}

func beginFetchedTaskBranchConflictResolution(
	ctx context.Context,
	plan taskBranchSyncPlan,
	result TaskBranchSyncResult,
) (TaskBranchSyncResult, error) {
	previousHead, err := HeadCommit(ctx, plan.Worktree)
	if err != nil {
		return TaskBranchSyncResult{}, err
	}
	result.PreviousHead = previousHead

	remoteRef := "refs/remotes/origin/" + plan.DefaultBranch
	containsDefault, err := branchContainsRef(ctx, plan.Worktree, "HEAD", remoteRef)
	if err != nil {
		return TaskBranchSyncResult{}, err
	}
	if containsDefault {
		pushed, err := pushTaskBranchIfRemoteBehind(ctx, plan.Worktree, plan.Branch, previousHead)
		if err != nil {
			return TaskBranchSyncResult{}, err
		}
		result.Status = TaskBranchSyncAlreadyCurrent
		if pushed {
			result.Status = TaskBranchSyncPushed
		}
		result.Head = previousHead
		return result, nil
	}

	if err := mergeDefaultIntoTaskBranchForResolution(ctx, plan.Worktree, plan.DefaultBranch, remoteRef); err != nil {
		if errors.Is(err, ErrMergeConflict) {
			conflictFiles, filesErr := unmergedFiles(ctx, plan.Worktree)
			if filesErr != nil {
				return TaskBranchSyncResult{}, filesErr
			}
			result.Status = TaskBranchSyncConflicted
			result.Head = previousHead
			result.ConflictFiles = conflictFiles
			return result, nil
		}
		return TaskBranchSyncResult{}, err
	}
	if err := requireCleanRepoRootFor(ctx, plan.Worktree, "task sync branch update"); err != nil {
		return TaskBranchSyncResult{}, err
	}
	if err := PushTaskBranch(ctx, plan.Worktree, plan.Branch); err != nil {
		return TaskBranchSyncResult{}, err
	}

	head, err := HeadCommit(ctx, plan.Worktree)
	if err != nil {
		return TaskBranchSyncResult{}, err
	}
	result.Status = TaskBranchSyncUpdated
	result.Head = head
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

func validateRepoRootRun(ctx context.Context, plan repoRootPlan, allowDirty bool) (string, error) {
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
	if !allowDirty {
		if err := requireCleanRepoRoot(ctx, repoRoot); err != nil {
			return "", fmt.Errorf(
				"prepare repo-root task run for repo %s (%s): %w",
				plan.RepoID,
				plan.RepoName,
				err,
			)
		}
	}
	return repoRoot, nil
}

func validateRepoRootTaskBranchRun(ctx context.Context, plan taskWorktreePlan, allowDirty bool) (string, error) {
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
	if !allowDirty {
		if err := requireCleanRepoRootFor(ctx, repoRoot, "with --repo-root"); err != nil {
			return "", fmt.Errorf(
				"prepare repo-root task branch run %s for repo %s (%s): %w",
				plan.TaskID,
				plan.RepoID,
				plan.RepoName,
				err,
			)
		}
	}
	return repoRoot, nil
}

func requireCurrentBranch(ctx context.Context, repoRoot string, expected string, label string) error {
	current, err := currentBranchAt(ctx, repoRoot)
	if err != nil {
		return fmt.Errorf("verify current %s checkout: %w", label, err)
	}
	if current != expected {
		return fmt.Errorf("repo root is on branch %q; expected %s %q", current, label, expected)
	}
	return nil
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

func newTaskBranchSyncPlan(opts TaskBranchSyncOptions) (taskBranchSyncPlan, error) {
	repoPath, err := normalizeRegisteredRepoPath(opts.RepoPath)
	if err != nil {
		return taskBranchSyncPlan{}, err
	}
	worktree, err := normalizeRegisteredRepoPath(opts.Worktree)
	if err != nil {
		return taskBranchSyncPlan{}, fmt.Errorf("task branch worktree: %w", err)
	}

	return taskBranchSyncPlan{
		RepoPath:      repoPath,
		DefaultBranch: strings.TrimSpace(opts.DefaultBranch),
		Branch:        strings.TrimSpace(opts.Branch),
		Worktree:      worktree,
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

func validateTaskBranchSyncCheckout(ctx context.Context, plan taskBranchSyncPlan) error {
	repoCommonDir, err := validateTaskBranchSyncRegisteredRepo(ctx, plan)
	if err != nil {
		return err
	}
	if err := validateTaskBranchSyncWorktree(ctx, plan, repoCommonDir); err != nil {
		return err
	}
	current, err := currentBranchAt(ctx, plan.Worktree)
	if err != nil {
		return fmt.Errorf("sync task branch %q: inspect current branch: %w", plan.Branch, err)
	}
	if current != plan.Branch {
		return fmt.Errorf("sync task branch %q: worktree %q is on branch %q", plan.Branch, plan.Worktree, current)
	}
	return nil
}

func validateTaskBranchSyncRegisteredRepo(ctx context.Context, plan taskBranchSyncPlan) (string, error) {
	repoRoot, err := worktreeRoot(ctx, plan.RepoPath)
	if err != nil {
		return "", fmt.Errorf("sync task branch %q: inspect registered repo root %q: %w", plan.Branch, plan.RepoPath, err)
	}
	if repoRoot != plan.RepoPath {
		return "", fmt.Errorf(
			"sync task branch %q: registered repo path %q resolves to Git root %q; register the repository root before syncing PR branches",
			plan.Branch,
			plan.RepoPath,
			repoRoot,
		)
	}
	if err := validateBranchRef(ctx, repoRoot, "task branch", plan.Branch); err != nil {
		return "", fmt.Errorf("sync task branch %q: %w", plan.Branch, err)
	}
	if err := validateBranchRef(ctx, repoRoot, "default branch", plan.DefaultBranch); err != nil {
		return "", fmt.Errorf("sync task branch %q: %w", plan.Branch, err)
	}
	if err := requireOriginRemoteFor(ctx, repoRoot, "task sync branch updates"); err != nil {
		return "", fmt.Errorf("sync task branch %q: %w", plan.Branch, err)
	}
	repoCommonDir, err := gitCommonDir(ctx, repoRoot)
	if err != nil {
		return "", fmt.Errorf("sync task branch %q: inspect registered repo common Git dir: %w", plan.Branch, err)
	}
	return repoCommonDir, nil
}

func validateTaskBranchSyncWorktree(ctx context.Context, plan taskBranchSyncPlan, repoCommonDir string) error {
	worktreeRootPath, err := worktreeRoot(ctx, plan.Worktree)
	if err != nil {
		return fmt.Errorf("sync task branch %q: inspect worktree %q: %w", plan.Branch, plan.Worktree, err)
	}
	if worktreeRootPath != plan.Worktree {
		return fmt.Errorf(
			"sync task branch %q: worktree %q resolves to Git root %q; expected the worktree root itself",
			plan.Branch,
			plan.Worktree,
			worktreeRootPath,
		)
	}
	worktreeCommonDir, err := gitCommonDir(ctx, plan.Worktree)
	if err != nil {
		return fmt.Errorf("sync task branch %q: inspect worktree common Git dir: %w", plan.Branch, err)
	}
	if worktreeCommonDir != repoCommonDir {
		return fmt.Errorf(
			"sync task branch %q: worktree %q points at Git common dir %q; expected %q",
			plan.Branch,
			plan.Worktree,
			worktreeCommonDir,
			repoCommonDir,
		)
	}
	return nil
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

func fetchTaskBranch(ctx context.Context, repoRoot string, branch string) (bool, error) {
	remoteHead := "refs/heads/" + branch
	output, err := runGitContext(ctx, repoRoot, "ls-remote", "--exit-code", "origin", remoteHead)
	if err != nil {
		if gitExitCode(err) == 2 {
			return false, nil
		}
		return false, fmt.Errorf("inspect origin/%s: %w%s", branch, err, gitOutputSuffix(output))
	}
	if strings.TrimSpace(output) == "" {
		return false, nil
	}

	refspec := fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", branch, branch)
	output, err = runGitContext(ctx, repoRoot, "fetch", "origin", refspec)
	if err != nil {
		return false, fmt.Errorf("fetch origin/%s: %w%s", branch, err, gitOutputSuffix(output))
	}

	remoteRef := "refs/remotes/origin/" + branch
	if err := verifyRef(ctx, repoRoot, remoteRef); err != nil {
		return false, err
	}
	return true, nil
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

func fastForwardTaskBranchFromOrigin(ctx context.Context, repoRoot string, branch string) error {
	remoteRef := "refs/remotes/origin/" + branch
	containsRemote, err := branchContainsRef(ctx, repoRoot, "HEAD", remoteRef)
	if err != nil {
		return fmt.Errorf("fast-forward task branch %q from origin/%s: inspect ancestry: %w", branch, branch, err)
	}
	if containsRemote {
		return nil
	}

	remoteContainsLocal, err := branchContainsRef(ctx, repoRoot, remoteRef, "HEAD")
	if err != nil {
		return fmt.Errorf("fast-forward task branch %q from origin/%s: inspect ancestry: %w", branch, branch, err)
	}
	if !remoteContainsLocal {
		return fmt.Errorf(
			"fast-forward task branch %q from origin/%s: local branch is divergent from origin/%s",
			branch,
			branch,
			branch,
		)
	}

	output, err := runGitContext(ctx, repoRoot, "merge", "--ff-only", remoteRef)
	if err != nil {
		return fmt.Errorf("fast-forward task branch %q from origin/%s: %w%s", branch, branch, err, gitOutputSuffix(output))
	}
	return nil
}

func branchContainsRef(ctx context.Context, dir string, branch string, ref string) (bool, error) {
	output, err := runGitContext(ctx, dir, "merge-base", "--is-ancestor", ref, branch)
	if err == nil {
		return true, nil
	}
	if gitExitCode(err) == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect whether %s contains %s: %w%s", branch, ref, err, gitOutputSuffix(output))
}

func verifyMergeWouldBeClean(ctx context.Context, dir string, remoteRef string) error {
	output, err := runGitContext(ctx, dir, "merge-tree", "--write-tree", "HEAD", remoteRef)
	if err == nil {
		return nil
	}
	if gitExitCode(err) == 1 {
		return fmt.Errorf("%w: merge default branch %s into task branch would conflict; no changes were pushed%s", ErrMergeConflict, remoteRef, gitOutputSuffix(output))
	}
	return fmt.Errorf("preflight merge default branch %s into task branch: %w%s", remoteRef, err, gitOutputSuffix(output))
}

func mergeDefaultIntoTaskBranch(ctx context.Context, dir string, defaultBranch string, remoteRef string) error {
	output, err := runGitContext(ctx, dir, "merge", "--no-edit", remoteRef)
	if err == nil {
		return nil
	}

	abortOutput, abortErr := runGitContext(ctx, dir, "merge", "--abort")
	if abortErr != nil {
		return fmt.Errorf(
			"merge default branch %q into task branch: %w%s; additionally failed to abort merge: %w%s",
			defaultBranch,
			err,
			gitOutputSuffix(output),
			abortErr,
			gitOutputSuffix(abortOutput),
		)
	}
	return fmt.Errorf("merge default branch %q into task branch: %w%s", defaultBranch, err, gitOutputSuffix(output))
}

func mergeDefaultIntoTaskBranchForResolution(
	ctx context.Context,
	dir string,
	defaultBranch string,
	remoteRef string,
) error {
	output, err := runGitContext(ctx, dir, "merge", "--no-edit", remoteRef)
	if err == nil {
		return nil
	}
	if gitExitCode(err) == 1 {
		return fmt.Errorf("%w: merge default branch %q into task branch%s", ErrMergeConflict, defaultBranch, gitOutputSuffix(output))
	}
	return fmt.Errorf("merge default branch %q into task branch for conflict resolution: %w%s", defaultBranch, err, gitOutputSuffix(output))
}

func requireMergeInProgress(ctx context.Context, dir string) error {
	output, err := runGitContext(ctx, dir, "rev-parse", "--verify", "--quiet", "MERGE_HEAD")
	if err != nil {
		return fmt.Errorf("verify merge in progress: no merge is in progress%s", gitOutputSuffix(output))
	}
	if strings.TrimSpace(output) == "" {
		return errors.New("verify merge in progress: git returned an empty MERGE_HEAD")
	}
	return nil
}

func unmergedFiles(ctx context.Context, dir string) ([]string, error) {
	output, err := runGitContext(ctx, dir, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, fmt.Errorf("inspect unresolved merge conflicts: %w%s", err, gitOutputSuffix(output))
	}
	files := make([]string, 0)
	for _, line := range strings.Split(output, "\n") {
		file := strings.TrimSpace(line)
		if file != "" {
			files = append(files, file)
		}
	}
	return files, nil
}

type conflictResolutionState struct {
	mergeFiles    map[string]bool
	conflictFiles map[string]bool
}

func mergeResolutionState(
	ctx context.Context,
	dir string,
	conflictFiles []string,
) (conflictResolutionState, error) {
	output, err := runGitContext(ctx, dir, "diff", "--name-only", "-z", "HEAD", "MERGE_HEAD")
	if err != nil {
		return conflictResolutionState{}, fmt.Errorf("inspect merge resolution files: %w%s", err, gitOutputSuffix(output))
	}

	state := conflictResolutionState{
		mergeFiles:    map[string]bool{},
		conflictFiles: map[string]bool{},
	}
	for _, file := range splitNULPaths(output) {
		if path, ok := cleanGitPath(file); ok {
			state.mergeFiles[path] = true
		}
	}
	for _, file := range conflictFiles {
		path, ok := cleanGitPath(file)
		if !ok {
			continue
		}
		state.mergeFiles[path] = true
		state.conflictFiles[path] = true
	}
	return state, nil
}

func requireExpectedConflictResolutionChanges(
	ctx context.Context,
	dir string,
	state conflictResolutionState,
) error {
	output, err := runGitContext(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return fmt.Errorf("inspect resolved merge status: %w%s", err, gitOutputSuffix(output))
	}

	unexpected := map[string]bool{}
	for _, entry := range parseStatusPorcelainZ(output) {
		for _, file := range entry.contentPaths() {
			path, ok := cleanGitPath(file)
			if !ok {
				unexpected[file] = true
				continue
			}
			if !state.mergeFiles[path] || (!state.conflictFiles[path] && entry.hasWorktreeChange()) {
				unexpected[path] = true
			}
		}
	}
	if err := addUnexpectedCleanMergeIndexChanges(ctx, dir, state, unexpected); err != nil {
		return err
	}
	if len(unexpected) == 0 {
		return nil
	}

	files := make([]string, 0, len(unexpected))
	for file := range unexpected {
		files = append(files, file)
	}
	sort.Strings(files)
	return fmt.Errorf(
		"complete conflict resolution: unexpected changes outside merge conflict files: %s; "+
			"resolve only the reported merge conflicts or clean up the extra changes before retrying",
		strings.Join(files, ", "),
	)
}

func addUnexpectedCleanMergeIndexChanges(
	ctx context.Context,
	dir string,
	state conflictResolutionState,
	unexpected map[string]bool,
) error {
	cleanMergeFiles := make([]string, 0)
	for file := range state.mergeFiles {
		if !state.conflictFiles[file] {
			cleanMergeFiles = append(cleanMergeFiles, file)
		}
	}
	if len(cleanMergeFiles) == 0 {
		return nil
	}
	exists, err := gitRefExists(ctx, dir, "AUTO_MERGE")
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	args := append([]string{"diff", "--cached", "--name-only", "-z", "AUTO_MERGE", "--"}, cleanMergeFiles...)
	output, err := runGitContext(ctx, dir, args...)
	if err != nil {
		return fmt.Errorf("inspect clean merge index changes: %w%s", err, gitOutputSuffix(output))
	}
	for _, file := range splitNULPaths(output) {
		if path, ok := cleanGitPath(file); ok {
			unexpected[path] = true
		}
	}
	return nil
}

type statusEntry struct {
	x     byte
	y     byte
	paths []string
}

func (e statusEntry) hasWorktreeChange() bool {
	return e.y != ' '
}

func (e statusEntry) contentPaths() []string {
	if !e.isRenameOrCopy() || len(e.paths) <= 1 {
		return e.paths
	}
	return e.paths[:1]
}

func (e statusEntry) isRenameOrCopy() bool {
	return e.x == 'R' || e.x == 'C' || e.y == 'R' || e.y == 'C'
}

func parseStatusPorcelainZ(output string) []statusEntry {
	records := splitNULPaths(output)
	entries := make([]statusEntry, 0, len(records))
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) < 4 {
			continue
		}
		entry := statusEntry{
			x:     record[0],
			y:     record[1],
			paths: []string{record[3:]},
		}
		if entry.isRenameOrCopy() && i+1 < len(records) {
			entry.paths = append(entry.paths, records[i+1])
			i++
		}
		entries = append(entries, entry)
	}
	return entries
}

func splitNULPaths(output string) []string {
	parts := strings.Split(output, "\x00")
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			paths = append(paths, part)
		}
	}
	return paths
}

func gitRefExists(ctx context.Context, dir string, ref string) (bool, error) {
	output, err := runGitContext(ctx, dir, "rev-parse", "--verify", "--quiet", ref)
	if err == nil {
		return strings.TrimSpace(output) != "", nil
	}
	if gitExitCode(err) == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect git ref %q: %w%s", ref, err, gitOutputSuffix(output))
}

func cleanGitPath(file string) (string, bool) {
	file = strings.TrimSpace(file)
	if file == "" || filepath.IsAbs(file) {
		return "", false
	}
	path := filepath.ToSlash(filepath.Clean(file))
	if path == "." || path == ".." || strings.HasPrefix(path, "../") {
		return "", false
	}
	return path, true
}

func stageResolvedConflictFiles(ctx context.Context, dir string, conflictFiles []string) error {
	files := make([]string, 0, len(conflictFiles))
	seen := map[string]bool{}
	for _, file := range conflictFiles {
		file = strings.TrimSpace(file)
		if file == "" || seen[file] {
			continue
		}
		seen[file] = true
		files = append(files, file)
	}
	if len(files) == 0 {
		return nil
	}

	args := append([]string{"add", "--"}, files...)
	output, err := runGitContext(ctx, dir, args...)
	if err != nil {
		return fmt.Errorf("stage resolved conflict files: %w%s", err, gitOutputSuffix(output))
	}
	return nil
}

func rejectConflictMarkers(dir string, conflictFiles []string) error {
	for _, file := range conflictFiles {
		path, ok := cleanConflictFilePath(file)
		if !ok {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("inspect resolved conflict file %q: %w", file, err)
		}
		if hasConflictMarker(content) {
			return fmt.Errorf("resolved conflict file %q still contains conflict markers", file)
		}
	}
	return nil
}

func cleanConflictFilePath(file string) (string, bool) {
	file = strings.TrimSpace(file)
	if file == "" || filepath.IsAbs(file) {
		return "", false
	}
	path := filepath.Clean(file)
	if path == "." || path == ".." || strings.HasPrefix(path, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return path, true
}

func hasConflictMarker(content []byte) bool {
	for _, line := range strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "<<<<<<< ") ||
			trimmed == "=======" ||
			strings.HasPrefix(trimmed, ">>>>>>> ") {
			return true
		}
	}
	return false
}

func commitResolvedMerge(ctx context.Context, dir string) error {
	output, err := runGitContext(ctx, dir, "commit", "--no-edit")
	if err != nil {
		return fmt.Errorf("commit resolved merge: %w%s", err, gitOutputSuffix(output))
	}
	return nil
}

func commitAndPushResolvedMerge(ctx context.Context, dir string, branch string) (string, error) {
	if err := commitResolvedMerge(ctx, dir); err != nil {
		return "", err
	}
	if err := requireCleanRepoRootFor(ctx, dir, "resolved task branch merge push"); err != nil {
		return "", err
	}
	if err := PushTaskBranch(ctx, dir, branch); err != nil {
		return "", err
	}

	head, err := HeadCommit(ctx, dir)
	if err != nil {
		return "", err
	}
	return head, nil
}

func pushTaskBranchIfRemoteBehind(ctx context.Context, dir string, branch string, head string) (bool, error) {
	remoteRef := "refs/remotes/origin/" + branch
	output, err := runGitContext(ctx, dir, "rev-parse", "--verify", remoteRef)
	if err == nil && strings.TrimSpace(output) == head {
		return false, nil
	}
	if err := PushTaskBranch(ctx, dir, branch); err != nil {
		return false, err
	}
	return true, nil
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
