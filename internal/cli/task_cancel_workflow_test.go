//go:build integration

package cli_test

import (
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/hea3ven/orpheus/internal/pullrequest"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskCancelUpdatesQueuesWithoutSatisfyingDependency(t *testing.T) {
	fixture := newCommandWorkflow(t)
	retired := anOpenTask("op-retired")
	dependent := ownershipAuthoringTask("op-dependent", "Dependent work", taskmodel.StatusOpen, taskmodel.IssueTypeTask)
	dependent.Relations.DependencyIDs = []string{retired.ID}
	backend := newOwnershipAuthoringBackend(retired, dependent)
	setupOwnershipAuthoringWorkflow(t, fixture, ownershipAuthoringRepo(), backend)
	fixture.options.Dependencies.Terminal.InputIsTerminal = func(io.Reader) bool { return false }
	const reason = "Superseded by another plan"

	output, stderr := fixture.mustExecute("task", "cancel", retired.ID, "--reason", reason)
	assert.Contains(t, output, "Task op-retired cancelled")
	assert.Empty(t, stderr, "no dedicated worktree means no confirmation prompt")

	queue, _ := fixture.mustExecute("status", "--json")
	var rows []taskViewJSONTaskEntry
	require.NoError(t, json.Unmarshal([]byte(queue), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, dependent.ID, rows[0].ID)
	listed, _ := fixture.mustExecute("task", "list", "--json")
	require.NoError(t, json.Unmarshal([]byte(listed), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, dependent.ID, rows[0].ID)
	assert.Equal(t, "blocked", rows[0].Status)
	filtered, _ := fixture.mustExecute("task", "list", "--status", "cancelled", "--json")
	require.NoError(t, json.Unmarshal([]byte(filtered), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, retired.ID, rows[0].ID)
	assert.Equal(t, "cancelled", rows[0].Status)
	text, _ := fixture.mustExecute("task", "show", retired.ID)
	assert.Contains(t, text, "cancelled")
	assert.Contains(t, text, reason)
	jsonOut, _ := fixture.mustExecute("task", "show", retired.ID, "--json")
	var shown map[string]any
	require.NoError(t, json.Unmarshal([]byte(jsonOut), &shown))
	assert.Equal(t, "cancelled", shown["status"])
	assert.Equal(t, reason, shown["cancel_reason"])

	assertCompletionChoices(t, fixture, []string{"task", "edit", dependent.ID, "--remove-block", "op-"}, []string{retired.ID})
	fixture.mustExecute("task", "edit", dependent.ID, "--remove-block", retired.ID)
	ready, _ := fixture.mustExecute("status", "--json")
	require.NoError(t, json.Unmarshal([]byte(ready), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, dependent.ID, rows[0].ID)
	assert.Equal(t, "ready", rows[0].Status)
}

func TestIntegrationWorkflowTaskCancelWithoutRecordedGitTargetRequiresManualCleanup(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-unrecorded"))
	branch, worktree := fixture.expectedTarget("op-unrecorded")
	fixture.withBackendTarget("op-unrecorded", branch, worktree)

	out, warning, err := fixture.executeWithInput("y\n", "task", "cancel", "op-unrecorded")

	require.NoError(t, err)
	assert.Contains(t, out, "cancelled")
	assert.Contains(t, warning, worktree)
	assert.Contains(t, warning, "remove it manually")
	assert.NotContains(t, warning, "doctor --fix")
	_, retired := fixture.loadFinalTask("op-unrecorded")
	assert.Equal(t, taskmodel.StatusCancelled, retired.Status)
}

func TestIntegrationWorkflowTaskCancelAfterManualPublicationRecovery(t *testing.T) {
	const id = "op-recovered"
	f := newFeatureFinalizationFixture(t, id, false)
	_, worktree := f.expectedTarget(id)
	f.publication.packageError = errors.New("packaging interrupted")
	_, _, err := f.runError("", "task", "run", id)
	require.ErrorIs(t, err, f.publication.packageError)
	before, item := f.loadFinalTask(id)

	_, _, err = f.runError("y\n", "task", "cancel", id)
	require.ErrorContains(t, err, "unresolved publication")
	busy, stillActive := f.loadFinalTask(id)
	assert.Equal(t, item, stillActive)
	assert.Equal(t, before, busy)

	f.publication.packageError = nil
	selectOperatorCommit(f, worktree, "repaired", "operator-repaired contents\n", "amended or rewritten history")
	f.run("c\n", "task", "run", id)
	f.run("p\n", "task", "run", id)
	assert.Equal(t, "public-repaired", f.candidate.head)
	_, published := f.loadFinalTask(id)
	assert.Equal(t, f.pr.url, published.OrpheusMetadata().PRURL)
	f.pr.state = pullrequest.StateClosed
	_, _, err = f.runError("", "task", "sync", id)
	require.ErrorContains(t, err, "closed without merge")
	observed, _ := f.loadFinalTask(id)

	out, _ := f.run("y\n", "task", "cancel", id)
	assert.Contains(t, out, "cancelled")
	after, cancelled := f.loadFinalTask(id)
	assert.Equal(t, taskmodel.StatusCancelled, cancelled.Status)
	assert.Equal(t, observed, after, "recovery and cancellation must preserve the original operation")
	assert.Equal(t, []string{worktree}, f.publication.cleanupAttempts)
}

func checkCancellationAfterClosedPR(t *testing.T, f *syncWorkflowFixture, observed taskstate.TaskState, item taskmodel.Task) {
	t.Helper()
	worktree := item.OrpheusMetadata().Worktree
	f.forceInteractiveReviewOutput(100)
	output, prompt := f.run("\n", "task", "cancel", item.ID)
	assert.Contains(t, output, "not cancelled")
	assert.Contains(t, prompt, worktree)
	assert.Contains(t, prompt, "[y/N]")
	afterDecline, unchanged := f.loadFinalTask(item.ID)
	assert.Equal(t, observed, afterDecline)
	assert.Equal(t, item, unchanged)
	assert.Empty(t, f.publication.cleanupAttempts)

	f.publication.cleanupError = errors.New("fatal: worktree contains modified or untracked files")
	output, warning := f.run("y\n", "task", "cancel", item.ID)
	assert.Contains(t, output, "cancelled")
	assert.Contains(t, warning, worktree)
	assert.Contains(t, warning, f.publication.cleanupError.Error())
	assert.Contains(t, warning, "orpheus doctor --fix")
	assert.Equal(t, []string{worktree}, f.publication.cleanupAttempts)
	afterFailure, cancelled := f.loadFinalTask(item.ID)
	assert.Equal(t, taskmodel.StatusCancelled, cancelled.Status)
	assert.Contains(t, f.git.targets, worktree)
	assert.Equal(t, observed, afterFailure, "cancellation must not impersonate delivery finalization")

	f.publication.cleanupError = nil
	output, _ = f.run("", "doctor", "--fix")
	assert.Contains(t, output, "removed")
	assert.Equal(t, []string{worktree, worktree}, f.publication.cleanupAttempts)
	assert.NotContains(t, f.git.targets, worktree)
	_, stillCancelled := f.loadFinalTask(item.ID)
	assert.Equal(t, taskmodel.StatusCancelled, stillCancelled.Status)
}
