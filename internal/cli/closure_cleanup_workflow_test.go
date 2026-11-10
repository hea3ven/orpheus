//go:build integration

package cli_test

import (
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowClosureSucceedsWhenWorktreeRemovalFails(t *testing.T) {
	for _, flow := range []publication.IntegrationFlow{publication.IntegrationFlowPullRequest, publication.IntegrationFlowDirectMerge} {
		t.Run(string(flow), func(t *testing.T) {
			const taskID = "op-retained"
			f := newFinalizationFixture(t, taskID)
			f.setConfig("publication", map[string]any{"integration_flow": flow})
			branch, workdir := f.expectedTarget(taskID)
			f.completion(taskID, branch, workdir, "Ready for PR", "Implemented task branch changes.", taskstate.RunStatusSucceeded)
			f.passedReview(taskID)
			_, dir := f.expectedTarget(taskID)
			f.setConfig("publication", map[string]any{"integration_flow": flow})
			f.publication.cleanupError = errors.New("fatal: contains modified or untracked files")

			stdout, stderr := f.run("", "task", "done", taskID)
			assert.Contains(t, stderr, "TASK")
			reads := f.pr.statusReads
			if flow == publication.IntegrationFlowPullRequest {
				assert.Empty(t, f.publication.cleanupAttempts, "open task cannot be cleaned up")
				f.pr.state = pullrequest.StateMerged
			}
			assert.Empty(t, f.tasks.closed)
			assert.Empty(t, f.publication.cleanupAttempts)
			stdout, stderr = f.run("", "task", "sync", "--all")
			assert.Contains(t, stderr, "TASK")
			assert.Contains(t, stdout, "task was closed")

			assert.Contains(t, stdout, "Worktree "+dir+" was retained")
			assert.Contains(t, stdout, f.publication.cleanupError.Error())
			assert.Equal(t, []string{dir}, f.publication.cleanupAttempts)
			assert.Contains(t, f.git.targets, dir)
			saved, item := f.loadFinalTask(taskID)
			assert.Equal(t, taskmodel.StatusClosed, item.Status)
			require.NotEmpty(t, saved.Events)
			assert.Equal(t, taskstate.EventTaskClosed, saved.Events[len(saved.Events)-1].Type)
			for _, event := range saved.Events {
				assert.NotEqual(t, taskstate.EventWorktreeRemoved, event.Type)
			}
			f.publication.cleanupError = nil
			require.NoError(t, state.SetMemoryDataWriteError(f.paths, func(_ string, data []byte) error {
				next, err := workstate.Decode(data)
				if err != nil {
					return err
				}
				final := next.Workflows[0].LastPhase().(*work.FinalizationPhase)
				for _, observation := range final.Cleanup {
					if _, removed := observation.(*work.WorktreeRemoved); removed {
						return errors.New("cleanup receipt lost")
					}
				}
				return nil
			}))
			output, _ := f.run("", "task", "sync", "--all")
			assert.Contains(t, output, "cleanup receipt lost")
			require.NoError(t, state.SetMemoryDataWriteError(f.paths, nil))
			unrecorded, _ := f.loadFinalTask(taskID)
			assert.Equal(t, taskstate.EventTaskClosed, unrecorded.Events[len(unrecorded.Events)-1].Type)
			assert.NotContains(t, f.git.targets, dir, "native removal survives a failed cleanup receipt")
			f.run("", "task", "sync", "--all")
			cleaned, _ := f.loadFinalTask(taskID)
			assert.Equal(t, []string{taskID}, f.tasks.closed)
			assert.Len(t, f.publication.cleanupAttempts, 2)
			assert.NotContains(t, f.git.targets, dir)
			assert.Equal(t, taskstate.EventWorktreeRemoved, cleaned.Events[len(cleaned.Events)-1].Type)
			assert.Equal(t, 1, f.publication.packageCalls)
			assert.Len(t, f.candidate.pushes, 1)
			if flow == publication.IntegrationFlowPullRequest {
				assert.Equal(t, reads+1, f.pr.statusReads)
			} else {
				assert.Zero(t, f.pr.statusReads)
			}
		})
	}
}
