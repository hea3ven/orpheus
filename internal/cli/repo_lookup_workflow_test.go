//go:build integration

package cli_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowRepoBeadsDirResolvesRegisteredSourceByIDNameOrPrefix(t *testing.T) {
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

func TestIntegrationWorkflowRepoBeadsDirRejectsUnknownRepo(t *testing.T) {
	fixture := newRepoConfigFixture(t)

	stdout, stderr, err := fixture.execute("repo", "beads-dir", "missing")

	assert.ErrorContains(t, err, `repo "missing" is not registered`)
	assert.ErrorContains(t, err, "orpheus repo list")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
}

func TestIntegrationWorkflowTaskShowReportsMalformedAndUnknownPrefixes(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newRepoConfigFixture(t)
	repo := aRegisteredRepo("alpha")
	repo.Name, repo.BeadsPrefix = "Alpha", "op"
	fixture.withRegisteredRepos(repo)

	stdout, stderr, err := fixture.execute("task", "show", "notprefixed")
	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "malformed task id")
	is.ErrorContains(err, "expected <prefix>-<number>")

	stdout, stderr, err = fixture.execute("task", "show", "zz-1")
	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "unknown task id prefix")
	is.ErrorContains(err, "orpheus repo list")
	is.ErrorContains(err, "register the repo")
}

func TestIntegrationWorkflowTaskDirReportsMalformedAndUnknownPrefixes(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newRepoConfigFixture(t)
	repo := aRegisteredRepo("alpha")
	repo.Name, repo.BeadsPrefix = "Alpha", "op"
	fixture.withRegisteredRepos(repo)

	stdout, stderr, err := fixture.execute("task", "dir", "notprefixed")
	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "malformed task id")
	is.ErrorContains(err, "expected <prefix>-<number>")

	stdout, stderr, err = fixture.execute("task", "dir", "zz-1")
	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "unknown task id prefix")
	is.ErrorContains(err, "orpheus repo list")
	is.ErrorContains(err, "register the repo")
}
