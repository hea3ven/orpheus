//go:build integration

package cli_test

import (
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowClosureSucceedsWhenWorktreeRemovalFails(t *testing.T) {
	for _, flow := range []publication.IntegrationFlow{publication.IntegrationFlowPullRequest, publication.IntegrationFlowDirectMerge} {
		t.Run(string(flow), func(t *testing.T) {
			const taskID = "op-retained"
			f := newFeatureFinalizationFixture(t, taskID, false)
			_, dir := f.expectedTarget(taskID)
			f.setConfig("publication", map[string]any{"integration_flow": flow})
			f.publication.cleanupError = errors.New("fatal: contains modified or untracked files")

			stdout, stderr := f.run("", "task", "done", taskID)
			assert.Empty(t, stderr)
			if flow == publication.IntegrationFlowPullRequest {
				assert.Empty(t, f.publication.cleanupAttempts, "open task cannot be cleaned up")
				f.pr.state = pullrequest.StateMerged
				stdout, stderr = f.run("", "task", "sync", taskID)
				assert.Empty(t, stderr)
				assert.Contains(t, stdout, "Backend task was closed")
			} else {
				assert.Contains(t, stdout, "Finalized "+taskID)
			}

			assert.Contains(t, stdout, "Worktree "+dir+" was retained")
			assert.Contains(t, stdout, f.publication.cleanupError.Error())
			assert.Equal(t, []string{dir}, f.publication.cleanupAttempts)
			assert.Contains(t, f.git.targets, dir)
			state, item := f.loadFinalTask(taskID)
			assert.Equal(t, taskmodel.StatusClosed, item.Status)
			require.NotEmpty(t, state.Events)
			assert.Equal(t, taskstate.EventTaskClosed, state.Events[len(state.Events)-1].Type)
			for _, event := range state.Events {
				assert.NotEqual(t, taskstate.EventWorktreeRemoved, event.Type)
			}
		})
	}
}
