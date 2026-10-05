package registry_test

import (
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryProjectsMixedSourcesWithoutChangingLegacyBeads(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	require.NoError(t, err)
	store := registry.NewStore(paths)
	legacy := registry.Repo{ID: "legacy", Name: "legacy", Path: filepath.Join(root, "legacy"), BeadsMode: "local", BeadsPrefix: "bd"}
	managed := registry.Repo{ID: "managed", Name: "managed", Path: filepath.Join(root, "managed"), BeadsMode: "managed", BeadsPrefix: "bm"}
	modern := registry.Repo{ID: "modern", Name: "modern", Path: filepath.Join(root, "modern"), TaskSource: "gig", TaskMode: "managed", TaskPrefix: "gg"}
	require.NoError(t, store.Save(registry.Registry{Repos: []registry.Repo{legacy, managed, modern}}))
	reg, err := store.Load()
	require.NoError(t, err)
	assert.Equal(t, []registry.Repo{legacy, managed, modern}, reg.Repos)
	sources, err := store.TaskRepositorySources(reg)
	require.NoError(t, err)
	assert.Equal(t, "beads", sources[0].Kind)
	assert.Equal(t, legacy.Path, sources[0].BackendDir)
	assert.False(t, sources[0].MaintenanceOwned)
	assert.Equal(t, "beads", sources[1].Kind)
	assert.True(t, sources[1].MaintenanceOwned)
	assert.Equal(t, "gig", sources[2].Kind)
	assert.Equal(t, "gg", sources[2].Repository.TaskIDPrefix)
	assert.True(t, sources[2].MaintenanceOwned)
	assert.Equal(t, filepath.Join(root, "data", "repos", "modern", "gig"), sources[2].BackendDir)
	resolved, err := reg.Resolve("gg")
	require.NoError(t, err)
	assert.Equal(t, modern, resolved)
	_, err = store.BeadsDir(modern)
	assert.ErrorContains(t, err, "applies only to Beads")
	_, err = store.ManagedGigDir("../outside")
	require.Error(t, err)
	collision := registry.Repo{ID: "collision", Name: "collision", Path: filepath.Join(root, "collision"), TaskSource: "gig", TaskMode: "managed", TaskPrefix: "bd"}
	assert.ErrorContains(t, reg.Add(collision), "duplicate task prefix")
}

func TestRegistryRejectsUnsupportedSourceModeCombinations(t *testing.T) {
	for name, repo := range map[string]registry.Repo{
		"unknown":               {TaskSource: "unknown"},
		"local gig":             {TaskSource: "gig", TaskMode: "local", TaskPrefix: "gg"},
		"missing gig mode":      {TaskSource: "gig", TaskPrefix: "gg"},
		"missing gig prefix":    {TaskSource: "gig", TaskMode: "managed"},
		"invalid gig prefix":    {TaskSource: "gig", TaskMode: "managed", TaskPrefix: "two words"},
		"gig with beads mode":   {TaskSource: "gig", TaskMode: "managed", TaskPrefix: "gg", BeadsMode: "local"},
		"beads with gig fields": {TaskSource: "beads", TaskMode: "managed", TaskPrefix: "gg"},
	} {
		t.Run(name, func(t *testing.T) {
			repo.ID, repo.Name, repo.Path = "repo", "repo", "/fixture/repo"
			var reg registry.Registry
			require.Error(t, reg.Add(repo))
			assert.Empty(t, reg.Repos)
		})
	}
}
