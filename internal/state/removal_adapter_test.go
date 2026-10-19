//go:build integration

package state_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAdapterContractDataTreeRemovalIsScopedRetryableAndRejectsSymlinkAncestors(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	require.NoError(t, err)
	for _, rel := range []string{"repos/alpha/tasks/owned.yaml", "repos/alphabet/tasks/keep.yaml", "repos/beta/tasks/keep.yaml"} {
		require.NoError(t, paths.WriteDataYAML(rel, map[string]string{"content": rel}))
	}
	outside := filepath.Join(root, "outside")
	require.NoError(t, os.MkdirAll(outside, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "keep"), []byte("original"), 0o644))
	owned, err := paths.DataPath("repos/alpha")
	require.NoError(t, err)
	require.NoError(t, os.Symlink(outside, filepath.Join(owned, "link")))

	require.NoError(t, paths.CheckDataTree("repos/alpha"))
	require.NoError(t, paths.RemoveDataTree("repos/alpha"))
	require.NoError(t, paths.RemoveDataTree("repos/alpha"), "missing trees are already complete")
	assert.NoDirExists(t, owned)
	for _, rel := range []string{"repos/alphabet/tasks/keep.yaml", "repos/beta/tasks/keep.yaml"} {
		var value map[string]string
		require.NoError(t, paths.ReadDataYAML(rel, &value))
		assert.Equal(t, map[string]string{"content": rel}, value)
	}
	require.NoError(t, os.Symlink(outside, owned))
	assert.Error(t, paths.CheckDataTree("repos/alpha"))
	assert.Error(t, paths.RemoveDataTree("repos/alpha/tasks"))
	data, err := os.ReadFile(filepath.Join(outside, "keep"))
	require.NoError(t, err)
	assert.Equal(t, "original", string(data))
}
