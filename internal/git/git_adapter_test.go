//go:build integration

package git_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	orpheusgit "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestIntegrationAdapterContractInspectDetectsRootRemoteAndOriginHEAD(t *testing.T) {
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

func TestIntegrationAdapterContractInspectMissingRemoteKeepsDefaultBranchCandidate(t *testing.T) {
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

func TestIntegrationAdapterContractInspectFallsBackToCurrentBranchWhenOriginHEADIsMissing(t *testing.T) {
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

func TestIntegrationAdapterContractInspectRejectsNonGitPath(t *testing.T) {
	_, err := orpheusgit.Inspect(testutil.CanonicalTempDir(t))
	if err == nil {
		t.Fatal("inspect non-git path succeeded, want error")
	}
	if !errors.Is(err, orpheusgit.ErrNotRepository) {
		t.Fatalf("error = %v, want ErrNotRepository", err)
	}
}

func TestIntegrationAdapterContractInspectDetachedHEADUsesOriginDefaultBranch(t *testing.T) {
	repoPath := newGitRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://example.com/repo.git")
	runGit(t, repoPath, "update-ref", "refs/remotes/origin/main", "HEAD")
	runGit(t, repoPath, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	runGit(t, repoPath, "checkout", "--detach", "HEAD")

	got, err := orpheusgit.Inspect(repoPath)
	if err != nil {
		t.Fatalf("inspect git repo: %v", err)
	}
	if got.DefaultBranchCandidate != "main" || got.DefaultBranchSource != orpheusgit.DefaultBranchSourceOriginHEAD {
		t.Fatalf("default branch = %q from %q, want main from origin/HEAD", got.DefaultBranchCandidate, got.DefaultBranchSource)
	}
	if got.CurrentBranch != "" {
		t.Fatalf("current branch = %q, want empty detached HEAD", got.CurrentBranch)
	}
}

func TestIntegrationAdapterContractInspectMalformedOriginHEADFallsBackToCurrentBranch(t *testing.T) {
	repoPath := newGitRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://example.com/repo.git")
	originDir := filepath.Join(repoPath, ".git", "refs", "remotes", "origin")
	if err := os.MkdirAll(originDir, 0o755); err != nil {
		t.Fatalf("create origin refs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(originDir, "HEAD"), []byte("not-a-ref\n"), 0o644); err != nil {
		t.Fatalf("write malformed origin HEAD: %v", err)
	}

	got, err := orpheusgit.Inspect(repoPath)
	if err != nil {
		t.Fatalf("inspect git repo: %v", err)
	}
	if got.DefaultBranchCandidate != "main" || got.DefaultBranchSource != orpheusgit.DefaultBranchSourceCurrentBranch {
		t.Fatalf("default branch = %q from %q, want main from current branch", got.DefaultBranchCandidate, got.DefaultBranchSource)
	}
}

func newGitRepo(t *testing.T) string {
	t.Helper()

	repoPath := testutil.CanonicalTempDir(t)
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
