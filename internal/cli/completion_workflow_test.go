//go:build integration

package cli_test

import (
	"testing"

	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAgentCompletionFixture(t *testing.T, taskID string, worktree bool) *finalizationWorkflowFixture {
	t.Helper()
	f := newFinalizationFixture(t, taskID)
	branch, dir := "main", taskWorkflowRepoRoot
	if worktree {
		branch, dir = f.expectedTarget(taskID)
	}
	item := f.backend.tasks[taskID]
	item.Metadata[taskmodel.MetadataBranch], item.Metadata[taskmodel.MetadataWorktree] = branch, dir
	f.backend.tasks[taskID] = item
	f.git.targets[dir] = memoryGitTarget{branch: branch}
	_, err := f.taskStore.StartRun("alpha", taskID, taskstate.StartRunOptions{Agent: "recorder", Interactive: true, Branch: branch, Worktree: dir})
	require.NoError(t, err)
	f.options.AgentWorkingDirectory = dir
	f.options.Environment["ORPHEUS_REPO_ID"] = "alpha"
	f.options.Environment["ORPHEUS_TASK_ID"] = taskID
	f.options.Environment["ORPHEUS_BRANCH"] = branch
	f.options.Environment["ORPHEUS_WORKTREE"] = dir
	return f
}

func TestIntegrationAgentDoneRecordsMainCompletionForLocalReview(t *testing.T) {
	f := newAgentCompletionFixture(t, "op-main", false)
	before := f.backend.tasks["op-main"].Clone()

	stdout, stderr := f.run("", "agent", "done", "--summary", "Add local review file", "--description", "Created ORPHEUS_TEST.txt for local review.", "--detailed-description", "## PR body\n\nCreated ORPHEUS_TEST.txt for local review.", "--technical-explanation", "Updated the main-target completion path and left changes uncommitted for local review.")

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Recorded completion for op-main")
	state, item := f.loadFinalTask("op-main")
	require.Len(t, state.Runs, 1)
	latest := state.Runs[0]
	assert.Equal(t, taskstate.RunStatusRunning, latest.Status)
	require.NotNil(t, latest.Completion)
	assert.Equal(t, "Add local review file", latest.Completion.Summary)
	assert.Equal(t, "Created ORPHEUS_TEST.txt for local review.", latest.Completion.Description)
	assert.Equal(t, "## PR body\n\nCreated ORPHEUS_TEST.txt for local review.", latest.Completion.DetailedDescription)
	assert.Equal(t, "Updated the main-target completion path and left changes uncommitted for local review.", latest.Completion.TechnicalExplanation)
	assert.Equal(t, before, item)
	assert.True(t, f.git.hasCandidateChanges)
	f.assertUnpublished("op-main")
}

func TestIntegrationAgentDoneRepeatedMainCompletionIsNoopWithGuidance(t *testing.T) {
	f := newAgentCompletionFixture(t, "op-main", false)
	attempt := completeAgentTestRun(t, f.taskStore)
	require.NotZero(t, attempt.Attempt)

	stdout, stderr := f.run("", "agent", "done", "--summary", "Second summary", "--description", "Second details.", "--detailed-description", "Second detailed PR body.", "--technical-explanation", "Second technical explanation.")

	assert.Empty(t, stderr)
	assertRepeatedAgentDoneOutput(t, stdout)
	assertRepeatedAgentCompletion(t, f.taskStore, attempt)
	f.assertUnpublished("op-main")
}

// The historical name is retained in the assertion map. Completion deliberately
// leaves the candidate uncommitted; publication owns commit creation.
func TestIntegrationAgentDoneCommitsWorktreeCompletion(t *testing.T) {
	f := newAgentCompletionFixture(t, "op-1", true)

	stdout, stderr := f.run("", "agent", "done", "--summary", "Add worktree review file", "--description", "Created ORPHEUS_WORKTREE_TEST.txt for pull request review.", "--detailed-description", "## Pull request\n\nCreated ORPHEUS_WORKTREE_TEST.txt for pull request review.", "--technical-explanation", "Added the worktree validation fixture so review can inspect an uncommitted candidate change.")

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Recorded completion for op-1; advance the workflow with `orpheus task run op-1`")
	state, _ := f.loadFinalTask("op-1")
	latest := state.Runs[0]
	assert.Equal(t, taskstate.RunStatusRunning, latest.Status)
	require.NotNil(t, latest.Completion)
	assert.Equal(t, "Added the worktree validation fixture so review can inspect an uncommitted candidate change.", latest.Completion.TechnicalExplanation)
	assert.Empty(t, latest.Completion.Commit)
	assert.Empty(t, latest.Completion.CommitError)
	assert.True(t, f.git.hasCandidateChanges)
	f.assertUnpublished("op-1")
}

func TestIntegrationAgentDoneRequiresMainWorkingTreeChangesBeforeWriting(t *testing.T) {
	f := newAgentCompletionFixture(t, "op-main", false)
	f.git.hasCandidateChanges = false

	stdout, stderr, err := f.runError("", "agent", "done", "--summary", "No changes", "--description", "Should fail before writing.", "--detailed-description", "Should fail before writing a PR body.", "--technical-explanation", "Should fail before writing a technical explanation.")

	require.ErrorContains(t, err, "working tree has no changes")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	state, _ := f.loadFinalTask("op-main")
	assert.Equal(t, taskstate.RunStatusRunning, state.Runs[0].Status)
	assert.Nil(t, state.Runs[0].Completion)
	f.assertUnpublished("op-main")
}
