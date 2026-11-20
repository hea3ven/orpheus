//go:build integration

package git_test

import (
	"context"
	"fmt"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"testing"

	orpheusgit "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/task"
)

func TestIntegrationAdapterContractDirectIntegrationUsesNamedDestination(t *testing.T) {
	ctx := context.Background()
	repo := newGitRepoWithLocalOrigin(t)
	runGit(t, repo, "checkout", "-b", "release/next")
	commitFile(t, repo, "release.txt", "release\n", "Release work")
	runGit(t, repo, "push", "origin", "release/next")
	destination := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	main := strings.TrimSpace(runGit(t, repo, "rev-parse", "main"))
	runGit(t, repo, "checkout", "-b", "task")
	commitFile(t, repo, "reviewed.txt", "reviewed\n", "Reviewed work")
	source, err := orpheusgit.InspectPublicationCheckout(ctx, repo, "task")
	require.NoError(t, err)
	req := orpheusgit.DirectIntegrationRequest{Repository: task.Repository{Path: repo, DefaultBranch: "main"}, Directory: repo, Branch: "task", Destination: "release/next", DestinationCommit: destination, Source: source}

	require.NoError(t, orpheusgit.VerifyRemoteBranch(ctx, repo, req.Destination))
	result, err := orpheusgit.PrepareDirectIntegration(ctx, req)
	require.NoError(t, err)
	assertGitBranch(t, repo, req.Destination)
	assert.Equal(t, source, result)
	delivered, err := orpheusgit.PushDirectIntegration(ctx, req, result)
	require.NoError(t, err)

	assert.Equal(t, source, delivered)
	assert.Equal(t, source.Commit, strings.Fields(runGit(t, repo, "ls-remote", "origin", "refs/heads/release/next"))[0])
	assert.Equal(t, main, strings.Fields(runGit(t, repo, "ls-remote", "origin", "refs/heads/main"))[0])
	require.ErrorContains(t, orpheusgit.VerifyRemoteBranch(ctx, repo, "release/missing"), "does not exist on origin")
	require.ErrorContains(t, orpheusgit.VerifyRemoteBranch(ctx, repo, "-unsafe"), "safe Git branch name")
}

func TestIntegrationAdapterContractPreparedDirectDeliverySyncsLocallyBeforeRecordedIntegrationPush(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, history := range []string{"linear", "diverged", "diverged with conflict"} {
			t.Run(fmt.Sprintf("linked_%t/%s", linked, history), func(t *testing.T) {
				conflict := history == "diverged with conflict"
				repo := newGitRepoWithLocalOrigin(t)
				branch, dir := "orpheus/reviewed", repo
				if linked {
					dir = filepath.Join(testutil.CanonicalTempDir(t), "task")
					runGit(t, repo, "worktree", "add", "-b", branch, dir)
				} else {
					runGit(t, repo, "checkout", "-b", branch)
				}
				baseline := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
				prepared, err := orpheusgit.PrepareBaseline(context.Background(), dir, branch, "refs/orpheus/baselines/0", nil)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(dir, "reviewed.txt"), []byte("reviewed output\n"), 0o644))
				output, err := orpheusgit.CaptureRevision(context.Background(), orpheusgit.RevisionCapture{Directory: dir, Input: orpheusgit.CheckoutIdentity{Commit: baseline, Ref: "refs/heads/" + branch}, Ref: "refs/orpheus/revisions/aa/bb/0/1", Message: "feat: retained input"})
				require.NoError(t, err)
				repeatedBaseline, err := orpheusgit.PrepareBaseline(context.Background(), dir, branch, prepared.RetainedRef, &prepared)
				require.NoError(t, err)
				assert.Equal(t, prepared, repeatedBaseline)
				if !linked {
					runGit(t, repo, "checkout", "main")
				}
				if history != "linear" {
					path := "destination.txt"
					if conflict {
						path = "reviewed.txt"
					}
					commitFile(t, repo, path, "destination output\n", "Destination advanced")
					runGit(t, repo, "push", "origin", "main")
				}
				destination := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
				if !linked {
					runGit(t, repo, "checkout", branch)
				}
				repository := task.Repository{ID: "alpha", Path: repo, DefaultBranch: "main"}
				req := orpheusgit.PublicCommitRequest{Directory: dir, Branch: branch, Parent: baseline, Ref: "refs/orpheus/revisions/aa/bb/0/3/public", Message: "feat: publish prepared history", Input: output}
				require.NoError(t, orpheusgit.RetainPublicationInput(context.Background(), req))
				public, err := orpheusgit.PackagePublicCommit(context.Background(), req)
				require.NoError(t, err)
				require.NoError(t, orpheusgit.ReleasePrivatePublicationRefs(context.Background(), dir, []string{"refs/orpheus/revisions/aa/bb/0/"}))
				// Make the discarded loose private commit unavailable without pruning shared
				// objects or expiring reflogs. Public trees and checkpoints remain intact.
				require.NoError(t, os.Remove(filepath.Join(repo, ".git", "objects", output.Commit[:2], output.Commit[2:])))
				opts := orpheusgit.TaskBranchSyncOptions{RepoPath: repo, Worktree: dir, Branch: branch, DefaultBranch: "main"}
				if linked {
					require.NoError(t, os.WriteFile(filepath.Join(repo, "operator.txt"), []byte("keep operator work\n"), 0o644))
					_, err = orpheusgit.BeginLocalTaskBranchSync(context.Background(), opts, destination)
					require.ErrorContains(t, err, "uncommitted")
					assert.Equal(t, public.Commit, strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD")))
					require.NoError(t, os.Remove(filepath.Join(repo, "operator.txt")))
				}
				synced, err := orpheusgit.BeginLocalTaskBranchSync(context.Background(), opts, destination)
				require.NoError(t, err)
				if conflict {
					assert.Equal(t, orpheusgit.TaskBranchSyncConflicted, synced.Status)
					assert.Equal(t, []string{"reviewed.txt"}, synced.ConflictFiles)
					require.NoError(t, os.WriteFile(filepath.Join(dir, "reviewed.txt"), []byte("resolved output\n"), 0o644))
					runGit(t, dir, "add", "reviewed.txt")
					synced, err = orpheusgit.CommitTaskBranchConflictResolution(context.Background(), opts, synced.ConflictFiles)
					require.NoError(t, err)
					assert.Equal(t, "resolved output\n", runGit(t, dir, "show", "HEAD:reviewed.txt"))
				} else if history == "diverged" {
					assert.Equal(t, "destination output\n", runGit(t, dir, "show", "HEAD:destination.txt"))
				}
				assert.Empty(t, strings.TrimSpace(runGit(t, dir, "status", "--porcelain")))
				actual, err := orpheusgit.InspectPublicationCheckout(context.Background(), dir, branch)
				require.NoError(t, err)
				assert.Equal(t, synced.Head, actual.Commit)
				wantParents := baseline
				if history == "linear" {
					assert.Equal(t, orpheusgit.TaskBranchSyncAlreadyCurrent, synced.Status)
					assert.Equal(t, public, actual)
				} else {
					wantParents = public.Commit + " " + destination
				}
				integration := orpheusgit.DirectIntegrationRequest{Repository: repository, Directory: dir, Branch: branch, Destination: "main", DestinationCommit: destination, Source: actual}
				result, err := orpheusgit.PrepareDirectIntegration(context.Background(), integration)
				require.NoError(t, err)
				assert.Equal(t, actual, result)
				assert.Equal(t, destination, strings.Fields(runGit(t, repo, "ls-remote", "origin", "refs/heads/main"))[0], "integration cannot push before its caller records the result")
				checkpoints, err := orpheusgit.InspectDirectIntegrationCheckpoints(context.Background(), integration)
				require.NoError(t, err)
				assert.Equal(t, actual, checkpoints.Source)
				assert.Equal(t, actual, checkpoints.Destination, "observation reads the branch, not merely the active task checkout")
				reprepared, err := orpheusgit.PrepareDirectIntegration(context.Background(), integration)
				require.NoError(t, err)
				assert.Equal(t, result, reprepared, "local retry reuses the exact prepared tip")
				delivered, err := orpheusgit.PushDirectIntegration(context.Background(), integration, result)
				require.NoError(t, err)
				assert.Equal(t, actual, delivered)
				assert.Equal(t, actual.Commit, strings.Fields(runGit(t, repo, "ls-remote", "origin", "refs/heads/main"))[0])
				assert.Equal(t, wantParents, strings.TrimSpace(runGit(t, repo, "show", "-s", "--format=%P", delivered.Commit)))
				assert.Equal(t, actual.Commit, strings.TrimSpace(runGit(t, repo, "rev-parse", "refs/heads/"+branch)))
				assert.Empty(t, strings.TrimSpace(runGit(t, repo, "ls-remote", "--heads", "origin", "refs/heads/"+branch)), "direct delivery never pushes the source branch")
				// A later local edit cannot invalidate an already successful delivery.
				commitFile(t, repo, "local.txt", "operator only\n", "Local work after push")
				retry, err := orpheusgit.PushDirectIntegration(context.Background(), integration, result)
				require.NoError(t, err)
				assert.Equal(t, delivered, retry)
				observed, err := orpheusgit.PublishedRevision(context.Background(), repo, "main")
				require.NoError(t, err)
				assert.Equal(t, delivered, observed)
			})
		}
	}
	t.Run("destination changes reviewed tree", func(t *testing.T) {
		repo := newGitRepoWithLocalOrigin(t)
		baseline := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
		runGit(t, repo, "checkout", "-b", "task")
		require.NoError(t, os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644))
		output, err := orpheusgit.CaptureRevision(context.Background(), orpheusgit.RevisionCapture{Directory: repo, Input: orpheusgit.CheckoutIdentity{Commit: baseline, Ref: "refs/heads/task"}, Ref: "refs/orpheus/revision", Message: "feat: reviewed"})
		require.NoError(t, err)
		runGit(t, repo, "checkout", "main")
		commitFile(t, repo, "unreviewed.txt", "destination changed\n", "Destination advanced")
		runGit(t, repo, "push", "origin", "main")
		remote := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
		runGit(t, repo, "checkout", "task")
		_, err = orpheusgit.PrepareDirectIntegration(context.Background(), orpheusgit.DirectIntegrationRequest{Repository: task.Repository{Path: repo, DefaultBranch: "main"}, Directory: repo, Branch: "task", Destination: "main", DestinationCommit: baseline, Source: output})
		require.ErrorContains(t, err, "destination advanced after sync")
		recovery := orpheusgit.PublicCommitRequest{Directory: repo, Branch: "main", Input: orpheusgit.Revision{Commit: baseline}}
		disclosure, err := orpheusgit.InspectPublicationRecovery(context.Background(), recovery)
		require.NoError(t, err, "an unchanged task checkout still permits disclosure and manual repair after a refused destination effect")
		assert.Equal(t, "refs/heads/task", disclosure.Head.Ref)
		assert.Contains(t, disclosure.Changes, "target branch main")
		require.ErrorContains(t, orpheusgit.RestorePublicationInput(context.Background(), recovery, disclosure), "target branch to be checked out")

		assert.Equal(t, output.Commit, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")))
		assert.Equal(t, remote, strings.Fields(runGit(t, repo, "ls-remote", "origin", "refs/heads/main"))[0])

		opts := orpheusgit.TaskBranchSyncOptions{RepoPath: repo, Worktree: repo, Branch: "task", DefaultBranch: "main"}
		_, err = orpheusgit.BeginLocalTaskBranchSync(context.Background(), opts, remote)
		require.NoError(t, err)
		synced, err := orpheusgit.InspectPublicationCheckout(context.Background(), repo, "task")
		require.NoError(t, err)
		req := orpheusgit.DirectIntegrationRequest{Repository: task.Repository{Path: repo}, Directory: repo, Branch: "task", Destination: "main", DestinationCommit: remote, Source: synced}
		merged, err := orpheusgit.PrepareDirectIntegration(context.Background(), req)
		require.NoError(t, err)
		runGit(t, repo, "checkout", "task")
		commitFile(t, repo, "operator.txt", "amended task output\n", "Operator selected changed source")
		amended, err := orpheusgit.InspectPublicationCheckout(context.Background(), repo, "task")
		require.NoError(t, err)
		checkpoints, err := orpheusgit.InspectDirectIntegrationCheckpoints(context.Background(), req)
		require.NoError(t, err, "repo-root reconciliation may inspect the clean task branch without switching it")
		assert.Equal(t, amended, checkpoints.Source)
		assert.NotEqual(t, req.Source.Commit, checkpoints.Source.Commit)
		assert.Equal(t, merged, checkpoints.Destination)
		runGit(t, repo, "checkout", "main")
		runGit(t, repo, "reset", "--hard", remote) // Explicit operator repair in this isolated adapter fixture.
		checkpoints, err = orpheusgit.InspectDirectIntegrationCheckpoints(context.Background(), req)
		require.NoError(t, err)
		assert.Equal(t, remote, checkpoints.Destination.Commit)
		assert.Equal(t, amended, checkpoints.Source, "destination repair does not discard the selected task input")
		runGit(t, repo, "checkout", "task")
		req.Source = amended
		merged, err = orpheusgit.PrepareDirectIntegration(context.Background(), req)
		require.NoError(t, err)
		// The remote moved after preparation. Even a fast-forward from this new
		// checkpoint would not be the delivery that the operator authorized.
		runGit(t, repo, "push", "origin", synced.Commit+":refs/heads/main")
		_, err = orpheusgit.PushDirectIntegration(context.Background(), req, merged)
		require.ErrorContains(t, err, "destination advanced after synchronization")
		assert.Equal(t, merged.Commit, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")), "a stale checkpoint cannot rewrite local output or silently remerge")
		assert.Equal(t, synced.Commit, strings.Fields(runGit(t, repo, "ls-remote", "origin", "refs/heads/main"))[0])
	})
}
