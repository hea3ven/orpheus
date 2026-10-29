//go:build integration

package cli_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskSyncPollsExistingPRURLWithoutPushOrMutation(t *testing.T) {
	for _, missingMetadata := range []bool{false, true} {
		t.Run(fmt.Sprintf("missing metadata=%t", missingMetadata), func(t *testing.T) {
			f := newSyncFixture(t)
			// Unsupported worktree metadata still permits polling, but never a Git update.
			item := f.backend.tasks["op-sync"]
			item.Metadata[taskmodel.MetadataWorktree] = "/fixture/unused-worktree"
			item.Metadata[taskmodel.MetadataPRURL] = " " + f.pr.url + " "
			if missingMetadata {
				delete(item.Metadata, taskmodel.MetadataWorktree)
				delete(item.Metadata, taskmodel.MetadataBranch)
			}
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
		})
	}
}
func (f *syncWorkflowFixture) assertSyncUnchanged(before taskmodel.Task) {
	f.t.Helper()
	state, item := f.loadFinalTask(before.ID)
	assert.Equal(f.t, before, item)
	assert.Equal(f.t, f.initial, state, "polling/refusal cannot alter the already recorded workflow")
	assert.Nil(f.t, state.ActiveSyncConflict)
	assert.Empty(f.t, f.tasks.closed)
	assert.Empty(f.t, f.mutations.prURLs)
	assert.Empty(f.t, f.candidate.pushes)
	assert.Empty(f.t, f.pr.created)
	assert.Zero(f.t, f.pr.findReads)
}

func TestIntegrationWorkflowTaskSyncRecordsConflictResolutionUsageTelemetry(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch=%t", batch), func(t *testing.T) {
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
					Usage:        &taskstate.AgentUsage{InputTokens: 120, OutputTokens: 70, TotalTokens: 190},
					UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: "matched_codex_session", CandidateCount: 1},
					Model:        "gpt-5",
				}
			}
			before := f.candidate.head

			command := []string{"task", "sync", "op-sync"}
			if batch {
				command = []string{"task", "sync", "--all"}
			}
			stdout, stderr := f.run("", command...)

			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "op-sync")
			assert.Contains(t, stdout, "resolved merge conflicts with the configured agent")
			assert.True(t, f.syncGit.resolved)
			assert.NotEqual(t, before, f.candidate.head)
			assert.Equal(t, f.candidate.head, f.publication.remote["orpheus/op-sync"])
			state, item := f.loadFinalTask("op-sync")
			assert.Equal(t, taskmodel.StatusInProgress, item.Status)
			assert.Nil(t, state.ActiveSyncConflict)
			events := resolverEvents(state.Events)
			require.Len(t, events, 2)
			started, finished := events[0], events[1]
			assert.Equal(t, taskstate.EventSyncConflictStarted, started.Type)
			require.NotNil(t, started.Execution)
			assert.Equal(t, "sync-codex", started.Execution.Profile)
			assert.Equal(t, taskstate.EventSyncConflictFinished, finished.Type)
			foundPush := false
			for _, event := range state.Events {
				if event.Type == taskstate.EventBranchSyncPushed {
					foundPush = true
					assert.Equal(t, f.candidate.head, event.Commit)
				}
			}
			assert.True(t, foundPush, "resolver success and independently observed remote delivery must remain separate facts")
			require.NotNil(t, finished.Execution)
			assert.Equal(t, taskstate.AgentExecutionPurposeSyncConflictResolution, finished.Execution.Purpose)
			assert.Equal(t, "sync-codex", finished.Execution.Profile)
			assert.Equal(t, "gpt-5", finished.Execution.Model)
			assert.False(t, finished.Execution.StartedAt.IsZero())
			require.NotNil(t, finished.Execution.FinishedAt)
			assert.False(t, finished.Execution.FinishedAt.Before(finished.Execution.StartedAt))
			_, targetDir := f.expectedTarget("op-sync")
			assert.Equal(t, targetDir, finished.Worktree)
			assert.Equal(t, "main", finished.DefaultBranch)
			assert.Equal(t, "orpheus/op-sync", finished.Branch)
			assert.Equal(t, f.pr.url, finished.PRURL)
			assert.Equal(t, []string{"conflict.txt"}, finished.ConflictFiles)
			assert.Equal(t, taskstate.RunStatusSucceeded, finished.Execution.Status)
			require.NotNil(t, finished.Execution.Session)
			assert.Equal(t, "sync-session", finished.Execution.Session.ID)
			require.NotNil(t, finished.Execution.Usage)
			assert.Equal(t, 190, finished.Execution.Usage.TotalTokens)
			assert.Equal(t, 120, finished.Execution.Usage.InputTokens)
			assert.Equal(t, 70, finished.Execution.Usage.OutputTokens)
			assert.Equal(t, taskstate.UsageCaptureCaptured, finished.Execution.UsageCapture.Status)
			assert.Equal(t, "matched_codex_session", finished.Execution.UsageCapture.Reason)
			assert.Equal(t, 1, finished.Execution.UsageCapture.CandidateCount)
			require.Len(t, f.agent.launches, 1)
			launch := f.agent.launches[0]
			assert.Equal(t, "conflict_resolution", launch.environment["ORPHEUS_AGENT_PURPOSE"])
			assert.Equal(t, "conflict.txt", launch.environment["ORPHEUS_CONFLICT_FILES"])
			assert.Equal(t, "orpheus/op-sync", launch.environment["ORPHEUS_BRANCH"])
			f.run("", "task", "sync", "op-sync")
			assert.Len(t, f.agent.launches, 1)
			assert.Equal(t, 1, f.syncGit.commits)
			assert.Len(t, f.candidate.pushes, 1)
			require.NotEmpty(t, f.syncGit.calls)
			expectedPolicy := gitmeta.TaskBranchUpdateAlways
			if batch {
				expectedPolicy = gitmeta.TaskBranchUpdateConflictsOnly
			}
			assert.Equal(t, expectedPolicy, f.syncGit.calls[0].UpdatePolicy)
		})
	}
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
	require.Len(t, state.Events, len(f.initial.Events)+1)
	assertMergedAudit(t, state.Events[len(state.Events)-1], f.pr.url)
	retryOut, retryErr := f.run("", "task", "sync", item.ID)
	assert.Empty(t, retryErr)
	for _, want := range []string{"Skipped op-sync", "task is closed", "No backend changes were made"} {
		assert.Contains(t, retryOut, want)
	}
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
		"provider request failed", "closed without merge", "unsupported provider state",
	} {
		t.Run(message, func(t *testing.T) {
			f := newSyncFixture(t)
			before := f.backend.tasks["op-sync"].Clone()
			if message == "closed without merge" {
				f.pr.state = pullrequest.StateClosed
			} else if message == "unsupported provider state" {
				f.pr.state = pullrequest.State("draft")
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

func TestIntegrationWorkflowTaskSyncSkipsUnpublishedTargetsWithoutEffects(t *testing.T) {
	for _, target := range []string{"repository root", "worktree", "malformed metadata"} {
		t.Run(target, func(t *testing.T) {
			var f *finalizationWorkflowFixture
			if target == "repository root" {
				f = newMainFinalizationFixture(t, "op-sync")
			} else {
				f = newFeatureFinalizationFixture(t, "op-sync", true)
			}
			if target == "malformed metadata" {
				item := f.backend.tasks["op-sync"]
				delete(item.Metadata, taskmodel.MetadataBranch)
				f.backend.tasks[item.ID] = item
			}
			before, item := f.loadFinalTask("op-sync")

			stdout, stderr := f.run("", "task", "sync", item.ID)

			assert.Empty(t, stderr)
			for _, want := range []string{"Skipped op-sync", "orpheus.pr_url is not set", "No backend changes were made"} {
				assert.Contains(t, stdout, want)
			}
			after, got := f.loadFinalTask(item.ID)
			assert.Equal(t, before, after)
			assert.Equal(t, item, got)
			f.assertUnpublished(item.ID)
			assert.Zero(t, f.pr.statusReads)
		})
	}
}
