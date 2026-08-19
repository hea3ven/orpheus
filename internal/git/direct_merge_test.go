//go:build integration

package git_test

import (
	"context"
	"strings"
	"testing"

	orpheusgit "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/task"
)

func TestIntegrationMergeTaskBranchIntoDefaultCreatesMergeWithoutPush(t *testing.T) {
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

func TestIntegrationMergeTaskBranchIntoNamedDestinationAndVerifyRemoteBranch(t *testing.T) {
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
