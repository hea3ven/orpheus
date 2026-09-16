//go:build integration

package cli_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationRepoBeadsDirResolvesRegisteredSourceByIDNameOrPrefix(t *testing.T) {
	for _, mode := range []string{registry.BeadsModeLocal, registry.BeadsModeManaged} {
		t.Run(mode, func(t *testing.T) {
			fixture := newRepoConfigFixture(t)
			repo := aRegisteredRepo("alpha-id")
			repo.Name, repo.BeadsPrefix, repo.BeadsMode = "Alpha Repo", "alpha-prefix", mode
			fixture.withRegisteredRepos(repo)
			wantDir := repo.Path
			if mode == registry.BeadsModeManaged {
				var err error
				wantDir, err = fixture.registryStore.ManagedBeadsDir(repo.ID)
				require.NoError(t, err)
			}
			for _, token := range []string{repo.ID, repo.Name, repo.BeadsPrefix} {
				t.Run(token, func(t *testing.T) {
					stdout, stderr, err := fixture.execute("repo", "beads-dir", token)

					require.NoError(t, err)
					assert.Equal(t, wantDir+"\n", stdout)
					assert.Empty(t, stderr)
				})
			}
		})
	}
}

func TestIntegrationRepoBeadsDirVerboseEmitsDiagnosticsToStderr(t *testing.T) {
	fixture := newRepoConfigFixture(t)
	repo := aRegisteredRepo("alpha-id")
	repo.BeadsPrefix = "alpha-prefix"
	fixture.withRegisteredRepos(repo)

	stdout, stderr, err := fixture.execute("--verbose", "repo", "beads-dir", repo.BeadsPrefix)

	require.NoError(t, err)
	assert.Equal(t, repo.Path+"\n", stdout)
	assert.NotContains(t, stdout, "level=DEBUG")
	assertDiagnosticLine(t, stderr, "level=DEBUG", "operation=repo_beads_dir", "token=alpha-prefix", "repo_id=alpha-id", "beads_dir="+repo.Path)
}

func TestIntegrationRepoBeadsDirRejectsUnknownRepo(t *testing.T) {
	fixture := newRepoConfigFixture(t)

	stdout, stderr, err := fixture.execute("repo", "beads-dir", "missing")

	assert.ErrorContains(t, err, `repo "missing" is not registered`)
	assert.ErrorContains(t, err, "orpheus repo list")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
}
