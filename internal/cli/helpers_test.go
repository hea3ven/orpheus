package cli_test

import (
	"bytes"
	"io"
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

	root := newTestState(t)

	config := testRepoConfig{withRemote: true}
	for _, opt := range opts {
		opt(&config)
	}

	return newTestRepoAt(t, root, filepath.Join("repos", "alpha"), config)
}

func newTestRepoAt(t *testing.T, root string, relativePath string, config testRepoConfig) string {
	t.Helper()
	must := require.New(t)

	repoPath := filepath.Join(root, relativePath)
	must.NoError(os.MkdirAll(repoPath, 0o755))
	initGitRepo(t, repoPath)
	if config.withRemote {
		name := filepath.Base(repoPath)
		runGit(t, repoPath, "remote", "add", "origin", "git@example.com:org/"+name+".git")
		runGit(t, repoPath, "update-ref", "refs/remotes/origin/main", "HEAD")
		runGit(t, repoPath, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	}
	return repoPath
}

func newTestRepoWithLocalOriginAt(t *testing.T, root string, relativePath string) string {
	t.Helper()
	must := require.New(t)

	originPath := filepath.Join(root, "origins", filepath.Base(relativePath)+".git")
	must.NoError(os.MkdirAll(originPath, 0o755))
	runGit(t, originPath, "init", "--bare")
	runGit(t, originPath, "symbolic-ref", "HEAD", "refs/heads/main")

	repoPath := filepath.Join(root, relativePath)
	must.NoError(os.MkdirAll(repoPath, 0o755))
	initGitRepo(t, repoPath)
	runGit(t, repoPath, "remote", "add", "origin", originPath)
	runGit(t, repoPath, "push", "--set-upstream", "origin", "main")
	runGit(t, repoPath, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return repoPath
}

func newTestState(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg-data"))
	clearOrpheusAgentEnv(t)
	return root
}

func clearOrpheusAgentEnv(t *testing.T) {
	t.Helper()

	for _, name := range []string{
		"ORPHEUS_REPO_ID",
		"ORPHEUS_TASK_ID",
		"ORPHEUS_WORKTREE",
		"ORPHEUS_BRANCH",
		"ORPHEUS_AGENT_PROMPT",
		"ORPHEUS_AGENT_PURPOSE",
		"ORPHEUS_CONFLICT_FILES",
		"ORPHEUS_REVIEW_ATTEMPT",
		"ORPHEUS_REVIEW_STEP",
	} {
		t.Setenv(name, "")
	}
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
	return executeCommandWithInputAndError(t, args, nil)
}

func executeCommandWithInput(t *testing.T, args []string, input string) (stdout string, stderr string) {
	t.Helper()
	must := require.New(t)

	stdout, stderr, err := executeCommandWithInputAndError(t, args, []byte(input))
	must.NoError(err, "execute %v\nstderr: %s", args, stderr)
	return stdout, stderr
}

func executeCommandWithInputAndError(t *testing.T, args []string, input []byte) (stdout string, stderr string, err error) {
	t.Helper()
	return executeCommandWithReaderAndError(t, args, bytes.NewBuffer(input))
}

func executeCommandWithScriptedInput(t *testing.T, args []string, input ...string) (stdout string, stderr string) {
	t.Helper()
	must := require.New(t)

	stdout, stderr, err := executeCommandWithReaderAndError(t, args, &scriptedInput{chunks: input})
	must.NoError(err, "execute %v\nstderr: %s", args, stderr)
	return stdout, stderr
}

type scriptedInput struct {
	chunks []string
	index  int
	offset int
}

func (r *scriptedInput) Read(p []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	chunk := r.chunks[r.index]
	if chunk == "" {
		r.index++
		r.offset = 0
		return 0, io.EOF
	}
	n := copy(p, chunk[r.offset:])
	r.offset += n
	if r.offset >= len(chunk) {
		r.index++
		r.offset = 0
	}
	return n, nil
}

func executeCommandWithReaderAndError(t *testing.T, args []string, input io.Reader) (stdout string, stderr string, err error) {
	t.Helper()

	cmd := cli.NewRootCommand()
	out := new(bytes.Buffer)
	errOut := new(bytes.Buffer)
	cmd.SetIn(input)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}

func withFakeBDInit(t *testing.T) string {
	t.Helper()

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := `#!/bin/sh
if [ -n "${FAKE_BD_LOCK_PATH-}" ] && [ ! -f "$FAKE_BD_LOCK_PATH" ]; then
  printf 'missing lock: %s\n' "$FAKE_BD_LOCK_PATH" >&2
  exit 43
fi
{
  pwd
  printf '%s\n' "$@"
  printf 'BD_NON_INTERACTIVE=%s\n' "${BD_NON_INTERACTIVE-unset}"
  printf 'BEADS_DIR=%s\n' "${BEADS_DIR-unset}"
} >> "$FAKE_BD_LOG"
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("FAKE_BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}
