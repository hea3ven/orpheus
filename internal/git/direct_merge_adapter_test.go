//go:build integration

package git_test

import (
	"context"
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

func TestIntegrationAdapterContractReviewedDirectDeliveryPreservesTreeAndRecoversVerifiedPush(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "repository root", true: "linked worktree"}[linked], func(t *testing.T) {
			repo := newGitRepoWithLocalOrigin(t)
			branch := "orpheus/reviewed"
			dir := repo
			if linked {
				dir = filepath.Join(testutil.CanonicalTempDir(t), "task")
				runGit(t, repo, "worktree", "add", "-b", branch, dir)
			} else {
				runGit(t, repo, "checkout", "-b", branch)
			}
			baseline := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
			prepared, err := orpheusgit.PrepareBaseline(context.Background(), dir, branch, "refs/orpheus/baselines/0", nil)
			require.NoError(t, err)
			assert.Equal(t, baseline, prepared.Commit)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "reviewed.txt"), []byte("reviewed output\n"), 0o644))
			output, err := orpheusgit.CaptureRevision(context.Background(), orpheusgit.RevisionCapture{Directory: dir, Input: orpheusgit.CheckoutIdentity{Commit: baseline, Ref: "refs/heads/" + branch}, Ref: "refs/orpheus/revisions/0/1", Message: "feat: retained input"})
			require.NoError(t, err)
			repeatedBaseline, err := orpheusgit.PrepareBaseline(context.Background(), dir, branch, prepared.RetainedRef, &prepared)
			require.NoError(t, err)
			assert.Equal(t, prepared, repeatedBaseline, "preparation retries retain the original baseline after implementation advances HEAD")
			repository := task.Repository{ID: "alpha", Path: repo, DefaultBranch: "main"}

			delivered, err := orpheusgit.DirectRevisionDelivery(context.Background(), repository, dir, branch, "main", baseline, "refs/orpheus/delivery/0/3", output)

			require.NoError(t, err)
			assert.NotEqual(t, output.Commit, delivered.Commit, "the real integration merge is distinct from the implementation revision")
			assert.Equal(t, output.Tree, delivered.Tree)
			assert.Equal(t, delivered.Commit, strings.Fields(runGit(t, repo, "ls-remote", "origin", "refs/heads/main"))[0])
			assert.Equal(t, output.Commit, strings.TrimSpace(runGit(t, repo, "rev-parse", "refs/heads/"+branch)), "publication must not create another implementation commit")
			retry, err := orpheusgit.DirectRevisionDelivery(context.Background(), repository, dir, branch, "main", baseline, "refs/orpheus/delivery/0/3", output)
			require.NoError(t, err)
			assert.Equal(t, delivered, retry, "a push observed after disconnected persistence is recovered, not merged again")
			observed, err := orpheusgit.PublishedRevision(context.Background(), repo, "main")
			require.NoError(t, err)
			assert.Equal(t, delivered, observed)
		})
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
		_, err = orpheusgit.DirectRevisionDelivery(context.Background(), task.Repository{Path: repo, DefaultBranch: "main"}, repo, "task", "main", baseline, "refs/orpheus/delivery", output)
		require.ErrorContains(t, err, "destination changed since review")
		assert.Equal(t, output.Commit, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")))
		assert.Equal(t, remote, strings.Fields(runGit(t, repo, "ls-remote", "origin", "refs/heads/main"))[0])
	})
}
