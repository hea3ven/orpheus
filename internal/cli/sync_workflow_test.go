//go:build integration

package cli_test

import (
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskSyncPollsExistingPRURLWithoutPushOrMutation(t *testing.T) {
	f := newSyncFixture(t)
	// Unsupported worktree metadata still permits polling, but never a Git update.
	item := f.backend.tasks["op-sync"]
	item.Metadata[taskmodel.MetadataWorktree] = "/fixture/unused-worktree"
	f.backend.tasks[item.ID] = item

	stdout, stderr := f.run("", "task", "sync", item.ID)

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Synced op-sync")
	assert.Contains(t, stdout, "PR "+f.pr.url+" is still open for review")
	assert.Equal(t, 1, f.pr.statusReads)
	assert.Equal(t, f.pr.url, f.pr.statusRequests[0].URL)
	assert.Zero(t, f.pr.findReads)
	assert.Empty(t, f.pr.created)
	assert.Empty(t, f.syncGit.calls)
	f.assertSyncUnchanged(item)
}

func (f *syncWorkflowFixture) assertSyncUnchanged(before taskmodel.Task) {
	f.t.Helper()
	state, item := f.loadFinalTask(before.ID)
	assert.Equal(f.t, before, item)
	assert.Empty(f.t, state.Events)
	assert.Nil(f.t, state.ActiveSyncConflict)
	assert.Empty(f.t, f.tasks.closed)
	assert.Empty(f.t, f.mutations.prURLs)
	assert.Empty(f.t, f.candidate.pushes)
	assert.Empty(f.t, f.pr.created)
	assert.Zero(f.t, f.pr.findReads)
}

func TestIntegrationWorkflowTaskSyncRecordsConflictResolutionUsageTelemetry(t *testing.T) {
	f := newSyncFixture(t)
	f.resolvingAgent(nil)
	const usageRoot = "/fixture/sync-usage/codex"
	f.options.Environment["CODEX_HOME"] = usageRoot
	f.mustRemainOffDisk(usageRoot)
	f.options.Dependencies.CaptureUsage = func(opts agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
		_, dir := f.expectedTarget("op-sync")
		assert.Equal(t, map[string]string{"CODEX_HOME": usageRoot}, opts.Env)
		assert.Equal(t, "codex", opts.Harness)
		assert.Equal(t, dir, opts.ExecutionDir)
		assert.Equal(t, "sync-conflict-op-sync", opts.SessionName)
		assert.False(t, opts.StartedAt.IsZero())
		return taskstate.RecordRunUsageOptions{
			Session:      &taskstate.AgentSession{ID: "sync-session"},
			Usage:        &taskstate.AgentUsage{TotalTokens: 190},
			UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: "matched_codex_session"},
		}
	}
	before := f.candidate.head

	stdout, stderr := f.run("", "task", "sync", "op-sync")

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Synced op-sync")
	assert.Contains(t, stdout, "resolved merge conflicts with the configured agent")
	assert.True(t, f.syncGit.resolved)
	assert.NotEqual(t, before, f.candidate.head)
	assert.Equal(t, f.candidate.head, f.publication.remote["orpheus/op-sync"])
	state, item := f.loadFinalTask("op-sync")
	assert.Equal(t, taskmodel.StatusInProgress, item.Status)
	assert.Nil(t, state.ActiveSyncConflict)
	require.Len(t, state.Events, 2)
	started, finished := state.Events[0], state.Events[1]
	assert.Equal(t, taskstate.EventSyncConflictStarted, started.Type)
	require.NotNil(t, started.Execution)
	assert.Equal(t, "sync-codex", started.Execution.Profile)
	assert.Equal(t, taskstate.EventSyncConflictFinished, finished.Type)
	assert.Equal(t, f.candidate.head, finished.Commit)
	require.NotNil(t, finished.Execution)
	assert.Equal(t, taskstate.AgentExecutionPurposeSyncConflictResolution, finished.Execution.Purpose)
	assert.Equal(t, "sync-codex", finished.Execution.Profile)
	assert.Equal(t, taskstate.RunStatusSucceeded, finished.Execution.Status)
	require.NotNil(t, finished.Execution.Session)
	assert.Equal(t, "sync-session", finished.Execution.Session.ID)
	require.NotNil(t, finished.Execution.Usage)
	assert.Equal(t, 190, finished.Execution.Usage.TotalTokens)
	assert.Equal(t, taskstate.UsageCaptureCaptured, finished.Execution.UsageCapture.Status)
	assert.Equal(t, "matched_codex_session", finished.Execution.UsageCapture.Reason)
	require.Len(t, f.agent.launches, 1)
	launch := f.agent.launches[0]
	assert.Equal(t, "conflict_resolution", launch.environment["ORPHEUS_AGENT_PURPOSE"])
	assert.Equal(t, "conflict.txt", launch.environment["ORPHEUS_CONFLICT_FILES"])
	assert.Equal(t, "orpheus/op-sync", launch.environment["ORPHEUS_BRANCH"])
	f.run("", "task", "sync", "op-sync")
	assert.Len(t, f.agent.launches, 1)
	assert.Equal(t, 1, f.syncGit.commits)
	assert.Len(t, f.candidate.pushes, 1)
}

func TestIntegrationWorkflowTaskSyncClosesBackendAndRecordsLocalAuditForMergedPR(t *testing.T) {
	f := newSyncFixture(t)
	item := f.backend.tasks["op-sync"]
	item.Metadata[taskmodel.MetadataWorktree] = "/fixture/unused-worktree"
	f.backend.tasks[item.ID] = item
	f.pr.state = pullrequest.StateMerged

	stdout, stderr := f.run("", "task", "sync", item.ID)

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Synced op-sync")
	assert.Contains(t, stdout, "PR "+f.pr.url+" is merged")
	assert.Contains(t, stdout, "Backend task was closed")
	assert.Equal(t, []string{item.ID}, f.tasks.closed)
	assert.Empty(t, f.mutations.prURLs)
	assert.Empty(t, f.pr.created)
	assert.Zero(t, f.pr.findReads)
	assert.Equal(t, 1, f.pr.statusReads)
	state, item := f.loadFinalTask(item.ID)
	assert.Equal(t, taskmodel.StatusClosed, item.Status)
	require.Len(t, state.Events, 1)
	assertMergedAudit(t, state.Events[0], f.pr.url)
	f.run("", "task", "sync", item.ID)
	after, _ := f.loadFinalTask(item.ID)
	assert.Equal(t, state, after)
	assert.Len(t, f.tasks.closed, 1)
	assert.Equal(t, 1, f.pr.statusReads)
}

func assertMergedAudit(t *testing.T, event taskstate.Event, url string) {
	t.Helper()
	assert.Equal(t, taskstate.EventTaskClosed, event.Type)
	assert.Equal(t, taskstate.CloseReasonPRMerged, event.CloseReason)
	assert.Equal(t, url, event.PRURL)
	assert.Equal(t, "merged", event.ObservedPRState)
}

func TestIntegrationWorkflowTaskSyncExistingPRErrorsDoNotMutateBackendOrAudit(t *testing.T) {
	// Parsing and error translation are asserted by the GH adapter contracts.
	for _, message := range []string{
		`pull request URL "not-a-url" is invalid`,
		"provider omitted a valid PR URL", "provider returned no valid PR URL",
		"repository could not be resolved by gh", "gh authentication failed or is missing", "closed without merge",
	} {
		t.Run(message, func(t *testing.T) {
			f := newSyncFixture(t)
			before := f.backend.tasks["op-sync"].Clone()
			if message == "closed without merge" {
				f.pr.state = pullrequest.StateClosed
			} else {
				f.pr.statusError = errors.New(message)
			}

			stdout, stderr, err := f.runError("", "task", "sync", "op-sync")

			require.ErrorContains(t, err, message)
			if f.pr.statusError != nil {
				assert.ErrorIs(t, err, f.pr.statusError)
			}
			assert.Empty(t, stdout)
			assert.Empty(t, stderr)
			assert.Equal(t, 1, f.pr.statusReads)
			assert.Empty(t, f.syncGit.calls)
			f.assertSyncUnchanged(before)
		})
	}
}

func TestIntegrationWorkflowTaskSyncSkipsClosedTaskWithoutPRPolling(t *testing.T) {
	f := newSyncFixture(t)
	item := f.backend.tasks["op-sync"]
	item.Status = taskmodel.StatusClosed
	f.backend.tasks[item.ID] = item

	stdout, stderr := f.run("", "task", "sync", item.ID)

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Skipped op-sync")
	assert.Contains(t, stdout, "task is closed")
	assert.Contains(t, stdout, "No backend changes were made")
	assert.Zero(t, f.pr.statusReads)
	f.assertSyncUnchanged(item)
}

func TestIntegrationWorkflowTaskSyncSkipsTaskWithoutPRURLAtRepoRoot(t *testing.T) {
	f := newFeatureFinalizationFixture(t, "op-sync", true)
	before, item := f.loadFinalTask("op-sync")

	stdout, stderr := f.run("", "task", "sync", item.ID)

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Skipped op-sync")
	assert.Contains(t, stdout, "orpheus.pr_url is not set")
	assert.Contains(t, stdout, "No backend changes were made")
	after, got := f.loadFinalTask(item.ID)
	assert.Equal(t, before, after)
	assert.Equal(t, item, got)
	f.assertUnpublished(item.ID)
	assert.Zero(t, f.pr.statusReads)
}

func TestIntegrationWorkflowTaskSyncSkipsMainSoloLocalReadyTaskWithoutPRURL(t *testing.T) {
	f := newMainFinalizationFixture(t, "op-main")
	before, item := f.loadFinalTask("op-main")

	stdout, stderr := f.run("", "task", "sync", item.ID)

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Skipped op-main")
	assert.Contains(t, stdout, "orpheus.pr_url is not set")
	assert.Contains(t, stdout, "No backend changes were made")
	after, got := f.loadFinalTask(item.ID)
	assert.Equal(t, before, after)
	assert.Equal(t, item, got)
	f.assertUnpublished(item.ID)
	assert.Zero(t, f.pr.statusReads)
}
