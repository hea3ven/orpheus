//go:build integration

package cli_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorktreeLocalReviewTaskDonePRFlowEndToEnd(t *testing.T) {
	f := newFinalizationFixture(t, "op-m5-sync")
	item := anOpenTask("op-m5-sync")
	item.Title = "M5 sync flow"
	f.backend.tasks[item.ID] = item
	f.withCompletingAgent(aCompletion())

	stdout, stderr := f.run("", "task", "run", item.ID)

	assert.Contains(t, stderr, "Review for "+item.ID+" is waiting for manual step \"local-review\"")
	assert.Contains(t, stdout, "Recorded completion for "+item.ID)
	state, _ := f.loadFinalTask(item.ID)
	require.Len(t, state.Runs, 1)
	require.NotNil(t, state.Runs[0].Completion)
	assert.Empty(t, state.Runs[0].Completion.Commit)
	target, ok := taskstate.GitFactsFor(state)
	require.True(t, ok)
	branch, dir := f.expectedTarget(item.ID)
	assert.Equal(t, branch, target.Branch)
	assert.Equal(t, dir, target.Worktree)
	f.passedReview(item.ID)

	stdout, stderr = f.run("", "task", "done", item.ID)

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Published "+item.ID)
	assert.Contains(t, stdout, "pushed "+branch)
	assert.Contains(t, stdout, "created PR "+f.pr.url)
	assert.Contains(t, stdout, "Backend task remains open for PR review")
	f.assertPublishedPR(item.ID)
	stdout, stderr = f.run("", "status")
	assert.Empty(t, stderr)
	for _, want := range []string{"Reviewing", item.ID, item.Title, f.pr.url} {
		assert.Contains(t, stdout, want)
	}
	assert.NotContains(t, stdout, "needs PR")
	assert.Zero(t, f.pr.statusReads)
	f.pr.state = pullrequest.StateOpen

	stdout, stderr = f.run("", "task", "sync", item.ID)

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "PR "+f.pr.url+" is still open for review")
	assert.Equal(t, 1, f.pr.statusReads)
	assert.Len(t, f.pr.created, 1)
	f.assertPublishedPR(item.ID)
	f.pr.state = pullrequest.StateMerged

	stdout, stderr = f.run("", "task", "sync", item.ID)

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "PR "+f.pr.url+" is merged")
	assert.Contains(t, stdout, "Backend task was closed")
	assert.Contains(t, stdout, "Worktree "+dir+" was removed")
	state, item = f.loadFinalTask(item.ID)
	assert.Equal(t, taskmodel.StatusClosed, item.Status)
	assert.NotContains(t, f.git.targets, dir)
	require.GreaterOrEqual(t, len(state.Events), 2)
	closed := state.Events[len(state.Events)-2]
	assert.Equal(t, taskstate.EventTaskClosed, closed.Type)
	assert.Equal(t, taskstate.CloseReasonPRMerged, closed.CloseReason)
	assert.Equal(t, f.pr.url, closed.PRURL)
	cleanup := state.Events[len(state.Events)-1]
	assert.Equal(t, taskstate.EventWorktreeRemoved, cleanup.Type)
	assert.Equal(t, dir, cleanup.Worktree)
	assert.Len(t, f.pr.created, 1)
	assert.Len(t, f.tasks.closed, 1)
	assert.Equal(t, []string{f.pr.url}, f.mutations.prURLs)
	stdout, stderr = f.run("", "status", "--full")
	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Done / closed")
	assert.Contains(t, stdout, item.ID)
}

func TestIntegrationRepoRootLocalReviewTaskDonePRFlowEndToEnd(t *testing.T) {
	f := newFinalizationFixture(t, "op-repo-root-sync")
	item := anOpenTask("op-repo-root-sync")
	f.backend.tasks[item.ID] = item
	f.withCompletingAgent(aCompletion())

	stdout, stderr := f.run("", "task", "run", "--repo-root", item.ID)

	assert.Contains(t, stderr, "Review for "+item.ID+" is waiting for manual step \"local-review\"")
	assert.Contains(t, stdout, "Recorded completion for "+item.ID)
	assert.Equal(t, "main", f.git.targets[taskWorkflowRepoRoot].branch)
	require.Len(t, f.agent.contexts, 1)
	for _, want := range []string{"- Current branch: main", "- Work Directory: " + taskWorkflowRepoRoot, "- Current directory: " + taskWorkflowRepoRoot, "registered repository root on the registered default branch", "deterministic task branch is created only after review"} {
		assert.Contains(t, f.agent.contexts[0], want)
	}
	stdout, stderr = f.run("", "task", "dir", item.ID)
	assert.Empty(t, stderr)
	assert.Equal(t, taskWorkflowRepoRoot+"\n", stdout)
	state, _ := f.loadFinalTask(item.ID)
	require.Len(t, state.Runs, 1)
	require.NotNil(t, state.Runs[0].Completion)
	assert.Empty(t, state.Runs[0].Completion.Commit)
	target, ok := taskstate.GitFactsFor(state)
	require.True(t, ok)
	assert.Equal(t, "main", target.Branch)
	assert.Equal(t, taskWorkflowRepoRoot, target.Worktree)
	assert.True(t, f.git.hasCandidateChanges)
	f.passedReview(item.ID)

	stdout, stderr = f.run("", "task", "done", item.ID)

	assert.Empty(t, stderr)
	branch := "orpheus/" + item.ID
	for _, want := range []string{"Published " + item.ID, "pushed " + branch, "created PR " + f.pr.url, "Backend task remains open for PR review"} {
		assert.Contains(t, stdout, want)
	}
	f.assertPublishedPR(item.ID)
	assert.False(t, f.git.hasCandidateChanges)
	assert.Equal(t, branch, f.git.targets[taskWorkflowRepoRoot].branch)
	f.pr.state = pullrequest.StateOpen
	stdout, stderr = f.run("", "task", "sync", item.ID)
	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "PR "+f.pr.url+" is still open for review")
	assert.Equal(t, 1, f.pr.statusReads)
	f.pr.state = pullrequest.StateMerged

	stdout, stderr = f.run("", "task", "sync", item.ID)

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "PR "+f.pr.url+" is merged")
	assert.Contains(t, stdout, "Backend task was closed")
	state, item = f.loadFinalTask(item.ID)
	assert.Equal(t, taskmodel.StatusClosed, item.Status)
	require.NotEmpty(t, state.Events)
	closed := state.Events[len(state.Events)-1]
	assert.Equal(t, taskstate.EventTaskClosed, closed.Type)
	assert.Equal(t, taskstate.CloseReasonPRMerged, closed.CloseReason)
	assert.Equal(t, f.pr.url, closed.PRURL)
	assert.Contains(t, f.git.targets, taskWorkflowRepoRoot)
	assert.Len(t, f.pr.created, 1)
	assert.Len(t, f.tasks.closed, 1)
	assert.Equal(t, []string{f.pr.url}, f.mutations.prURLs)
}

func (g *memoryPublicationGit) SyncTaskBranchWithDefault(_ context.Context, opts gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchSyncResult, error) {
	if g.targets[opts.Worktree].branch != opts.Branch || g.remote[opts.Branch] != g.head {
		return gitmeta.TaskBranchSyncResult{}, errors.New("sync requires published branch")
	}
	return gitmeta.TaskBranchSyncResult{Status: gitmeta.TaskBranchSyncAlreadyCurrent, Branch: opts.Branch, DefaultBranch: opts.DefaultBranch, Head: g.head}, nil
}
func (*memoryPublicationGit) BeginTaskBranchConflictResolution(context.Context, gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchSyncResult, error) {
	return gitmeta.TaskBranchSyncResult{}, errors.New("unexpected conflict resolution")
}
func (*memoryPublicationGit) CompleteTaskBranchConflictResolution(context.Context, gitmeta.TaskBranchSyncOptions, []string) (gitmeta.TaskBranchSyncResult, error) {
	return gitmeta.TaskBranchSyncResult{}, errors.New("unexpected conflict completion")
}
func (g *memoryPublicationGit) InspectClosedTaskWorktree(_ context.Context, opts gitmeta.ClosedTaskWorktreeOptions) gitmeta.ClosedTaskWorktreeInspection {
	dir, err := opts.Paths.DataPath(filepath.Join("repos", opts.RepoID, "worktrees", opts.TaskID))
	if err != nil || g.targets[dir].branch != opts.Branch || g.hasCandidateChanges {
		return gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeUnsafe, Worktree: dir, Reason: "unexpected cleanup target"}
	}
	return gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeClean, Worktree: dir}
}
func (g *memoryPublicationGit) RemoveClosedTaskWorktree(ctx context.Context, opts gitmeta.ClosedTaskWorktreeOptions) gitmeta.ClosedTaskWorktreeRemoval {
	inspection := g.InspectClosedTaskWorktree(ctx, opts)
	if inspection.Outcome != gitmeta.ClosedTaskWorktreeClean {
		return gitmeta.ClosedTaskWorktreeRemoval{Outcome: gitmeta.ClosedTaskWorktreeUnsafe, Worktree: inspection.Worktree}
	}
	delete(g.targets, inspection.Worktree)
	return gitmeta.ClosedTaskWorktreeRemoval{Outcome: gitmeta.ClosedTaskWorktreeRemoved, Worktree: inspection.Worktree}
}
