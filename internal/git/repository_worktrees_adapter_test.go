//go:build integration

package git_test

import (
	"os"
	"path/filepath"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAdapterContractRepositoryWorktreeDiscoveryIncludesOrphansAndAbsentRegistrations(t *testing.T) {
	repo := newGitRepoWithLocalOrigin(t)
	paths := newStatePaths(t)
	root, err := paths.DataPath("repos/alpha/worktrees")
	require.NoError(t, err)
	git := gitmeta.LocalClosedTaskWorktreeGit{}
	opts := gitmeta.TaskWorktreeOptions{RepoID: "alpha", RepoPath: repo, DefaultBranch: "main", Paths: paths}
	worktrees, err := git.DiscoverRepositoryWorktrees(t.Context(), opts)
	require.NoError(t, err)
	assert.Empty(t, worktrees, "a repository without a worktree directory has nothing to remove")
	orphan, missing := filepath.Join(root, "a-orphan"), filepath.Join(root, "a-missing")
	unrelated := filepath.Join(testutil.CanonicalTempDir(t), "unrelated")
	runGit(t, repo, "worktree", "add", "-b", "custom/orphan", orphan, "main")
	runGit(t, repo, "worktree", "add", "-b", "custom/missing", missing, "main")
	runGit(t, repo, "worktree", "add", "-b", "unrelated", unrelated, "main")
	require.NoError(t, os.RemoveAll(missing))
	worktrees, err = git.DiscoverRepositoryWorktrees(t.Context(), opts)

	require.NoError(t, err)
	require.Len(t, worktrees, 2)
	assert.Equal(t, "a-missing", worktrees[0].TaskID)
	assert.Equal(t, "custom/missing", worktrees[0].Branch)
	assert.Equal(t, "a-orphan", worktrees[1].TaskID)
	assert.Equal(t, "custom/orphan", worktrees[1].Branch)
	// Ownership inspection and non-forced content protection are covered by
	// the existing closed-task cleanup adapter contracts.
	assert.Equal(t, gitmeta.ClosedTaskWorktreeRemoved, git.RemoveClosedTaskWorktree(t.Context(), worktrees[1]).Outcome)
	worktrees, err = git.DiscoverRepositoryWorktrees(t.Context(), opts)
	require.NoError(t, err)
	require.Len(t, worktrees, 1, "a successfully removed orphan stays absent on retry")
	assert.Equal(t, "a-missing", worktrees[0].TaskID)
	assert.DirExists(t, unrelated)
	assert.DirExists(t, repo)
	runGit(t, repo, "show-ref", "--verify", "refs/heads/custom/orphan")
}

func TestIntegrationAdapterContractRepositoryWorktreeDiscoveryRefusesAmbiguousOwnedPaths(t *testing.T) {
	repo := newGitRepoWithLocalOrigin(t)
	for _, kind := range []string{"symlinked worktree root", "unexpected registered layout", "unregistered directory", "foreign worktree", "whitespace-padded dirty worktree", "whitespace-padded unregistered directory"} {
		t.Run(kind, func(t *testing.T) {
			paths := newStatePaths(t)
			root, err := paths.DataPath("repos/alpha/worktrees")
			require.NoError(t, err)
			path := filepath.Join(root, "a-unsafe")
			require.NoError(t, os.MkdirAll(filepath.Dir(root), 0o755))
			switch kind {
			case "symlinked worktree root":
				require.NoError(t, os.Symlink(testutil.CanonicalTempDir(t), root))
			case "unexpected registered layout":
				path = filepath.Join(filepath.Dir(root), "elsewhere")
				runGit(t, repo, "worktree", "add", "-b", "unexpected-layout", path, "main")
			case "unregistered directory", "whitespace-padded unregistered directory":
				if kind == "whitespace-padded unregistered directory" {
					path = filepath.Join(root, " a-unregistered ")
				}
				require.NoError(t, os.MkdirAll(path, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(path, "keep"), []byte("operator data"), 0o644))
			case "foreign worktree":
				foreign := newGitRepoWithLocalOrigin(t)
				runGit(t, foreign, "worktree", "add", "-b", "foreign", path, "main")
			case "whitespace-padded dirty worktree":
				path = filepath.Join(root, " a-dirty ")
				runGit(t, repo, "worktree", "add", "-b", "review-dirty", path, "main")
				require.NoError(t, os.WriteFile(filepath.Join(path, "keep"), []byte("operator data"), 0o644))
			}
			registrations := runGit(t, repo, "worktree", "list", "--porcelain", "-z")
			git := gitmeta.LocalClosedTaskWorktreeGit{}
			worktrees, err := git.DiscoverRepositoryWorktrees(t.Context(), gitmeta.TaskWorktreeOptions{RepoID: "alpha", RepoPath: repo, DefaultBranch: "main", Paths: paths})
			if kind == "unregistered directory" || kind == "foreign worktree" {
				require.NoError(t, err)
				require.Len(t, worktrees, 1)
				inspection := git.InspectClosedTaskWorktree(t.Context(), worktrees[0])
				assert.Equal(t, gitmeta.ClosedTaskWorktreeUnsafe, inspection.Outcome)
			} else {
				require.Error(t, err)
				assert.Nil(t, worktrees)
			}
			if kind == "symlinked worktree root" {
				info, statErr := os.Lstat(root)
				require.NoError(t, statErr)
				assert.NotZero(t, info.Mode()&os.ModeSymlink)
			} else {
				assert.DirExists(t, filepath.Dir(path))
			}
			if kind == "unregistered directory" || kind == "whitespace-padded unregistered directory" || kind == "whitespace-padded dirty worktree" {
				data, err := os.ReadFile(filepath.Join(path, "keep"))
				require.NoError(t, err)
				assert.Equal(t, "operator data", string(data))
			}
			if kind == "whitespace-padded unregistered directory" || kind == "whitespace-padded dirty worktree" {
				assert.ErrorContains(t, err, path)
				assert.ErrorContains(t, err, "before retrying removal")
				assert.Equal(t, registrations, runGit(t, repo, "worktree", "list", "--porcelain", "-z"))
				if kind == "whitespace-padded dirty worktree" {
					assert.Contains(t, registrations, "worktree "+path+"\x00")
					assert.Equal(t, "?? keep\n", runGit(t, path, "status", "--porcelain"))
				}
			}
		})
	}
}
