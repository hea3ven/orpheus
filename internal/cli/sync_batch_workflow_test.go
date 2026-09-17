//go:build integration

package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/registry"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationTaskSyncAllPollsPRBoundaryTasks(t *testing.T) {
	f := newFeatureFinalizationFixture(t, "op-create", false)
	f.backend.tasks["op-open"] = syncTask("op-open", f.pr.url)
	epic := syncTask("op-epic", "https://github.test/org/alpha/pull/88")
	epic.IssueType = taskmodel.IssueTypeEpic
	f.backend.tasks[epic.ID] = epic
	closed := syncTask("op-closed", "https://github.test/org/alpha/pull/99")
	closed.Status = taskmodel.StatusClosed
	f.backend.tasks[closed.ID] = closed
	f.pr.state = pullrequest.StateOpen
	before, item := f.loadFinalTask("op-create")

	stdout, stderr := f.run("", "task", "sync", "--all")

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Open/in-review PRs (1):")
	assert.Contains(t, stdout, "op-open (alpha): PR "+f.pr.url+" is still open for review")
	for _, omitted := range []string{"op-create", "op-epic", "op-closed"} {
		assert.NotContains(t, stdout, omitted)
	}
	require.Len(t, f.pr.statusRequests, 1)
	assert.Equal(t, f.pr.url, f.pr.statusRequests[0].URL)
	assert.Empty(t, f.pr.created)
	assert.Zero(t, f.pr.findReads)
	assert.Empty(t, f.tasks.closed)
	assert.Empty(t, f.mutations.prURLs)
	after, got := f.loadFinalTask(item.ID)
	assert.Equal(t, before, after)
	assert.Equal(t, item, got)
	f.assertUnpublished(item.ID)
}

func TestIntegrationTaskSyncAllReturnsNonZeroAfterCandidateError(t *testing.T) {
	f := newSyncFixture(t)
	delete(f.backend.tasks, "op-sync")
	f.backend.tasks["op-closed-pr"] = syncTask("op-closed-pr", f.pr.url)
	f.pr.state = pullrequest.StateClosed
	before := f.backend.tasks["op-closed-pr"].Clone()

	stdout, stderr, err := f.runError("", "task", "sync", "--all")

	require.ErrorContains(t, err, "task sync --all failed for 1 item")
	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Errors (1):")
	assert.Contains(t, stdout, "op-closed-pr (alpha): sync:")
	assert.Contains(t, stdout, "closed without merge")
	f.assertSyncUnchanged(before)
}

type unavailableSyncTasks struct{ *memoryTaskBackend }

func (*unavailableSyncTasks) List(context.Context) ([]taskmodel.Task, error) {
	return nil, errors.New("task source unavailable")
}

func TestIntegrationTaskSyncAllGroupsCrossRepoResultsAndReturnsNonZeroAfterFailures(t *testing.T) {
	f := newSyncFixture(t)
	alpha, beta, gamma := taskWorkflowRepository(), taskWorkflowRepository(), taskWorkflowRepository()
	alpha.BeadsPrefix = "a"
	beta.ID, beta.Name, beta.Path, beta.BeadsPrefix = "beta", "Beta Repo", "/fixture/task-workflow/repos/beta", "b"
	gamma.ID, gamma.Name, gamma.Path, gamma.BeadsPrefix = "gamma", "Gamma Repo", "/fixture/task-workflow/repos/gamma", "g"
	f.withRegisteredRepos([]registry.Repo{alpha, beta, gamma}...)
	f.mustRemainOffDisk(beta.Path, gamma.Path)
	openURL, mergedURL, closedURL := "https://github.test/org/alpha/pull/77", "https://github.test/org/beta/pull/88", "https://github.test/org/beta/pull/99"
	f.backend.tasks = map[string]taskmodel.Task{
		"a-create": syncTask("a-create", ""), "a-open": syncTask("a-open", openURL),
	}
	f.completion("a-create", "orpheus/a-create", taskWorkflowRepoRoot, "Ready for PR", "Completed but not published", "succeeded")
	betaTasks := &memoryReviewTasks{memoryTaskBackend: newMemoryTaskBackend([]taskmodel.Task{syncTask("b-merged", mergedURL), syncTask("b-closed", closedURL)})}
	gammaTasks := &unavailableSyncTasks{newMemoryTaskBackend(nil)}
	f.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		switch source.Repository.ID {
		case "alpha":
			return f.mutations, nil
		case "beta":
			return betaTasks, nil
		case "gamma":
			return gammaTasks, nil
		default:
			return nil, errors.New("unknown repository")
		}
	}
	f.pr.statuses = map[string]pullrequest.State{openURL: pullrequest.StateOpen, mergedURL: pullrequest.StateMerged, closedURL: pullrequest.StateClosed}

	stdout, stderr, err := f.runError("", "task", "sync", "--all")

	require.ErrorContains(t, err, "task sync --all failed for 2 item")
	assert.Empty(t, stderr)
	for _, want := range []string{
		"Open/in-review PRs (1):", "a-open (alpha): PR " + openURL + " is still open for review",
		"Merged/closed tasks (1):", "b-merged (beta): PR " + mergedURL + " is merged; backend task was closed",
		"Errors (2):", "repo gamma: scan_tasks:", "task source unavailable", "b-closed (beta): sync:", "closed without merge",
	} {
		assert.Contains(t, stdout, want)
	}
	assert.NotContains(t, stdout, "a-create")
	assert.Empty(t, f.pr.created)
	assert.Zero(t, f.pr.findReads)
	assert.Equal(t, 3, f.pr.statusReads)
	assert.Empty(t, f.mutations.prURLs)
	assert.Equal(t, []string{"b-merged"}, betaTasks.closed)
	closed, err := betaTasks.Get(context.Background(), "b-closed")
	require.NoError(t, err)
	assert.Equal(t, taskmodel.StatusInProgress, closed.Status)
	state, err := f.taskStore.Load("beta", "b-merged")
	require.NoError(t, err)
	require.Len(t, state.Events, 1)
	assertMergedAudit(t, state.Events[0], mergedURL)
}
