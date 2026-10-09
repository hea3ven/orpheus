//go:build integration

package git_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	orpheusgit "github.com/hea3ven/orpheus/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAdapterContractRepoRootContinuationPreservesCheckout(t *testing.T) {
	for _, branch := range []string{"main", "orpheus/op-continue"} {
		for _, checkout := range []string{"matching", "other branch", "detached"} {
			t.Run(branch+"/"+checkout, func(t *testing.T) {
				repo := newGitRepoWithLocalOrigin(t)
				paths := newStatePaths(t)
				commitFile(t, repo, "tracked.txt", "original\n", "original content")
				if branch != "main" {
					runGit(t, repo, "checkout", "-b", branch)
				}
				switch checkout {
				case "other branch":
					runGit(t, repo, "checkout", "-b", "unrelated")
				case "detached":
					runGit(t, repo, "checkout", "--detach")
				}
				require.NoError(t, os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("staged\n"), 0o644))
				runGit(t, repo, "add", "tracked.txt")
				require.NoError(t, os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("unstaged\n"), 0o644))
				require.NoError(t, os.WriteFile(filepath.Join(repo, "untracked.bin"), []byte{0, 1, 2, 255}, 0o644))
				// Continuation must work offline and must not even attempt to fetch.
				runGit(t, repo, "remote", "set-url", "origin", filepath.Join(repo, "missing-origin.git"))
				before := captureRepoRootCheckout(t, repo)

				var result orpheusgit.TaskWorktreeSetupResult
				var err error
				if branch == "main" {
					result, err = orpheusgit.SetupRepoRoot(context.Background(), orpheusgit.RepoRootOptions{
						RepoID: "alpha", RepoPath: repo, DefaultBranch: "main", AllowDirty: true,
					})
				} else {
					result, err = orpheusgit.SetupRepoRootTaskBranch(context.Background(), orpheusgit.TaskWorktreeOptions{
						RepoID: "alpha", RepoPath: repo, DefaultBranch: "main", TaskID: "op-continue", Paths: paths, AllowDirty: true,
					})
				}

				if checkout == "matching" {
					require.NoError(t, err)
					assert.Equal(t, orpheusgit.TaskWorktreeSetupResult{
						Branch: branch, WorktreePath: repo, Lifecycle: orpheusgit.TaskWorktreeLifecycleReused,
					}, result)
				} else {
					require.Error(t, err, "continuation must not switch to its target branch")
				}
				assert.Equal(t, before, captureRepoRootCheckout(t, repo))
				assert.Equal(t, "staged\n", runGit(t, repo, "show", ":tracked.txt"))
				content, readErr := os.ReadFile(filepath.Join(repo, "tracked.txt"))
				require.NoError(t, readErr)
				assert.Equal(t, "unstaged\n", string(content))
				untracked, readErr := os.ReadFile(filepath.Join(repo, "untracked.bin"))
				require.NoError(t, readErr)
				assert.Equal(t, []byte{0, 1, 2, 255}, untracked)
			})
		}
	}
}

func captureRepoRootCheckout(t *testing.T, repo string) map[string]string {
	t.Helper()
	return map[string]string{
		"head":     runGit(t, repo, "rev-parse", "HEAD"),
		"branch":   runGit(t, repo, "branch", "--show-current"),
		"status":   runGit(t, repo, "status", "--porcelain=v1"),
		"staged":   runGit(t, repo, "diff", "--cached", "--binary"),
		"unstaged": runGit(t, repo, "diff", "--binary"),
		"refs":     runGit(t, repo, "show-ref"),
		"reflog":   runGit(t, repo, "reflog", "--all"),
		"stashes":  runGit(t, repo, "stash", "list"),
	}
}
