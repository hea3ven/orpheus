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

func TestIntegrationAdapterContractInspectChoosesDefaultBranchAcrossRemoteAndCheckoutStates(t *testing.T) {
	for _, kind := range []string{"no remote", "missing remote HEAD", "malformed remote HEAD", "detached checkout"} {
		t.Run(kind, func(t *testing.T) {
			repo := newGitRepo(t)
			current, branch, source := "main", "main", orpheusgit.DefaultBranchSourceCurrentBranch
			if kind != "no remote" {
				runGit(t, repo, "remote", "add", "origin", "https://example.com/repo.git")
			}
			switch kind {
			case "missing remote HEAD":
				runGit(t, repo, "checkout", "-b", "feature")
				current, branch = "feature", "feature"
			case "malformed remote HEAD":
				dir := filepath.Join(repo, ".git", "refs", "remotes", "origin")
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("not-a-ref\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "detached checkout":
				runGit(t, repo, "update-ref", "refs/remotes/origin/main", "HEAD")
				runGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
				runGit(t, repo, "checkout", "--detach", "HEAD")
				current, source = "", orpheusgit.DefaultBranchSourceOriginHEAD
			}

			got, err := orpheusgit.Inspect(repo)

			if err != nil {
				t.Fatal(err)
			}
			if got.Root != repo || got.CurrentBranch != current || got.DefaultBranchCandidate != branch || got.DefaultBranchSource != source || got.DefaultBranchErr != nil {
				t.Fatalf("inspection = %#v, want current %q default %q from %q", got, current, branch, source)
			}
			if kind == "no remote" {
				if !errors.Is(got.RemoteErr, orpheusgit.ErrNoRemote) || got.RemoteCandidate != "" || got.RemoteCandidateName != "" {
					t.Fatalf("unexpected remote: %#v", got)
				}
			} else if got.RemoteErr != nil || got.RemoteCandidateName != "origin" || got.RemoteCandidate != "https://example.com/repo.git" {
				t.Fatalf("lost origin: %#v", got)
			}
		})
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

func newGitRepo(t *testing.T) string {
	t.Helper()

	// Inherited identities override repository configuration. Setenv registers
	// cleanup, but Git needs these variables absent rather than empty.
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}
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
