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

func TestMergeTaskBranchIntoNamedDestinationAndVerifyRemoteBranch(t *testing.T) {
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

func TestDirectMergeRejectsPseudoRevisionDestinationsBeforeMutation(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	runGit(t, repoPath, "checkout", "-b", "orpheus/op-1")
	commitFile(t, repoPath, "reviewed.txt", "reviewed\n", "Reviewed work")
	runGit(t, repoPath, "checkout", "main")
	mainBefore := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "refs/heads/main"))

	for _, destination := range []string{"@", "HEAD"} {
		t.Run(destination, func(t *testing.T) {
			runGit(t, repoPath, "update-ref", "refs/heads/"+destination, "refs/heads/main")
			runGit(t, repoPath, "push", "origin", "refs/heads/"+destination+":refs/heads/"+destination)
			if got := strings.TrimSpace(runGit(t, repoPath, "ls-remote", "--heads", "origin", "refs/heads/"+destination)); got == "" {
				t.Fatalf("origin destination %q was not created", destination)
			}

			if err := orpheusgit.VerifyRemoteBranch(context.Background(), repoPath, destination); err == nil || !strings.Contains(err.Error(), "safe Git branch name") {
				t.Fatalf("verify destination error = %v, want unsafe-name rejection", err)
			}
			if _, err := orpheusgit.MergeTaskBranchIntoDestination(context.Background(), task.Repository{
				ID: "alpha", Path: repoPath, DefaultBranch: "main",
			}, destination, "orpheus/op-1"); err == nil || !strings.Contains(err.Error(), "safe Git branch name") {
				t.Fatalf("direct merge error = %v, want unsafe-name rejection", err)
			}
			if got := strings.TrimSpace(runGit(t, repoPath, "branch", "--show-current")); got != "main" {
				t.Fatalf("current branch = %q, want main; destination must be rejected before checkout", got)
			}
			if got := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "refs/heads/main")); got != mainBefore {
				t.Fatalf("main = %s, want unchanged %s; destination must be rejected before merge or push", got, mainBefore)
			}
		})
	}
}

func TestDirectMergeRejectsRemoteTaskBranchAsDestinationBeforeMutation(t *testing.T) {
	repoPath := newGitRepoWithLocalOrigin(t)
	runGit(t, repoPath, "checkout", "-b", "orpheus/op-1")
	commitFile(t, repoPath, "reviewed.txt", "reviewed\n", "Reviewed work")
	runGit(t, repoPath, "push", "origin", "refs/heads/orpheus/op-1:refs/heads/orpheus/op-1")
	runGit(t, repoPath, "checkout", "main")
	mainBefore := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "refs/heads/main"))

	if err := orpheusgit.VerifyRemoteBranch(context.Background(), repoPath, "orpheus/op-1"); err != nil {
		t.Fatalf("verify existing task branch destination: %v", err)
	}
	if _, err := orpheusgit.MergeTaskBranchIntoDestination(context.Background(), task.Repository{
		ID: "alpha", Path: repoPath, DefaultBranch: "main",
	}, "orpheus/op-1", "orpheus/op-1"); err == nil || !strings.Contains(err.Error(), "is the task branch") {
		t.Fatalf("direct merge error = %v, want task-branch destination rejection", err)
	}
	if got := strings.TrimSpace(runGit(t, repoPath, "branch", "--show-current")); got != "main" {
		t.Fatalf("current branch = %q, want main; task destination must be rejected before checkout", got)
	}
	if got := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "refs/heads/main")); got != mainBefore {
		t.Fatalf("main = %s, want unchanged %s; task destination must be rejected before merge or push", got, mainBefore)
	}
}
