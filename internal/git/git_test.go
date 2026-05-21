package git_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	orpheusgit "github.com/hea3ven/orpheus/internal/git"
)

func TestInspectDetectsRootRemoteAndOriginHEAD(t *testing.T) {
	repoPath := newGitRepo(t)
	runGit(t, repoPath, "remote", "add", "upstream", "https://example.com/upstream.git")
	runGit(t, repoPath, "remote", "add", "origin", "git@example.com:org/repo.git")
	runGit(t, repoPath, "update-ref", "refs/remotes/origin/trunk", "HEAD")
	runGit(t, repoPath, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/trunk")
	runGit(t, repoPath, "checkout", "-b", "feature/local")

	nestedPath := filepath.Join(repoPath, "nested", "dir")
	if err := os.MkdirAll(nestedPath, 0o755); err != nil {
		t.Fatalf("create nested path: %v", err)
	}

	got, err := orpheusgit.Inspect(nestedPath)
	if err != nil {
		t.Fatalf("inspect git repo: %v", err)
	}

	if got.Root != repoPath {
		t.Fatalf("root = %q, want %q", got.Root, repoPath)
	}
	if got.RemoteCandidateName != "origin" || got.RemoteCandidate != "git@example.com:org/repo.git" {
		t.Fatalf("remote candidate = %q %q, want origin URL", got.RemoteCandidateName, got.RemoteCandidate)
	}
	if got.DefaultBranchCandidate != "trunk" || got.DefaultBranchSource != orpheusgit.DefaultBranchSourceOriginHEAD {
		t.Fatalf("default branch = %q from %q, want trunk from origin/HEAD", got.DefaultBranchCandidate, got.DefaultBranchSource)
	}
	if got.CurrentBranch != "feature/local" {
		t.Fatalf("current branch = %q, want feature/local", got.CurrentBranch)
	}
	if got.RemoteErr != nil || got.DefaultBranchErr != nil {
		t.Fatalf("optional errors = remote %v default branch %v, want nil", got.RemoteErr, got.DefaultBranchErr)
	}
}

func TestInspectMissingRemoteKeepsDefaultBranchCandidate(t *testing.T) {
	repoPath := newGitRepo(t)

	got, err := orpheusgit.Inspect(repoPath)
	if err != nil {
		t.Fatalf("inspect git repo: %v", err)
	}

	if !errors.Is(got.RemoteErr, orpheusgit.ErrNoRemote) {
		t.Fatalf("remote error = %v, want ErrNoRemote", got.RemoteErr)
	}
	if got.RemoteCandidate != "" || got.RemoteCandidateName != "" {
		t.Fatalf("remote candidate = %q %q, want empty", got.RemoteCandidateName, got.RemoteCandidate)
	}
	if got.DefaultBranchCandidate != "main" || got.DefaultBranchSource != orpheusgit.DefaultBranchSourceCurrentBranch {
		t.Fatalf("default branch = %q from %q, want main from current branch", got.DefaultBranchCandidate, got.DefaultBranchSource)
	}
}

func TestInspectFallsBackToCurrentBranchWhenOriginHEADIsMissing(t *testing.T) {
	repoPath := newGitRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://example.com/repo.git")
	runGit(t, repoPath, "checkout", "-b", "feature")

	got, err := orpheusgit.Inspect(repoPath)
	if err != nil {
		t.Fatalf("inspect git repo: %v", err)
	}

	if got.DefaultBranchCandidate != "feature" || got.DefaultBranchSource != orpheusgit.DefaultBranchSourceCurrentBranch {
		t.Fatalf("default branch = %q from %q, want feature from current branch", got.DefaultBranchCandidate, got.DefaultBranchSource)
	}
	if got.RemoteCandidate != "https://example.com/repo.git" {
		t.Fatalf("remote candidate = %q, want origin URL", got.RemoteCandidate)
	}
}

func TestInspectRejectsNonGitPath(t *testing.T) {
	_, err := orpheusgit.Inspect(t.TempDir())
	if err == nil {
		t.Fatal("inspect non-git path succeeded, want error")
	}
	if !errors.Is(err, orpheusgit.ErrNotRepository) {
		t.Fatalf("error = %v, want ErrNotRepository", err)
	}
}

func newGitRepo(t *testing.T) string {
	t.Helper()

	repoPath := t.TempDir()
	runGit(t, repoPath, "init")
	runGit(t, repoPath, "checkout", "-b", "main")
	runGit(t, repoPath,
		"-c", "user.name=Orpheus Test",
		"-c", "user.email=orpheus@example.com",
		"commit", "--allow-empty", "-m", "initial",
	)
	return repoPath
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return string(output)
}
