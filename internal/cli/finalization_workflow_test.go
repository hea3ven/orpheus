//go:build integration

package cli_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskDoneCommitsPushesClosesAndRecordsFinalization(t *testing.T) {
	f := newMainFinalizationFixture(t, "op-main")

	stdout, stderr := f.run("", "task", "done", "op-main")

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Finalized op-main")
	assert.Contains(t, stdout, f.candidate.head)
	require.Equal(t, []string{"Implement task done\n\nCommit reviewed repo-root changes."}, f.candidate.commits)
	assert.Equal(t, f.candidate.head, f.publication.remote["main"])
	state, item := f.loadFinalTask("op-main")
	assert.Equal(t, taskmodel.StatusClosed, item.Status)
	assert.Equal(t, []string{"op-main"}, f.tasks.closed)
	facts := taskstate.FinalizationFacts(state)
	assert.Equal(t, f.candidate.head, facts.Commit)
	assert.NotNil(t, facts.CommittedAt)
	assert.NotNil(t, facts.PushedAt)
	assert.NotNil(t, facts.ClosedAt)
	assert.Equal(t, taskstate.RunStatusSucceeded, state.Runs[0].Status)
	assert.Empty(t, f.pr.created)
}

func TestIntegrationWorkflowTaskDoneRequiresPassedReview(t *testing.T) {
	f := newFinalizationFixture(t, "op-main")
	f.completion("op-main", "main", taskWorkflowRepoRoot, "Implement task done", "Commit reviewed repo-root changes.", taskstate.RunStatusSucceeded)

	stdout, stderr, err := f.runError("", "task", "done", "op-main")

	require.ErrorContains(t, err, "has no local review attempt")
	assert.ErrorContains(t, err, "orpheus task run op-main")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	assert.True(t, f.git.hasCandidateChanges)
	f.assertUnpublished("op-main")
}

func TestIntegrationWorkflowTaskDoneRefusesRunningCompletionWithoutInteractiveConfirmation(t *testing.T) {
	f := newFinalizationFixture(t, "op-main")
	f.completion("op-main", "main", taskWorkflowRepoRoot, "Implement task done", "Commit reviewed repo-root changes.", taskstate.RunStatusRunning)
	f.passedReview("op-main")

	stdout, stderr, err := f.runError("", "task", "done", "op-main")

	require.ErrorContains(t, err, "explicit interactive confirmation is required")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	state, _ := f.loadFinalTask("op-main")
	assert.Equal(t, taskstate.RunStatusRunning, state.Runs[0].Status)
	assert.True(t, f.git.hasCandidateChanges)
	f.assertUnpublished("op-main")
}

func TestIntegrationWorkflowTaskDoneRecoversExistingBranchPR(t *testing.T) {
	f := newFeatureFinalizationFixture(t, "op-sync", false)
	f.pr.url = "https://github.test/org/alpha/pull/7"
	f.pr.existing = &pullrequest.CreateRequest{RepositoryPath: taskWorkflowRepoRoot, HeadBranch: "orpheus/op-sync", BaseBranch: "main"}

	stdout, stderr := f.run("", "task", "done", "op-sync")

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "recovered existing PR "+f.pr.url)
	assert.Contains(t, stdout, "Backend task remains open for PR review")
	assert.Empty(t, f.pr.created)
	f.assertPublishedPR("op-sync")
	state, _ := f.loadFinalTask("op-sync")
	assert.Equal(t, taskstate.EventPRRecovered, state.Events[len(state.Events)-1].Type)
}

func TestIntegrationWorkflowTaskDoneFeatureBranchPushFailureIsNonZero(t *testing.T) {
	f := newFeatureFinalizationFixture(t, "op-sync", false)
	f.publication.pushError = errors.New("push task branch to origin: remote unavailable")

	stdout, stderr, err := f.runError("", "task", "done", "op-sync")

	require.ErrorContains(t, err, "push task branch")
	assert.ErrorContains(t, err, "origin")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	state, item := f.loadFinalTask("op-sync")
	facts := taskstate.FinalizationFacts(state)
	assert.Equal(t, f.candidate.head, facts.Commit)
	assert.Nil(t, facts.PushedAt)
	assert.Nil(t, facts.ClosedAt)
	assert.Equal(t, taskmodel.StatusInProgress, item.Status)
	assert.Empty(t, item.OrpheusMetadata().PRURL)
	assert.Empty(t, f.pr.created)
	assert.Empty(t, f.publication.remote)
	failed, ok := taskstate.LatestFinalizationFailure(state)
	require.True(t, ok)
	assert.Contains(t, failed.Error, "remote unavailable")
	review, ok := taskstate.LatestReview(state)
	require.True(t, ok)
	assert.Equal(t, taskstate.ReviewStatusPassed, review.Status)

	f.publication.pushError = nil
	f.run("", "task", "done", "op-sync")
	f.assertPublishedPR("op-sync")
	assert.Len(t, f.candidate.commits, 1)
}

func TestIntegrationWorkflowTaskDoneInfersSingleMainReadyTaskFromRepoRootAndUsesOverrides(t *testing.T) {
	f := newMainFinalizationFixture(t, "op-infer")
	f.options.TaskWorkingDirectory = taskWorkflowRepoRoot

	stdout, stderr := f.run("", "task", "done", "--summary", "Human reviewed summary", "--description", "Human adjusted details.")

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Finalized op-infer")
	assert.Equal(t, []string{"Human reviewed summary\n\nHuman adjusted details."}, f.candidate.commits)
	state, item := f.loadFinalTask("op-infer")
	assert.Equal(t, taskmodel.StatusClosed, item.Status)
	assert.Equal(t, "Implement task done", state.Runs[0].Completion.Summary)
}

func TestIntegrationWorkflowTaskDoneWithoutTaskIDRequiresExactRegisteredRepoRoot(t *testing.T) {
	f := newMainFinalizationFixture(t, "op-main")
	nested := taskWorkflowRepoRoot + "/nested"
	f.options.TaskWorkingDirectory = nested
	f.git.targets[nested] = memoryGitTarget{branch: "main"}

	stdout, stderr, err := f.runError("", "task", "done")

	require.ErrorContains(t, err, "cwd must be exactly a registered repo root")
	assert.ErrorContains(t, err, "pass <task-id>")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	f.assertUnpublished("op-main")
}

func TestIntegrationWorkflowTaskDoneRefusesNoChangesWithoutRecordedFinalizationCommit(t *testing.T) {
	f := newMainFinalizationFixture(t, "op-clean")
	f.git.hasCandidateChanges = false

	stdout, stderr, err := f.runError("", "task", "done", "op-clean")

	require.ErrorContains(t, err, "has no changes to commit")
	assert.ErrorContains(t, err, "has no recorded finalization commit")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	f.assertUnpublished("op-clean")
}

func TestIntegrationWorkflowTaskDoneRetriesPushAndCloseFromRecordedFinalizationCommit(t *testing.T) {
	f := newMainFinalizationFixture(t, "op-retry")
	f.publication.pushError = errors.New("origin unavailable")
	_, _, err := f.runError("", "task", "done", "op-retry")
	require.ErrorContains(t, err, "origin unavailable")
	commit := f.candidate.head
	f.publication.pushError = nil
	f.mutations.closeError = errors.New("task source unavailable")
	_, _, err = f.runError("", "task", "done", "op-retry")
	require.ErrorContains(t, err, "task source unavailable")
	state, item := f.loadFinalTask("op-retry")
	assert.Equal(t, taskmodel.StatusInProgress, item.Status)
	assert.NotNil(t, taskstate.FinalizationFacts(state).PushedAt)
	assert.Nil(t, taskstate.FinalizationFacts(state).ClosedAt)
	f.mutations.closeError = nil

	stdout, stderr := f.run("", "task", "done", "op-retry")

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Finalized op-retry")
	assert.Equal(t, commit, f.candidate.head)
	assert.Equal(t, commit, f.publication.remote["main"])
	state, item = f.loadFinalTask("op-retry")
	facts := taskstate.FinalizationFacts(state)
	assert.Equal(t, commit, facts.Commit)
	assert.NotNil(t, facts.PushedAt)
	assert.NotNil(t, facts.ClosedAt)
	assert.Equal(t, taskmodel.StatusClosed, item.Status)
	assert.Len(t, f.candidate.commits, 1)
	assert.Equal(t, []string{"main"}, f.candidate.pushes)
	assert.Equal(t, []string{"op-retry"}, f.tasks.closed)

	f.run("", "task", "done", "op-retry")
	assert.Len(t, f.candidate.commits, 1)
	assert.Len(t, f.candidate.pushes, 1)
	assert.Len(t, f.tasks.closed, 1)
}

func TestIntegrationWorkflowPublicationRetriesPRCreationAndMetadataWithoutRepublishing(t *testing.T) {
	for _, failure := range []string{"create", "metadata"} {
		t.Run(failure, func(t *testing.T) {
			f := newFeatureFinalizationFixture(t, "op-retry", false)
			cause := errors.New("provider unavailable")
			if failure == "create" {
				f.pr.createError = cause
			} else {
				f.mutations.metadataError = cause
			}
			stdout, stderr, err := f.runError("", "task", "done", "op-retry")
			require.ErrorIs(t, err, cause)
			assert.Empty(t, stdout)
			assert.Empty(t, stderr)
			state, item := f.loadFinalTask("op-retry")
			assert.Empty(t, item.OrpheusMetadata().PRURL)
			assert.NotNil(t, taskstate.FinalizationFacts(state).PushedAt)
			assert.Equal(t, taskstate.EventFinalizationFailed, state.Events[len(state.Events)-1].Type)
			f.pr.createError, f.mutations.metadataError = nil, nil

			stdout, stderr = f.run("", "task", "done", "op-retry")

			assert.Empty(t, stderr)
			if failure == "metadata" {
				assert.Contains(t, stdout, "recovered existing PR")
			} else {
				assert.Contains(t, stdout, "created PR")
			}
			f.assertPublishedPR("op-retry")
			assert.Len(t, f.pr.created, 1)
			assert.Len(t, f.candidate.commits, 1)
			assert.Len(t, f.candidate.pushes, 1)
			_, _, err = f.runError("", "task", "done", "op-retry")
			require.ErrorContains(t, err, "use task sync")
			assert.Len(t, f.pr.created, 1)
			assert.Len(t, f.candidate.commits, 1)
			assert.Len(t, f.candidate.pushes, 1)
		})
	}
}

func TestIntegrationWorkflowPublicationDirectMergeRetriesPushAndClose(t *testing.T) {
	for _, repoRoot := range []bool{false, true} {
		t.Run(fmt.Sprintf("repo_root_%t", repoRoot), func(t *testing.T) {
			f := newFeatureFinalizationFixture(t, "op-merge", repoRoot)
			_, dir := f.expectedTarget("op-merge")
			if repoRoot {
				dir = taskWorkflowRepoRoot
			}
			f.setConfig("publication", map[string]any{"integration_flow": publication.IntegrationFlowDirectMerge})
			var state taskstate.TaskState
			var item taskmodel.Task
			assert.Empty(t, f.publication.cleanupAttempts)
			if repoRoot {
				f.publication.pushError = errors.New("origin unavailable")

				_, _, err := f.runError("", "task", "done", "op-merge")

				require.ErrorContains(t, err, "origin unavailable")
				state, item = f.loadFinalTask("op-merge")
				assert.Equal(t, "merge-commit", taskstate.FinalizationFacts(state).MergeCommit)
				assert.Nil(t, taskstate.FinalizationFacts(state).PushedAt)
				assert.Equal(t, taskmodel.StatusInProgress, item.Status)
				assert.Contains(t, f.git.targets, dir, "failed push must retain the checkout")
				assert.Empty(t, f.publication.cleanupAttempts)
				for _, event := range state.Events {
					assert.NotEqual(t, taskstate.EventWorktreeRemoved, event.Type)
				}
				f.publication.pushError = nil
				f.mutations.closeError = errors.New("close unavailable")

				_, _, err = f.runError("", "task", "done", "op-merge")

				require.ErrorContains(t, err, "close unavailable")
				state, item = f.loadFinalTask("op-merge")
				assert.Equal(t, taskmodel.StatusInProgress, item.Status)
				assert.NotNil(t, taskstate.FinalizationFacts(state).PushedAt)
				assert.Nil(t, taskstate.FinalizationFacts(state).ClosedAt)
				assert.Contains(t, f.git.targets, dir, "failed closure must retain the checkout")
				assert.Empty(t, f.publication.cleanupAttempts)
				for _, event := range state.Events {
					assert.NotEqual(t, taskstate.EventWorktreeRemoved, event.Type)
				}
				f.mutations.closeError = nil
			}

			stdout, stderr := f.run("", "task", "done", "op-merge")

			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "Finalized op-merge")
			state, item = f.loadFinalTask("op-merge")
			facts := taskstate.FinalizationFacts(state)
			assert.Equal(t, publication.IntegrationFlowDirectMerge, facts.IntegrationFlow)
			assert.Equal(t, "commit-1", facts.Commit)
			assert.Equal(t, "merge-commit", facts.MergeCommit)
			assert.NotNil(t, facts.PushedAt)
			assert.NotNil(t, facts.ClosedAt)
			assert.Equal(t, taskmodel.StatusClosed, item.Status)
			assert.Equal(t, []string{"op-merge"}, f.tasks.closed)
			assert.Empty(t, item.OrpheusMetadata().PRURL)
			assert.Empty(t, f.mutations.prURLs)
			assert.Empty(t, f.pr.created)
			assert.Zero(t, f.pr.findReads)
			assert.Equal(t, map[string]string{"main": "merge-commit"}, f.publication.remote)
			assert.Equal(t, []string{"main"}, f.candidate.pushes, "direct merge never pushes the task branch")
			assert.Equal(t, "main", f.git.targets[taskWorkflowRepoRoot].branch)
			assert.Equal(t, 1, f.publication.merges)
			assert.Len(t, f.candidate.commits, 1)
			var cleanups []taskstate.Event
			for _, event := range state.Events {
				if event.Type == taskstate.EventWorktreeRemoved {
					cleanups = append(cleanups, event)
				}
			}
			if repoRoot {
				assert.Contains(t, f.git.targets, taskWorkflowRepoRoot)
				assert.Empty(t, f.publication.cleanupAttempts)
				assert.Empty(t, cleanups)
				assert.NotContains(t, stdout, "was removed")
			} else {
				assert.NotContains(t, f.git.targets, dir)
				assert.Equal(t, []string{dir}, f.publication.cleanupAttempts)
				require.Len(t, cleanups, 1)
				assert.Equal(t, dir, cleanups[0].Worktree)
				assert.Equal(t, taskstate.EventTaskClosed, state.Events[len(state.Events)-2].Type)
				assert.Equal(t, taskstate.EventWorktreeRemoved, state.Events[len(state.Events)-1].Type)
				assert.Contains(t, stdout, "Worktree "+dir+" was removed")
			}
		})
	}
}

func TestIntegrationWorkflowTaskDoneInfersFeatureTaskAcrossWorkingDirectories(t *testing.T) {
	for _, root := range []bool{false, true} {
		t.Run(fmt.Sprintf("repo_root_%t", root), func(t *testing.T) {
			f := newFeatureFinalizationFixture(t, "op-infer", root)

			stdout, stderr := f.run("", "task", "done")

			assert.Empty(t, stderr)
			for _, want := range []string{"Published op-infer", "pushed orpheus/op-infer", "created PR " + f.pr.url} {
				assert.Contains(t, stdout, want)
			}
			f.assertPublishedPR("op-infer")
		})
	}
}
