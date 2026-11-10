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

func TestIntegrationAdapterContractMergeTaskBranchIntoDefaultCreatesMergeWithoutPush(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	runGit(t, repoPath, "checkout", "-b", "orpheus/op-1")
	commitFile(t, repoPath, "reviewed.txt", "reviewed\n", "Reviewed work")
	runGit(t, repoPath, "checkout", "main")

	merge, err := orpheusgit.MergeTaskBranchIntoDefault(context.Background(), task.Repository{
		ID: "alpha", Path: repoPath, DefaultBranch: "main",
	}, "orpheus/op-1")
	if err != nil {
		t.Fatalf("direct merge: %v", err)
	}
	if got := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD")); got != merge {
		t.Fatalf("HEAD = %s, want merge %s", got, merge)
	}
	if got := strings.TrimSpace(runGit(t, repoPath, "rev-list", "--parents", "-n", "1", "HEAD")); len(strings.Fields(got)) != 3 {
		t.Fatalf("merge parents = %q, want merge commit", got)
	}
	if got := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "origin/main")); got == merge {
		t.Fatal("origin/main advanced before direct-merge caller pushed it")
	}

	again, err := orpheusgit.MergeTaskBranchIntoDefault(context.Background(), task.Repository{
		ID: "alpha", Path: repoPath, DefaultBranch: "main",
	}, "orpheus/op-1")
	if err != nil || again != merge {
		t.Fatalf("retry = %q, %v; want recorded local merge %q", again, err, merge)
	}
}

func TestIntegrationAdapterContractMergeTaskBranchIntoNamedDestinationAndVerifyRemoteBranch(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	runGit(t, repoPath, "checkout", "-b", "release/next")
	runGit(t, repoPath, "push", "origin", "release/next")
	runGit(t, repoPath, "checkout", "-b", "orpheus/op-1")
	commitFile(t, repoPath, "reviewed.txt", "reviewed\n", "Reviewed work")
	runGit(t, repoPath, "checkout", "main")

	if err := orpheusgit.VerifyRemoteBranch(context.Background(), repoPath, "release/next"); err != nil {
		t.Fatalf("verify named destination: %v", err)
	}
	merge, err := orpheusgit.MergeTaskBranchIntoDestination(context.Background(), task.Repository{
		ID: "alpha", Path: repoPath, DefaultBranch: "main",
	}, "release/next", "orpheus/op-1")
	if err != nil {
		t.Fatalf("direct merge into named destination: %v", err)
	}
	if got := strings.TrimSpace(runGit(t, repoPath, "branch", "--show-current")); got != "release/next" {
		t.Fatalf("current branch = %q, want release/next", got)
	}
	if got := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD")); got != merge {
		t.Fatalf("HEAD = %s, want merge %s", got, merge)
	}
	if got := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "origin/release/next")); got == merge {
		t.Fatal("origin/release/next advanced before caller pushed it")
	}
	if err := orpheusgit.VerifyRemoteBranch(context.Background(), repoPath, "release/missing"); err == nil || !strings.Contains(err.Error(), "does not exist on origin") {
		t.Fatalf("missing branch error = %v, want remote absence", err)
	}
	if err := orpheusgit.VerifyRemoteBranch(context.Background(), repoPath, "-unsafe"); err == nil || !strings.Contains(err.Error(), "safe Git branch name") {
		t.Fatalf("unsafe branch error = %v, want branch validation", err)
	}
}

func TestIntegrationAdapterContractPreparedDirectDeliverySyncsLocallyBeforeRecordedIntegrationPush(t *testing.T) {
	for _, linked := range []bool{false, true} {
		for _, conflict := range []bool{false, true} {
			t.Run(fmt.Sprintf("linked_%t/conflict_%t", linked, conflict), func(t *testing.T) {
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
				path := "destination.txt"
				if conflict {
					path = "reviewed.txt"
				}
				commitFile(t, repo, path, "destination output\n", "Destination advanced")
				runGit(t, repo, "push", "origin", "main")
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
				} else {
					assert.Equal(t, "destination output\n", runGit(t, dir, "show", "HEAD:destination.txt"))
				}
				assert.Empty(t, strings.TrimSpace(runGit(t, dir, "status", "--porcelain")))
				actual, err := orpheusgit.InspectPublicationCheckout(context.Background(), dir, branch)
				require.NoError(t, err)
				assert.Equal(t, synced.Head, actual.Commit)
				assert.NotEqual(t, public.Commit, actual.Commit)
				integration := orpheusgit.DirectIntegrationRequest{Repository: repository, Directory: dir, Branch: branch, Destination: "main", DestinationCommit: destination, Source: actual}
				merged, err := orpheusgit.PrepareDirectIntegration(context.Background(), integration)
				require.NoError(t, err)
				assert.Equal(t, destination, strings.Fields(runGit(t, repo, "ls-remote", "origin", "refs/heads/main"))[0], "integration cannot push before its caller records the result")
				assert.Equal(t, destination+" "+actual.Commit, strings.TrimSpace(runGit(t, repo, "show", "-s", "--format=%P", merged.Commit)))
				checkpoints, err := orpheusgit.InspectDirectIntegrationCheckpoints(context.Background(), integration)
				require.NoError(t, err)
				assert.Equal(t, actual, checkpoints.Source)
				assert.Equal(t, merged, checkpoints.Destination, "observation reads the branch, not merely the active task checkout")
				delivered, err := orpheusgit.PushDirectIntegration(context.Background(), integration, merged)
				require.NoError(t, err)
				assert.Equal(t, actual.Tree, delivered.Tree)
				assert.Equal(t, delivered.Commit, strings.Fields(runGit(t, repo, "ls-remote", "origin", "refs/heads/main"))[0])
				assert.Equal(t, actual.Commit, strings.TrimSpace(runGit(t, repo, "rev-parse", "refs/heads/"+branch)))
				assert.Empty(t, strings.TrimSpace(runGit(t, repo, "ls-remote", "--heads", "origin", "refs/heads/"+branch)), "direct delivery never pushes the source branch")
				retry, err := orpheusgit.PushDirectIntegration(context.Background(), integration, merged)
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
		origin := strings.TrimSpace(runGit(t, repo, "remote", "get-url", "origin"))
		other := filepath.Join(testutil.CanonicalTempDir(t), "other")
		runGit(t, repo, "clone", "--branch", "main", origin, other)
		runGit(t, other, "config", "user.name", "Other operator")
		runGit(t, other, "config", "user.email", "other@example.test")
		commitFile(t, other, "ahead.txt", "concurrent destination\n", "Destination moved again")
		runGit(t, other, "push", "origin", "main")
		advanced := strings.TrimSpace(runGit(t, other, "rev-parse", "HEAD"))
		_, err = orpheusgit.PushDirectIntegration(context.Background(), req, merged)
		require.ErrorContains(t, err, "destination advanced after synchronization")
		assert.Equal(t, merged.Commit, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")), "a stale checkpoint cannot rewrite local output or silently remerge")
		assert.Equal(t, advanced, strings.Fields(runGit(t, repo, "ls-remote", "origin", "refs/heads/main"))[0])
	})
}
