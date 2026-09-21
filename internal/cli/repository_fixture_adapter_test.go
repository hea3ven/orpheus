//go:build integration

//nolint:testpackage // Checks private repository-fixture infrastructure used by binary E2E.
package cli

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// This is a contract of the seeded repository fixture, not a CLI product contract.
// Binary E2E still consumes this helper and requires independent local origins.
func TestIntegrationAdapterContractCLIRepositoryFixtureCreatesIndependentOrigins(t *testing.T) {
	root := newTestState(t)
	firstRepo := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "first"))
	secondRepo := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "second"))

	firstOrigin := strings.TrimSpace(runGit(t, firstRepo, "remote", "get-url", "origin"))
	secondOrigin := strings.TrimSpace(runGit(t, secondRepo, "remote", "get-url", "origin"))
	require.NotEqual(t, firstOrigin, secondOrigin)

	runGit(t, firstRepo, "checkout", "-b", "only-first")
	runGit(t, firstRepo, "commit", "--allow-empty", "-m", "only first")
	runGit(t, firstRepo, "push", "origin", "only-first")

	command := exec.Command("git", "show-ref", "--verify", "--quiet", "refs/remotes/origin/only-first")
	command.Dir = secondRepo
	require.Error(t, command.Run())
	require.Equal(t, "main\n", runGit(t, secondRepo, "branch", "--show-current"))
}
