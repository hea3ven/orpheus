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

func TestIntegrationWorkflowTaskDonePublishesPRReadyTaskBranch(t *testing.T) {
	f := newFeatureFinalizationFixture(t, "op-sync", false)

	stdout, stderr := f.run("", "task", "done", "op-sync")

	assert.Empty(t, stderr)
	for _, want := range []string{"Published op-sync", "pushed orpheus/op-sync", "created PR " + f.pr.url, "Backend task remains open for PR review"} {
		assert.Contains(t, stdout, want)
	}
	require.Len(t, f.pr.created, 1)
	request := f.pr.created[0]
	assert.Equal(t, taskWorkflowRepoRoot, request.RepositoryPath)
	assert.Equal(t, "orpheus/op-sync", request.HeadBranch)
	assert.Equal(t, "main", request.BaseBranch)
	assert.Equal(t, "Ready for PR", request.Title)
	assert.Contains(t, request.Body, "Detailed PR body.")
	for _, unwanted := range []string{"Created by Orpheus.", "Ready for sync", "op-sync:"} {
		assert.NotContains(t, request.Body, unwanted)
	}
	f.assertPublishedPR("op-sync")
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

func TestIntegrationWorkflowTaskDoneInfersRepoRootFeatureBranchTask(t *testing.T) {
	f := newFeatureFinalizationFixture(t, "op-root-infer", true)

	stdout, stderr := f.run("", "task", "done")

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Published op-root-infer")
	assert.Contains(t, stdout, "pushed orpheus/op-root-infer")
	assert.Contains(t, stdout, "created PR "+f.pr.url)
	f.assertPublishedPR("op-root-infer")
}

func TestIntegrationWorkflowTaskDoneInfersWorktreeTask(t *testing.T) {
	f := newFeatureFinalizationFixture(t, "op-worktree-infer", false)

	stdout, stderr := f.run("", "task", "done")

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Published op-worktree-infer")
	assert.Contains(t, stdout, "pushed orpheus/op-worktree-infer")
	assert.Contains(t, stdout, "created PR "+f.pr.url)
	f.assertPublishedPR("op-worktree-infer")
}

func TestIntegrationWorkflowTaskDoneRejectsRemovedDetailsOverride(t *testing.T) {
	f := newMainFinalizationFixture(t, "op-main")

	stdout, stderr, err := f.runError("", "task", "done", "op-main", "--details", "Old details.")

	require.ErrorContains(t, err, "unknown flag: --details")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	f.assertUnpublished("op-main")
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
	f := newFeatureFinalizationFixture(t, "op-merge", true)
	f.setConfig("publication", map[string]any{"integration_flow": publication.IntegrationFlowDirectMerge})
	f.publication.pushError = errors.New("origin unavailable")
	_, _, err := f.runError("", "task", "done", "op-merge")
	require.ErrorContains(t, err, "origin unavailable")
	state, item := f.loadFinalTask("op-merge")
	assert.Equal(t, "merge-commit", taskstate.FinalizationFacts(state).MergeCommit)
	assert.Nil(t, taskstate.FinalizationFacts(state).PushedAt)
	assert.Equal(t, taskmodel.StatusInProgress, item.Status)
	f.publication.pushError = nil
	f.mutations.closeError = errors.New("close unavailable")
	_, _, err = f.runError("", "task", "done", "op-merge")
	require.ErrorContains(t, err, "close unavailable")
	f.mutations.closeError = nil

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
	assert.Empty(t, item.OrpheusMetadata().PRURL)
	assert.Equal(t, map[string]string{"main": "merge-commit"}, f.publication.remote)
	assert.Equal(t, "main", f.git.targets[taskWorkflowRepoRoot].branch)
	assert.Equal(t, 1, f.publication.merges)
	assert.Len(t, f.candidate.commits, 1)
	assert.Len(t, f.candidate.pushes, 1)
	assert.Empty(t, f.pr.created)
}
