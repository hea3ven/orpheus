//go:build integration

package cli_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/hea3ven/orpheus/internal/agentexec"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (g *memoryDispatchGit) InspectWorkflowReset(ctx context.Context, req gitmeta.WorkflowResetRequest) (string, error) {
	branch, err := g.CurrentBranch(ctx, req.Directory)
	if err != nil {
		return "", err
	}
	if branch != req.Branch {
		return "", errors.New("wrong reset branch")
	}
	return g.ShortStatus(ctx, req.Directory)
}
func (g *memoryDispatchGit) ResolveWorkflowReset(_ context.Context, req gitmeta.WorkflowResetRequest) (gitmeta.Revision, error) {
	g.resetResolutions = append(g.resetResolutions, req)
	if g.resetFetchError != nil {
		return gitmeta.Revision{}, g.resetFetchError
	}
	target := gitmeta.Revision{Commit: g.startingCommit, Tree: g.trees[g.startingCommit], RetainedRef: req.Ref}
	g.retained[req.Ref] = target
	return target, nil
}
func (g *memoryDispatchGit) ApplyWorkflowReset(_ context.Context, req gitmeta.WorkflowResetRequest) error {
	if g.beforeResetApply != nil {
		g.beforeResetApply(req)
	}
	g.resetApplications = append(g.resetApplications, req)
	g.heads[req.Directory] = req.Target.Commit
	// Model an interruption after moving HEAD, before removing untracked output.
	if g.resetApplyError != nil {
		return g.resetApplyError
	}
	g.hasCandidateChanges, g.hasStagedChanges, g.repoRootDirty = false, false, false
	return nil
}

func TestIntegrationWorkflowTaskResetSchedulesThenPreparationRetriesOneTargetAndConsumesAuthorization(t *testing.T) {
	for _, repoRoot := range []bool{false, true} {
		t.Run(fmt.Sprintf("repo_root_%t", repoRoot), func(t *testing.T) {
			f := newTaskWorkflowFixture(t, anOpenTask("op-reset"))
			f.withCompletingAgent(aCompletion())
			f.withRealReviewPipeline()
			args := []string{"task", "run", "op-reset"}
			if repoRoot {
				args = append(args, "--repo-root")
			}
			_, stderr, err := f.execute(args...)
			require.NoError(t, err, stderr)
			before, _ := f.loadFinalTask("op-reset")
			require.NotEmpty(t, before.Reviews)
			old := before.Work.Workflows[0]
			oldRef := before.Runs[0].Address
			dir := old.Context().Directory
			head := f.git.heads[dir]
			f.git.hasCandidateChanges, f.git.hasStagedChanges = true, true
			setups := len(f.git.setups)

			for _, answer := range []string{"n\n", "", "y"} {
				stdout, _, err := f.executeWithInput(answer, "task", "reset", "op-reset")
				require.NoError(t, err)
				assert.Contains(t, stdout, "not scheduled")
				unchanged, _ := f.loadFinalTask("op-reset")
				assert.Equal(t, before.Work, unchanged.Work)
			}
			f.options.Dependencies.Terminal.InputIsTerminal = func(io.Reader) bool { return false }
			_, _, err = f.executeWithInput("y\n", "task", "reset", "op-reset")
			require.ErrorContains(t, err, "interactive terminal")
			f.options.Dependencies.Terminal.InputIsTerminal = func(io.Reader) bool { return true }
			stdout, warning, err := f.executeWithInput("y\n", "task", "reset", "op-reset")
			require.NoError(t, err)
			assert.Contains(t, stdout, "Restart scheduled")
			for _, fact := range []string{"op-reset", dir, old.Context().TaskBranch, "A  reviewed.txt", "including any edits made before that run", "Ignored files may be overwritten or", "removed when needed to restore the baseline", "Other ignored files and recorded", "No backup", "latest remote default branch", "Nothing in the checkout will change now"} {
				assert.Contains(t, warning, fact)
			}
			scheduled, _ := f.loadFinalTask("op-reset")
			require.Len(t, scheduled.Work.Workflows, 2)
			assert.Equal(t, old, scheduled.Work.Workflows[0])
			fresh := scheduled.Work.ActiveWorkflow()
			require.NotNil(t, fresh.ResetRequestedAt)
			assert.Empty(t, fresh.Phases)
			assert.Nil(t, fresh.Publication)
			assert.Empty(t, fresh.ReviewContracts)
			assert.Equal(t, head, f.git.heads[dir])
			assert.True(t, f.git.hasCandidateChanges)
			assert.Empty(t, f.git.resetResolutions)
			assert.Empty(t, f.git.resetApplications)
			assert.Len(t, f.git.setups, setups)
			assert.Len(t, f.agent.launches, 1)
			_, err = f.taskStore.CompleteImplementation(oldRef, taskstate.CompleteRunOptions{CommitSubject: "stale child", CommitBody: "must not attach"})
			require.Error(t, err)
			inspect, _, err := f.execute("task", "show", "workflow", "op-reset")
			require.NoError(t, err)
			assert.Contains(t, inspect, "Restart pending")
			assert.Regexp(t, `WORKFLOW\s+2`, inspect)

			fetchFailure := errors.New("fetch unavailable")
			f.git.resetFetchError = fetchFailure
			_, _, err = f.execute("task", "run", "op-reset")
			require.ErrorIs(t, err, fetchFailure)
			failed, _ := f.loadFinalTask("op-reset")
			assert.NotNil(t, failed.Work.ActiveWorkflow().ResetRequestedAt)
			assert.Empty(t, failed.Work.ActiveWorkflow().Phases)
			assert.Equal(t, head, f.git.heads[dir])
			assert.True(t, f.git.hasCandidateChanges)
			assert.Empty(t, f.git.resetApplications)

			// Preparation, not the reset request, selects this later default-branch tip.
			f.git.resetFetchError = nil
			f.git.startingCommit, f.git.trees["advanced-default"] = "advanced-default", "advanced-tree"
			partial := errors.New("cleanup interrupted")
			f.git.resetApplyError = partial
			f.git.beforeResetApply = func(req gitmeta.WorkflowResetRequest) {
				recorded, _ := f.loadFinalTask("op-reset")
				p := recorded.Work.ActiveWorkflow().Phases[0].(*work.PreparationPhase)
				require.NotNil(t, p.ResetTarget)
				assert.Equal(t, req.Target.Commit, p.ResetTarget.Baseline.Commit)
				assert.Nil(t, p.EstablishedAt)
				assert.NotNil(t, recorded.Work.ActiveWorkflow().ResetRequestedAt)
			}
			_, _, err = f.execute("task", "run", "op-reset")
			require.ErrorIs(t, err, partial)
			partialState, _ := f.loadFinalTask("op-reset")
			assert.Nil(t, partialState.Work.ActiveWorkflow().Context())
			assert.Equal(t, "advanced-default", f.git.heads[dir])
			assert.True(t, f.git.hasCandidateChanges)
			assert.Len(t, f.agent.launches, 1)

			f.git.startingCommit, f.git.trees["newer-default"] = "newer-default", "newer-tree"
			f.git.resetApplyError = nil
			f.withAgentExitingWithoutCompletion(1)
			_, stderr, err = f.execute("task", "run", "op-reset")
			require.NoError(t, err, stderr)
			established, _ := f.loadFinalTask("op-reset")
			assert.Nil(t, established.Work.ActiveWorkflow().ResetRequestedAt)
			assert.Equal(t, "advanced-default", established.BaselineCommit)
			assert.Equal(t, old, established.Work.Workflows[0])
			assert.Len(t, f.git.resetResolutions, 2, "failed fetch and successful selection, no fetch on partial retry")
			assert.Len(t, f.git.resetApplications, 2)
			assert.False(t, f.git.hasCandidateChanges)
			assert.Nil(t, established.Work.ActiveWorkflow().Publication)
			assert.Empty(t, established.Work.ActiveWorkflow().ReviewContracts)
			assert.Empty(t, established.Work.Workflows[1].Phases[1].(*work.ImplementationPhase).TargetFindings)
			assert.Equal(t, dir, f.agent.launches[1].dir)

			// A normal continuation preserves later edits and does not select a new tip.
			f.git.hasCandidateChanges, f.git.repoRootDirty = true, true
			f.withAgentExitingWithoutCompletion(1)
			_, stderr, err = f.execute("task", "run", "op-reset")
			require.NoError(t, err, stderr)
			continued, _ := f.loadFinalTask("op-reset")
			assert.Equal(t, "advanced-default", continued.BaselineCommit)
			assert.True(t, f.git.hasCandidateChanges)
			assert.Len(t, f.git.resetResolutions, 2)
			assert.Len(t, f.git.resetApplications, 2)
		})
	}
}

func TestIntegrationWorkflowTaskResetRefusesLiveOrUnverifiableExecutionAndReconcilesAbsentChild(t *testing.T) {
	for _, liveness := range []agentexec.ProcessLiveness{agentexec.ProcessLive, agentexec.ProcessUnknown, agentexec.ProcessAbsent} {
		t.Run(string(liveness), func(t *testing.T) {
			f := newTaskWorkflowFixture(t, anInProgressTask("op-reset"))
			f.seedRunningAttempt("op-reset", 600, 601)
			f.options.Dependencies.ProcessProbe = func(int) (agentexec.ProcessLiveness, error) { return liveness, nil }
			stdout, _, err := f.executeWithInput("y\n", "task", "reset", "op-reset")
			final, _ := f.loadFinalTask("op-reset")
			if liveness == agentexec.ProcessAbsent {
				require.NoError(t, err)
				assert.Contains(t, stdout, "Restart scheduled")
				assert.Len(t, final.Work.Workflows, 2)
				assert.Equal(t, taskstate.RunStatusInterrupted, final.Runs[0].Status)
			} else {
				require.ErrorContains(t, err, "cannot reset workflow")
				assert.Len(t, final.Work.Workflows, 1)
				assert.Equal(t, taskstate.RunStatusRunning, final.Runs[0].Status)
			}
			assert.Empty(t, f.git.resetApplications)
			assert.Empty(t, f.git.resetResolutions)
		})
	}
}
