//go:build integration

package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskSyncUpdatesBranchAndRetriesPushWithoutAnotherMerge(t *testing.T) {
	f := newSyncFixture(t)
	f.syncGit.behind = true
	f.publication.pushError = errors.New("origin unavailable")

	stdout, stderr, err := f.runError("", "task", "sync", "op-sync")

	require.ErrorIs(t, err, f.publication.pushError)
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	head := f.candidate.head
	assert.NotEqual(t, head, f.publication.remote["orpheus/op-sync"])
	assert.Equal(t, 1, f.syncGit.commits)
	f.publication.pushError = nil
	stdout, stderr = f.run("", "task", "sync", "op-sync")
	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "already includes main")
	assert.Equal(t, head, f.publication.remote["orpheus/op-sync"])
	currentOut, currentErr := f.run("", "task", "sync", "op-sync")
	assert.Empty(t, currentErr)
	assert.Contains(t, currentOut, "already includes main")
	assert.Equal(t, 1, f.syncGit.commits)
	assert.Len(t, f.candidate.pushes, 1)
	assert.Empty(t, f.agent.launches)
	assert.Empty(t, f.pr.created)
	assert.Empty(t, f.tasks.closed)
	branch, worktree := f.expectedTarget("op-sync")
	for _, call := range f.syncGit.calls {
		assert.Equal(t, taskWorkflowRepoRoot, call.RepoPath)
		assert.Equal(t, "main", call.DefaultBranch)
		assert.Equal(t, branch, call.Branch)
		assert.Equal(t, worktree, call.Worktree)
		assert.Equal(t, gitmeta.TaskBranchUpdateAlways, call.UpdatePolicy)
	}
}

func TestIntegrationWorkflowTaskSyncAllLeavesConflictFreeBranchUnchanged(t *testing.T) {
	f := newSyncFixture(t)
	f.syncGit.behind = true
	head := f.candidate.head

	stdout, stderr := f.run("", "task", "sync", "--all")

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "conflict-only batch sync left it unchanged")
	assert.Equal(t, head, f.candidate.head)
	assert.Equal(t, head, f.publication.remote["orpheus/op-sync"])
	assert.Empty(t, f.candidate.pushes)
	assert.Zero(t, f.syncGit.commits)
	require.Len(t, f.syncGit.calls, 1)
	assert.Equal(t, gitmeta.TaskBranchUpdateConflictsOnly, f.syncGit.calls[0].UpdatePolicy)
	f.run("", "task", "sync", "op-sync")
	assert.NotEqual(t, head, f.candidate.head)
	assert.Equal(t, f.candidate.head, f.publication.remote["orpheus/op-sync"])
	assert.Equal(t, 1, f.syncGit.commits)
}

func TestIntegrationWorkflowTaskSyncConflictFailureRollsBackAndRetries(t *testing.T) {
	for _, failure := range []string{"resolver", "cancellation", "start failure", "unresolved files", "push"} {
		t.Run(failure, func(t *testing.T) {
			f := newSyncFixture(t)
			cause := errors.New("repair unavailable")
			if failure == "resolver" || failure == "cancellation" || failure == "start failure" {
				if failure == "cancellation" {
					cause = context.Canceled
				}
				f.resolvingAgent(cause)
				if failure == "start failure" {
					f.agent.outcomes[0].err = nil
					f.agent.outcomes[0].startFailure = &agentexec.StartError{Err: cause}
				}
			} else if failure == "unresolved files" {
				f.resolvingAgent(nil)
				f.agent.outcomes[0].mutateCandidate = func() {}
			} else {
				f.resolvingAgent(nil)
				f.publication.pushError = cause
			}
			captured := false
			f.options.Dependencies.CaptureUsage = func(opts agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
				captured = true
				assert.Equal(t, "sync-conflict-op-sync", opts.SessionName)
				return taskstate.RecordRunUsageOptions{UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureUnknown, Reason: "failed execution has no usage record"}}
			}
			head := f.candidate.head

			stdout, stderr, err := f.runError("", "task", "sync", "op-sync")

			if failure == "unresolved files" {
				require.ErrorContains(t, err, "unresolved conflict files")
			} else {
				require.ErrorIs(t, err, cause)
			}
			assert.Empty(t, stdout)
			assert.Empty(t, stderr)
			assert.Equal(t, head, f.candidate.head)
			assert.Equal(t, head, f.publication.remote["orpheus/op-sync"])
			assert.False(t, f.syncGit.mergeActive)
			assert.Equal(t, 1, f.syncGit.rollbacks)
			state, item := f.loadFinalTask("op-sync")
			assert.Nil(t, state.ActiveSyncConflict)
			assert.Equal(t, taskmodel.StatusInProgress, item.Status)
			events := resolverEvents(state.Events)
			require.Len(t, events, 2)
			assert.Equal(t, taskstate.EventSyncConflictStarted, events[0].Type)
			finished := events[1]
			require.NotNil(t, finished.Execution)
			native := state.Work.Workflows[0].Phases[4].(*work.ExternalReviewPhase).BranchSyncs[0]
			require.NotNil(t, native.Rollback)
			if failure == "unresolved files" {
				assert.Contains(t, native.Rollback.Reason, "unresolved conflict files")
			} else {
				assert.Contains(t, native.Rollback.Reason, cause.Error())
			}
			assert.Equal(t, failure != "start failure", captured)
			if failure == "unresolved files" || failure == "push" {
				assert.Equal(t, taskstate.RunStatusSucceeded, finished.Execution.Status, "a successful native resolver exit does not establish successful merge/push")
				exit := native.ConflictResolution.Execution.Process.(*work.ExitedProcess)
				assert.Equal(t, 0, *exit.Code)
			} else {
				assert.Equal(t, taskstate.RunStatusFailed, finished.Execution.Status)
			}
			assert.Nil(t, finished.Execution.Usage)
			assert.Equal(t, taskstate.UsageCaptureUnknown, finished.Execution.UsageCapture.Status)
			if failure == "start failure" {
				assert.IsType(t, &work.StartFailedProcess{}, native.ConflictResolution.Execution.Process)
				assert.Equal(t, "agent process failed before usage capture", finished.Execution.UsageCapture.Reason)
			} else {
				assert.Equal(t, "failed execution has no usage record", finished.Execution.UsageCapture.Reason)
			}

			f.publication.pushError = nil
			f.resolvingAgent(nil)

			stdout, stderr = f.run("", "task", "sync", "op-sync")

			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "resolved merge conflicts with the configured agent")
			assert.NotEqual(t, head, f.candidate.head)
			assert.Equal(t, f.candidate.head, f.publication.remote["orpheus/op-sync"])
			state, _ = f.loadFinalTask("op-sync")
			assert.Nil(t, state.ActiveSyncConflict)
			events = resolverEvents(state.Events)
			assert.Equal(t, taskstate.EventSyncConflictFinished, events[len(events)-1].Type)
			f.run("", "task", "sync", "op-sync")
			assert.Len(t, f.agent.launches, 2)
			assert.Len(t, f.candidate.pushes, 1)
			assert.Empty(t, f.pr.created)
			assert.Empty(t, f.tasks.closed)
		})
	}
}

func TestIntegrationWorkflowTaskSyncRecoversAmbiguousPushWithoutRepeatingRepair(t *testing.T) {
	for _, moved := range []bool{false, true} {
		name := "published head confirmed"
		if moved {
			name = "remote moved requires manual recovery"
		}
		t.Run(name, func(t *testing.T) {
			f := newSyncFixture(t)
			f.resolvingAgent(nil)
			f.syncGit.remoteError = errors.New("remote inspection unavailable")

			_, _, err := f.runError("", "task", "sync", "op-sync")

			require.ErrorContains(t, err, "remote inspection unavailable")
			state, _ := f.loadFinalTask("op-sync")
			require.NotNil(t, state.ActiveSyncConflict)
			assert.Equal(t, taskstate.SyncConflictPhaseLocalCompleted, state.ActiveSyncConflict.Phase)
			assert.Equal(t, f.candidate.head, state.ActiveSyncConflict.LocalHead)
			assert.Equal(t, f.candidate.head, f.publication.remote["orpheus/op-sync"])
			require.NotNil(t, state.ActiveSyncConflict.Execution)
			assert.Equal(t, taskstate.RunStatusSucceeded, state.ActiveSyncConflict.Execution.Status, "native exit is retained while remote inspection is unavailable")
			beforeRetry := state

			f.syncGit.remoteError = nil
			if moved {
				f.publication.remote["orpheus/op-sync"] = "someone-elses-commit"
			}

			stdout, stderr, err := f.runError("", "task", "sync", "op-sync")

			assert.Empty(t, stderr)
			state, _ = f.loadFinalTask("op-sync")
			if moved {
				require.ErrorContains(t, err, "remote task branch changed from the recorded checkpoint")
				assert.Empty(t, stdout)
				require.NotNil(t, state.ActiveSyncConflict)
				assert.Equal(t, taskstate.SyncConflictPhaseLocalCompleted, state.ActiveSyncConflict.Phase)
				assert.Equal(t, "someone-elses-commit", f.publication.remote["orpheus/op-sync"])
				assert.Equal(t, beforeRetry, state, "remote uncertainty cannot rewrite a native exit or add an unresolved-status journal")
				_, _, err = f.runError("", "task", "sync", "op-sync")
				require.ErrorContains(t, err, "unresolved sync conflict recovery")
			} else {
				require.NoError(t, err)
				assert.Contains(t, stdout, "is still open for review")
				assert.Nil(t, state.ActiveSyncConflict)
				events := resolverEvents(state.Events)
				require.Len(t, events, 2)
				assert.Equal(t, taskstate.EventSyncConflictFinished, events[1].Type)
				first := state.Work.Workflows[0].Phases[4].(*work.ExternalReviewPhase).BranchSyncs[0]
				require.NotNil(t, first.Push)
				assert.Equal(t, f.candidate.head, first.Push.Head)
			}
			assert.Len(t, f.agent.launches, 1)
			assert.Equal(t, 1, f.syncGit.commits)
			assert.Len(t, f.candidate.pushes, 1)
			assert.Zero(t, f.syncGit.rollbacks)
		})
	}
}

func TestIntegrationWorkflowTaskSyncMergedPRRetriesFailedClosure(t *testing.T) {
	f := newSyncFixture(t)
	f.pr.state = pullrequest.StateMerged
	f.mutations.closeError = errors.New("backend unavailable")
	beforeState, before := f.loadFinalTask("op-sync")

	_, _, err := f.runError("", "task", "sync", "op-sync")

	require.ErrorIs(t, err, f.mutations.closeError)
	failedState, failedTask := f.loadFinalTask("op-sync")
	assert.Equal(t, beforeState, failedState)
	assert.Equal(t, before, failedTask)
	assert.Empty(t, f.tasks.closed)
	f.mutations.closeError = nil
	f.run("", "task", "sync", "op-sync")
	state, item := f.loadFinalTask("op-sync")
	assert.Equal(t, taskmodel.StatusClosed, item.Status)
	require.Len(t, state.Events, len(beforeState.Events)+2)
	assertMergedAudit(t, state.Events[len(beforeState.Events)], f.pr.url)
	assert.Equal(t, taskstate.EventWorktreeRemoved, state.Events[len(state.Events)-1].Type)
	assert.NotContains(t, f.git.targets, before.OrpheusMetadata().Worktree)
	f.run("", "task", "sync", "op-sync")
	assert.Len(t, f.tasks.closed, 1)
	assert.Equal(t, 2, f.pr.statusReads)
}

func TestIntegrationWorkflowPublicationRetriesPRLookupWithoutRepublishing(t *testing.T) {
	f := newFeatureFinalizationFixture(t, "op-retry", false)
	f.pr.findError = errors.New("PR lookup unavailable")

	_, _, err := f.runError("", "task", "done", "op-retry")

	require.ErrorIs(t, err, f.pr.findError)
	assert.Empty(t, f.pr.created)
	assert.Empty(t, f.mutations.prURLs)
	f.pr.findError = nil
	f.run("", "task", "done", "op-retry")
	f.assertPublishedPR("op-retry")
	assert.Len(t, f.pr.created, 1)
	assert.Len(t, f.candidate.commits, 1)
	assert.Len(t, f.candidate.pushes, 1)
}
