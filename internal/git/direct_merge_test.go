package git_test

import (
	"context"
	"strings"
	"testing"

	orpheusgit "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/task"
)

func TestMergeTaskBranchIntoDefaultCreatesMergeWithoutPush(t *testing.T) {
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

func TestMergeTaskBranchIntoDefaultRejectsLocallyAheadDefaultContainingTask(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	runGit(t, repoPath, "checkout", "-b", "orpheus/op-1")
	commitFile(t, repoPath, "reviewed.txt", "reviewed\n", "Reviewed work")
	runGit(t, repoPath, "checkout", "main")
	runGit(t, repoPath, "merge", "--ff-only", "orpheus/op-1")

	_, err := orpheusgit.MergeTaskBranchIntoDefault(context.Background(), task.Repository{
		ID: "alpha", Path: repoPath, DefaultBranch: "main",
	}, "orpheus/op-1")
	if err == nil || !strings.Contains(err.Error(), "local branch is ahead of or divergent") {
		t.Fatalf("direct merge error = %v, want locally-ahead rejection", err)
	}
}

func TestMergeTaskBranchIntoDefaultRejectsUnrelatedCommitAfterMerge(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	runGit(t, repoPath, "checkout", "-b", "orpheus/op-1")
	commitFile(t, repoPath, "reviewed.txt", "reviewed\n", "Reviewed work")
	runGit(t, repoPath, "checkout", "main")
	merge, err := orpheusgit.MergeTaskBranchIntoDefault(context.Background(), task.Repository{
		ID: "alpha", Path: repoPath, DefaultBranch: "main",
	}, "orpheus/op-1")
	if err != nil {
		t.Fatalf("initial direct merge: %v", err)
	}
	commitFile(t, repoPath, "unrelated.txt", "unrelated\n", "Unrelated local commit")

	_, err = orpheusgit.MergeTaskBranchIntoDefault(context.Background(), task.Repository{
		ID: "alpha", Path: repoPath, DefaultBranch: "main",
	}, "orpheus/op-1")
	if err == nil || !strings.Contains(err.Error(), "local branch is ahead of or divergent") {
		t.Fatalf("retry error = %v, want unrelated local commit rejection", err)
	}
	if got := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD^")); got != merge {
		t.Fatalf("parent of unrelated commit = %s, want merge %s", got, merge)
	}
}

func TestValidateRecordedDirectMergeRecoversMergeAlreadyOnOrigin(t *testing.T) {
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
	runGit(t, repoPath, "push", "origin", "main")
	runGit(t, repoPath, "reset", "--hard", "HEAD^")

	alreadyPushed, err := orpheusgit.ValidateRecordedDirectMerge(context.Background(), task.Repository{
		ID: "alpha", Path: repoPath, DefaultBranch: "main",
	}, merge)
	if err != nil {
		t.Fatalf("validate recorded merge: %v", err)
	}
	if !alreadyPushed {
		t.Fatal("validation did not recover merge already on origin")
	}
}

func TestValidateRecordedDirectMergeRejectsResetLocalDefault(t *testing.T) {
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
	runGit(t, repoPath, "reset", "--hard", "HEAD^")

	_, err = orpheusgit.ValidateRecordedDirectMerge(context.Background(), task.Repository{
		ID: "alpha", Path: repoPath, DefaultBranch: "main",
	}, merge)
	if err == nil || !strings.Contains(err.Error(), "not recorded merge") {
		t.Fatalf("validation error = %v, want reset local default rejection", err)
	}
}
