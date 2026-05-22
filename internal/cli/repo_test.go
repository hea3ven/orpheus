package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	for _, want := range []string{"ID", "NAME", "PATH", "REMOTE", "DEFAULT_BRANCH", "BEADS_MODE", "BEADS_PREFIX", "alpha", repoPath, "git@example.com:org/alpha.git", "main"} {
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
