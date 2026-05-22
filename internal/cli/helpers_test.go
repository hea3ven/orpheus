package cli_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/cli"
	"github.com/stretchr/testify/require"
)

type testRepoOption func(*testRepoConfig)

type testRepoConfig struct {
	withRemote bool
}

func withoutRemote() testRepoOption {
	return func(config *testRepoConfig) {
		config.withRemote = false
	}
}

func newTestRepoPath(t *testing.T, opts ...testRepoOption) string {
	t.Helper()
	must := require.New(t)

	root := newTestState(t)

	config := testRepoConfig{withRemote: true}
	for _, opt := range opts {
		opt(&config)
	}

	repoPath := filepath.Join(root, "repos", "alpha")
	must.NoError(os.MkdirAll(repoPath, 0o755))
	initGitRepo(t, repoPath)
	if config.withRemote {
		runGit(t, repoPath, "remote", "add", "origin", "git@example.com:org/alpha.git")
		runGit(t, repoPath, "update-ref", "refs/remotes/origin/main", "HEAD")
		runGit(t, repoPath, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	}
	return repoPath
}

func newTestState(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg-data"))
	return root
}

func initGitRepo(t *testing.T, repoPath string) {
	t.Helper()

	runGit(t, repoPath, "init")
	runGit(t, repoPath, "checkout", "-b", "main")
	runGit(t, repoPath,
		"-c", "user.name=Orpheus Test",
		"-c", "user.email=orpheus@example.com",
		"commit", "--allow-empty", "-m", "initial",
	)
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

func executeCommand(t *testing.T, args []string) (stdout string, stderr string) {
	t.Helper()
	must := require.New(t)

	stdout, stderr, err := executeCommandWithError(t, args)
	must.NoError(err, "execute %v\nstderr: %s", args, stderr)
	return stdout, stderr
}

func executeCommandWithError(t *testing.T, args []string) (stdout string, stderr string, err error) {
	t.Helper()

	cmd := cli.NewRootCommand()
	out := new(bytes.Buffer)
	errOut := new(bytes.Buffer)
	cmd.SetIn(bytes.NewBuffer(nil))
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}
