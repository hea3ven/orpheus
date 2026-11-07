//go:build integration

package git_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/testguard"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func retainedPublicInput(t *testing.T, dir, ref string) gitmeta.Revision {
	t.Helper()
	commit := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
	tree := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD^{tree}"))
	runGit(t, dir, "update-ref", ref, commit)
	return gitmeta.Revision{Commit: commit, Tree: tree, RetainedRef: ref}
}

func TestIntegrationAdapterContractPublicHistorySquashesThenAppendsRewrittenInputWithoutMovingLocalHistory(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "repository root", true: "linked worktree"}[linked], func(t *testing.T) {
			repo := newGitRepoWithLocalOrigin(t)
			branch, dir := "task", repo
			if linked {
				dir = filepath.Join(testutil.CanonicalTempDir(t), "task")
				runGit(t, repo, "worktree", "add", "-b", branch, dir)
			} else {
				runGit(t, repo, "checkout", "-b", branch)
			}
			baseline := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
			commitFile(t, dir, "feature.txt", "first\n", "first effort")
			first := retainedPublicInput(t, dir, "refs/orpheus/local/first")
			commitFile(t, dir, "feature.txt", "reviewed\n", "repair effort")
			selected := retainedPublicInput(t, dir, "refs/orpheus/local/reviewed")
			req := gitmeta.PublicCommitRequest{Directory: dir, Branch: branch, Parent: baseline, Ref: "refs/orpheus/publication/first", Message: "feat: original feature\n\nExact publication narrative.", Input: selected}
			ctx := context.Background()

			public, err := gitmeta.PackagePublicCommit(ctx, req)
			require.NoError(t, err)
			assert.NotEqual(t, selected.Commit, public.Commit)
			assert.Equal(t, selected.Tree, public.Tree)
			assert.Equal(t, baseline, strings.TrimSpace(runGit(t, dir, "show", "-s", "--format=%P", public.Commit)))
			assert.Equal(t, req.Message, strings.TrimSpace(runGit(t, dir, "show", "-s", "--format=%B", public.Commit)))
			assert.Equal(t, selected.Commit, strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD")))
			assert.Empty(t, strings.TrimSpace(runGit(t, dir, "status", "--porcelain=v1")))
			require.NoError(t, gitmeta.PushPublicCommit(ctx, req, public, ""))
			remote, err := gitmeta.PublishedRevision(ctx, dir, branch)
			require.NoError(t, err)
			assert.Equal(t, public.Commit, remote.Commit)
			assert.Equal(t, selected.Tree, remote.Tree)
			repeated, err := gitmeta.PackagePublicCommit(ctx, req)
			require.NoError(t, err)
			assert.Equal(t, public, repeated)

			// Rewritten operator history need not descend from either reviewed output
			// or the existing public commit. Only the delivered tree is selected.
			runGit(t, dir, "reset", "--hard", baseline)
			commitFile(t, dir, "feature.txt", "operator rewritten\n", "operator rewrite")
			rewritten := retainedPublicInput(t, dir, "refs/orpheus/operator/rewritten")
			update := req
			update.Parent = public.Commit
			update.Ref = "refs/orpheus/publication/update"
			update.Input = rewritten
			next, err := gitmeta.PackagePublicCommit(ctx, update)
			require.NoError(t, err)
			assert.Equal(t, public.Commit, strings.TrimSpace(runGit(t, dir, "show", "-s", "--format=%P", next.Commit)))
			assert.Equal(t, rewritten.Tree, next.Tree)
			require.NoError(t, gitmeta.PushPublicCommit(ctx, update, next, public.Commit))
			remote, err = gitmeta.PublishedRevision(ctx, dir, branch)
			require.NoError(t, err)
			assert.Equal(t, next.Commit, remote.Commit)
			assert.Equal(t, rewritten.Tree, remote.Tree)
			assert.Equal(t, rewritten.Commit, strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD")))
			assert.Empty(t, strings.TrimSpace(runGit(t, dir, "status", "--porcelain=v1")))
			require.NoError(t, gitmeta.VerifyRetainedRevision(ctx, dir, first))
			require.NoError(t, gitmeta.VerifyRetainedRevision(ctx, dir, selected))

			// A concurrent rewind after observation must not be overwritten, even
			// when that older head is an ancestor of the authorized public commit.
			third := update
			third.Parent = next.Commit
			third.Ref = "refs/orpheus/publication/third"
			pending, err := gitmeta.PackagePublicCommit(ctx, third)
			require.NoError(t, err)
			origin := strings.TrimSpace(runGit(t, dir, "remote", "get-url", "origin"))
			hooks := testutil.CanonicalTempDir(t)
			runGit(t, dir, "config", "core.hooksPath", hooks)
			script := "#!/bin/sh\ngit --git-dir='" + origin + "' update-ref refs/heads/task " + public.Commit + "\n"
			require.NoError(t, testguard.WriteExecutable(filepath.Join(hooks, "pre-push"), []byte(script)))
			require.Error(t, gitmeta.PushPublicCommit(ctx, third, pending, next.Commit))
			assert.Equal(t, public.Commit, strings.TrimSpace(runGit(t, origin, "rev-parse", "refs/heads/task")))
			assert.Equal(t, rewritten.Commit, strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD")))
		})
	}
}

func TestIntegrationAdapterContractPublicPackagingHonorsHooksAndReconcilesInterruptedCommit(t *testing.T) {
	for _, kind := range []string{"hook rejection", "signing rejection", "hook changes tree", "lost commit observation"} {
		t.Run(kind, func(t *testing.T) {
			repo := newGitRepoWithLocalOrigin(t)
			runGit(t, repo, "checkout", "-b", "task")
			baseline := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
			commitFile(t, repo, "feature.txt", "authorized\n", "selected input")
			input := retainedPublicInput(t, repo, "refs/orpheus/input")
			hooks := testutil.CanonicalTempDir(t)
			runGit(t, repo, "config", "core.hooksPath", hooks)
			name, script := "pre-commit", "#!/bin/sh\nexit 17\n"
			switch kind {
			case "signing rejection":
				runGit(t, repo, "config", "commit.gpgsign", "true")
				runGit(t, repo, "config", "gpg.program", filepath.Join(hooks, "unavailable-signer"))
				script = "#!/bin/sh\nexit 0\n"
			case "hook changes tree":
				script = "#!/bin/sh\nprintf 'hook output\\n' > feature.txt\ngit add feature.txt\n"
			case "lost commit observation":
				name = "post-commit"
				script = "#!/bin/sh\nkill -TERM \"$PPID\"\n"
			}
			require.NoError(t, testguard.WriteExecutable(filepath.Join(hooks, name), []byte(script)))
			req := gitmeta.PublicCommitRequest{Directory: repo, Branch: "task", Parent: baseline, Ref: "refs/orpheus/package", Message: "feat: publish\n\nBody", Input: input}
			result, err := gitmeta.PackagePublicCommit(context.Background(), req)
			if kind == "hook changes tree" {
				require.NoError(t, err)
				assert.NotEqual(t, input.Tree, result.Tree)
				assert.Equal(t, "hook output\n", runGit(t, repo, "show", result.Commit+":feature.txt"))
				require.ErrorContains(t, gitmeta.PushPublicCommit(context.Background(), req, result, ""), "preserve input")
			} else {
				require.Error(t, err)
				require.NoError(t, os.Remove(filepath.Join(hooks, name)))
				if kind == "signing rejection" {
					runGit(t, repo, "config", "commit.gpgsign", "false")
				}
				result, err = gitmeta.PackagePublicCommit(context.Background(), req)
				require.NoError(t, err)
				assert.Equal(t, input.Tree, result.Tree)
				repeated, err := gitmeta.PackagePublicCommit(context.Background(), req)
				require.NoError(t, err)
				assert.Equal(t, result, repeated, "lost observation cannot cause another commit")
			}
			head, err := gitmeta.ObservePublicHead(context.Background(), repo, "task")
			require.NoError(t, err)
			assert.Empty(t, head, "packaging alone does not authorize a push")
			assert.Equal(t, input.Commit, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")))
			assert.Empty(t, strings.TrimSpace(runGit(t, repo, "status", "--porcelain=v1")))
			assert.Equal(t, "authorized\n", runGit(t, repo, "show", "HEAD:feature.txt"))
		})
	}
}
