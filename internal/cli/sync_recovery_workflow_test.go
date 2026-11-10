//go:build integration

package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
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
	assert.Contains(t, stderr, "EXTERNAL REVIEW")
	head := f.candidate.head
	assert.NotEqual(t, head, f.publication.remote["orpheus/op-sync"])
	assert.Equal(t, 1, f.syncGit.commits)
	f.publication.pushError = nil
	stdout, stderr = f.run("", "task", "sync", "op-sync")
	assert.Contains(t, stderr, "EXTERNAL REVIEW")
	assert.Contains(t, stdout, "already includes main")
	assert.Equal(t, head, f.publication.remote["orpheus/op-sync"])
	currentOut, currentErr := f.run("", "task", "sync", "op-sync")
	assert.Contains(t, currentErr, "EXTERNAL REVIEW")
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

	assert.Contains(t, stderr, "EXTERNAL REVIEW")
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
			assert.Contains(t, stderr, "EXTERNAL REVIEW")
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

			assert.Contains(t, stderr, "EXTERNAL REVIEW")
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

			if moved {
				assert.Empty(t, stderr)
			} else {
				assert.Contains(t, stderr, "EXTERNAL REVIEW")
			}
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
	for _, flow := range []publication.IntegrationFlow{publication.IntegrationFlowPullRequest, publication.IntegrationFlowDirectMerge} {
		for _, failure := range []string{"close rejected", "ambiguous close", "closure recording", "closure inspection", "closure intent", "acceptance recording", "terminal observation"} {
			if flow == publication.IntegrationFlowDirectMerge && (failure == "acceptance recording" || failure == "terminal observation") {
				continue
			}
			t.Run(string(flow)+"/"+failure, func(t *testing.T) {
				var f *finalizationWorkflowFixture
				if flow == publication.IntegrationFlowPullRequest {
					f = newSyncFixture(t).finalizationWorkflowFixture
					f.pr.state = pullrequest.StateMerged
				} else {
					f = newFinalizationFixture(t, "op-sync")
					f.setConfig("publication", map[string]any{"integration_flow": flow})
					branch, dir := f.expectedTarget("op-sync")
					f.completion("op-sync", branch, dir, "feat: deliver work", "Deliver once.", taskstate.RunStatusSucceeded)
					f.passedReview("op-sync")
					f.run("", "task", "run", "op-sync")
				}
				before, _ := f.loadFinalTask("op-sync")
				pushes, packages, syncs, merges := len(f.candidate.pushes), f.publication.packageCalls, f.publication.syncCalls, f.publication.merges
				injected := errors.New("injected finalization failure")
				f.mutations.beforeClose = func() {
					saved, err := f.taskStore.Load("alpha", "op-sync")
					require.NoError(t, err)
					w := &saved.Work.Workflows[0]
					final := w.LastPhase().(*work.FinalizationPhase)
					require.NotNil(t, final.ClosureIntentAt, "closure intent must survive before the source effect")
					if accepted, ok := final.Acceptance.(*work.PRAcceptance); ok {
						require.NotNil(t, w.Phases[accepted.ExternalReview].(*work.ExternalReviewPhase).Outcome)
						assert.Equal(t, work.PRMerged, w.Phases[accepted.ExternalReview].(*work.ExternalReviewPhase).Outcome.State)
					}
				}
				switch failure {
				case "close rejected":
					f.mutations.closeError = injected
				case "ambiguous close":
					f.mutations.afterCloseError = injected
				case "closure inspection":
					f.mutations.closedReadError = injected
				default:
					require.NoError(t, state.SetMemoryDataWriteError(f.paths, func(_ string, data []byte) error {
						proposed, err := workstate.Decode(data)
						if err != nil {
							return err
						}
						last := proposed.Workflows[0].LastPhase()
						final, isFinal := last.(*work.FinalizationPhase)
						external, isExternal := last.(*work.ExternalReviewPhase)
						match := (failure == "closure recording" && isFinal && final.Closure != nil) ||
							(failure == "closure intent" && isFinal && final.ClosureIntentAt != nil) ||
							(failure == "acceptance recording" && isFinal) ||
							(failure == "terminal observation" && isExternal && external.Outcome != nil)
						if match {
							return injected
						}
						return nil
					}))
				}

				_, _, err := f.runError("", "task", "sync", "op-sync")

				require.ErrorIs(t, err, injected)
				pending, loadErr := f.taskStore.Load("alpha", "op-sync")
				require.NoError(t, loadErr)
				assert.Equal(t, before.Runs, pending.Runs)
				assert.Equal(t, before.Reviews, pending.Reviews)
				assert.Nil(t, taskstate.FinalizationFacts(pending).ClosedAt)
				if flow == publication.IntegrationFlowPullRequest && failure != "terminal observation" {
					outcome := pending.Work.Workflows[0].Phases[4].(*work.ExternalReviewPhase).Outcome
					require.NotNil(t, outcome)
					assert.Equal(t, work.PRMerged, outcome.State)
					// A terminal outcome, not another provider call, owns closure continuation.
					f.pr.statusError = errors.New("provider no longer reachable")
				}
				f.mutations.closedReadError = nil
				inspection := f.inspectWorkflow("op-sync")
				if failure != "terminal observation" && failure != "acceptance recording" && failure != "closure intent" {
					assert.NotNil(t, inspection.ClosureIntentAt)
				}
				expectedSource := taskmodel.StatusInProgress
				if failure == "ambiguous close" || failure == "closure recording" || failure == "closure inspection" {
					expectedSource = taskmodel.StatusClosed
				}
				// Source lifecycle remains independent of the local closure receipt.
				assert.Equal(t, expectedSource, inspection.SourceStatus)
				assert.Nil(t, inspection.Closure)
				if flow == publication.IntegrationFlowPullRequest && failure != "terminal observation" {
					require.NotNil(t, inspection.Publication.PROutcome)
					assert.Equal(t, work.PRMerged, inspection.Publication.PROutcome.State)
				}
				if failure == "closure intent" || failure == "acceptance recording" || failure == "terminal observation" || failure == "close rejected" {
					assert.Empty(t, f.tasks.closed)
				}
				f.mutations.closeError, f.mutations.afterCloseError, f.mutations.closedReadError = nil, nil, nil
				require.NoError(t, state.SetMemoryDataWriteError(f.paths, nil))

				f.run("", "task", "sync", "--all")

				settled, item := f.loadFinalTask("op-sync")
				assert.Equal(t, taskmodel.StatusClosed, item.Status)
				assert.NotNil(t, taskstate.FinalizationFacts(settled).ClosedAt)
				assert.Equal(t, []string{"op-sync"}, f.tasks.closed, "reconcile source success before attempting closure again")
				assert.Equal(t, taskstate.EventWorktreeRemoved, settled.Events[len(settled.Events)-1].Type)
				assert.Equal(t, pushes, len(f.candidate.pushes))
				assert.Equal(t, packages, f.publication.packageCalls)
				assert.Equal(t, syncs, f.publication.syncCalls)
				assert.Equal(t, merges, f.publication.merges)
				reads := f.pr.statusReads
				f.run("", "task", "sync", "op-sync")
				assert.Equal(t, reads, f.pr.statusReads)
				assert.Equal(t, []string{"op-sync"}, f.tasks.closed)
			})
		}
	}
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
