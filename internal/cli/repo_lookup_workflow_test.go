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
			for _, token := range []string{repo.ID, repo.Name, repo.BeadsPrefix, "missing"} {
				t.Run(token, func(t *testing.T) {
					stdout, stderr, err := fixture.execute("repo", "beads-dir", token)

					if token == "missing" {
						require.ErrorContains(t, err, `repo "missing" is not registered`)
						assert.ErrorContains(t, err, "orpheus repo list")
						assert.Empty(t, stdout)
						assert.Empty(t, stderr)
						return
					}
					require.NoError(t, err)
					assert.Equal(t, wantDir+"\n", stdout)
					assert.Empty(t, stderr)
				})
			}
		})
	}
}

func TestIntegrationWorkflowTaskLookupRejectsMalformedAndUnknownPrefixes(t *testing.T) {
	for _, command := range []string{"show", "dir"} {
		for _, test := range []struct {
			id       string
			guidance []string
		}{
			{"notprefixed", []string{"malformed task id", "expected <prefix>-<suffix>"}},
			{"zz-1", []string{"unknown task id prefix", "orpheus repo list", "register the repo"}},
		} {
			t.Run(command+"/"+test.id, func(t *testing.T) {
				fixture := newRepoConfigFixture(t)
				repo := aRegisteredRepo("alpha")
				repo.Name, repo.BeadsPrefix = "Alpha", "op"
				fixture.withRegisteredRepos(repo)

				stdout, stderr, err := fixture.execute("task", command, test.id)

				require.Error(t, err)
				for _, want := range test.guidance {
					assert.ErrorContains(t, err, want)
				}
				assert.Empty(t, stdout)
				assert.Empty(t, stderr)
			})
		}
	}
}
