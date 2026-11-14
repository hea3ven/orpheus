//go:build integration

package cli_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowPublicationDirectMergeRetriesPushAndClose(t *testing.T) {
	for _, repoRoot := range []bool{false, true} {
		t.Run(fmt.Sprintf("repo_root_%t", repoRoot), func(t *testing.T) {
			f := newFinalizationFixture(t, "op-merge")
			f.setConfig("publication", map[string]any{"integration_flow": publication.IntegrationFlowDirectMerge})
			branch, dir := f.expectedTarget("op-merge")
			if repoRoot {
				dir = taskWorkflowRepoRoot
			}
			f.completion("op-merge", branch, dir, "feat: original intent", "Deliver the reviewed implementation.", taskstate.RunStatusSucceeded, work.PublicationContent{CommitSubject: "feat: public cumulative outcome", CommitBody: "Public change rationale", PRDescription: "PR-only cumulative outcome"})
			f.passedReview("op-merge")
			before, item := f.loadFinalTask("op-merge")
			require.Equal(t, "output-1", before.Runs[0].Completion.Commit)
			assert.Equal(t, taskmodel.StatusInProgress, item.Status)
			assert.Empty(t, f.publication.remote)
			// The registered root is affected even when task work uses a dedicated directory.
			owner := anInProgressTask("op-root-owner")
			owner.Metadata = taskmodel.Metadata{taskmodel.MetadataBranch: "unrelated-branch", taskmodel.MetadataWorktree: taskWorkflowRepoRoot}
			f.backend.tasks[owner.ID] = owner
			_, _, ownershipErr := f.runError("", "task", "done", "op-merge")
			require.ErrorContains(t, ownershipErr, "op-root-owner owning repo-root metadata")
			assert.Zero(t, f.publication.packageCalls)
			delete(f.backend.tasks, owner.ID)
			f.publication.pushError = errors.New("origin unavailable")
			f.publication.beforeSync = func() {
				saved, _ := f.loadFinalTask("op-merge")
				p := saved.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
				require.NotNil(t, p.Operation.Public)
				require.NotNil(t, p.PrivateRefsReleasedAt, "private history release precedes local synchronization")
				assert.Equal(t, "output-1", p.Input.Code.Commit)
				assert.Equal(t, "public-output-1", p.Operation.Public.Commit)
				assert.Empty(t, f.publication.remote)
				for _, revision := range f.candidate.retained {
					assert.NotEqual(t, "output-1", revision.Commit)
				}
				delete(f.candidate.trees, "output-1")
			}
			f.publication.beforeIntegrationPush = func() {
				saved, _ := f.loadFinalTask("op-merge")
				p := saved.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
				require.NotNil(t, p.Integration.Result, "integration result must be durable before push")
				assert.Equal(t, "merge-commit", p.Integration.Result.Commit)
				assert.Nil(t, p.Delivery)
			}

			stdout, stderr, err := f.runError("", "task", "done", "op-merge")

			require.ErrorIs(t, err, f.publication.pushError)
			assert.Empty(t, stdout)
			assert.Contains(t, stderr, "TASK")
			pending, item := f.loadFinalTask("op-merge")
			assert.Equal(t, before.Runs, pending.Runs)
			assert.Equal(t, before.Reviews, pending.Reviews)
			assert.Nil(t, pending.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase).Delivery, "a local merge cannot fabricate delivery")
			assert.Equal(t, taskmodel.StatusInProgress, item.Status)
			assert.Empty(t, f.publication.remote)
			assert.Equal(t, 1, f.publication.merges, "ordinary integration merge exists locally despite the disconnected push")
			assert.Empty(t, f.publication.cleanupAttempts)
			item.Title = "Edited title after direct-delivery authorization"
			item.Description = "Edited description after direct-delivery authorization"
			item.AcceptanceCriteria = "Edited criteria after direct-delivery authorization"
			f.backend.tasks[item.ID] = item
			f.publication.pushError = nil
			f.publication.losePush = true
			_, _, err = f.runError("", "task", "done", "op-merge")
			require.ErrorContains(t, err, "lost destination push response")
			assert.Equal(t, "merge-commit", f.publication.remote["main"])
			f.publication.losePush, f.publication.observeError = false, nil
			require.NoError(t, state.SetMemoryDataWriteError(f.paths, func(_ string, data []byte) error {
				snapshot, decodeErr := workstate.Decode(data)
				if decodeErr == nil {
					if p, ok := snapshot.ActiveWorkflow().LastPhase().(*work.PublicationPhase); ok && p.Delivery != nil {
						return errors.New("lost delivery recording")
					}
				}
				return nil
			}))
			_, _, err = f.runError("", "task", "done", "op-merge")
			require.ErrorContains(t, err, "lost delivery recording")
			require.NoError(t, state.SetMemoryDataWriteError(f.paths, nil))
			f.mutations.closeError = errors.New("task source unavailable")

			f.run("", "task", "run", "op-merge")
			assert.Empty(t, f.tasks.closed)
			assert.Empty(t, f.publication.cleanupAttempts)
			_, _, err = f.runError("", "task", "sync", "op-merge")
			require.ErrorIs(t, err, f.mutations.closeError)
			delivered, item := f.loadFinalTask("op-merge")
			assert.Equal(t, before.Reviews, delivered.Reviews)
			assert.Equal(t, before.Runs, delivered.Runs)
			assert.Equal(t, before.Work.Workflows[0].Phases[3].(*work.PublicationPhase).Consent, delivered.Work.Workflows[0].Phases[3].(*work.PublicationPhase).Consent)
			facts := taskstate.FinalizationFacts(delivered)
			assert.Equal(t, "public-output-1", facts.Commit, "prepared history remains distinct from review and integration identities")
			assert.Equal(t, "merge-commit", facts.MergeCommit)
			assert.Equal(t, "merge-commit", f.publication.remote["main"])
			assert.NotNil(t, facts.PushedAt)
			assert.Nil(t, facts.ClosedAt)
			assert.Equal(t, taskmodel.StatusInProgress, item.Status)
			assert.Empty(t, f.publication.cleanupAttempts)
			statusOutput, _ := f.run("", "status", "--no-truncate")
			assert.Contains(t, statusOutput, "delivery verified; run task sync for closure")
			inspection, _ := f.run("", "task", "show", "workflow", "op-merge")
			assert.Contains(t, inspection, "Workflow next action: retry_finalization")
			assert.Contains(t, inspection, "FINALIZATION")
			f.mutations.closeError = nil

			stdout, stderr = f.run("", "task", "sync", "op-merge")

			assert.Contains(t, stderr, "TASK")
			assert.Contains(t, stdout, "Synced op-merge")
			assert.Contains(t, stdout, "Backend task was closed")
			closed, item := f.loadFinalTask("op-merge")
			assert.Equal(t, taskmodel.StatusClosed, item.Status)
			assert.NotNil(t, taskstate.FinalizationFacts(closed).ClosedAt)
			assert.Equal(t, []string{"feat: original intent\n\nDeliver the reviewed implementation."}, f.candidate.commits)
			assert.Equal(t, 1, f.publication.merges)
			assert.Equal(t, 1, f.publication.packageCalls)
			for _, packaged := range f.publication.packages {
				assert.Equal(t, "feat: public cumulative outcome\n\nPublic change rationale", packaged.Message)
				assert.NotContains(t, packaged.Message, "PR-only cumulative outcome")
				assert.NotContains(t, packaged.Message, "Technical explanation.")
				assert.NotContains(t, packaged.Message, "Review process")
			}
			assert.Equal(t, 1, f.publication.syncCalls)
			assert.Equal(t, []string{"main"}, f.candidate.pushes)
			assert.Equal(t, []string{"op-merge"}, f.tasks.closed)
			assert.Empty(t, f.pr.created)
			if repoRoot {
				assert.Empty(t, f.publication.cleanupAttempts)
			} else {
				assert.Equal(t, []string{dir}, f.publication.cleanupAttempts)
			}

			f.run("", "task", "done", "op-merge")
			assert.Equal(t, []string{"op-merge"}, f.tasks.closed)
			assert.Len(t, f.candidate.pushes, 1)
			assert.Len(t, f.candidate.commits, 1)
		})
	}
	for _, amend := range []bool{true, false} {
		for _, repoRoot := range []bool{false, true} {
			t.Run(fmt.Sprintf("amended_%t/repo_root_%t", amend, repoRoot), func(t *testing.T) {
				f := newFinalizationFixture(t, "op-amended-delivery")
				f.setConfig("publication", map[string]any{"integration_flow": publication.IntegrationFlowDirectMerge})
				branch, dir := f.expectedTarget("op-amended-delivery")
				if repoRoot {
					dir = taskWorkflowRepoRoot
				}
				f.completion("op-amended-delivery", branch, dir, "feat: original intent", "Deliver the reviewed implementation.", taskstate.RunStatusSucceeded)
				f.passedReview("op-amended-delivery")
				f.publication.pushError = errors.New("destination push unavailable")
				_, _, err := f.runError("", "task", "done", "op-amended-delivery")
				require.ErrorIs(t, err, f.publication.pushError)
				recorded, _ := f.loadFinalTask("op-amended-delivery")
				original := recorded.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
				require.NotNil(t, original.Integration.Result)
				assert.Equal(t, "merge-commit", original.Integration.Result.Commit)
				f.publication.pushError = nil
				f.git.targets[dir] = memoryGitTarget{branch: branch}
				selectedCommit := original.Integration.Source.Commit
				if amend {
					// The operator selects a new clean committed source after the failed push.
					selectedCommit = "operator-amended"
					selectOperatorCommit(f, dir, selectedCommit, "operator selected new delivery output\n", "amended or rewritten history")
				} else {
					// Destination-only manual repair also invalidates the recorded local result.
					f.publication.destinationHead = "initial-commit"
					f.candidate.head = selectedCommit
				}
				selectedTree := f.candidate.trees[selectedCommit]
				if amend {
					assert.NotEqual(t, original.Integration.Source.Tree, selectedTree)
				}
				delete(f.candidate.trees, "output-1")

				f.run("m\n", "task", "run", "op-amended-delivery")

				invalidated, _ := f.loadFinalTask("op-amended-delivery")
				old := invalidated.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
				require.NotNil(t, old.Integration.InvalidatedAt)
				assert.Equal(t, original.Integration.Result, old.Integration.Result, "the old integration is history, not rewritten to match the new source")
				assert.Equal(t, original.Consent, old.Consent)
				assert.Equal(t, "public-output-1", old.Integration.Source.Commit)
				assert.Equal(t, selectedCommit, f.publication.taskHead)
				assert.Empty(t, f.publication.remote)
				assert.Empty(t, f.tasks.closed)
				assert.Equal(t, 1, f.publication.merges)
				pending := f.inspectWorkflow("op-amended-delivery")
				require.NotNil(t, pending.Pending)
				assert.Equal(t, workflow.InteractionPublicationRecovery, pending.Pending.ID.Kind)
				if amend {
					_, _, err = f.runError("c\n", "task", "run", "op-amended-delivery")
					require.ErrorContains(t, err, "still needs repair")
				}
				assert.Empty(t, f.candidate.pushes, "manual continuation cannot silently reuse the obsolete local merge")

				// Manual destination restoration must make continuation usable without a
				// forced source reset or editing the recorded integration in workflow state.
				f.publication.destinationHead = "initial-commit"
				f.run("c\n", "task", "run", "op-amended-delivery")
				renewed, _ := f.loadFinalTask("op-amended-delivery")
				plan := renewed.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
				assert.Nil(t, plan.Consent)
				assert.Nil(t, plan.Integration)
				require.NotNil(t, plan.PreparedFrom)
				assert.Equal(t, work.InputOperator, plan.Input.Provenance)
				assert.Equal(t, selectedCommit, plan.Input.Code.Commit)
				assert.Equal(t, selectedTree, plan.Input.Code.Tree)
				assert.Equal(t, selectedCommit, f.publication.taskHead)
				assert.Equal(t, recorded.Reviews, renewed.Reviews)
				assert.Equal(t, recorded.Runs, renewed.Runs)
				assert.Zero(t, f.publication.restores, "manual repair never requires a later destructive reset")
				f.run("b\n", "task", "run", "op-amended-delivery")
				aborted, _ := f.loadFinalTask("op-amended-delivery")
				assert.Equal(t, renewed, aborted, "abort leaves the amended input and pending consent intact")
				assert.Empty(t, f.candidate.pushes)

				f.run("p\n", "task", "run", "op-amended-delivery")

				delivered, item := f.loadFinalTask("op-amended-delivery")
				assert.Equal(t, taskmodel.StatusInProgress, item.Status)
				assert.Equal(t, recorded.Reviews, delivered.Reviews, "the new exact-input decision does not rerun review")
				assert.Equal(t, recorded.Runs, delivered.Runs)
				assert.Equal(t, "merge-commit-2", f.publication.remote["main"])
				assert.Equal(t, selectedTree, f.candidate.trees[f.publication.remote["main"]])
				assert.Equal(t, 1, f.publication.packageCalls)
				assert.Equal(t, 2, f.publication.merges, "only reconciled checkpoints require a new integration")
				assert.Equal(t, []string{"main"}, f.candidate.pushes)
				assert.Empty(t, f.pr.created)
				assert.Zero(t, f.pr.findReads)
			})
		}
	}
}

func TestIntegrationWorkflowTaskDoneInfersFeatureTaskAcrossWorkingDirectories(t *testing.T) {
	for _, repoRoot := range []bool{false, true} {
		t.Run(fmt.Sprintf("repo_root_%t", repoRoot), func(t *testing.T) {
			f := newFeatureFinalizationFixture(t, "op-infer", repoRoot)
			// Inference follows the recorded directory, not a later branch template or
			// publication configuration; neither may rewrite the authorized intent.
			f.options.TaskWorkingDirectory += "/nested"
			f.setConfig("publication", map[string]any{"integration_flow": publication.IntegrationFlowDirectMerge})

			stdout, stderr := f.run("", "task", "done")

			assert.Contains(t, stderr, "TASK")
			assert.Contains(t, stdout, "Published op-infer")
			assert.Contains(t, stdout, "authorized input output-1")
			assert.Contains(t, stdout, "observed PR "+f.pr.url)
			f.assertPublishedPR("op-infer")
			require.Len(t, f.pr.created, 1)
			assert.Equal(t, "Ready for PR", f.pr.created[0].Title)
			assert.Equal(t, "main", f.pr.created[0].BaseBranch)
			assert.Equal(t, []string{"Ready for PR\n\nImplemented task branch changes."}, f.candidate.commits)
			f.run("", "task", "done")
			assert.Len(t, f.pr.created, 1)
			assert.Len(t, f.candidate.pushes, 1)
		})
	}
}

func TestIntegrationWorkflowTaskDoneCannotBypassLiveHandoffOrPublicationConsent(t *testing.T) {
	f := newAgentCompletionFixture(t, "op-main", false)
	f.run("", "agent", "done", "--commit-subject", "feat: still active", "--commit-body", "Not yet exited.", "--pr-description", "Review body.", "--technical-explanation", "Implementation rationale.")
	before, item := f.loadFinalTask("op-main")
	require.IsType(t, &work.RunningProcess{}, before.Work.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution.Process)

	stdout, stderr, err := f.runError("yes\n", "task", "done", "op-main")

	require.ErrorContains(t, err, "active local work prevents publication")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	after, afterItem := f.loadFinalTask("op-main")
	assert.Equal(t, before, after)
	assert.Equal(t, item, afterItem)
	assert.Empty(t, f.candidate.commits)
	f.assertUnpublished("op-main")
	_, _, err = f.runError("", "task", "done", "op-main", "--commit-subject", "Cannot replace committed intent")
	require.ErrorContains(t, err, "unknown flag: --commit-subject")
}

func TestIntegrationWorkflowDirectSyncDisclosesUnreviewedPublicHeadBeforeFreshConsent(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprintf("conflict_%t", conflict), func(t *testing.T) {
			f := newFinalizationFixture(t, "op-sync-direct")
			f.setConfig("publication", map[string]any{"integration_flow": publication.IntegrationFlowDirectMerge})
			branch, dir := f.expectedTarget("op-sync-direct")
			f.completion("op-sync-direct", branch, dir, "feat: original intent", "Original feature narrative.", taskstate.RunStatusSucceeded)
			f.passedReview("op-sync-direct")
			reviewed, _ := f.loadFinalTask("op-sync-direct")
			f.publication.syncHead, f.publication.syncTree, f.publication.syncConflicts = "synced-public", "synced-tree", conflict
			if conflict {
				f.configureAgentProfiles(agent.AgentDefaults{Implementer: "resolver", SyncConflictResolver: "resolver"}, map[string]agent.Profile{"resolver": {Harness: "codex", Model: "resolver-model"}})
				f.agent.outcomes = append(f.agent.outcomes, semanticAgentOutcome{captureContext: true, exitWithoutCompletion: true, mutateCandidate: func() {
					snapshot, _ := f.loadFinalTask("op-sync-direct")
					p := snapshot.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
					require.NotNil(t, p.DirectSync.Execution)
					assert.NotZero(t, p.DirectSync.Execution.ChildPID, "attached work must release mutation protection for PID persistence")
					assert.NotNil(t, p.PrivateRefsReleasedAt)
					assert.Nil(t, p.DirectSync.Result)
					delete(f.candidate.trees, "output-1")
				}})
			}

			_, notice := f.run("q\n", "task", "run", "op-sync-direct")

			assert.Contains(t, notice, "Actual input: synced-public")
			assert.Contains(t, notice, "not passed that review")
			assert.Contains(t, notice, "Detailed PR body.")
			snapshot, item := f.loadFinalTask("op-sync-direct")
			assert.Equal(t, taskmodel.StatusInProgress, item.Status)
			assert.Empty(t, f.publication.remote)
			assert.Empty(t, f.pr.created)
			assert.Zero(t, f.pr.findReads, "direct sync never queries a PR")
			assert.Empty(t, f.tasks.closed)
			assert.Equal(t, reviewed.Reviews, snapshot.Reviews, "neither clean sync nor resolver edits rerun task review")
			assert.Equal(t, reviewed.Runs, snapshot.Runs, "sync must not fabricate an implementation handoff")
			p := snapshot.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
			assert.Nil(t, p.Consent)
			assert.Equal(t, work.InputSynchronized, p.Input.Provenance)
			assert.Equal(t, "synced-public", p.Input.Code.Commit)
			require.NotNil(t, p.SyncFrom)
			source := snapshot.Work.ActiveWorkflow().Phases[p.SyncFrom.Phase].(*work.PublicationPhase).DirectSync
			require.NotNil(t, source.Result)
			assert.Equal(t, "synced-public", source.Result.Commit)
			if conflict {
				require.Len(t, f.agent.contexts, 1, "the configured resolver must complete its bootstrap context command")
				assert.Contains(t, f.agent.contexts[0], "Integration destination: origin/main at initial-commit")
				assert.Contains(t, f.agent.contexts[0], "  - feature.txt")
				assert.NotContains(t, f.agent.contexts[0], "Pull request:")
				assert.NotContains(t, f.agent.contexts[0], "open PR branch")
				assert.Contains(t, f.agent.contexts[0], "local sync after you exit")
				require.NotNil(t, source.Execution)
				assert.Equal(t, "resolver", source.Execution.Profile)
				assert.Equal(t, "resolver-model", source.Execution.Model)
				assert.IsType(t, &work.ExitedProcess{}, source.Execution.Process)
				assert.NotEmpty(t, source.Execution.UsageCapture.Status, "actual usage outcome is retained, even when unknown")
			}

			f.run("p\n", "task", "run", "op-sync-direct")

			delivered, item := f.loadFinalTask("op-sync-direct")
			assert.Equal(t, taskmodel.StatusInProgress, item.Status)
			assert.Equal(t, reviewed.Reviews, delivered.Reviews)
			assert.Equal(t, 1, f.publication.packageCalls)
			assert.Equal(t, 1, f.publication.syncCalls)
			assert.Equal(t, 1, f.publication.merges)
			assert.Equal(t, []string{"main"}, f.candidate.pushes)
			assert.Equal(t, "synced-tree", f.candidate.trees[f.publication.remote["main"]])
			assert.Empty(t, f.pr.created)
		})
	}
}

func TestIntegrationWorkflowDirectPublicRecoveryUsesCheckpointsAndSupportsManualRetry(t *testing.T) {
	for _, repoRoot := range []bool{false, true} {
		for _, stage := range []string{"sync", "integration"} {
			t.Run(fmt.Sprintf("repo_root_%t/%s", repoRoot, stage), func(t *testing.T) {
				f := newFinalizationFixture(t, "op-direct-recovery")
				f.setConfig("publication", map[string]any{"integration_flow": publication.IntegrationFlowDirectMerge})
				branch, dir := f.expectedTarget("op-direct-recovery")
				if repoRoot {
					dir = taskWorkflowRepoRoot
				}
				f.completion("op-direct-recovery", branch, dir, "feat: recover publication", "Keep feature intent.", taskstate.RunStatusSucceeded)
				f.passedReview("op-direct-recovery")
				if stage == "sync" {
					f.publication.syncHead, f.publication.syncTree = "manually-selected-sync", "manual-sync-tree"
				}
				require.NoError(t, state.SetMemoryDataWriteError(f.paths, func(_ string, data []byte) error {
					snapshot, err := workstate.Decode(data)
					if err != nil {
						return nil
					}
					p, ok := snapshot.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
					if !ok {
						return nil
					}
					if stage == "sync" && p.DirectSync != nil && p.DirectSync.Result != nil {
						return errors.New("local result recording lost")
					}
					if stage == "integration" && p.Integration != nil && p.Integration.Result != nil {
						return errors.New("local result recording lost")
					}
					return nil
				}))
				_, _, err := f.runError("", "task", "done", "op-direct-recovery")
				require.ErrorContains(t, err, "local result recording lost")
				require.NoError(t, state.SetMemoryDataWriteError(f.paths, nil))
				delete(f.candidate.trees, "output-1")
				pending := f.inspectWorkflow("op-direct-recovery")
				require.NotNil(t, pending.Pending)
				assert.Equal(t, workflow.InteractionPublicationRecovery, pending.Pending.ID.Kind)
				before, item := f.loadFinalTask("op-direct-recovery")
				item.Title = "Edited title before public-checkpoint recovery"
				item.Description = "Edited description before public-checkpoint recovery"
				item.AcceptanceCriteria = "Edited criteria before public-checkpoint recovery"
				f.backend.tasks[item.ID] = item
				head := f.candidate.head

				_, notice := f.run("m\n", "task", "run", "op-direct-recovery")
				assert.Contains(t, notice, "Directory ")
				assert.Contains(t, notice, "branch ")
				f.run("r", "task", "run", "op-direct-recovery")
				preserved, _ := f.loadFinalTask("op-direct-recovery")
				assert.Equal(t, before, preserved, "manual-repair pause and partial EOF do not authorize reset")
				assert.Equal(t, head, f.candidate.head)
				assert.Zero(t, f.publication.restores)
				assert.Empty(t, f.candidate.pushes)
				assert.Empty(t, f.tasks.closed)

				if stage == "sync" {
					// Explicitly select this repaired clean checkout; do not force a later reset
					// or infer that the lost local receipt was durably recorded.
					f.run("c\n", "task", "run", "op-direct-recovery")
					assert.Zero(t, f.publication.restores)
				} else {
					assert.Contains(t, notice, taskWorkflowRepoRoot)
					assert.Contains(t, notice, "branch main")
					assert.Contains(t, notice, "initial-commit")
					f.run("r\n", "task", "run", "op-direct-recovery")
					assert.Equal(t, 1, f.publication.restores)
				}
				selected, _ := f.loadFinalTask("op-direct-recovery")
				p := selected.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
				require.NotNil(t, p.PreparedFrom)
				assert.Nil(t, p.Consent, "recovery exposes actual input for fresh consent without review or squash")
				assert.Equal(t, before.Reviews, selected.Reviews)
				assert.Empty(t, f.candidate.pushes)

				f.run("p\n", "task", "run", "op-direct-recovery")

				closed, item := f.loadFinalTask("op-direct-recovery")
				assert.Equal(t, taskmodel.StatusInProgress, item.Status)
				assert.Equal(t, before.Reviews, closed.Reviews)
				assert.Equal(t, before.Runs, closed.Runs)
				assert.Equal(t, before.Work.Workflows[0].Phases[3], closed.Work.Workflows[0].Phases[3], "continuation preserves the original consent and historical scope")
				assert.Equal(t, 1, f.publication.packageCalls)
				assert.Equal(t, []string{"main"}, f.candidate.pushes)
				assert.Empty(t, f.pr.created)
			})
		}
	}
}

func TestIntegrationWorkflowDirectPreparationPersistsMappingBeforeRetryablePrivateRefRelease(t *testing.T) {
	for _, failure := range []string{"public record", "private ref release", "cleanup record", "hook changed tree"} {
		t.Run(failure, func(t *testing.T) {
			f := newFinalizationFixture(t, "op-direct-prepare")
			f.setConfig("publication", map[string]any{"integration_flow": publication.IntegrationFlowDirectMerge})
			branch, dir := f.expectedTarget("op-direct-prepare")
			f.completion("op-direct-prepare", branch, dir, "feat: durable direct history", "Preserve original intent.", taskstate.RunStatusSucceeded)
			f.passedReview("op-direct-prepare")
			switch failure {
			case "private ref release":
				f.publication.releaseError = errors.New("private cleanup unavailable")
			case "hook changed tree":
				f.publication.packageTree = "actual-hook-tree"
			default:
				require.NoError(t, state.SetMemoryDataWriteError(f.paths, func(_ string, data []byte) error {
					snapshot, err := workstate.Decode(data)
					if err != nil {
						return nil
					}
					p, ok := snapshot.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
					if !ok {
						return nil
					}
					if failure == "public record" && p.Operation != nil && p.Operation.Public != nil {
						return errors.New("publication record unavailable")
					}
					if failure == "cleanup record" && p.PrivateRefsReleasedAt != nil {
						return errors.New("cleanup record unavailable")
					}
					return nil
				}))
			}

			_, _, err := f.runError("", "task", "done", "op-direct-prepare")

			require.Error(t, err)
			snapshot, item := f.loadFinalTask("op-direct-prepare")
			p := snapshot.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
			assert.Nil(t, p.Delivery)
			assert.Zero(t, f.publication.syncCalls, "preparation and private-ref release precede sync")
			assert.Empty(t, f.publication.remote)
			assert.Equal(t, taskmodel.StatusInProgress, item.Status)
			if failure == "public record" {
				assert.Nil(t, p.Operation.Public)
				assert.Zero(t, f.publication.releases, "failed public persistence cannot authorize ref deletion")
				assert.NotEmpty(t, f.candidate.retained)
			} else {
				require.NotNil(t, p.Operation.Public)
				assert.Equal(t, "output-1", p.Input.Code.Commit)
				assert.Equal(t, "public-output-1", p.Operation.Public.Commit)
			}
			if failure == "cleanup record" || failure == "hook changed tree" {
				for _, revision := range f.candidate.retained {
					assert.NotEqual(t, "output-1", revision.Commit)
				}
				delete(f.candidate.trees, "output-1")
			}
			require.NoError(t, state.SetMemoryDataWriteError(f.paths, nil))
			f.publication.releaseError = nil
			switch failure {
			case "public record":
				f.run("r\n", "task", "run", "op-direct-prepare")
			case "hook changed tree":
				f.run("q\n", "task", "run", "op-direct-prepare")
				pending, _ := f.loadFinalTask("op-direct-prepare")
				selected := pending.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
				assert.Equal(t, "actual-hook-tree", selected.Input.Code.Tree)
				assert.Nil(t, selected.Consent)
				assert.Contains(t, selected.Comparison.Changes, "fixture actual public changes")
				assert.Equal(t, snapshot.Reviews, pending.Reviews)
				f.run("p\n", "task", "run", "op-direct-prepare")
			default:
				f.run("", "task", "done", "op-direct-prepare")
			}
			final, item := f.loadFinalTask("op-direct-prepare")
			assert.Equal(t, taskmodel.StatusInProgress, item.Status)
			assert.Equal(t, snapshot.Reviews, final.Reviews)
			packages := 1
			if failure == "public record" {
				packages = 2
			}
			assert.Equal(t, packages, f.publication.packageCalls)
			assert.Equal(t, 1, f.publication.syncCalls)
			assert.Equal(t, []string{"main"}, f.candidate.pushes)
		})
	}
}
