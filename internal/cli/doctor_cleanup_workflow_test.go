//go:build integration

package cli_test

import (
	"context"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowDoctorRepairsCleanClosedTaskWorktreeAndPreservesDirtyAndLockedWorktrees(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	opts := &fixture.options
	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha Repo", "op")
	paths := fixture.paths
	store := taskstate.NewStore(paths)
	git := &doctorWorktrees{states: map[string]gitmeta.ClosedTaskWorktreeInspection{}}
	opts.Dependencies.CleanupGit = git
	opts.Dependencies.DoctorEffects.WorktreeExists = func(path string) bool { return git.states[path].Outcome != gitmeta.ClosedTaskWorktreeAbsent }
	var tasks []taskmodel.Task
	for _, id := range []string{"op-doctor-worktree", "op-doctor-locked"} {
		path, err := paths.DataPath("repos/alpha/worktrees/" + id)
		require.NoError(t, err)
		_, err = store.StartRun("alpha", id, taskstate.StartRunOptions{Agent: "implementer", WorkDirectory: path, Branch: "orpheus/" + id, Worktree: path})
		require.NoError(t, err)
		tasks = append(tasks, taskmodel.Task{ID: id, Title: id, Status: taskmodel.StatusClosed, IssueType: taskmodel.IssueTypeTask, Metadata: taskmodel.Metadata{taskmodel.MetadataBranch: "orpheus/" + id, taskmodel.MetadataWorktree: path}})
		git.states[path] = gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeDirty, Worktree: path, Reason: "dirty"}
	}
	worktree := tasks[0].Metadata[taskmodel.MetadataWorktree]
	locked := tasks[1].Metadata[taskmodel.MetadataWorktree]
	git.states[locked] = gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeUnsafe, Worktree: locked, Reason: "locked by operator"}
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: tasks}})

	for _, args := range [][]string{{"doctor"}, {"doctor", "--fix"}} {
		output, stderr := fixture.mustExecute(args...)
		assert.Empty(t, stderr)
		for _, want := range []string{"Closed-task worktree cleanup", "dirty", worktree, "unsafe", locked, "locked"} {
			assert.Contains(t, output, want)
		}
		assert.Empty(t, git.removed, "dirty and locked targets must not reach removal")
	}
	git.states[worktree] = gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeClean, Worktree: worktree}
	output, stderr := fixture.mustExecute("doctor", "--fix")
	assert.Empty(t, stderr)
	assert.Contains(t, output, "removed")
	assert.Equal(t, []string{worktree}, git.removed)
	assert.Equal(t, gitmeta.ClosedTaskWorktreeUnsafe, git.states[locked].Outcome)

	git.states[locked] = gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeClean, Worktree: locked}
	output, stderr = fixture.mustExecute("doctor", "--fix")
	assert.Empty(t, stderr)
	assert.Contains(t, output, "removed")
	assert.Equal(t, []string{worktree, locked}, git.removed)
	output, stderr = fixture.mustExecute("doctor")
	assert.Empty(t, stderr)
	assert.Contains(t, output, "No lingering closed-task worktrees found.")
	assert.NotContains(t, output, "already_absent")
	for _, item := range tasks {
		loaded, err := store.Load("alpha", item.ID)
		require.NoError(t, err)
		assert.Equal(t, taskstate.EventWorktreeRemoved, loaded.Events[len(loaded.Events)-1].Type)
	}
}

type doctorWorktrees struct {
	states  map[string]gitmeta.ClosedTaskWorktreeInspection
	removed []string
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
	if inspection.Outcome != gitmeta.ClosedTaskWorktreeClean {
		return gitmeta.ClosedTaskWorktreeRemoval{Outcome: inspection.Outcome, Worktree: inspection.Worktree, Reason: "unexpected removal"}
	}
	g.removed = append(g.removed, inspection.Worktree)
	g.states[inspection.Worktree] = gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeAbsent, Worktree: inspection.Worktree}
	return gitmeta.ClosedTaskWorktreeRemoval{Outcome: gitmeta.ClosedTaskWorktreeRemoved, Worktree: inspection.Worktree}
}
