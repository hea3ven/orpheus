//go:build integration

package cli_test

import (
	"testing"

	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowRepoRegistrationSelectsStatusBackendAndMaintenanceOwnership(t *testing.T) {
	for _, mode := range []string{"local", "managed"} {
		t.Run(mode, func(t *testing.T) {
			repo := aGitRepository(t, "alpha")
			fixture := newRepoWorkflowFixture(t, repo)
			wantDir := repo.path
			if mode == "local" {
				fixture.withLocalBeads(repo, "alpha")
			} else {
				fixture.withoutLocalBeads()
				wantDir = repoWorkflowDataRoot + "/repos/alpha/beads"
			}
			item := anOpenTask("alpha-visible")
			backend := newMemoryTaskBackend([]taskmodel.Task{item})
			var sources []taskmodel.RepositorySource
			fixture.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
				sources = append(sources, source)
				return backend, nil
			}

			_, addStderr, err := fixture.execute("repo", "add", "--task-source", "beads", repo.path)
			require.NoError(t, err, "stderr: %s", addStderr)
			stdout, stderr, err := fixture.execute("status", "--no-truncate")

			require.NoError(t, err)
			assert.Empty(t, stderr)
			assert.Contains(t, stdout, item.ID)
			assert.Contains(t, stdout, item.Title)
			require.NotEmpty(t, sources)
			for _, source := range sources {
				assert.Equal(t, "alpha", source.Repository.ID)
				assert.Equal(t, repo.path, source.Repository.Path)
				assert.Equal(t, wantDir, source.BackendDir)
				assert.Equal(t, mode == "managed", source.MaintenanceOwned)
			}
		})
	}
}
