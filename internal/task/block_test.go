package task_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreBlockStatusRestoresOnlySupportedLifecycles(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata task.Metadata
		want     task.Status
	}{
		{name: "native block without restoration", want: task.StatusOpen},
		{name: "recorded open", metadata: task.Metadata{task.MetadataPreBlockStatus: "open"}, want: task.StatusOpen},
		{name: "recorded in progress", metadata: task.Metadata{task.MetadataPreBlockStatus: "in_progress"}, want: task.StatusInProgress},
		{name: "corrupt restoration must not close work", metadata: task.Metadata{task.MetadataPreBlockStatus: "closed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, err := task.PreBlockStatus(task.Task{ID: "op-item", Metadata: tc.metadata})
			if tc.want == task.StatusUnknown {
				require.ErrorIs(t, err, task.ErrMutationConflict)
				assert.ErrorContains(t, err, "inspect task-source metadata")
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.want, status)
		})
	}
}
