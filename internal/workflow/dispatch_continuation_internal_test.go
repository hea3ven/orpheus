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

func TestDispatchStartRequiresUnfinishedHistoryForRepoRootReuse(t *testing.T) {
	for _, test := range []struct {
		name       string
		runs       []taskstate.RunAttempt
		directory  string
		allowDirty bool
	}{
		{name: "target without history", directory: "/fixture/repo"},
		{name: "successful unfinished run", directory: "/fixture/repo", runs: []taskstate.RunAttempt{
			{Attempt: 1, Status: taskstate.RunStatusSucceeded},
		}, allowDirty: true},
		{name: "failed unfinished run", directory: "/fixture/repo", runs: []taskstate.RunAttempt{
			{Attempt: 1, Status: taskstate.RunStatusFailed},
		}, allowDirty: true},
		{name: "interrupted unfinished run", directory: "/fixture/repo", runs: []taskstate.RunAttempt{
			{Attempt: 1, Status: taskstate.RunStatusInterrupted},
		}, allowDirty: true},
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
			assert.Equal(t, test.allowDirty, git.allowDirty)
		})
	}
}

type continuationDispatchGit struct {
	allowDirty bool
	setupError error
}

func (g *continuationDispatchGit) SetupRepoRoot(_ context.Context, opts gitmeta.RepoRootOptions) (gitmeta.TaskWorktreeSetupResult, error) {
	g.allowDirty = opts.AllowDirty
	return gitmeta.TaskWorktreeSetupResult{}, g.setupError
}

func (*continuationDispatchGit) SetupRepoRootTaskBranch(context.Context, gitmeta.TaskWorktreeOptions) (gitmeta.TaskWorktreeSetupResult, error) {
	return gitmeta.TaskWorktreeSetupResult{}, errors.New("unexpected repo-root task branch setup")
}

func (*continuationDispatchGit) SetupTaskWorktree(context.Context, gitmeta.TaskWorktreeOptions) (gitmeta.TaskWorktreeSetupResult, error) {
	return gitmeta.TaskWorktreeSetupResult{}, errors.New("unexpected worktree setup")
}
