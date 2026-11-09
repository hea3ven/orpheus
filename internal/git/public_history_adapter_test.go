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

func TestIntegrationAdapterContractPublicHistorySquashesInPlaceThenReleasesOnlyOwnedPrivateRefs(t *testing.T) {
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
			const revisions = "refs/orpheus/revisions/616c706861/6f702d31/0/"
			const inputs = "refs/orpheus/publication-inputs/616c706861/6f702d31/0/"
			commitFile(t, dir, "feature.txt", "first\n", "first effort")
			first := retainedPublicInput(t, dir, revisions+"1/output")
			commitFile(t, dir, "feature.txt", "reviewed\n", "repair effort")
			input := retainedPublicInput(t, dir, revisions+"3/output")
			runGit(t, dir, "update-ref", revisions+"3/input", first.Commit)
			runGit(t, dir, "update-ref", inputs+input.Commit, input.Commit)
			runGit(t, dir, "update-ref", "refs/heads/operator-saved", first.Commit)
			unrelated := "refs/orpheus/revisions/616c706861/6f702d32/0/1/output"
			runGit(t, dir, "update-ref", unrelated, input.Commit)
			worktrees := runGit(t, dir, "worktree", "list", "--porcelain")
			req := gitmeta.PublicCommitRequest{Directory: dir, Branch: branch, Parent: baseline, Ref: revisions + "5/public", Message: "feat: original intent\n\nExact feature narrative.", Input: input}
			ctx := context.Background()
			_, err := gitmeta.PackagePublicCommit(ctx, req)
			require.Error(t, err, "rewriting requires the recovery input to exist first")
			assert.Equal(t, input.Commit, strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD")))
			require.NoError(t, gitmeta.RetainPublicationInput(ctx, req))
			assert.Equal(t, input.Commit, strings.TrimSpace(runGit(t, dir, "rev-parse", req.Ref+"/input")))

			public, err := gitmeta.PackagePublicCommit(ctx, req)
			require.NoError(t, err)
			assert.Equal(t, input.Tree, public.Tree)
			assert.Equal(t, public.Commit, strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD")))
			assert.Equal(t, baseline, strings.TrimSpace(runGit(t, dir, "show", "-s", "--format=%P", public.Commit)))
			assert.Equal(t, req.Message, strings.TrimSpace(runGit(t, dir, "show", "-s", "--format=%B", public.Commit)))
			assert.Equal(t, strings.Count(worktrees, "worktree "), strings.Count(runGit(t, dir, "worktree", "list", "--porcelain"), "worktree "), "packaging must not allocate another checkout")
			assert.Empty(t, strings.TrimSpace(runGit(t, dir, "status", "--porcelain=v1")))
			require.NoError(t, gitmeta.VerifyRetainedRevision(ctx, dir, input), "incomplete publication still retains private history")
			require.NoError(t, gitmeta.PushPublicCommit(ctx, req, public))
			remote, err := gitmeta.PublishedRevision(ctx, dir, branch)
			require.NoError(t, err)
			assert.Equal(t, public.Commit, remote.Commit)

			require.NoError(t, gitmeta.ReleasePrivatePublicationRefs(ctx, dir, []string{revisions, inputs}))
			assert.Empty(t, strings.TrimSpace(runGit(t, dir, "for-each-ref", "--format=%(refname)", revisions, inputs)))
			assert.Equal(t, first.Commit, strings.TrimSpace(runGit(t, dir, "rev-parse", "refs/heads/operator-saved")))
			assert.Equal(t, input.Commit, strings.TrimSpace(runGit(t, dir, "rev-parse", unrelated)))
			require.NoError(t, gitmeta.ReleasePrivatePublicationRefs(ctx, dir, []string{revisions, inputs}), "cleanup retry is independent of delivery")
			synced, err := gitmeta.BeginTaskBranchConflictResolution(ctx, gitmeta.TaskBranchSyncOptions{RepoPath: repo, Worktree: dir, Branch: branch, DefaultBranch: "main"})
			require.NoError(t, err)
			assert.Equal(t, gitmeta.TaskBranchSyncAlreadyCurrent, synced.Status)
			assert.Equal(t, public.Commit, synced.Head)
			commitFile(t, dir, "later.txt", "ordinary later fix\n", "fix: later public work")
			later := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
			assert.Equal(t, public.Commit, strings.TrimSpace(runGit(t, dir, "show", "-s", "--format=%P", later)))
			require.NoError(t, gitmeta.PushTaskBranch(ctx, dir, branch))
			remote, err = gitmeta.PublishedRevision(ctx, dir, branch)
			require.NoError(t, err)
			assert.Equal(t, later, remote.Commit, "later work extends public history without initial squash machinery")
		})
	}
}

func TestIntegrationAdapterContractPublicPackagingHonorsPolicyAndRequiresExplicitInterruptedRecovery(t *testing.T) {
	for _, kind := range []string{"hook rejection", "signing rejection", "hook changes tree", "lost commit response"} {
		t.Run(kind, func(t *testing.T) {
			repo := newGitRepoWithLocalOrigin(t)
			runGit(t, repo, "checkout", "-b", "task")
			baseline := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
			commitFile(t, repo, "feature.txt", "authorized\n", "selected input")
			input := retainedPublicInput(t, repo, "refs/orpheus/revisions/aa/bb/0/1/output")
			req := gitmeta.PublicCommitRequest{Directory: repo, Branch: "task", Parent: baseline, Ref: "refs/orpheus/revisions/aa/bb/0/3/public", Message: "feat: publish\n\nFeature body", Input: input}
			require.NoError(t, gitmeta.RetainPublicationInput(context.Background(), req))
			hooks := testutil.CanonicalTempDir(t)
			runGit(t, repo, "config", "core.hooksPath", hooks)
			name, script := "pre-commit", "#!/bin/sh\nexit 17\n"
			switch kind {
			case "signing rejection":
				runGit(t, repo, "config", "commit.gpgsign", "true")
				runGit(t, repo, "config", "gpg.program", filepath.Join(hooks, "missing-signer"))
				script = "#!/bin/sh\nexit 0\n"
			case "hook changes tree":
				script = "#!/bin/sh\nprintf 'hook output\\n' > feature.txt\ngit add feature.txt\n"
			case "lost commit response":
				name = "post-commit"
				script = "#!/bin/sh\nkill -TERM \"$PPID\"\n"
			}
			require.NoError(t, testguard.WriteExecutable(filepath.Join(hooks, name), []byte(script)))
			result, err := gitmeta.PackagePublicCommit(context.Background(), req)
			if kind == "hook changes tree" {
				require.NoError(t, err)
				assert.NotEqual(t, input.Tree, result.Tree)
				assert.Equal(t, result.Commit, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")))
				assert.Equal(t, "hook output\n", runGit(t, repo, "show", "HEAD:feature.txt"))
				require.ErrorContains(t, gitmeta.PushPublicCommit(context.Background(), req, result), "authorized publication")
				return
			}
			require.Error(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(repo, "discarded.txt"), []byte("operator must see this\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("ignored.log\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(repo, "ignored.log"), []byte("keep ignored data\n"), 0o644))
			beforeHead := runGit(t, repo, "rev-parse", "HEAD")
			beforeIndex := runGit(t, repo, "write-tree")
			disclosure, err := gitmeta.InspectPublicationRecovery(context.Background(), req)
			require.NoError(t, err)
			assert.Contains(t, disclosure.Changes, input.Commit)
			assert.Contains(t, disclosure.Changes, "discarded.txt")
			assert.Equal(t, beforeHead, runGit(t, repo, "rev-parse", "HEAD"), "inspection/manual repair choice cannot reset automatically")
			assert.Equal(t, beforeIndex, runGit(t, repo, "write-tree"))
			data, err := os.ReadFile(filepath.Join(repo, "discarded.txt"))
			require.NoError(t, err)
			assert.Equal(t, "operator must see this\n", string(data))
			require.NoError(t, os.Remove(filepath.Join(hooks, name)))
			if kind == "signing rejection" {
				runGit(t, repo, "config", "commit.gpgsign", "false")
			}
			if kind == "hook rejection" {
				require.NoError(t, os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("manual progress after disclosure\n"), 0o644))
				require.ErrorContains(t, gitmeta.RestorePublicationInput(context.Background(), req, disclosure), "changed")
				data, err = os.ReadFile(filepath.Join(repo, "feature.txt"))
				require.NoError(t, err)
				assert.Equal(t, "manual progress after disclosure\n", string(data), "stale confirmation must not discard new changes")
				assert.Equal(t, beforeHead, runGit(t, repo, "rev-parse", "HEAD"))
				disclosure, err = gitmeta.InspectPublicationRecovery(context.Background(), req)
				require.NoError(t, err)
			}
			require.NoError(t, gitmeta.RestorePublicationInput(context.Background(), req, disclosure))
			assert.NoFileExists(t, filepath.Join(repo, "discarded.txt"))
			data, err = os.ReadFile(filepath.Join(repo, "ignored.log"))
			require.NoError(t, err)
			assert.Equal(t, "keep ignored data\n", string(data))
			assert.Equal(t, input.Commit, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")))
			result, err = gitmeta.PackagePublicCommit(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, input.Tree, result.Tree)
			assert.Equal(t, result.Commit, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")))
			head, err := gitmeta.ObservePublicHead(context.Background(), repo, "task")
			require.NoError(t, err)
			assert.Empty(t, head, "packaging/recovery cannot push before durable public recording")
		})
	}
}
