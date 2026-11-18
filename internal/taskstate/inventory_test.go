package taskstate_test

import (
	workmodel "github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workfixture"
	implementation "github.com/hea3ven/orpheus/internal/workflow/implementation"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreTaskIDsDiscoversOnlyDirectValidYAMLFilesInSortedOrder(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	require.NoError(t, err)
	store := workfixture.NewStore(paths)
	for _, id := range []string{"op-z", "op-a"} {
		_, err := store.SeedImplementation("alpha", id, implementation.Reservation{Context: &workmodel.WorkContext{Directory: "/fixture/work", TaskBranch: "task-branch", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}}, Agent: "recorder"})
		require.NoError(t, err)
	}
	for _, rel := range []string{
		"repos/alpha/tasks/notes.txt", "repos/alpha/tasks/.yaml", "repos/alpha/tasks/...yaml",
		"repos/alpha/tasks/nested.yaml/child.yaml", "repos/beta/tasks/op-other.yaml",
	} {
		require.NoError(t, paths.WriteDataYAML(rel, map[string]string{"ignored": "true"}))
	}

	ids, err := store.TaskIDs("alpha")

	require.NoError(t, err)
	assert.Equal(t, []string{"op-a", "op-z"}, ids)
	missing, err := store.TaskIDs("missing")
	require.NoError(t, err)
	assert.Empty(t, missing)
	_, err = store.TaskIDs("../escape")
	require.Error(t, err)
}
