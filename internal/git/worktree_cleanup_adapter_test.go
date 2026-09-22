//go:build integration

package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAdapterContractClosedTaskWorktreeCleanupLetsGitProtectChanges(t *testing.T) {
	repo := newGitRepoWithLocalOrigin(t)
	commitFile(t, repo, ".gitignore", "/orpheus\n/artifacts/test-coverage/\n", "Ignore build outputs")
	commitFile(t, repo, "tracked.txt", "original\n", "Add tracked file")
	runGit(t, repo, "push", "origin", "main")
	paths := newStatePaths(t)

	for _, kind := range []string{"clean", "ignored", "tracked", "staged", "deleted", "untracked", "last-minute"} {
		t.Run(kind, func(t *testing.T) {
			opts := gitmeta.ClosedTaskWorktreeOptions{RepoID: "alpha", RepoPath: repo, DefaultBranch: "main", TaskID: "op-" + kind, Paths: paths}
			setup, err := gitmeta.SetupTaskWorktree(context.Background(), opts)
			require.NoError(t, err)
			dir := setup.WorktreePath
			tracked := filepath.Join(dir, "tracked.txt")
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "artifacts/test-coverage"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "artifacts/test-coverage/report.json"), []byte("coverage"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "orpheus"), []byte("binary"), 0o755))
			if kind == "clean" {
				require.NoError(t, os.RemoveAll(filepath.Join(dir, "artifacts")))
				require.NoError(t, os.Remove(filepath.Join(dir, "orpheus")))
			}
			switch kind {
			case "tracked", "staged":
				require.NoError(t, os.WriteFile(tracked, []byte("edited\n"), 0o644))
				if kind == "staged" {
					runGit(t, dir, "add", "tracked.txt")
				}
			case "deleted":
				require.NoError(t, os.Remove(tracked))
			case "untracked":
				require.NoError(t, os.WriteFile(filepath.Join(dir, "untracked.txt"), []byte("keep me"), 0o644))
			}
			runner := &cleanupGitRunner{t: t}
			if kind == "last-minute" {
				runner.beforeRemove = func() {
					require.NoError(t, os.WriteFile(tracked, []byte("edited\n"), 0o644))
				}
			}
			ctx := gitmeta.ContextWithRunner(context.Background(), runner)

			inspection := gitmeta.InspectClosedTaskWorktree(ctx, opts)

			assert.Equal(t, gitmeta.ClosedTaskWorktreeEligible, inspection.Outcome)
			assert.Zero(t, runner.removes)
			before := runGit(t, dir, "status", "--porcelain=v1", "--ignored=matching")
			removal := gitmeta.RemoveClosedTaskWorktree(ctx, opts)
			assert.Equal(t, 1, runner.removes)
			if kind == "clean" || kind == "ignored" {
				assert.Equal(t, gitmeta.ClosedTaskWorktreeRemoved, removal.Outcome, removal.Reason)
				_, err := os.Stat(dir)
				assert.ErrorIs(t, err, os.ErrNotExist)
				assert.Equal(t, gitmeta.ClosedTaskWorktreeAbsent, gitmeta.RemoveClosedTaskWorktree(ctx, opts).Outcome)
				assert.Equal(t, 1, runner.removes)
				runGit(t, repo, "show-ref", "--verify", "refs/heads/"+setup.Branch)
				return
			}
			assert.Equal(t, gitmeta.ClosedTaskWorktreeFailed, removal.Outcome)
			assert.Contains(t, removal.Reason, "contains modified or untracked files")
			if kind != "last-minute" {
				assert.Equal(t, before, runGit(t, dir, "status", "--porcelain=v1", "--ignored=matching"))
			}
			assertFileContent(t, filepath.Join(dir, "orpheus"), "binary")
			assertFileContent(t, filepath.Join(dir, "artifacts/test-coverage/report.json"), "coverage")
			switch kind {
			case "tracked", "staged", "last-minute":
				assertFileContent(t, tracked, "edited\n")
			case "deleted":
				_, err := os.Stat(tracked)
				assert.ErrorIs(t, err, os.ErrNotExist)
			case "untracked":
				assertFileContent(t, filepath.Join(dir, "untracked.txt"), "keep me")
			}
		})
	}
}

func TestIntegrationAdapterContractClosedTaskWorktreeCleanupRejectsUnsafeIdentityBeforeRemoval(t *testing.T) {
	repo := newGitRepoWithLocalOrigin(t)
	paths := newStatePaths(t)
	for _, kind := range []string{"locked", "branch", "foreign"} {
		t.Run(kind, func(t *testing.T) {
			opts := gitmeta.ClosedTaskWorktreeOptions{RepoID: "alpha", RepoPath: repo, DefaultBranch: "main", TaskID: "op-" + kind, Paths: paths}
			owner := opts
			if kind == "foreign" {
				owner.RepoPath = newGitRepoWithLocalOrigin(t)
			}
			setup, err := gitmeta.SetupTaskWorktree(context.Background(), owner)
			require.NoError(t, err)
			switch kind {
			case "locked":
				runGit(t, repo, "worktree", "lock", "--reason", "operator repair", setup.WorktreePath)
			case "branch":
				runGit(t, setup.WorktreePath, "checkout", "-b", "other")
			}
			runner := &cleanupGitRunner{t: t}

			result := gitmeta.RemoveClosedTaskWorktree(gitmeta.ContextWithRunner(context.Background(), runner), opts)

			assert.Equal(t, gitmeta.ClosedTaskWorktreeUnsafe, result.Outcome, result.Reason)
			assert.Zero(t, runner.removes)
			_, err = os.Stat(setup.WorktreePath)
			assert.NoError(t, err)
		})
	}
}

// Only identity queries and one exact non-forced removal are allowed. This
// proves cleanup never runs a status preflight or a content-changing command.
type cleanupGitRunner struct {
	t            *testing.T
	removes      int
	beforeRemove func()
}

func (r *cleanupGitRunner) Run(ctx context.Context, command gitmeta.Command) (gitmeta.CommandResult, error) {
	r.t.Helper()
	switch command.Args[0] {
	case "rev-parse", "symbolic-ref":
	case "worktree":
		if command.Args[1] == "remove" {
			require.Equal(r.t, []string{"worktree", "remove", command.Directory}, command.Args)
			r.removes++
			if r.beforeRemove != nil {
				r.beforeRemove()
			}
		} else {
			require.Equal(r.t, []string{"worktree", "list", "--porcelain", "-z"}, command.Args)
		}
	default:
		r.t.Fatalf("unexpected cleanup command: %v", command.Args)
	}
	process := exec.CommandContext(ctx, "git", command.Args...)
	process.Dir = command.Directory
	process.Stdin = strings.NewReader(command.Input)
	output, err := process.CombinedOutput()
	return gitmeta.CommandResult{Stdout: string(output)}, err
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, want, string(data))
}
