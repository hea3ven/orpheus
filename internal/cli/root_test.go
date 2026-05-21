package cli_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRootCommandHelp(t *testing.T) {
	is := assert.New(t)

	output, _ := executeCommand(t, []string{"--help"})

	is.Contains(output, "Orpheus")
	is.Contains(output, "Usage:")
	is.Contains(output, "--verbose")
}

func TestRootCommandDoesNotEmitDebugByDefault(t *testing.T) {
	is := assert.New(t)

	stdout, stderr := executeCommand(t, []string{})

	is.NotContains(stdout, "level=DEBUG")
	is.NotContains(stderr, "level=DEBUG")
}

func TestRootCommandVerboseEmitsDebugToStderr(t *testing.T) {
	is := assert.New(t)

	stdout, stderr := executeCommand(t, []string{"--verbose"})

	is.NotContains(stdout, "level=DEBUG")
	is.Contains(stderr, "level=DEBUG")
	is.Contains(stderr, "msg=\"rendering root help\"")
}

func TestRepoAddAndListFlow(t *testing.T) {
	is := assert.New(t)

	repoPath := newTestRepoPath(t)

	addOut, addErr := executeCommand(t, []string{"repo", "add", repoPath})
	is.Empty(addErr)
	for _, want := range []string{"Added repo alpha", repoPath, "git@example.com:org/alpha.git", "main"} {
		is.Contains(addOut, want)
	}

	listOut, listErr := executeCommand(t, []string{"repo", "list"})
	is.Empty(listErr)
	for _, want := range []string{"ID", "NAME", "PATH", "REMOTE", "DEFAULT_BRANCH", "alpha", repoPath, "git@example.com:org/alpha.git", "main"} {
		is.Contains(listOut, want)
	}
}

func TestRepoAddStoresGitRootWhenPathIsNested(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	repoPath := newTestRepoPath(t)
	nestedPath := filepath.Join(repoPath, "nested", "dir")
	must.NoError(os.MkdirAll(nestedPath, 0o755))

	addOut, addErr := executeCommand(t, []string{"repo", "add", nestedPath})
	is.Empty(addErr)
	is.Contains(addOut, repoPath)
	is.NotContains(addOut, nestedPath)

	listOut, listErr := executeCommand(t, []string{"repo", "list"})
	is.Empty(listErr)
	is.Contains(listOut, repoPath)
	is.NotContains(listOut, nestedPath)
}

func TestRepoAddWarnsWhenRemoteIsMissing(t *testing.T) {
	is := assert.New(t)

	repoPath := newTestRepoPath(t, withoutRemote())

	addOut, addErr := executeCommand(t, []string{"repo", "add", repoPath})
	is.Contains(addErr, "No Git remote detected")
	is.Contains(addErr, "using current branch \"main\"")
	is.Contains(addOut, "Added repo alpha")
	is.Contains(addOut, "main")

	listOut, listErr := executeCommand(t, []string{"repo", "list"})
	is.Empty(listErr)
	is.Contains(listOut, "alpha")
	is.Contains(listOut, "main")
}

func TestRepoAddRejectsNonGitPath(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	root := newTestState(t)
	nonGitPath := filepath.Join(root, "repos", "alpha")
	must.NoError(os.MkdirAll(nonGitPath, 0o755))

	stdout, _, err := executeCommandWithError(t, []string{"repo", "add", nonGitPath})
	must.Error(err)
	is.ErrorContains(err, "not a git worktree")
	is.Empty(stdout)
}

func TestRepoAddRejectsDuplicatePath(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	repoPath := newTestRepoPath(t)

	_, errOutput := executeCommand(t, []string{"repo", "add", repoPath})
	is.Empty(errOutput)

	stdout, _, err := executeCommandWithError(t, []string{"repo", "add", filepath.Join(repoPath, ".")})
	must.Error(err)
	is.ErrorContains(err, "duplicate repo path")
	is.Empty(stdout)
}

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
