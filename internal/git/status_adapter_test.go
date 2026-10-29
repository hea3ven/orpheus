//go:build integration

package git_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	orpheusgit "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestIntegrationAdapterContractHasStagedChangesDistinguishesChangesFromGitFailure(t *testing.T) {
	repoPath := newGitRepo(t)
	trackedPath := filepath.Join(repoPath, "tracked.txt")
	commitTrackedFile(t, repoPath, "tracked.txt", "base\n")

	got, err := orpheusgit.HasStagedChanges(context.Background(), repoPath)
	if err != nil {
		t.Fatalf("has staged changes before edit: %v", err)
	}
	if got {
		t.Fatal("has staged changes before edit = true, want false")
	}

	if err := os.WriteFile(trackedPath, []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("change tracked file: %v", err)
	}
	runGit(t, repoPath, "add", "tracked.txt")

	got, err = orpheusgit.HasStagedChanges(context.Background(), repoPath)
	if err != nil {
		t.Fatalf("has staged changes after edit: %v", err)
	}
	if !got {
		t.Fatal("has staged changes after edit = false, want true")
	}

	got, err = orpheusgit.HasStagedChanges(context.Background(), testutil.CanonicalTempDir(t))
	if err == nil {
		t.Fatal("has staged changes outside repository succeeded, want error")
	}
	if got {
		t.Fatal("has staged changes outside repository = true, want false")
	}
	if !strings.Contains(err.Error(), "inspect staged changes: git diff --cached --quiet") {
		t.Fatalf("error = %v, want staged inspection context", err)
	}
}

func TestIntegrationAdapterContractCandidateGitOperationsInspectChangesAndCleanOnlyUntracked(t *testing.T) {
	repoPath := newGitRepo(t)
	trackedPath := filepath.Join(repoPath, "tracked.txt")
	commitTrackedFile(t, repoPath, "tracked.txt", "base\n")

	if err := os.WriteFile(trackedPath, []byte("changed\n"), 0o644); err != nil {
		t.Fatalf("change tracked file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(repoPath, "nested"), 0o755); err != nil {
		t.Fatalf("create nested dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "nested", "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	status, err := orpheusgit.CandidateStatus(context.Background(), repoPath)
	if err != nil {
		t.Fatalf("candidate status: %v", err)
	}
	if !strings.Contains(string(status), " M tracked.txt\x00") ||
		!strings.Contains(string(status), "?? nested/\x00") {
		t.Fatalf("candidate status = %q, want tracked entry and untracked directory", status)
	}

	untracked, err := orpheusgit.UntrackedFiles(context.Background(), repoPath)
	if err != nil {
		t.Fatalf("untracked files: %v", err)
	}
	if len(untracked) != 1 || untracked[0] != "nested/new.txt" {
		t.Fatalf("untracked files = %#v, want nested/new.txt", untracked)
	}

	if err := orpheusgit.CleanUntrackedFiles(context.Background(), repoPath); err != nil {
		t.Fatalf("clean untracked files: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoPath, "nested", "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("cleaned untracked path stat = %v, want absent", err)
	}
	tracked, err := os.ReadFile(trackedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(tracked) != "changed\n" {
		t.Fatalf("tracked bytes = %q, want preserved change", tracked)
	}
	if got := strings.TrimSpace(runGit(t, repoPath, "status", "--short")); got != "M tracked.txt" {
		t.Fatalf("status after clean = %q, want tracked modification", got)
	}
}

func commitTrackedFile(t *testing.T, repoPath string, name string, contents string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(repoPath, name), []byte(contents), 0o644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}
	runGit(t, repoPath, "add", name)
	runGit(t, repoPath,
		"-c", "user.name=Orpheus Test",
		"-c", "user.email=orpheus@example.com",
		"commit", "-m", "add tracked",
	)
}
