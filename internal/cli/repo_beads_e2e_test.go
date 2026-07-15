package cli_test

import (
	"context"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/registry"
)

func TestMain(m *testing.M) {
	code := m.Run()
	cleanupLocalBeadsFixture()
	os.Exit(code)
}

func TestRepoAddDetectsLocalBeadsEndToEnd(t *testing.T) {
	requireBD(t)
	withoutBeadsEnv(t)

	root := newTestState(t)
	repoPath := newRepoWithCopiedLocalBeads(t, root, "alpha")

	addOut, addErr := executeCommand(t, []string{"repo", "add", repoPath})
	if addErr != "" {
		t.Fatalf("repo add stderr = %q, want empty", addErr)
	}
	for _, want := range []string{"Added repo alpha", repoPath, "git@example.com:org/alpha.git", "main", "local", "op"} {
		if !strings.Contains(addOut, want) {
			t.Fatalf("repo add output = %q, want substring %q", addOut, want)
		}
	}

	listOut, listErr := executeCommand(t, []string{"repo", "list"})
	if listErr != "" {
		t.Fatalf("repo list stderr = %q, want empty", listErr)
	}
	for _, want := range []string{"BEADS_MODE", "BEADS_PREFIX", "alpha", "local", "op"} {
		if !strings.Contains(listOut, want) {
			t.Fatalf("repo list output = %q, want substring %q", listOut, want)
		}
	}
}

func TestRepoAddInitializesManagedBeadsEndToEnd(t *testing.T) {
	requireBD(t)
	withoutBeadsEnv(t)

	repoPath := newTestRepoPath(t)
	paths := currentTestPaths(t)

	addOut, addErr := executeCommand(t, []string{"repo", "add", repoPath})
	if addErr != "" {
		t.Fatalf("repo add stderr = %q, want empty", addErr)
	}
	for _, want := range []string{"Added repo alpha", repoPath, "git@example.com:org/alpha.git", "main", "managed", "alpha"} {
		if !strings.Contains(addOut, want) {
			t.Fatalf("repo add output = %q, want substring %q", addOut, want)
		}
	}

	managedDir, err := registry.ManagedBeadsDir(paths, "alpha")
	if err != nil {
		t.Fatalf("managed Beads dir: %v", err)
	}
	if info, err := os.Stat(filepath.Join(managedDir, ".beads")); err != nil || !info.IsDir() {
		t.Fatalf("managed .beads directory was not created: info=%v err=%v", info, err)
	}

	prefixOut := runBD(t, managedDir, "--json", "--readonly", "config", "get", "issue_prefix")
	if !strings.Contains(prefixOut, `"value":"alpha"`) && !strings.Contains(prefixOut, `"value": "alpha"`) {
		t.Fatalf("managed Beads prefix output = %q, want alpha", prefixOut)
	}
}

func TestRepoRegistrationFlowEndToEnd(t *testing.T) {
	requireBD(t)
	withoutBeadsEnv(t)

	root := newTestState(t)
	paths := currentTestPaths(t)
	localPath := newRepoWithCopiedLocalBeads(t, root, "localrepo")
	managedPath := newTestRepoAt(t, root, filepath.Join("repos", "managedrepo"), testRepoConfig{withRemote: true})

	localAddOut, localAddErr := executeCommand(t, []string{"repo", "add", localPath})
	if localAddErr != "" {
		t.Fatalf("local repo add stderr = %q, want empty", localAddErr)
	}
	for _, want := range []string{"Added repo localrepo", localPath, "local", "op"} {
		if !strings.Contains(localAddOut, want) {
			t.Fatalf("local repo add output = %q, want substring %q", localAddOut, want)
		}
	}

	managedAddOut, managedAddErr := executeCommand(t, []string{"repo", "add", managedPath})
	if managedAddErr != "" {
		t.Fatalf("managed repo add stderr = %q, want empty", managedAddErr)
	}
	for _, want := range []string{"Added repo managedrepo", managedPath, "managed", "managedrepo"} {
		if !strings.Contains(managedAddOut, want) {
			t.Fatalf("managed repo add output = %q, want substring %q", managedAddOut, want)
		}
	}

	listOut, listErr := executeCommand(t, []string{"repo", "list"})
	if listErr != "" {
		t.Fatalf("repo list stderr = %q, want empty", listErr)
	}
	for _, want := range []string{"ID", "NAME", "PATH", "REMOTE", "DEFAULT_BRANCH", "BEADS_MODE", "BEADS_PREFIX", "localrepo", "managedrepo", "local", "managed", "op"} {
		if !strings.Contains(listOut, want) {
			t.Fatalf("repo list output = %q, want substring %q", listOut, want)
		}
	}

	managedDir, err := registry.ManagedBeadsDir(paths, "managedrepo")
	if err != nil {
		t.Fatalf("managed Beads dir: %v", err)
	}

	assertBeadsDir(t, "localrepo", localPath)
	assertBeadsDir(t, "op", localPath)
	assertBeadsDir(t, "managedrepo", managedDir)

	runBD(t, localPath, "list")
	runBD(t, managedDir, "list")
}

func TestRepoAddRejectsDuplicateLocalBeadsPrefixEndToEnd(t *testing.T) {
	requireBD(t)
	withoutBeadsEnv(t)

	root := newTestState(t)
	alphaPath := newRepoWithCopiedLocalBeads(t, root, "alpha")
	betaPath := newRepoWithCopiedLocalBeads(t, root, "beta")

	_, addErr := executeCommand(t, []string{"repo", "add", alphaPath})
	if addErr != "" {
		t.Fatalf("first repo add stderr = %q, want empty", addErr)
	}

	stdout, _, err := executeCommandWithError(t, []string{"repo", "add", betaPath})
	if err == nil {
		t.Fatal("second repo add succeeded, want duplicate prefix error")
	}
	if stdout != "" {
		t.Fatalf("second repo add stdout = %q, want empty", stdout)
	}
	if !strings.Contains(err.Error(), "duplicate beads prefix \"op\"") {
		t.Fatalf("second repo add error = %v, want duplicate beads prefix", err)
	}
}

func assertBeadsDir(t *testing.T, token string, want string) {
	t.Helper()

	stdout, stderr := executeCommand(t, []string{"repo", "beads-dir", token})
	if stderr != "" {
		t.Fatalf("repo beads-dir %q stderr = %q, want empty", token, stderr)
	}
	if got := strings.TrimSpace(stdout); got != want {
		t.Fatalf("repo beads-dir %q = %q, want %q", token, got, want)
	}
}

func newRepoWithCopiedLocalBeads(t *testing.T, root string, name string) string {
	t.Helper()

	repoPath := filepath.Join(root, "repos", name)
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	initGitRepo(t, repoPath)
	copyDir(t, localBeadsFixtureDir(t), filepath.Join(repoPath, ".beads"))

	runGit(t, repoPath, "remote", "add", "origin", "git@example.com:org/"+name+".git")
	runGit(t, repoPath, "update-ref", "refs/remotes/origin/main", "HEAD")
	runGit(t, repoPath, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return repoPath
}

func requireBD(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("bd"); err != nil {
		t.Fatalf("bd executable not found: install Beads or ensure bd is on PATH: %v", err)
	}
}

var (
	localBeadsFixtureOnce sync.Once
	localBeadsFixtureRoot string
	localBeadsFixturePath string
	localBeadsFixtureErr  error
)

func localBeadsFixtureDir(t *testing.T) string {
	t.Helper()

	localBeadsFixtureOnce.Do(func() {
		root, err := os.MkdirTemp("", "orpheus-local-beads-fixture-*")
		if err != nil {
			localBeadsFixtureErr = err
			return
		}
		localBeadsFixtureRoot = root

		repoPath := filepath.Join(root, "repo")
		if err := os.MkdirAll(repoPath, 0o755); err != nil {
			localBeadsFixtureErr = err
			return
		}
		initGitRepo(t, repoPath)

		// Initialize Beads before adding a fake remote in per-test repos so bd does
		// not try to inspect or configure backup behavior against a non-routable URL.
		runBD(t, repoPath, "init", "--non-interactive", "--prefix", "op", "--skip-agents", "--skip-hooks", "--quiet")
		localBeadsFixturePath = filepath.Join(repoPath, ".beads")
	})
	if localBeadsFixtureErr != nil {
		t.Fatalf("create local Beads fixture: %v", localBeadsFixtureErr)
	}
	return localBeadsFixturePath
}

func cleanupLocalBeadsFixture() {
	if localBeadsFixtureRoot == "" {
		return
	}
	_ = os.RemoveAll(localBeadsFixtureRoot)
}

func runBD(t *testing.T, dir string, args ...string) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, "bd", args...)
	command.Dir = dir
	command.Env = append(sanitizedBeadsEnv(), "BD_NON_INTERACTIVE=1")
	output, err := command.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("bd %v timed out\n%s", args, output)
	}
	if err != nil {
		t.Fatalf("bd %v failed: %v\n%s", args, err, output)
	}
	return string(output)
}

func sanitizedBeadsEnv() []string {
	env := os.Environ()
	filtered := env[:0]
	for _, entry := range env {
		key, _, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(key, "BEADS_") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func withoutBeadsEnv(t *testing.T) {
	t.Helper()

	original := os.Environ()
	for _, entry := range original {
		key, _, ok := strings.Cut(entry, "=")
		if ok && strings.HasPrefix(key, "BEADS_") {
			if err := os.Unsetenv(key); err != nil {
				t.Fatalf("unset %s: %v", key, err)
			}
		}
	}
	t.Cleanup(func() {
		os.Clearenv()
		for _, entry := range original {
			key, value, ok := strings.Cut(entry, "=")
			if ok {
				_ = os.Setenv(key, value)
			}
		}
	})
}

func copyDir(t *testing.T, src string, dst string) {
	t.Helper()

	if err := filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		info, err := entry.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch {
		case entry.IsDir():
			return os.MkdirAll(target, mode.Perm())
		case mode.Type() == 0:
			return copyFile(path, target, mode.Perm())
		default:
			return nil
		}
	}); err != nil {
		t.Fatalf("copy %s to %s: %v", src, dst, err)
	}
}

func copyFile(src string, dst string, mode fs.FileMode) error {
	input, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}

	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}
