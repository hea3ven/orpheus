//go:build integration

package git_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func captureResetCheckout(t *testing.T, dir string) map[string]string {
	t.Helper()
	checkout := captureRepoRootCheckout(t, dir)
	// Fetch and owned target retention are allowed before checkout mutation.
	delete(checkout, "refs")
	delete(checkout, "reflog")
	return checkout
}

func TestIntegrationAdapterContractWorkflowResetCleansExistingCheckoutAndRetainsRecordedOutput(t *testing.T) {
	for _, repoRoot := range []bool{false, true} {
		t.Run(map[bool]string{false: "worktree", true: "repository root"}[repoRoot], func(t *testing.T) {
			ctx := context.Background()
			repo := newGitRepoWithLocalOrigin(t)
			commitFile(t, repo, ".gitignore", "ignored/\n", "ignore local cache")
			commitFile(t, repo, ".gitattributes", "configured.txt text eol=crlf\n", "configure normalization")
			commitFile(t, repo, "configured.txt", "baseline\n", "baseline content")
			runGit(t, repo, "push", "origin", "main")
			opts := gitmeta.TaskWorktreeOptions{RepoID: "alpha", RepoPath: repo, DefaultBranch: "main", TaskID: "op-reset", Paths: newStatePaths(t)}
			var setup gitmeta.TaskWorktreeSetupResult
			var err error
			if repoRoot {
				setup, err = gitmeta.SetupRepoRootTaskBranch(ctx, opts)
			} else {
				setup, err = gitmeta.SetupTaskWorktree(ctx, opts)
			}
			require.NoError(t, err)
			dir := setup.WorktreePath
			commitFile(t, dir, "recorded.txt", "recorded output\n", "task public output")
			previous := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
			runGit(t, dir, "update-ref", "refs/orpheus/revisions/retained", previous)
			// An already-released private ref must not be reconstructed by reset.
			runGit(t, dir, "update-ref", "refs/orpheus/revisions/released", previous)
			runGit(t, dir, "update-ref", "-d", "refs/orpheus/revisions/released")
			write := func(path, contents string) {
				require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, path), []byte(contents), 0o644))
			}
			write("recorded.txt", "staged changes\n")
			runGit(t, dir, "add", "recorded.txt")
			write("recorded.txt", "unstaged changes\n")
			write("untracked/nested.txt", "discard me\n")
			write("ignored/keep.txt", "keep operator cache\n")
			// Reset discards local ignore-rule edits too; baseline rules govern clean.
			write(".gitignore", "ignored/\nscratch*.cache\n")
			write("scratch[1].cache", "discard under baseline ignore rules\n")
			before := captureResetCheckout(t, dir)
			req := gitmeta.WorkflowResetRequest{RepoPath: repo, Directory: dir, Branch: setup.Branch, DefaultBranch: "main", Ref: "refs/orpheus/baselines/reset/1", RetainCommits: []string{previous}}
			changes, err := gitmeta.InspectWorkflowReset(ctx, req)
			require.NoError(t, err)
			assert.Contains(t, changes, "MM recorded.txt")
			assert.Contains(t, changes, "untracked/")
			assert.NotContains(t, changes, "ignored/")
			assert.Equal(t, before, captureResetCheckout(t, dir))

			origin := strings.TrimSpace(runGit(t, repo, "remote", "get-url", "origin"))
			runGit(t, repo, "remote", "set-url", "origin", filepath.Join(repo, "absent-origin.git"))
			_, err = gitmeta.ResolveWorkflowReset(ctx, req)
			require.ErrorContains(t, err, "fetch origin/main")
			assert.Equal(t, before, captureResetCheckout(t, dir))
			runGit(t, repo, "remote", "set-url", "origin", origin)
			target := pushRemoteBranchCommit(t, repo, "main", "configured.txt", "later baseline\n", "advance after scheduling")
			resolved, err := gitmeta.ResolveWorkflowReset(ctx, req)
			require.NoError(t, err)
			req.Target = resolved
			assert.Equal(t, target, req.Target.Commit)
			assert.Equal(t, before, captureResetCheckout(t, dir), "fetch and target retention do not change checkout")

			wrong := req
			wrong.Branch = "main"
			require.ErrorContains(t, gitmeta.ApplyWorkflowReset(ctx, wrong), "expected")
			assert.Equal(t, before, captureResetCheckout(t, dir))
			// Ordinary Git conversion and mode configuration remain authoritative.
			runGit(t, repo, "config", "core.fileMode", "false")
			require.NoError(t, gitmeta.ApplyWorkflowReset(ctx, req))
			assert.Equal(t, target, strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD")))
			assert.Empty(t, strings.TrimSpace(runGit(t, dir, "status", "--porcelain")))
			assert.NoFileExists(t, filepath.Join(dir, "recorded.txt"))
			assert.NoDirExists(t, filepath.Join(dir, "untracked"))
			assert.NoFileExists(t, filepath.Join(dir, "scratch[1].cache"))
			assert.Equal(t, "later baseline\r\n", readRevisionTestFile(t, dir, "configured.txt"))
			assert.Equal(t, "keep operator cache\n", readRevisionTestFile(t, dir, "ignored/keep.txt"))
			require.NoError(t, os.Chmod(filepath.Join(dir, "configured.txt"), 0o755))
			require.NoError(t, gitmeta.CheckCheckout(ctx, dir, gitmeta.CheckoutIdentity{Commit: target, Ref: "refs/heads/" + setup.Branch}))
			assert.Equal(t, previous, strings.TrimSpace(runGit(t, repo, "rev-parse", "refs/orpheus/revisions/retained")))
			assert.Equal(t, previous, strings.TrimSpace(runGit(t, repo, "rev-parse", req.Ref+"-history/"+previous)))
			refs := runGit(t, repo, "for-each-ref", "--format=%(refname)", "refs/orpheus/revisions/released")
			assert.Empty(t, strings.TrimSpace(refs))
			assert.Equal(t, "recorded output\n", runGit(t, repo, "show", previous+":recorded.txt"))
			assert.Equal(t, target, strings.TrimSpace(runGit(t, repo, "ls-remote", "origin", "refs/heads/main"))[:40])
			assert.Empty(t, strings.TrimSpace(runGit(t, repo, "ls-remote", "origin", "refs/heads/"+setup.Branch)), "reset never publishes the task branch")
		})
	}
}

func TestIntegrationAdapterContractWorkflowResetRestoresBaselineOverIgnoredObstructions(t *testing.T) {
	for _, repoRoot := range []bool{false, true} {
		t.Run(map[bool]string{false: "worktree", true: "repository root"}[repoRoot], func(t *testing.T) {
			ctx := context.Background()
			repo := newGitRepoWithLocalOrigin(t)
			commitFile(t, repo, ".gitignore", "cache/\nunrelated.cache\n", "ignore local caches")
			runGit(t, repo, "push", "origin", "main")
			opts := gitmeta.TaskWorktreeOptions{RepoID: "alpha", RepoPath: repo, DefaultBranch: "main", TaskID: "op-reset", Paths: newStatePaths(t)}
			var setup gitmeta.TaskWorktreeSetupResult
			var err error
			if repoRoot {
				setup, err = gitmeta.SetupRepoRootTaskBranch(ctx, opts)
			} else {
				setup, err = gitmeta.SetupTaskWorktree(ctx, opts)
			}
			require.NoError(t, err)
			dir := setup.WorktreePath
			exclude := strings.TrimSpace(runGit(t, dir, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude"))
			require.NoError(t, os.WriteFile(exclude, []byte("incoming.txt\n"), 0o644))
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "cache"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "cache/local.txt"), []byte("discard obstructing ignored directory\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "incoming.txt"), []byte("discard obstructing ignored file\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "unrelated.cache"), []byte("keep unrelated ignored data\n"), 0o644))
			req := gitmeta.WorkflowResetRequest{RepoPath: repo, Directory: dir, Branch: setup.Branch, DefaultBranch: "main", Ref: "refs/orpheus/baselines/reset/1"}
			pushRemoteCommit(t, repo, "cache", "incoming tracked file\n")
			pushRemoteBranchCommit(t, repo, "main", "incoming.txt", "incoming baseline content\n", "restore baseline over ignored file")
			resolved, err := gitmeta.ResolveWorkflowReset(ctx, req)
			require.NoError(t, err)
			req.Target = resolved
			before := captureResetCheckout(t, dir)
			assert.Equal(t, "discard obstructing ignored directory\n", readRevisionTestFile(t, dir, "cache/local.txt"))
			assert.Equal(t, "discard obstructing ignored file\n", readRevisionTestFile(t, dir, "incoming.txt"))
			assert.Equal(t, before, captureResetCheckout(t, dir), "resolution does not modify checkout")

			require.NoError(t, gitmeta.ApplyWorkflowReset(ctx, req))
			assert.Equal(t, "incoming tracked file\n", readRevisionTestFile(t, dir, "cache"))
			assert.Equal(t, "incoming baseline content\n", readRevisionTestFile(t, dir, "incoming.txt"))
			assert.Equal(t, "keep unrelated ignored data\n", readRevisionTestFile(t, dir, "unrelated.cache"))
			assert.Equal(t, req.Target.Commit, strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD")))
			assert.Empty(t, strings.TrimSpace(runGit(t, dir, "status", "--porcelain")))
		})
	}
}
