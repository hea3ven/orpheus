package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeGitState struct {
	branch     string
	hasChanges bool
	branchErr  error
	changesErr error
}

func (g fakeGitState) CurrentBranch(ctx context.Context, dir string) (string, error) {
	if g.branchErr != nil {
		return "", g.branchErr
	}
	return g.branch, nil
}

func (g fakeGitState) HasWorkingTreeChanges(ctx context.Context, dir string) (bool, error) {
	if g.changesErr != nil {
		return false, g.changesErr
	}
	return g.hasChanges, nil
}

func TestCompletionServiceCompletesMainRun(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-main")
	taskItem := mainTask("op-main", fixture.repoPath)
	_, err := fixture.store.StartRun("alpha", "op-main", taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "main",
		Worktree: fixture.repoPath,
	})
	must.NoError(err)

	service := agent.CompletionService{
		Paths:    fixture.paths,
		Resolver: fixture.resolver(taskItem, mainEnv("op-main", fixture.repoPath), fixture.repoPath),
		RunStore: fixture.store,
		Git:      fakeGitState{branch: "main", hasChanges: true},
	}

	completed, err := service.Complete(context.Background(), agent.CompleteOptions{
		Summary: "Implemented main completion",
		Details: "Recorded local review completion data.",
	})

	must.NoError(err)
	is.Equal("op-main", completed.Context.Task.ID)
	is.Equal(agent.ExecutionTargetMain, completed.Context.Target.Kind)
	is.Equal(taskstate.RunStatusSucceeded, completed.Run.Status)
	must.NotNil(completed.Run.Completion)
	is.Equal("Implemented main completion", completed.Run.Completion.Summary)
	is.Equal("Recorded local review completion data.", completed.Run.Completion.Details)

	latest, ok, err := fixture.store.LatestRun("alpha", "op-main")
	must.NoError(err)
	must.True(ok)
	is.Equal(taskstate.RunStatusSucceeded, latest.Status)
	must.NotNil(latest.Completion)
}

func TestCompletionServiceRejectsWorktreeTargetBeforeWriting(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	worktree := fixture.expectedWorktree(t, "op-1")
	taskItem := fixture.worktreeTask("op-1", worktree)
	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Branch:   "orpheus/op-1",
		Worktree: worktree,
	})
	must.NoError(err)

	service := agent.CompletionService{
		Paths:    fixture.paths,
		Resolver: fixture.resolver(taskItem, worktreeEnv("op-1", worktree), worktree),
		RunStore: fixture.store,
		Git:      fakeGitState{branch: "orpheus/op-1", hasChanges: true},
	}

	_, err = service.Complete(context.Background(), agent.CompleteOptions{
		Summary: "Done",
		Details: "Details",
	})

	must.Error(err)
	is.Contains(err.Error(), "supports main/solo runs only")
	latest, ok, loadErr := fixture.store.LatestRun("alpha", "op-1")
	must.NoError(loadErr)
	must.True(ok)
	is.Equal(taskstate.RunStatusRunning, latest.Status)
	is.Nil(latest.Completion)
}

func TestCompletionServiceRequiresChangesBeforeWriting(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-main")
	taskItem := mainTask("op-main", fixture.repoPath)
	_, err := fixture.store.StartRun("alpha", "op-main", taskstate.StartRunOptions{
		Branch:   "main",
		Worktree: fixture.repoPath,
	})
	must.NoError(err)

	service := agent.CompletionService{
		Paths:    fixture.paths,
		Resolver: fixture.resolver(taskItem, mainEnv("op-main", fixture.repoPath), fixture.repoPath),
		RunStore: fixture.store,
		Git:      fakeGitState{branch: "main", hasChanges: false},
	}

	_, err = service.Complete(context.Background(), agent.CompleteOptions{
		Summary: "Done",
		Details: "Details",
	})

	must.Error(err)
	is.Contains(err.Error(), "working tree has no changes")
	latest, ok, loadErr := fixture.store.LatestRun("alpha", "op-main")
	must.NoError(loadErr)
	must.True(ok)
	is.Equal(taskstate.RunStatusRunning, latest.Status)
	is.Nil(latest.Completion)
}

func TestCompletionServiceRejectsCurrentBranchMismatch(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-main")
	taskItem := mainTask("op-main", fixture.repoPath)
	_, err := fixture.store.StartRun("alpha", "op-main", taskstate.StartRunOptions{
		Branch:   "main",
		Worktree: fixture.repoPath,
	})
	must.NoError(err)

	service := agent.CompletionService{
		Paths:    fixture.paths,
		Resolver: fixture.resolver(taskItem, mainEnv("op-main", fixture.repoPath), fixture.repoPath),
		RunStore: fixture.store,
		Git:      fakeGitState{branch: "feature", hasChanges: true},
	}

	_, err = service.Complete(context.Background(), agent.CompleteOptions{
		Summary: "Done",
		Details: "Details",
	})

	must.Error(err)
	is.Contains(err.Error(), `current Git branch is "feature"`)
}

func TestCompletionServiceWrapsGitInspectionErrors(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-main")
	taskItem := mainTask("op-main", fixture.repoPath)
	_, err := fixture.store.StartRun("alpha", "op-main", taskstate.StartRunOptions{
		Branch:   "main",
		Worktree: fixture.repoPath,
	})
	must.NoError(err)

	branchErr := errors.New("detached head")
	service := agent.CompletionService{
		Paths:    fixture.paths,
		Resolver: fixture.resolver(taskItem, mainEnv("op-main", fixture.repoPath), fixture.repoPath),
		RunStore: fixture.store,
		Git:      fakeGitState{branchErr: branchErr, hasChanges: true},
	}

	_, err = service.Complete(context.Background(), agent.CompleteOptions{
		Summary: "Done",
		Details: "Details",
	})

	must.Error(err)
	is.Contains(err.Error(), "inspect current Git branch")
	is.ErrorIs(err, branchErr)
}

func mainTask(taskID string, repoPath string) taskmodel.Task {
	return taskmodel.Task{
		ID:     taskID,
		Title:  "Main target",
		Status: taskmodel.StatusInProgress,
		Metadata: taskmodel.Metadata{
			taskmodel.MetadataBranch:   "main",
			taskmodel.MetadataWorktree: repoPath,
		},
	}
}

func mainEnv(taskID string, repoPath string) map[string]string {
	return map[string]string{
		"ORPHEUS_REPO_ID":  "alpha",
		"ORPHEUS_TASK_ID":  taskID,
		"ORPHEUS_WORKTREE": repoPath,
		"ORPHEUS_BRANCH":   "main",
	}
}

func worktreeEnv(taskID string, worktree string) map[string]string {
	return map[string]string{
		"ORPHEUS_REPO_ID":  "alpha",
		"ORPHEUS_TASK_ID":  taskID,
		"ORPHEUS_WORKTREE": worktree,
		"ORPHEUS_BRANCH":   "orpheus/" + taskID,
	}
}
