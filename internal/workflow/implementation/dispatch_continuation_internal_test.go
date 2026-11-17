package implementation

import (
	"context"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDispatchKeepsDirtyCheckoutGuardForIncompleteContinuationHistory(t *testing.T) {
	for _, test := range []struct {
		name      string
		runs      []taskstate.RunAttempt
		directory string
	}{
		{name: "completed latest run", directory: "/fixture/repo", runs: []taskstate.RunAttempt{{Attempt: 1, Status: taskstate.RunStatusFailed}, {Attempt: 2, Status: taskstate.RunStatusSucceeded, Completion: &taskstate.Completion{CommitSubject: "done"}}}},
		{name: "unknown run status", directory: "/fixture/repo", runs: []taskstate.RunAttempt{{Attempt: 1, Status: "unknown"}}},
		{name: "missing work directory", runs: []taskstate.RunAttempt{{Attempt: 1, Status: taskstate.RunStatusFailed}}},
		{name: "mismatched work directory", directory: "/fixture/other", runs: []taskstate.RunAttempt{{Attempt: 1, Status: taskstate.RunStatusFailed}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := task.Repository{ID: "alpha", Path: "/fixture/repo", DefaultBranch: "main"}
			item := task.Task{ID: "op-continue", Status: task.StatusInProgress, Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-continue", task.MetadataWorktree: repo.Path}}
			store := fakeDispatchRunStore{state: taskstate.TaskState{RepoID: repo.ID, TaskID: item.ID, Runs: test.runs, BaselineCommit: "initial-commit", GitFacts: taskstate.GitFacts{Branch: "orpheus/op-continue", Worktree: repo.Path}, WorkDirectory: taskstate.WorkDirectory{Path: test.directory}}}
			service := Dispatch{Paths: newDispatchTestPaths(t), history: store.state}

			plan, err := service.validateStart(context.Background(), DispatchStartOptions{TaskID: item.ID, Source: task.RepositorySource{Repository: repo}, Backend: fakeDispatchBackend{taskItem: item}})

			require.NoError(t, err)
			assert.False(t, plan.continuing)
			assert.Nil(t, plan.followUp)
			assert.Equal(t, repo.Path, plan.expected.WorktreePath)
			assert.Equal(t, "orpheus/op-continue", plan.expected.Branch)
		})
	}
}
