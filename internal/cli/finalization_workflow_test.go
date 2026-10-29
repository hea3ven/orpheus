//go:build integration

package cli_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/hea3ven/orpheus/internal/publication"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowPublicationDirectMergeRetriesPushAndClose(t *testing.T) {
	for _, repoRoot := range []bool{false, true} {
		t.Run(fmt.Sprintf("repo_root_%t", repoRoot), func(t *testing.T) {
			f := newFinalizationFixture(t, "op-merge")
			f.setConfig("publication", map[string]any{"integration_flow": publication.IntegrationFlowDirectMerge})
			branch, dir := f.expectedTarget("op-merge")
			if repoRoot {
				dir = taskWorkflowRepoRoot
			}
			f.completion("op-merge", branch, dir, "feat: original intent", "Deliver the reviewed implementation.", taskstate.RunStatusSucceeded)
			f.passedReview("op-merge")
			before, item := f.loadFinalTask("op-merge")
			require.Equal(t, "output-1", before.Runs[0].Completion.Commit)
			assert.Equal(t, taskmodel.StatusInProgress, item.Status)
			assert.Empty(t, f.publication.remote)
			f.publication.pushError = errors.New("origin unavailable")

			stdout, stderr, err := f.runError("", "task", "done", "op-merge")

			require.ErrorIs(t, err, f.publication.pushError)
			assert.Empty(t, stdout)
			assert.Empty(t, stderr)
			pending, item := f.loadFinalTask("op-merge")
			assert.Equal(t, before, pending, "a failed push cannot fabricate observed delivery")
			assert.Equal(t, taskmodel.StatusInProgress, item.Status)
			assert.Empty(t, f.publication.remote)
			assert.Equal(t, 1, f.publication.merges, "ordinary integration merge exists locally despite the disconnected push")
			assert.Empty(t, f.publication.cleanupAttempts)
			f.publication.pushError = nil
			f.mutations.closeError = errors.New("task source unavailable")

			_, _, err = f.runError("", "task", "done", "op-merge")

			require.ErrorIs(t, err, f.mutations.closeError)
			delivered, item := f.loadFinalTask("op-merge")
			facts := taskstate.FinalizationFacts(delivered)
			assert.Equal(t, "output-1", facts.Commit, "published input is the retained implementation, not the integration merge")
			assert.Equal(t, "merge-commit", facts.MergeCommit)
			assert.Equal(t, "merge-commit", f.publication.remote["main"])
			assert.NotNil(t, facts.PushedAt)
			assert.Nil(t, facts.ClosedAt)
			assert.Equal(t, taskmodel.StatusInProgress, item.Status)
			assert.Empty(t, f.publication.cleanupAttempts)
			f.mutations.closeError = nil

			stdout, stderr = f.run("", "task", "done", "op-merge")

			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "Finalized op-merge")
			assert.Contains(t, stdout, "reviewed output-1")
			assert.Contains(t, stdout, "pushed main")
			closed, item := f.loadFinalTask("op-merge")
			assert.Equal(t, taskmodel.StatusClosed, item.Status)
			assert.NotNil(t, taskstate.FinalizationFacts(closed).ClosedAt)
			assert.Equal(t, []string{"feat: original intent\n\nDeliver the reviewed implementation."}, f.candidate.commits)
			assert.Equal(t, 1, f.publication.merges)
			assert.Equal(t, []string{"main"}, f.candidate.pushes)
			assert.Equal(t, []string{"op-merge"}, f.tasks.closed)
			assert.Empty(t, f.pr.created)
			if repoRoot {
				assert.Empty(t, f.publication.cleanupAttempts)
			} else {
				assert.Equal(t, []string{dir}, f.publication.cleanupAttempts)
			}

			f.run("", "task", "done", "op-merge")
			assert.Equal(t, []string{"op-merge"}, f.tasks.closed)
			assert.Len(t, f.candidate.pushes, 1)
			assert.Len(t, f.candidate.commits, 1)
		})
	}
}

func TestIntegrationWorkflowTaskDoneInfersFeatureTaskAcrossWorkingDirectories(t *testing.T) {
	for _, repoRoot := range []bool{false, true} {
		t.Run(fmt.Sprintf("repo_root_%t", repoRoot), func(t *testing.T) {
			f := newFeatureFinalizationFixture(t, "op-infer", repoRoot)
			// Inference follows the recorded directory, not a later branch template or
			// publication configuration; neither may rewrite the authorized intent.
			f.options.TaskWorkingDirectory += "/nested"
			f.setConfig("publication", map[string]any{"integration_flow": publication.IntegrationFlowDirectMerge})

			stdout, stderr := f.run("", "task", "done")

			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "Published op-infer")
			assert.Contains(t, stdout, "reviewed output-1")
			assert.Contains(t, stdout, "observed PR "+f.pr.url)
			f.assertPublishedPR("op-infer")
			require.Len(t, f.pr.created, 1)
			assert.Equal(t, "Ready for PR", f.pr.created[0].Title)
			assert.Equal(t, "main", f.pr.created[0].BaseBranch)
			assert.Equal(t, []string{"Ready for PR\n\nImplemented task branch changes."}, f.candidate.commits)
			f.run("", "task", "done")
			assert.Len(t, f.pr.created, 1)
			assert.Len(t, f.candidate.pushes, 1)
		})
	}
}

func TestIntegrationWorkflowTaskDoneCannotBypassLiveHandoffOrPublicationConsent(t *testing.T) {
	f := newAgentCompletionFixture(t, "op-main", false)
	f.run("", "agent", "done", "--summary", "feat: still active", "--description", "Not yet exited.", "--detailed-description", "Review body.", "--technical-explanation", "Implementation rationale.")
	before, item := f.loadFinalTask("op-main")
	require.IsType(t, &work.RunningProcess{}, before.Work.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Process)

	stdout, stderr, err := f.runError("yes\n", "task", "done", "op-main")

	require.ErrorContains(t, err, "publication awaits explicit exact-plan authorization")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	after, afterItem := f.loadFinalTask("op-main")
	assert.Equal(t, before, after)
	assert.Equal(t, item, afterItem)
	assert.Empty(t, f.candidate.commits)
	f.assertUnpublished("op-main")
	_, _, err = f.runError("", "task", "done", "op-main", "--summary", "Cannot replace committed intent")
	require.ErrorContains(t, err, "unknown flag: --summary")
}
