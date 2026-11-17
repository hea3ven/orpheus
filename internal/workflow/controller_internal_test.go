package workflow

import (
	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/state"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDispatchRefusesStaleControllerReservationBeforeEffects(t *testing.T) {
	for _, change := range []string{"version", "workflow", "phase", "step", "action"} {
		t.Run(change, func(t *testing.T) {
			snapshot := testutil.ReviewableWork("alpha", "op-1", "/fixture/repo", "orpheus/op-1")
			appendRouteBlocker(&snapshot.Workflows[0])
			view, err := taskstate.Project(snapshot)
			require.NoError(t, err)
			item := task.Task{ID: "op-1", Status: task.StatusInProgress}
			expected, err := SelectTaskRunRoute(item, view)
			require.NoError(t, err)
			switch change {
			case "version":
				expected.Version++
			case "workflow":
				expected.Owner.Workflow++
			case "phase":
				expected.Owner.Phase--
			case "step":
				expected.Owner.Step = routeNumber(1)
			case "action":
				expected.Action = TaskRunActionStartImplementation
			}
			err = implementationAdmission(&expected)(item, view)
			require.ErrorContains(t, err, "workflow advancement is stale")
			assert.Equal(t, snapshot, *view.Work)
		})
	}
}

func newDispatchTestPaths(t *testing.T) state.Paths {
	t.Helper()
	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config", state.AppName), filepath.Join(root, "data", state.AppName))
	require.NoError(t, err)
	require.NoError(t, testutil.WriteConfigYAML(paths, agent.ConfigFile, map[string]any{"agents": map[string]any{"defaults": map[string]any{"implementer": "implementer"}, "profiles": map[string]any{"implementer": map[string]any{"command": "fixture-agent"}}}}))
	return paths
}
