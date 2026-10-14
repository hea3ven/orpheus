//go:build integration

package git_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	orpheusgit "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestIntegrationAdapterContractSetupTaskWorktreeCreatesAndReusesDeterministicWorktree(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	paths := newStatePaths(t)

	got, err := orpheusgit.SetupTaskWorktree(context.Background(), orpheusgit.TaskWorktreeOptions{
		RepoID:        "alpha",
		RepoName:      "Alpha",
		RepoPath:      repoPath,
		DefaultBranch: "main",
		TaskID:        "op-1",
		Paths:         paths,
	})
	if err != nil {
		t.Fatalf("setup task worktree: %v", err)
	}

	expectedPath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-1"))
	if err != nil {
		t.Fatalf("resolve expected path: %v", err)
	}
	if got.Branch != "orpheus/op-1" || got.WorktreePath != expectedPath || got.Lifecycle != orpheusgit.TaskWorktreeLifecycleCreated {
		t.Fatalf("setup result = %#v, want branch/worktree/created", got)
	}
	assertGitBranch(t, got.WorktreePath, "orpheus/op-1")

	marker := filepath.Join(got.WorktreePath, "retry-marker.txt")
	if err := os.WriteFile(marker, []byte("preserve me"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	reused, err := orpheusgit.SetupTaskWorktree(context.Background(), orpheusgit.TaskWorktreeOptions{
		RepoID:        "alpha",
		RepoName:      "Alpha",
		RepoPath:      repoPath,
		DefaultBranch: "main",
		TaskID:        "op-1",
		Paths:         paths,
	})
	if err != nil {
		t.Fatalf("reuse task worktree: %v", err)
	}
	if reused.Lifecycle != orpheusgit.TaskWorktreeLifecycleReused {
		t.Fatalf("reuse lifecycle = %q, want %q", reused.Lifecycle, orpheusgit.TaskWorktreeLifecycleReused)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("marker was not preserved on reuse: %v", err)
	}
}

func TestIntegrationAdapterContractClosedTaskWorktreeCleanupReportsAbsentRegisteredWorktree(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	paths := newStatePaths(t)
	opts := orpheusgit.ClosedTaskWorktreeOptions{
		RepoID: "alpha", RepoName: "Alpha", RepoPath: repoPath, DefaultBranch: "main", TaskID: "op-partial", Paths: paths,
	}
	setup, err := orpheusgit.SetupTaskWorktree(context.Background(), opts)
	if err != nil {
		t.Fatalf("setup worktree: %v", err)
	}
	if err := os.RemoveAll(setup.WorktreePath); err != nil {
		t.Fatalf("remove worktree directory: %v", err)
	}

	inspection := orpheusgit.InspectClosedTaskWorktree(context.Background(), opts)
	if inspection.Outcome != orpheusgit.ClosedTaskWorktreeFailed || !strings.Contains(inspection.Reason, "still registers") {
		t.Fatalf("absent registered inspection = %#v, want failed unresolved registration", inspection)
	}
}

func TestIntegrationAdapterContractSetupTaskWorktreeAndRepoRootUseResolvedCustomBranch(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	paths := newStatePaths(t)
	branch := "feature/PROJ-7/Ship-the-thing"

	worktree, err := orpheusgit.SetupTaskWorktree(context.Background(), orpheusgit.TaskWorktreeOptions{
		RepoID: "alpha", RepoName: "Alpha", RepoPath: repoPath, DefaultBranch: "main", TaskID: "op-7", Branch: branch, Paths: paths,
	})
	if err != nil {
		t.Fatalf("setup custom worktree: %v", err)
	}
	if worktree.Branch != branch {
		t.Fatalf("worktree branch = %q, want %q", worktree.Branch, branch)
	}
	assertGitBranch(t, worktree.WorktreePath, branch)

	runGit(t, repoPath, "worktree", "remove", "--force", worktree.WorktreePath)
	root, err := orpheusgit.SetupRepoRootTaskBranch(context.Background(), orpheusgit.TaskWorktreeOptions{
		RepoID: "alpha", RepoName: "Alpha", RepoPath: repoPath, DefaultBranch: "main", TaskID: "op-7", Branch: branch, Paths: paths,
	})
	if err != nil {
		t.Fatalf("setup custom repo-root branch: %v", err)
	}
	if root.Branch != branch {
		t.Fatalf("repo-root branch = %q, want %q", root.Branch, branch)
	}
	assertGitBranch(t, repoPath, branch)
}

func TestIntegrationAdapterContractSetupTaskWorktreeRecreatesMissingWorktreeForExistingBranch(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	paths := newStatePaths(t)
	runGit(t, repoPath, "branch", "orpheus/op-2", "main")

	got, err := orpheusgit.SetupTaskWorktree(context.Background(), orpheusgit.TaskWorktreeOptions{
		RepoID:        "alpha",
		RepoName:      "Alpha",
		RepoPath:      repoPath,
		DefaultBranch: "main",
		TaskID:        "op-2",
		Paths:         paths,
	})
	if err != nil {
		t.Fatalf("setup task worktree: %v", err)
	}
	if got.Lifecycle != orpheusgit.TaskWorktreeLifecycleRecreated {
		t.Fatalf("lifecycle = %q, want %q", got.Lifecycle, orpheusgit.TaskWorktreeLifecycleRecreated)
	}
	assertGitBranch(t, got.WorktreePath, "orpheus/op-2")
}

func TestIntegrationAdapterContractSetupTaskWorktreeRefusesExistingPathOnDifferentBranch(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	paths := newStatePaths(t)
	expectedPath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-3"))
	if err != nil {
		t.Fatalf("resolve expected path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(expectedPath), 0o755); err != nil {
		t.Fatalf("create worktree parent: %v", err)
	}
	runGit(t, repoPath, "branch", "other", "main")
	runGit(t, repoPath, "worktree", "add", expectedPath, "other")

	_, err = orpheusgit.SetupTaskWorktree(context.Background(), orpheusgit.TaskWorktreeOptions{
		RepoID:        "alpha",
		RepoName:      "Alpha",
		RepoPath:      repoPath,
		DefaultBranch: "main",
		TaskID:        "op-3",
		Paths:         paths,
	})
	if err == nil {
		t.Fatal("setup succeeded, want branch mismatch error")
	}
	if !strings.Contains(err.Error(), "is on branch \"other\"; expected \"orpheus/op-3\"") {
		t.Fatalf("error = %v, want branch mismatch", err)
	}
}

func TestIntegrationAdapterContractSetupTaskWorktreeRequiresOriginRemote(t *testing.T) {
	repoPath := newGitRepo(t)
	paths := newStatePaths(t)

	_, err := orpheusgit.SetupTaskWorktree(context.Background(), orpheusgit.TaskWorktreeOptions{
		RepoID:        "alpha",
		RepoName:      "Alpha",
		RepoPath:      repoPath,
		DefaultBranch: "main",
		TaskID:        "op-4",
		Paths:         paths,
	})
	if err == nil {
		t.Fatal("setup succeeded, want missing origin error")
	}
	if !strings.Contains(err.Error(), "requires an origin remote") {
		t.Fatalf("error = %v, want missing origin", err)
	}
}

func TestIntegrationAdapterContractSetupTaskWorktreeRefusesWorktreeFromUnexpectedRepository(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	otherRepoPath := newGitRepoWithLocalOrigin(t)
	paths := newStatePaths(t)
	expectedPath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-5"))
	if err != nil {
		t.Fatalf("resolve expected path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(expectedPath), 0o755); err != nil {
		t.Fatalf("create worktree parent: %v", err)
	}
	runGit(t, otherRepoPath, "branch", "orpheus/op-5", "main")
	runGit(t, otherRepoPath, "worktree", "add", expectedPath, "orpheus/op-5")

	_, err = orpheusgit.SetupTaskWorktree(context.Background(), orpheusgit.TaskWorktreeOptions{
		RepoID:        "alpha",
		RepoName:      "Alpha",
		RepoPath:      repoPath,
		DefaultBranch: "main",
		TaskID:        "op-5",
		Paths:         paths,
	})
	if err == nil {
		t.Fatal("setup succeeded, want unexpected repo error")
	}
	if !strings.Contains(err.Error(), "points at Git common dir") {
		t.Fatalf("error = %v, want unexpected repo", err)
	}
}

func TestIntegrationAdapterContractSetupRepoRootSwitchesToDefaultBranchAndFastForwards(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	pushRemoteCommit(t, repoPath, "origin.txt", "from origin")
	runGit(t, repoPath, "checkout", "-b", "feature/local")

	got, err := orpheusgit.SetupRepoRoot(context.Background(), orpheusgit.RepoRootOptions{
		RepoID:        "alpha",
		RepoName:      "Alpha",
		RepoPath:      repoPath,
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("setup repo root: %v", err)
	}

	if got.Branch != "main" || got.WorktreePath != repoPath || got.Lifecycle != orpheusgit.TaskWorktreeLifecycleReused {
		t.Fatalf("setup result = %#v, want main/repo root/reused", got)
	}
	assertGitBranch(t, repoPath, "main")
	if _, err := os.Stat(filepath.Join(repoPath, "origin.txt")); err != nil {
		t.Fatalf("repo root was not fast-forwarded from origin: %v", err)
	}
}

func TestIntegrationAdapterContractSetupRepoRootTaskBranchSwitchesToTaskBranch(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	paths := newStatePaths(t)

	got, err := orpheusgit.SetupRepoRootTaskBranch(context.Background(), orpheusgit.TaskWorktreeOptions{
		RepoID:        "alpha",
		RepoName:      "Alpha",
		RepoPath:      repoPath,
		DefaultBranch: "main",
		TaskID:        "op-root",
		Paths:         paths,
	})
	if err != nil {
		t.Fatalf("setup repo-root task branch: %v", err)
	}

	if got.Branch != "orpheus/op-root" || got.WorktreePath != repoPath || got.Lifecycle != orpheusgit.TaskWorktreeLifecycleTaskBranchCreated {
		t.Fatalf("setup result = %#v, want task branch/repo root/task branch created", got)
	}
	assertGitBranch(t, repoPath, "orpheus/op-root")

	expectedWorktreePath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-root"))
	if err != nil {
		t.Fatalf("resolve deterministic worktree path: %v", err)
	}
	if _, err := os.Stat(expectedWorktreePath); !os.IsNotExist(err) {
		t.Fatalf("deterministic worktree stat err = %v, want not exist", err)
	}

	reused, err := orpheusgit.SetupRepoRootTaskBranch(context.Background(), orpheusgit.TaskWorktreeOptions{
		RepoID:        "alpha",
		RepoName:      "Alpha",
		RepoPath:      repoPath,
		DefaultBranch: "main",
		TaskID:        "op-root",
		Paths:         paths,
	})
	if err != nil {
		t.Fatalf("reuse repo-root task branch: %v", err)
	}
	if reused.Lifecycle != orpheusgit.TaskWorktreeLifecycleReused {
		t.Fatalf("reuse lifecycle = %q, want %q", reused.Lifecycle, orpheusgit.TaskWorktreeLifecycleReused)
	}
}

func TestIntegrationAdapterContractMaterializeRepoRootTaskBranchPreservesReviewedChanges(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	paths := newStatePaths(t)
	if err := os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644); err != nil {
		t.Fatalf("write reviewed change: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "staged.txt"), []byte("staged reviewed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repoPath, "add", "staged.txt")
	beforeStatus := runGit(t, repoPath, "status", "--porcelain=v1")
	beforeIndex := runGit(t, repoPath, "diff", "--cached", "--binary")

	got, err := orpheusgit.MaterializeRepoRootTaskBranch(context.Background(), orpheusgit.TaskWorktreeOptions{
		RepoID:        "alpha",
		RepoName:      "Alpha",
		RepoPath:      repoPath,
		DefaultBranch: "main",
		TaskID:        "op-reviewed",
		Paths:         paths,
		AllowDirty:    true,
	})
	if err != nil {
		t.Fatalf("materialize repo-root task branch: %v", err)
	}
	if got.Branch != "orpheus/op-reviewed" || got.WorktreePath != repoPath || got.Lifecycle != orpheusgit.TaskWorktreeLifecycleTaskBranchCreated {
		t.Fatalf("materialization = %#v, want task branch/repo root/task branch created", got)
	}
	assertGitBranch(t, repoPath, "orpheus/op-reviewed")
	assertFileContent(t, filepath.Join(repoPath, "reviewed.txt"), "reviewed\n")
	assertFileContent(t, filepath.Join(repoPath, "staged.txt"), "staged reviewed\n")
	if runGit(t, repoPath, "status", "--porcelain=v1") != beforeStatus || runGit(t, repoPath, "diff", "--cached", "--binary") != beforeIndex {
		t.Fatal("materialization changed reviewed working-tree or index contents")
	}
}

func TestIntegrationAdapterContractMaterializationRefusesDivergentTargetsWithoutChangingReviewedWork(t *testing.T) {
	for _, kind := range []string{"stale local", "ahead local", "current", "remote only"} {
		t.Run(kind, func(t *testing.T) {
			repo := newGitRepoWithLocalOrigin(t)
			const branch = "orpheus/op-target"
			switch kind {
			case "stale local":
				runGit(t, repo, "branch", branch, "main")
				pushRemoteCommit(t, repo, "default.txt", "new default\n")
				runGit(t, repo, "pull", "--ff-only", "origin", "main")
			default:
				runGit(t, repo, "checkout", "-b", branch)
				commitFile(t, repo, "stale.txt", "stale\n", "unreviewed task history")
				if kind == "remote only" {
					runGit(t, repo, "push", "--set-upstream", "origin", branch)
				}
				if kind != "current" {
					runGit(t, repo, "checkout", "main")
				}
				if kind == "remote only" {
					runGit(t, repo, "branch", "-D", branch)
				}
			}
			if err := os.WriteFile(filepath.Join(repo, "reviewed.txt"), []byte("reviewed\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			origin := strings.TrimSpace(runGit(t, repo, "remote", "get-url", "origin"))
			beforeRefs, beforeRemote := runGit(t, repo, "show-ref", "--heads"), runGit(t, origin, "show-ref", "--heads")
			beforeBranch, beforeStatus := runGit(t, repo, "branch", "--show-current"), runGit(t, repo, "status", "--porcelain=v1")

			_, err := orpheusgit.MaterializeRepoRootTaskBranch(context.Background(), orpheusgit.TaskWorktreeOptions{RepoID: "alpha", RepoName: "Alpha", RepoPath: repo, DefaultBranch: "main", TaskID: "op-target", Paths: newStatePaths(t), AllowDirty: true})

			if err == nil || !strings.Contains(err.Error(), "refusing to reuse divergent branch") {
				t.Fatalf("materialize = %v, want divergent target rejection", err)
			}
			scope := "local deterministic task branch"
			if kind == "remote only" {
				scope = "remote deterministic task branch"
			}
			if !strings.Contains(err.Error(), scope) {
				t.Fatalf("error = %v, want %s", err, scope)
			}
			if runGit(t, repo, "show-ref", "--heads") != beforeRefs || runGit(t, origin, "show-ref", "--heads") != beforeRemote || runGit(t, repo, "branch", "--show-current") != beforeBranch || runGit(t, repo, "status", "--porcelain=v1") != beforeStatus {
				t.Fatal("refusal changed branch, refs or reviewed state")
			}
			assertFileContent(t, filepath.Join(repo, "reviewed.txt"), "reviewed\n")
		})
	}
}

func TestIntegrationAdapterContractRepositoryRootSetupPreservesDirtyCheckoutAcrossModes(t *testing.T) {
	for _, taskBranch := range []bool{false, true} {
		t.Run(fmt.Sprintf("task branch=%t", taskBranch), func(t *testing.T) {
			repo := newGitRepoWithLocalOrigin(t)
			runGit(t, repo, "checkout", "-b", "feature/local")
			if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("reviewed dirty bytes\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			before := runGit(t, repo, "show-ref", "--heads")
			var err error

			if taskBranch {
				_, err = orpheusgit.SetupRepoRootTaskBranch(context.Background(), orpheusgit.TaskWorktreeOptions{RepoID: "alpha", RepoPath: repo, DefaultBranch: "main", TaskID: "op-dirty", Paths: newStatePaths(t)})
			} else {
				_, err = orpheusgit.SetupRepoRoot(context.Background(), orpheusgit.RepoRootOptions{RepoID: "alpha", RepoPath: repo, DefaultBranch: "main"})
			}

			if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
				t.Fatalf("setup error = %v, want dirty refusal", err)
			}
			assertGitBranch(t, repo, "feature/local")
			assertFileContent(t, filepath.Join(repo, "dirty.txt"), "reviewed dirty bytes\n")
			if runGit(t, repo, "show-ref", "--heads") != before {
				t.Fatal("dirty setup changed refs")
			}
		})
	}
}

func TestIntegrationAdapterContractSetupRepoRootRefusesDivergentDefaultBranch(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	pushRemoteCommit(t, repoPath, "origin.txt", "from origin")
	if err := os.WriteFile(filepath.Join(repoPath, "local.txt"), []byte("local"), 0o644); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	runGit(t, repoPath, "add", "local.txt")
	runGit(t, repoPath,
		"-c", "user.name=Orpheus Test",
		"-c", "user.email=orpheus@example.com",
		"commit", "-m", "local main commit",
	)

	_, err := orpheusgit.SetupRepoRoot(context.Background(), orpheusgit.RepoRootOptions{
		RepoID:        "alpha",
		RepoName:      "Alpha",
		RepoPath:      repoPath,
		DefaultBranch: "main",
	})
	if err == nil {
		t.Fatal("setup repo root succeeded, want divergent branch error")
	}
	if !strings.Contains(err.Error(), "fast-forward default branch") {
		t.Fatalf("error = %v, want fast-forward failure", err)
	}
	assertGitBranch(t, repoPath, "main")
}

func TestIntegrationAdapterContractSyncTaskBranchWithDefaultMergesAndPushesCleanDefaultChanges(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	worktreePath := addTaskBranchWorktree(t, repoPath, "orpheus/op-sync")
	commitFile(t, worktreePath, "task.txt", "task\n", "task work")
	runGit(t, worktreePath, "push", "--set-upstream", "origin", "orpheus/op-sync")
	pushRemoteCommit(t, repoPath, "default.txt", "default\n")

	result, err := orpheusgit.SyncTaskBranchWithDefault(context.Background(), orpheusgit.TaskBranchSyncOptions{
		RepoPath:      repoPath,
		DefaultBranch: "main",
		Branch:        "orpheus/op-sync",
		Worktree:      worktreePath,
		UpdatePolicy:  orpheusgit.TaskBranchUpdateAlways,
	})
	if err != nil {
		t.Fatalf("sync task branch: %v", err)
	}
	if result.Status != orpheusgit.TaskBranchSyncUpdated ||
		result.Branch != "orpheus/op-sync" ||
		result.DefaultBranch != "main" ||
		result.PreviousHead == "" ||
		result.Head == "" ||
		result.Head == result.PreviousHead {
		t.Fatalf("result = %#v, want updated branch with old/new heads", result)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "default.txt")); err != nil {
		t.Fatalf("default branch change was not merged into task branch: %v", err)
	}
	runGit(t, worktreePath, "merge-base", "--is-ancestor", "refs/remotes/origin/main", "HEAD")

	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	originTaskHead := strings.TrimSpace(runGit(t, originPath, "rev-parse", "refs/heads/orpheus/op-sync"))
	localTaskHead := strings.TrimSpace(runGit(t, worktreePath, "rev-parse", "HEAD"))
	if originTaskHead != localTaskHead {
		t.Fatalf("origin task branch head = %s, want local head %s", originTaskHead, localTaskHead)
	}
}

func TestIntegrationAdapterContractSyncTaskBranchWithDefaultConflictOnlyLeavesCleanBranchUnchanged(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	const branch = "orpheus/op-clean-batch"
	worktreePath := addTaskBranchWorktree(t, repoPath, branch)
	commitFile(t, worktreePath, "task.txt", "task\n", "task work")
	runGit(t, worktreePath, "push", "--set-upstream", "origin", branch)
	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	originTaskHeadBefore := strings.TrimSpace(runGit(t, originPath, "rev-parse", "refs/heads/"+branch))

	commitFile(t, worktreePath, "local.txt", "local only\n", "local task work")
	localHeadBefore := strings.TrimSpace(runGit(t, worktreePath, "rev-parse", "HEAD"))
	pushRemoteCommit(t, repoPath, "default.txt", "clean default change\n")

	result, err := orpheusgit.SyncTaskBranchWithDefault(context.Background(), orpheusgit.TaskBranchSyncOptions{
		RepoPath:      repoPath,
		DefaultBranch: "main",
		Branch:        branch,
		Worktree:      worktreePath,
		UpdatePolicy:  orpheusgit.TaskBranchUpdateConflictsOnly,
	})
	if err != nil {
		t.Fatalf("sync task branch: %v", err)
	}
	if result.Status != orpheusgit.TaskBranchSyncConflictFree || result.Head != localHeadBefore {
		t.Fatalf("result = %#v, want conflict-free branch at unchanged local head %s", result, localHeadBefore)
	}
	localHeadAfter := strings.TrimSpace(runGit(t, worktreePath, "rev-parse", "HEAD"))
	if localHeadAfter != localHeadBefore {
		t.Fatalf("local HEAD = %s, want unchanged %s", localHeadAfter, localHeadBefore)
	}
	originTaskHeadAfter := strings.TrimSpace(runGit(t, originPath, "rev-parse", "refs/heads/"+branch))
	if originTaskHeadAfter != originTaskHeadBefore {
		t.Fatalf("origin task branch = %s, want unchanged %s", originTaskHeadAfter, originTaskHeadBefore)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "default.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default branch change was merged into task branch, stat error = %v", err)
	}
}

func TestIntegrationAdapterContractSyncTaskBranchWithDefaultConflictOnlyRetainsRemoteFastForward(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	const branch = "orpheus/op-remote-ahead"
	worktreePath := addTaskBranchWorktree(t, repoPath, branch)
	commitFile(t, worktreePath, "task.txt", "task\n", "task work")
	runGit(t, worktreePath, "push", "--set-upstream", "origin", branch)
	remoteTaskHead := pushRemoteBranchCommit(t, repoPath, branch, "remote-task.txt", "remote task\n", "remote task work")
	pushRemoteCommit(t, repoPath, "default.txt", "clean default change\n")

	result, err := orpheusgit.SyncTaskBranchWithDefault(context.Background(), orpheusgit.TaskBranchSyncOptions{
		RepoPath:      repoPath,
		DefaultBranch: "main",
		Branch:        branch,
		Worktree:      worktreePath,
		UpdatePolicy:  orpheusgit.TaskBranchUpdateConflictsOnly,
	})
	if err != nil {
		t.Fatalf("sync task branch: %v", err)
	}
	if result.Status != orpheusgit.TaskBranchSyncConflictFree || result.Head != remoteTaskHead {
		t.Fatalf("result = %#v, want conflict-free branch fast-forwarded to %s", result, remoteTaskHead)
	}
	localHead := strings.TrimSpace(runGit(t, worktreePath, "rev-parse", "HEAD"))
	if localHead != remoteTaskHead {
		t.Fatalf("local HEAD = %s, want fetched remote task head %s", localHead, remoteTaskHead)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "remote-task.txt")); err != nil {
		t.Fatalf("remote task change was not fast-forwarded: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "default.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default branch change was merged into task branch, stat error = %v", err)
	}
}

func TestIntegrationAdapterContractSyncTaskBranchWithDefaultDetectsConflictWithoutPushing(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	worktreePath := addTaskBranchWorktree(t, repoPath, "orpheus/op-conflict")
	commitFile(t, worktreePath, "conflict.txt", "task\n", "task conflict")
	runGit(t, worktreePath, "push", "--set-upstream", "origin", "orpheus/op-conflict")
	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	originTaskHeadBefore := strings.TrimSpace(runGit(t, originPath, "rev-parse", "refs/heads/orpheus/op-conflict"))
	pushRemoteCommit(t, repoPath, "conflict.txt", "default\n")
	localHeadBefore := strings.TrimSpace(runGit(t, worktreePath, "rev-parse", "HEAD"))

	_, err := orpheusgit.SyncTaskBranchWithDefault(context.Background(), orpheusgit.TaskBranchSyncOptions{
		RepoPath:      repoPath,
		DefaultBranch: "main",
		Branch:        "orpheus/op-conflict",
		Worktree:      worktreePath,
		UpdatePolicy:  orpheusgit.TaskBranchUpdateConflictsOnly,
	})
	if err == nil || !strings.Contains(err.Error(), "would conflict") {
		t.Fatalf("error = %v, want conflict preflight", err)
	}

	localHeadAfter := strings.TrimSpace(runGit(t, worktreePath, "rev-parse", "HEAD"))
	if localHeadAfter != localHeadBefore {
		t.Fatalf("local HEAD = %s, want unchanged %s", localHeadAfter, localHeadBefore)
	}
	originTaskHeadAfter := strings.TrimSpace(runGit(t, originPath, "rev-parse", "refs/heads/orpheus/op-conflict"))
	if originTaskHeadAfter != originTaskHeadBefore {
		t.Fatalf("origin task branch = %s, want unchanged %s", originTaskHeadAfter, originTaskHeadBefore)
	}
	status := strings.TrimSpace(runGit(t, worktreePath, "status", "--porcelain=v1"))
	if status != "" {
		t.Fatalf("worktree status = %q, want clean after conflict preflight", status)
	}
	content, err := os.ReadFile(filepath.Join(worktreePath, "conflict.txt"))
	if err != nil {
		t.Fatalf("read conflict file: %v", err)
	}
	if string(content) != "task\n" {
		t.Fatalf("conflict file = %q, want task branch content preserved", string(content))
	}
}

func TestIntegrationAdapterContractTaskBranchConflictResolutionCompletesMergeAndPushes(t *testing.T) {
	fixture := newConflictResolutionFixture(t, "orpheus/op-resolve", nil, func(t *testing.T, repoPath string) {
		t.Helper()
		pushRemoteCommit(t, repoPath, "clean.txt", "clean merge\n")
	})
	repoPath := fixture.repoPath
	worktreePath := fixture.worktreePath
	result := fixture.result
	if result.Status != orpheusgit.TaskBranchSyncConflicted ||
		result.PreviousHead == "" ||
		len(result.ConflictFiles) != 1 ||
		result.ConflictFiles[0] != "conflict.txt" {
		t.Fatalf("result = %#v, want conflicted conflict.txt", result)
	}
	status := runGit(t, worktreePath, "status", "--porcelain=v1")
	if !strings.Contains(status, "conflict.txt") {
		t.Fatalf("status = %q, want unresolved conflict", status)
	}

	if err := os.WriteFile(filepath.Join(worktreePath, "conflict.txt"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatalf("write resolved conflict: %v", err)
	}
	runGit(t, worktreePath, "add", "conflict.txt")
	completed, err := orpheusgit.CompleteTaskBranchConflictResolution(
		context.Background(),
		orpheusgit.TaskBranchSyncOptions{
			RepoPath:      repoPath,
			DefaultBranch: "main",
			Branch:        "orpheus/op-resolve",
			Worktree:      worktreePath,
		},
		result.ConflictFiles,
	)
	if err != nil {
		t.Fatalf("complete conflict resolution: %v", err)
	}
	if completed.Status != orpheusgit.TaskBranchSyncUpdated || completed.Head == "" {
		t.Fatalf("completed = %#v, want updated head", completed)
	}
	runGit(t, worktreePath, "merge-base", "--is-ancestor", "refs/remotes/origin/main", "HEAD")

	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	originTaskHead := strings.TrimSpace(runGit(t, originPath, "rev-parse", "refs/heads/orpheus/op-resolve"))
	localTaskHead := strings.TrimSpace(runGit(t, worktreePath, "rev-parse", "HEAD"))
	if originTaskHead != localTaskHead {
		t.Fatalf("origin task branch head = %s, want local head %s", originTaskHead, localTaskHead)
	}
	if status := strings.TrimSpace(runGit(t, worktreePath, "status", "--porcelain=v1")); status != "" {
		t.Fatalf("status = %q, want clean after completion", status)
	}
	cleanContent, err := os.ReadFile(filepath.Join(worktreePath, "clean.txt"))
	if err != nil {
		t.Fatalf("read clean merge file: %v", err)
	}
	if string(cleanContent) != "clean merge\n" {
		t.Fatalf("clean merge file = %q, want default branch content", string(cleanContent))
	}
}

func TestIntegrationAdapterContractTaskBranchConflictResolutionCompletesMergeWithCleanDefaultRename(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	commitFile(t, repoPath, "old.txt", "base\n", "add old file")
	runGit(t, repoPath, "push", "origin", "main")

	branch := "orpheus/op-resolve-rename"
	worktreePath := addTaskBranchWorktree(t, repoPath, branch)
	commitFile(t, worktreePath, "conflict.txt", "task\n", "task conflict")
	runGit(t, worktreePath, "push", "--set-upstream", "origin", branch)
	pushRemoteRenameAndConflict(t, repoPath)

	result, err := orpheusgit.BeginTaskBranchConflictResolution(context.Background(), orpheusgit.TaskBranchSyncOptions{
		RepoPath:      repoPath,
		DefaultBranch: "main",
		Branch:        branch,
		Worktree:      worktreePath,
	})
	if err != nil {
		t.Fatalf("begin conflict resolution: %v", err)
	}
	if result.Status != orpheusgit.TaskBranchSyncConflicted ||
		len(result.ConflictFiles) != 1 ||
		result.ConflictFiles[0] != "conflict.txt" {
		t.Fatalf("result = %#v, want conflicted conflict.txt", result)
	}
	status := runGit(t, worktreePath, "status", "--porcelain=v1")
	if !strings.Contains(status, "R  old.txt -> new.txt") {
		t.Fatalf("status = %q, want clean default rename", status)
	}

	if err := os.WriteFile(filepath.Join(worktreePath, "conflict.txt"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatalf("write resolved conflict: %v", err)
	}
	runGit(t, worktreePath, "add", "conflict.txt")
	completed, err := orpheusgit.CompleteTaskBranchConflictResolution(
		context.Background(),
		orpheusgit.TaskBranchSyncOptions{
			RepoPath:      repoPath,
			DefaultBranch: "main",
			Branch:        branch,
			Worktree:      worktreePath,
		},
		result.ConflictFiles,
	)
	if err != nil {
		t.Fatalf("complete conflict resolution: %v", err)
	}
	if completed.Status != orpheusgit.TaskBranchSyncUpdated || completed.Head == "" {
		t.Fatalf("completed = %#v, want updated head", completed)
	}
	if _, err := os.Stat(filepath.Join(worktreePath, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old.txt stat error = %v, want file removed by rename", err)
	}
	newContent, err := os.ReadFile(filepath.Join(worktreePath, "new.txt"))
	if err != nil {
		t.Fatalf("read renamed file: %v", err)
	}
	if string(newContent) != "base\n" {
		t.Fatalf("new.txt = %q, want renamed default content", string(newContent))
	}
}

type conflictResolutionFixture struct {
	repoPath     string
	worktreePath string
	result       orpheusgit.TaskBranchSyncResult
}

func newConflictResolutionFixture(
	t *testing.T,
	branch string,
	beforePush func(t *testing.T, worktreePath string),
	afterDefaultConflict func(t *testing.T, repoPath string),
) conflictResolutionFixture {
	t.Helper()

	repoPath := newGitRepoWithLocalOrigin(t)
	worktreePath := addTaskBranchWorktree(t, repoPath, branch)
	commitFile(t, worktreePath, "conflict.txt", "task\n", "task conflict")
	if beforePush != nil {
		beforePush(t, worktreePath)
	}
	runGit(t, worktreePath, "push", "--set-upstream", "origin", branch)
	pushRemoteCommit(t, repoPath, "conflict.txt", "default\n")
	if afterDefaultConflict != nil {
		afterDefaultConflict(t, repoPath)
	}

	result, err := orpheusgit.BeginTaskBranchConflictResolution(context.Background(), orpheusgit.TaskBranchSyncOptions{
		RepoPath:      repoPath,
		DefaultBranch: "main",
		Branch:        branch,
		Worktree:      worktreePath,
	})
	if err != nil {
		t.Fatalf("begin conflict resolution: %v", err)
	}
	return conflictResolutionFixture{
		repoPath:     repoPath,
		worktreePath: worktreePath,
		result:       result,
	}
}

func newStatePaths(t *testing.T) state.Paths {
	t.Helper()

	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("create state paths: %v", err)
	}
	return paths
}

func newGitRepoWithLocalOrigin(t *testing.T) string {
	t.Helper()

	root := testutil.CanonicalTempDir(t)
	originPath := filepath.Join(root, "origin.git")
	if err := os.MkdirAll(originPath, 0o755); err != nil {
		t.Fatalf("create origin: %v", err)
	}
	runGit(t, originPath, "init", "--bare")
	runGit(t, originPath, "symbolic-ref", "HEAD", "refs/heads/main")

	repoPath := filepath.Join(root, "repo")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("create repo: %v", err)
	}
	runGit(t, repoPath, "init")
	runGit(t, repoPath, "config", "user.name", "Orpheus Test")
	runGit(t, repoPath, "config", "user.email", "orpheus@example.com")
	runGit(t, repoPath, "checkout", "-b", "main")
	runGit(t, repoPath, "commit", "--allow-empty", "-m", "initial")
	runGit(t, repoPath, "remote", "add", "origin", originPath)
	runGit(t, repoPath, "push", "--set-upstream", "origin", "main")
	return repoPath
}

func addTaskBranchWorktree(t *testing.T, repoPath string, branch string) string {
	t.Helper()

	worktreePath := filepath.Join(testutil.CanonicalTempDir(t), "task-worktree")
	runGit(t, repoPath, "branch", branch, "main")
	runGit(t, repoPath, "worktree", "add", worktreePath, branch)
	return worktreePath
}

func commitFile(t *testing.T, dir string, name string, content string, message string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir,
		"-c", "user.name=Orpheus Test",
		"-c", "user.email=orpheus@example.com",
		"commit", "-m", message,
	)
}

func pushRemoteCommit(t *testing.T, repoPath string, name string, content string) {
	t.Helper()

	pushRemoteBranchCommit(t, repoPath, "main", name, content, "remote commit")
}

func pushRemoteRenameAndConflict(t *testing.T, repoPath string) {
	t.Helper()

	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	cloneParent := testutil.CanonicalTempDir(t)
	clonePath := filepath.Join(cloneParent, "origin-work")
	runGit(t, cloneParent, "clone", originPath, clonePath)
	runGit(t, clonePath, "checkout", "main")
	runGit(t, clonePath, "mv", "old.txt", "new.txt")
	if err := os.WriteFile(filepath.Join(clonePath, "conflict.txt"), []byte("default\n"), 0o644); err != nil {
		t.Fatalf("write remote conflict file: %v", err)
	}
	runGit(t, clonePath, "add", "conflict.txt")
	runGit(t, clonePath,
		"-c", "user.name=Orpheus Test",
		"-c", "user.email=orpheus@example.com",
		"commit", "-m", "rename file and add conflict",
	)
	runGit(t, clonePath, "push", "origin", "main")
}

func pushRemoteBranchCommit(
	t *testing.T,
	repoPath string,
	branch string,
	name string,
	content string,
	message string,
) string {
	t.Helper()

	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	cloneParent := testutil.CanonicalTempDir(t)
	clonePath := filepath.Join(cloneParent, "origin-work")
	runGit(t, cloneParent, "clone", originPath, clonePath)
	runGit(t, clonePath, "checkout", branch)
	if err := os.WriteFile(filepath.Join(clonePath, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write remote commit file: %v", err)
	}
	runGit(t, clonePath, "add", name)
	runGit(t, clonePath,
		"-c", "user.name=Orpheus Test",
		"-c", "user.email=orpheus@example.com",
		"commit", "-m", message,
	)
	runGit(t, clonePath, "push", "origin", branch)
	return strings.TrimSpace(runGit(t, clonePath, "rev-parse", "HEAD"))
}

func assertGitBranch(t *testing.T, worktreePath string, expected string) {
	t.Helper()

	branch := strings.TrimSpace(runGit(t, worktreePath, "symbolic-ref", "--quiet", "--short", "HEAD"))
	if branch != expected {
		t.Fatalf("branch at %q = %q, want %q", worktreePath, branch, expected)
	}
	root := strings.TrimSpace(runGit(t, worktreePath, "rev-parse", "--show-toplevel"))
	if root != worktreePath {
		t.Fatalf("worktree root = %q, want %q", root, worktreePath)
	}
}
