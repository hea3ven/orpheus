//go:build integration

package cli_test

import (
	"context"
	"errors"
	workmodel "github.com/hea3ven/orpheus/internal/work"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowDoctorReportsPendingThenAttemptsCleanupAndPreservesRefusedAndLockedWorktrees(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	opts := &fixture.options
	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha Repo", "op")
	paths := fixture.paths
	store := taskstate.NewStore(paths)
	git := &doctorWorktrees{states: map[string]gitmeta.ClosedTaskWorktreeInspection{}, failures: map[string]string{}}
	opts.Dependencies.CleanupGit = git
	opts.Dependencies.DoctorEffects.WorktreeExists = func(path string) bool { return git.states[path].Outcome != gitmeta.ClosedTaskWorktreeAbsent }
	var tasks []taskmodel.Task
	for _, id := range []string{"op-doctor-worktree", "op-doctor-locked"} {
		path, err := paths.DataPath("repos/alpha/worktrees/" + id)
		require.NoError(t, err)
		snapshot := readerDeliveredHistory(t, "alpha", id, path, "orpheus/"+id, workflowTime("2026-07-07T10:00:00Z"))
		at := workflowTime("2026-07-07T10:30:00Z")
		snapshot.Workflows[0].Phases = append(snapshot.Workflows[0].Phases, &workmodel.FinalizationPhase{Acceptance: &workmodel.PRAcceptance{ExternalReview: 4, ObservedAt: at, Evidence: "Synthetic independently observed merged PR"}, Closure: &workmodel.TaskClosure{At: at, Reason: taskstate.CloseReasonPRMerged, Evidence: "Synthetic task source confirmed closure"}})
		saveReaderHistory(t, fixture, snapshot)

		tasks = append(tasks, taskmodel.Task{ID: id, Title: id, Status: taskmodel.StatusClosed, IssueType: taskmodel.IssueTypeTask, Metadata: taskmodel.Metadata{taskmodel.MetadataBranch: "orpheus/" + id, taskmodel.MetadataWorktree: path}})
		git.states[path] = gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeEligible, Worktree: path}
	}
	worktree := tasks[0].Metadata[taskmodel.MetadataWorktree]
	locked := tasks[1].Metadata[taskmodel.MetadataWorktree]
	git.states[locked] = gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeUnsafe, Worktree: locked, Reason: "locked by operator"}
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: tasks}})
	lockedBefore, err := store.Load("alpha", tasks[1].ID)
	require.NoError(t, err)

	git.failures[worktree] = "fatal: contains modified or untracked files"
	output, stderr := fixture.mustExecute("doctor")
	assert.Empty(t, stderr)
	for _, want := range []string{"Closed-task worktree cleanup", "cleanup_pending", "orpheus doctor --fix", worktree, "unsafe", locked, "locked"} {
		assert.Contains(t, output, want)
	}
	assert.NotContains(t, output, "modified or untracked")
	assert.Empty(t, git.attempted)
	lockedAfter, err := store.Load("alpha", tasks[1].ID)
	require.NoError(t, err)
	assert.Equal(t, lockedBefore, lockedAfter)
	before, err := store.Load("alpha", tasks[0].ID)
	require.NoError(t, err)

	output, stderr = fixture.mustExecute("doctor", "--fix")
	assert.Empty(t, stderr)
	assert.Contains(t, output, "failed")
	assert.Contains(t, output, git.failures[worktree])
	assert.Equal(t, []string{worktree}, git.attempted)
	assert.Empty(t, git.removed)
	after, err := store.Load("alpha", tasks[0].ID)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	lockedAfter, err = store.Load("alpha", tasks[1].ID)
	require.NoError(t, err)
	assert.Equal(t, lockedBefore, lockedAfter)

	delete(git.failures, worktree)
	output, stderr = fixture.mustExecute("doctor", "--fix")
	assert.Empty(t, stderr)
	assert.Contains(t, output, "removed")
	assert.Equal(t, []string{worktree, worktree}, git.attempted)
	assert.Equal(t, []string{worktree}, git.removed)
	assert.Equal(t, gitmeta.ClosedTaskWorktreeUnsafe, git.states[locked].Outcome)
	lockedAfter, err = store.Load("alpha", tasks[1].ID)
	require.NoError(t, err)
	assert.Equal(t, lockedBefore, lockedAfter)

	git.states[locked] = gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeEligible, Worktree: locked}
	output, stderr = fixture.mustExecute("doctor", "--fix")
	assert.Empty(t, stderr)
	assert.Contains(t, output, "removed")
	assert.Equal(t, []string{worktree, locked}, git.removed)
	output, stderr = fixture.mustExecute("doctor")
	assert.Empty(t, stderr)
	assert.Contains(t, output, "No lingering closed-task worktrees found.")
	assert.NotContains(t, output, "already_absent")
	completedStates := make(map[string]taskstate.TaskState)
	for _, item := range tasks {
		loaded, err := store.Load("alpha", item.ID)
		require.NoError(t, err)
		assert.Equal(t, taskstate.EventWorktreeRemoved, loaded.Events[len(loaded.Events)-1].Type)
		completedStates[item.ID] = loaded
	}

	opts.Dependencies.TaskBackendFactory = func(taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		return nil, errors.New("task source unavailable")
	}
	output, stderr = fixture.mustExecute("doctor", "--fix")
	assert.Empty(t, stderr)
	assert.Contains(t, output, "No lingering closed-task worktrees found.")
	assert.Equal(t, []string{worktree, worktree, locked}, git.attempted)
	assert.Equal(t, []string{worktree, locked}, git.removed)
	for _, item := range tasks {
		loaded, err := store.Load("alpha", item.ID)
		require.NoError(t, err)
		assert.Equal(t, completedStates[item.ID], loaded, "repeated cleanup must not append audit events")
	}
}

type doctorWorktrees struct {
	states    map[string]gitmeta.ClosedTaskWorktreeInspection
	removed   []string
	attempted []string
	failures  map[string]string
}

func (g *doctorWorktrees) InspectClosedTaskWorktree(_ context.Context, opts gitmeta.ClosedTaskWorktreeOptions) gitmeta.ClosedTaskWorktreeInspection {
	path, err := opts.Paths.DataPath("repos/" + opts.RepoID + "/worktrees/" + opts.TaskID)
	if err != nil {
		return gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeFailed, Reason: err.Error()}
	}
	return g.states[path]
}
func (g *doctorWorktrees) RemoveClosedTaskWorktree(ctx context.Context, opts gitmeta.ClosedTaskWorktreeOptions) gitmeta.ClosedTaskWorktreeRemoval {
	inspection := g.InspectClosedTaskWorktree(ctx, opts)
	if inspection.Outcome != gitmeta.ClosedTaskWorktreeEligible {
		return gitmeta.ClosedTaskWorktreeRemoval{Outcome: inspection.Outcome, Worktree: inspection.Worktree, Reason: "unexpected removal"}
	}
	g.attempted = append(g.attempted, inspection.Worktree)
	if reason := g.failures[inspection.Worktree]; reason != "" {
		return gitmeta.ClosedTaskWorktreeRemoval{Outcome: gitmeta.ClosedTaskWorktreeFailed, Worktree: inspection.Worktree, Reason: reason}
	}
	g.removed = append(g.removed, inspection.Worktree)
	g.states[inspection.Worktree] = gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeAbsent, Worktree: inspection.Worktree}
	return gitmeta.ClosedTaskWorktreeRemoval{Outcome: gitmeta.ClosedTaskWorktreeRemoved, Worktree: inspection.Worktree}
}
