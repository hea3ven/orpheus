package workflow

import (
	"context"
	"errors"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDispatchStartKeepsDirtyCheckoutGuardForIncompleteContinuationHistory(t *testing.T) {
	for _, test := range []struct {
		name      string
		runs      []taskstate.RunAttempt
		directory string
	}{
		{name: "completed latest run", directory: "/fixture/repo", runs: []taskstate.RunAttempt{
			{Attempt: 1, Status: taskstate.RunStatusFailed},
			{Attempt: 2, Status: taskstate.RunStatusSucceeded, Completion: &taskstate.Completion{Summary: "done"}},
		}},
		{name: "unknown run status", directory: "/fixture/repo", runs: []taskstate.RunAttempt{
			{Attempt: 1, Status: "unknown"},
		}},
		{name: "missing work directory", runs: []taskstate.RunAttempt{
			{Attempt: 1, Status: taskstate.RunStatusFailed},
		}},
		{name: "mismatched work directory", directory: "/fixture/other", runs: []taskstate.RunAttempt{
			{Attempt: 1, Status: taskstate.RunStatusFailed},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := task.Repository{ID: "alpha", Path: "/fixture/repo", DefaultBranch: "main"}
			item := task.Task{ID: "op-continue", Status: task.StatusInProgress, Metadata: task.Metadata{
				task.MetadataBranch: "main", task.MetadataWorktree: repo.Path,
			}}
			store := fakeDispatchRunStore{state: taskstate.TaskState{
				RepoID: repo.ID, TaskID: item.ID, Runs: test.runs,
				GitFacts:      taskstate.GitFacts{Branch: "main", Worktree: repo.Path},
				WorkDirectory: taskstate.WorkDirectory{Path: test.directory},
			}}
			git := &continuationDispatchGit{setupError: errors.New("stop at Git setup")}
			service := DispatchService{Paths: newDispatchTestPaths(t), RunStore: store, Git: git}

			_, err := service.Start(context.Background(), DispatchStartOptions{
				TaskID: item.ID, Source: task.RepositorySource{Repository: repo},
				Backend: fakeDispatchBackend{taskItem: item},
			})

			require.ErrorIs(t, err, git.setupError)
			assert.False(t, git.allowDirty)
			assert.Equal(t, 1, git.setupCalls)
		})
	}
}

type continuationDispatchGit struct {
	allowDirty bool
	setupError error
	setupCalls int
}

func (g *continuationDispatchGit) SetupRepoRoot(_ context.Context, opts gitmeta.RepoRootOptions) (gitmeta.TaskWorktreeSetupResult, error) {
	g.setupCalls++
	g.allowDirty = opts.AllowDirty
	return gitmeta.TaskWorktreeSetupResult{}, g.setupError
}

func (*continuationDispatchGit) SetupRepoRootTaskBranch(context.Context, gitmeta.TaskWorktreeOptions) (gitmeta.TaskWorktreeSetupResult, error) {
	return gitmeta.TaskWorktreeSetupResult{}, errors.New("unexpected repo-root task branch setup")
}

func (*continuationDispatchGit) SetupTaskWorktree(context.Context, gitmeta.TaskWorktreeOptions) (gitmeta.TaskWorktreeSetupResult, error) {
	return gitmeta.TaskWorktreeSetupResult{}, errors.New("unexpected worktree setup")
}
