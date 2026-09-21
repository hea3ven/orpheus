//go:build integration

package cli_test

import (
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/cli"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowRootCommandCopiesExplicitStatePaths(t *testing.T) {
	t.Parallel()
	paths, err := state.NewMemoryPaths("/fixture/config", "/fixture/data")
	require.NoError(t, err)
	seedRegisteredRepo(t, paths, "alpha")
	command := cli.NewRootCommandWithOptions(cli.CommandOptions{Paths: &paths, Environment: map[string]string{}})
	paths, err = state.NewMemoryPaths("/fixture/other-config", "/fixture/other-data")
	require.NoError(t, err)
	seedRegisteredRepo(t, paths, "beta")

	stdout, stderr, err := executeRootCommand(command, "repo", "list")

	require.NoError(t, err, "stderr: %s", stderr)
	assert.Contains(t, stdout, "alpha")
	assert.NotContains(t, stdout, "beta")
}

func TestIntegrationWorkflowRootCommandCopiesExplicitEnvironmentForPathResolution(t *testing.T) {
	t.Parallel()
	root := testutil.CanonicalTempDir(t)
	environment := map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(root, "config"),
		"XDG_DATA_HOME":   filepath.Join(root, "data"),
	}
	paths, err := state.Resolve(state.ResolveOptions{Env: environment})
	require.NoError(t, err)
	seedRegisteredRepo(t, paths, "alpha")
	command := cli.NewRootCommandWithOptions(cli.CommandOptions{Environment: environment})
	environment["XDG_CONFIG_HOME"] = "invalid-relative-root"
	environment["XDG_DATA_HOME"] = "invalid-relative-root"

	stdout, stderr, err := executeRootCommand(command, "repo", "list")

	require.NoError(t, err, "stderr: %s", stderr)
	assert.Contains(t, stdout, "alpha")
}

func TestIntegrationWorkflowRootCommandUsesProcessEnvironmentOnlyWhenNotSupplied(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "invalid-process-root")

	_, _, defaultErr := executeRootCommand(cli.NewRootCommand(), "repo", "list")
	_, _, emptyEnvErr := executeRootCommand(cli.NewRootCommandWithOptions(cli.CommandOptions{
		Environment: map[string]string{},
	}), "repo", "list")

	assert.ErrorContains(t, defaultErr, "XDG_CONFIG_HOME must be an absolute path")
	assert.ErrorContains(t, emptyEnvErr, "home directory is required")
}

func seedRegisteredRepo(t *testing.T, paths state.Paths, repoID string) {
	t.Helper()
	require.NoError(t, registry.NewStore(paths).Save(registry.Registry{Repos: []registry.Repo{{
		ID: repoID, Name: repoID, Path: "/fixture/repos/" + repoID, DefaultBranch: "main",
		BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "op",
	}}}))
}
