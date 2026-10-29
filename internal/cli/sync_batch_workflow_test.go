//go:build integration

package cli_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/registry"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskSyncAllPollsPRBoundaryTasks(t *testing.T) {
	f := newFeatureFinalizationFixture(t, "op-create", false)
	f.backend.tasks["op-open"] = syncTask("op-open", f.pr.url)
	seedPublishedPollingTask(t, f.workflowFixture, "alpha", "op-open", taskWorkflowRepoRoot, f.pr.url)
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

type unavailableSyncTasks struct{ *memoryTaskBackend }

func TestIntegrationWorkflowTaskSyncAllContinuesAfterGitAndConflictAgentFailures(t *testing.T) {
	for _, failure := range []string{"Git update", "conflict agent"} {
		t.Run(failure, func(t *testing.T) {
			f := newSyncFixture(t)
			cause := errors.New("repair unavailable")
			if failure == "Git update" {
				f.syncGit.syncError = cause
			} else {
				f.resolvingAgent(cause)
			}
			f.backend.tasks["op-z-current"] = syncTask("op-z-current", "https://github.test/org/alpha/pull/99")
			seedPublishedPollingTask(t, f.workflowFixture, "alpha", "op-z-current", taskWorkflowRepoRoot, "https://github.test/org/alpha/pull/99")
			f.pr.statuses = map[string]pullrequest.State{"https://github.test/org/alpha/pull/99": pullrequest.StateOpen}
			head := f.candidate.head
			remote := f.publication.remote["orpheus/op-sync"]

			stdout, stderr, err := f.runError("", "task", "sync", "--all")

			require.ErrorContains(t, err, "task sync --all failed for 1 item")
			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "repair unavailable")
			assert.Contains(t, stdout, "op-z-current (alpha): PR")
			assert.Contains(t, stdout, "Open/in-review PRs (1):")
			assert.Equal(t, head, f.candidate.head)
			assert.Equal(t, remote, f.publication.remote["orpheus/op-sync"])
			assert.Empty(t, f.candidate.pushes)
			assert.Empty(t, f.tasks.closed)
			assert.Empty(t, f.mutations.prURLs)
			state, item := f.loadFinalTask("op-sync")
			assert.Equal(t, taskmodel.StatusInProgress, item.Status)
			assert.Nil(t, state.ActiveSyncConflict)
			require.Len(t, f.syncGit.calls, 1)
			assert.Equal(t, gitmeta.TaskBranchUpdateConflictsOnly, f.syncGit.calls[0].UpdatePolicy)
			native := state.Work.Workflows[0].Phases[4].(*work.ExternalReviewPhase).BranchSyncs
			require.Len(t, native, 1)
			require.NotNil(t, native[0].Rollback, "a failed native begin or resolver can settle only by independently verified restoration")
			assert.Contains(t, native[0].Rollback.Reason, cause.Error())
			if failure == "conflict agent" {
				assert.Equal(t, 1, f.syncGit.rollbacks)
				assert.False(t, f.syncGit.mergeActive)
				events := resolverEvents(state.Events)
				require.Len(t, events, 2)
				assert.Equal(t, taskstate.EventSyncConflictFailed, events[1].Type)
				assert.Equal(t, taskstate.RunStatusFailed, events[1].Execution.Status)
			} else {
				assert.Empty(t, resolverEvents(state.Events))
				assert.Empty(t, f.agent.launches)
			}

		})
	}
}

func (*unavailableSyncTasks) List(context.Context) ([]taskmodel.Task, error) {
	return nil, errors.New("task source unavailable")
}

func TestIntegrationWorkflowTaskSyncAllGroupsCrossRepoResultsAndReturnsNonZeroAfterFailures(t *testing.T) {
	for _, factoryFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("factory failure=%t", factoryFailure), func(t *testing.T) {
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
					if factoryFailure {
						return nil, errors.New("task source unavailable")
					}
					return gammaTasks, nil
				default:
					return nil, errors.New("unknown repository")
				}
			}
			seedPublishedPollingTask(t, f.workflowFixture, "alpha", "a-open", alpha.Path, openURL)
			seedPublishedPollingTask(t, f.workflowFixture, "beta", "b-merged", beta.Path, mergedURL)
			seedPublishedPollingTask(t, f.workflowFixture, "beta", "b-closed", beta.Path, closedURL)
			failedBefore, err := betaTasks.Get(context.Background(), "b-closed")
			require.NoError(t, err)
			failedStateBefore, err := f.taskStore.Load("beta", "b-closed")
			require.NoError(t, err)
			f.pr.statuses = map[string]pullrequest.State{openURL: pullrequest.StateOpen, mergedURL: pullrequest.StateMerged, closedURL: pullrequest.StateClosed}

			stdout, stderr, err := f.runError("", "task", "sync", "--all")

			require.ErrorContains(t, err, "task sync --all failed for 2 item")
			assert.Empty(t, stderr)
			for _, want := range []string{
				"Open/in-review PRs (1):", "a-open (alpha): PR " + openURL + " is still open for review",
				"Merged/closed tasks (1):", "b-merged (beta): PR " + mergedURL + " is merged; backend task was closed",
				"Errors (2):", "repo gamma:", "task source unavailable", "b-closed (beta): sync:", "closed without merge",
			} {
				assert.Contains(t, stdout, want)
			}
			if factoryFailure {
				assert.Contains(t, stdout, "create_scan_backend:")
			} else {
				assert.Contains(t, stdout, "scan_tasks:")
			}
			assert.NotContains(t, stdout, "a-create")
			assert.Empty(t, f.pr.created)
			assert.Zero(t, f.pr.findReads)
			assert.Equal(t, 3, f.pr.statusReads)
			assert.Empty(t, f.mutations.prURLs)
			assert.Equal(t, []string{"b-merged"}, betaTasks.closed)
			closed, err := betaTasks.Get(context.Background(), "b-closed")
			require.NoError(t, err)
			assert.Equal(t, failedBefore, closed)
			failedStateAfter, err := f.taskStore.Load("beta", "b-closed")
			require.NoError(t, err)
			assert.Equal(t, failedStateBefore, failedStateAfter)
			state, err := f.taskStore.Load("beta", "b-merged")
			require.NoError(t, err)
			assertMergedAudit(t, state.Events[len(state.Events)-1], mergedURL)
		})
	}
}
