package git_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	orpheusgit "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
)

func TestSetupTaskWorktreeCreatesAndReusesDeterministicWorktree(t *testing.T) {
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

func TestSetupTaskWorktreeRecreatesMissingWorktreeForExistingBranch(t *testing.T) {
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

func TestSetupTaskWorktreeRefusesExistingPathOnDifferentBranch(t *testing.T) {
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

func TestSetupTaskWorktreeRequiresOriginRemote(t *testing.T) {
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

func TestSetupTaskWorktreeRefusesWorktreeFromUnexpectedRepository(t *testing.T) {
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

func TestSetupRepoRootSwitchesToDefaultBranchAndFastForwards(t *testing.T) {
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

func TestSetupRepoRootTaskBranchSwitchesToTaskBranch(t *testing.T) {
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

	if got.Branch != "orpheus/op-root" || got.WorktreePath != repoPath || got.Lifecycle != orpheusgit.TaskWorktreeLifecycleReused {
		t.Fatalf("setup result = %#v, want task branch/repo root/reused", got)
	}
	assertGitBranch(t, repoPath, "orpheus/op-root")

	expectedWorktreePath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-root"))
	if err != nil {
		t.Fatalf("resolve deterministic worktree path: %v", err)
	}
	if _, err := os.Stat(expectedWorktreePath); !os.IsNotExist(err) {
		t.Fatalf("deterministic worktree stat err = %v, want not exist", err)
	}
}

func TestSetupRepoRootTaskBranchRefusesDirtyRepoBeforeSwitching(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	paths := newStatePaths(t)
	if err := os.WriteFile(filepath.Join(repoPath, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	_, err := orpheusgit.SetupRepoRootTaskBranch(context.Background(), orpheusgit.TaskWorktreeOptions{
		RepoID:        "alpha",
		RepoName:      "Alpha",
		RepoPath:      repoPath,
		DefaultBranch: "main",
		TaskID:        "op-dirty",
		Paths:         paths,
	})
	if err == nil {
		t.Fatal("setup repo-root task branch succeeded, want dirty checkout error")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("error = %v, want uncommitted changes", err)
	}
	assertGitBranch(t, repoPath, "main")
}

func TestSetupRepoRootRefusesDirtyRepoBeforeSwitching(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	runGit(t, repoPath, "checkout", "-b", "feature/local")
	if err := os.WriteFile(filepath.Join(repoPath, "dirty.txt"), []byte("dirty"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	_, err := orpheusgit.SetupRepoRoot(context.Background(), orpheusgit.RepoRootOptions{
		RepoID:        "alpha",
		RepoName:      "Alpha",
		RepoPath:      repoPath,
		DefaultBranch: "main",
	})
	if err == nil {
		t.Fatal("setup repo root succeeded, want dirty checkout error")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("error = %v, want uncommitted changes", err)
	}
	assertGitBranch(t, repoPath, "feature/local")
}

func TestSetupRepoRootRefusesDivergentDefaultBranch(t *testing.T) {
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

func newStatePaths(t *testing.T) state.Paths {
	t.Helper()

	root := t.TempDir()
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("create state paths: %v", err)
	}
	return paths
}

func newGitRepoWithLocalOrigin(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
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
	runGit(t, repoPath, "checkout", "-b", "main")
	runGit(t, repoPath,
		"-c", "user.name=Orpheus Test",
		"-c", "user.email=orpheus@example.com",
		"commit", "--allow-empty", "-m", "initial",
	)
	runGit(t, repoPath, "remote", "add", "origin", originPath)
	runGit(t, repoPath, "push", "--set-upstream", "origin", "main")
	return repoPath
}

func pushRemoteCommit(t *testing.T, repoPath string, name string, content string) {
	t.Helper()

	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	cloneParent := t.TempDir()
	clonePath := filepath.Join(cloneParent, "origin-work")
	runGit(t, cloneParent, "clone", originPath, clonePath)
	if err := os.WriteFile(filepath.Join(clonePath, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write remote commit file: %v", err)
	}
	runGit(t, clonePath, "add", name)
	runGit(t, clonePath,
		"-c", "user.name=Orpheus Test",
		"-c", "user.email=orpheus@example.com",
		"commit", "-m", "remote commit",
	)
	runGit(t, clonePath, "push", "origin", "main")
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
