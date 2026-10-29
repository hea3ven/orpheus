//go:build integration

package cli_test

import (
	workmodel "github.com/hea3ven/orpheus/internal/work"
	"os"
	"path/filepath"
	"testing"

	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
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
	_, err := f.taskStore.StartRun("alpha", taskID, taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: dir, TaskBranch: branch, CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}}, Agent: "recorder", Interactive: true, Branch: branch, Worktree: dir})
	require.NoError(t, err)
	f.options.AgentWorkingDirectory = dir
	f.options.Environment["ORPHEUS_WORKFLOW"] = "0"
	f.options.Environment["ORPHEUS_PHASE"] = "1"
	f.options.Environment["ORPHEUS_REPO_ID"] = "alpha"
	f.options.Environment["ORPHEUS_TASK_ID"] = taskID
	f.options.Environment["ORPHEUS_BRANCH"] = branch
	f.options.Environment["ORPHEUS_WORKTREE"] = dir
	return f
}

func TestIntegrationWorkflowAgentDoneRecordsCompletionForLocalReviewWithoutCommitting(t *testing.T) {
	for _, target := range []struct {
		name, taskID string
		worktree     bool
	}{
		{"main", "op-main", false},
		{"worktree", "op-1", true},
	} {
		t.Run(target.name, func(t *testing.T) {
			f := newAgentCompletionFixture(t, target.taskID, target.worktree)
			before := f.backend.tasks[target.taskID].Clone()
			body := "## PR body\n\nPreserve the final newline.\n"
			technical := "## Technical explanation\n\nPreserve these exact bytes too.\n"
			dir := testutil.CanonicalTempDir(t)
			bodyPath, technicalPath := filepath.Join(dir, "body.md"), filepath.Join(dir, "technical.md")
			require.NoError(t, os.WriteFile(bodyPath, []byte(body), 0o600))
			require.NoError(t, os.WriteFile(technicalPath, []byte(technical), 0o600))

			stdout, stderr := f.run("", "agent", "done", "--summary", "Add local review file", "--description", "Created a candidate for local review.", "--detailed-description-file", bodyPath, "--technical-explanation-file", technicalPath)

			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "Recorded completion for "+target.taskID)
			if target.worktree {
				assert.Contains(t, stdout, "advance the workflow with `orpheus task run "+target.taskID+"`")
			}
			state, item := f.loadFinalTask(target.taskID)
			require.Len(t, state.Runs, 1)
			latest := state.Runs[0]
			assert.Equal(t, taskstate.RunStatusRunning, latest.Status)
			require.NotNil(t, latest.Completion)
			assert.Equal(t, "Add local review file", latest.Completion.Summary)
			assert.Equal(t, "Created a candidate for local review.", latest.Completion.Description)
			assert.Equal(t, body, latest.Completion.DetailedDescription)
			assert.Equal(t, technical, latest.Completion.TechnicalExplanation)
			assert.Empty(t, latest.Completion.Commit)
			assert.Empty(t, latest.Completion.CommitError)
			assert.Equal(t, before, item)
			assert.True(t, f.git.hasCandidateChanges)
			f.assertUnpublished(target.taskID)
		})
	}
}

func TestIntegrationWorkflowAgentDoneRepeatedMainCompletionIsNoopWithGuidance(t *testing.T) {
	f := newAgentCompletionFixture(t, "op-main", false)
	attempt := completeAgentTestRun(t, f.taskStore)
	require.NotZero(t, attempt.Attempt)

	stdout, stderr := f.run("", "agent", "done", "--summary", "Second summary", "--description", "Second details.", "--detailed-description", "Second detailed PR body.", "--technical-explanation", "Second technical explanation.")

	assert.Empty(t, stderr)
	assertRepeatedAgentDoneOutput(t, stdout)
	assertRepeatedAgentCompletion(t, f.taskStore, attempt)
	f.assertUnpublished("op-main")
}

func TestIntegrationWorkflowAgentDoneAcceptsNoChangeHandoffWithoutClaimingCapturedOutput(t *testing.T) {
	f := newAgentCompletionFixture(t, "op-main", false)
	f.git.hasCandidateChanges = false

	stdout, stderr, err := f.runError("", "agent", "done", "--summary", "No changes", "--description", "Should fail before writing.", "--detailed-description", "Should fail before writing a PR body.", "--technical-explanation", "Should fail before writing a technical explanation.")

	require.NoError(t, err)
	assert.Contains(t, stdout, "Recorded completion")
	assert.Empty(t, stderr)
	state, _ := f.loadFinalTask("op-main")
	assert.Equal(t, taskstate.RunStatusRunning, state.Runs[0].Status)
	require.NotNil(t, state.Runs[0].Completion)
	assert.Empty(t, state.Runs[0].Completion.Commit)
	f.assertUnpublished("op-main")
}
