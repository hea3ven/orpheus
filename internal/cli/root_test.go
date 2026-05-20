package cli_test

import (
	"bytes"
	"os"
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
	is.Contains(addOut, "Added repo alpha")
	is.Contains(addOut, repoPath)

	listOut, listErr := executeCommand(t, []string{"repo", "list"})
	is.Empty(listErr)
	for _, want := range []string{"ID", "NAME", "PATH", "alpha", repoPath} {
		is.Contains(listOut, want)
	}
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

func newTestRepoPath(t *testing.T) string {
	t.Helper()
	must := require.New(t)

	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg-config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg-data"))

	repoPath := filepath.Join(root, "repos", "alpha")
	must.NoError(os.MkdirAll(repoPath, 0o755))
	return repoPath
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
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	cmd.SetArgs(args)
	err = cmd.Execute()
	return out.String(), errOut.String(), err
}
