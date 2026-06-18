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
	stageErr   error
	commit     string
	commitErr  error
	staged     int
	committed  int
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

func (g *fakeGitState) StageAll(ctx context.Context, dir string) error {
	g.staged++
	return g.stageErr
}

func (g *fakeGitState) Commit(ctx context.Context, dir string, message string) (string, error) {
	g.committed++
	if g.commitErr != nil {
		return "", g.commitErr
	}
	return g.commit, nil
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
		Git:      &fakeGitState{branch: "main", hasChanges: true},
	}

	completed, err := service.Complete(context.Background(), agent.CompleteOptions{
		Summary:             "Implemented main completion",
		Description:         "Recorded local review completion data.",
		DetailedDescription: "Detailed PR body.",
	})

	must.NoError(err)
	is.Equal("op-main", completed.Context.Task.ID)
	is.Equal(agent.ExecutionTargetMain, completed.Context.Target.Kind)
	is.Equal(taskstate.RunStatusRunning, completed.Run.Status)
	must.NotNil(completed.Run.Completion)
	is.Equal("Implemented main completion", completed.Run.Completion.Summary)
	is.Equal("Recorded local review completion data.", completed.Run.Completion.Description)
	is.Equal("Detailed PR body.", completed.Run.Completion.DetailedDescription)

	latest, ok, err := fixture.store.LatestRun("alpha", "op-main")
	must.NoError(err)
	must.True(ok)
	is.Equal(taskstate.RunStatusRunning, latest.Status)
	must.NotNil(latest.Completion)
}

func TestCompletionServiceCompletesWorktreeRunWithoutCommit(t *testing.T) {
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
		Git:      &fakeGitState{branch: "orpheus/op-1", hasChanges: true, commit: "abc123"},
	}

	completed, err := service.Complete(context.Background(), agent.CompleteOptions{
		Summary:             "Done",
		Description:         "Details",
		DetailedDescription: "Detailed PR body.",
	})

	must.NoError(err)
	is.Equal(taskstate.RunStatusRunning, completed.Run.Status)
	must.NotNil(completed.Run.Completion)
	is.Empty(completed.Run.Completion.Commit)

	latest, ok, loadErr := fixture.store.LatestRun("alpha", "op-1")
	must.NoError(loadErr)
	must.True(ok)
	is.Equal(taskstate.RunStatusRunning, latest.Status)
	must.NotNil(latest.Completion)
	is.Empty(latest.Completion.Commit)
}

func TestCompletionServiceWorktreeCompletionDoesNotStageOrCommit(t *testing.T) {
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

	commitErr := errors.New("commit failed")
	gitState := &fakeGitState{branch: "orpheus/op-1", hasChanges: true, commitErr: commitErr}
	service := agent.CompletionService{
		Paths:    fixture.paths,
		Resolver: fixture.resolver(taskItem, worktreeEnv("op-1", worktree), worktree),
		RunStore: fixture.store,
		Git:      gitState,
	}

	completed, err := service.Complete(context.Background(), agent.CompleteOptions{
		Summary:             "Done",
		Description:         "Details",
		DetailedDescription: "Detailed PR body.",
	})

	must.NoError(err)
	is.NoError(completed.CommitError)
	is.Equal(0, gitState.staged)
	is.Equal(0, gitState.committed)
	must.NotNil(completed.Run.Completion)
	is.Empty(completed.Run.Completion.Commit)
	is.Empty(completed.Run.Completion.CommitError)
}

func TestCompletionServiceIdempotentWorktreeCompletionDoesNotCommitAgain(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	worktree := fixture.expectedWorktree(t, "op-1")
	taskItem := fixture.worktreeTask("op-1", worktree)
	attempt, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Branch:   "orpheus/op-1",
		Worktree: worktree,
	})
	must.NoError(err)
	_, err = fixture.store.CompleteRun("alpha", "op-1", attempt.Attempt, taskstate.CompleteRunOptions{
		Summary:             "Done",
		Description:         "Details",
		DetailedDescription: "Detailed PR body.",
	})
	must.NoError(err)

	gitState := &fakeGitState{branch: "orpheus/op-1", hasChanges: true, commit: "abc123"}
	service := agent.CompletionService{
		Paths:    fixture.paths,
		Resolver: fixture.resolver(taskItem, worktreeEnv("op-1", worktree), worktree),
		RunStore: fixture.store,
		Git:      gitState,
	}

	completed, err := service.Complete(context.Background(), agent.CompleteOptions{
		Summary:             "Done",
		Description:         "Details",
		DetailedDescription: "Detailed PR body.",
	})

	must.NoError(err)
	is.Equal(0, gitState.staged)
	is.Equal(0, gitState.committed)
	must.NotNil(completed.Run.Completion)
	is.Empty(completed.Run.Completion.Commit)
}

func TestCompletionServiceRepeatedWorktreeCompletionWithDifferentPayloadIsNoop(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	worktree := fixture.expectedWorktree(t, "op-1")
	taskItem := fixture.worktreeTask("op-1", worktree)
	attempt, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "orpheus/op-1",
		Worktree: worktree,
	})
	must.NoError(err)
	first, err := fixture.store.CompleteRun("alpha", "op-1", attempt.Attempt, taskstate.CompleteRunOptions{
		Summary:             "First summary",
		Description:         "First details.",
		DetailedDescription: "Detailed PR body.",
		Commit:              "abc123",
	})
	must.NoError(err)

	gitState := &fakeGitState{
		branchErr:  errors.New("unexpected Git branch inspection"),
		changesErr: errors.New("unexpected Git status inspection"),
		commit:     "def456",
	}
	service := agent.CompletionService{
		Paths:    fixture.paths,
		Resolver: fixture.resolver(taskItem, worktreeEnv("op-1", worktree), worktree),
		RunStore: fixture.store,
		Git:      gitState,
	}

	completed, err := service.Complete(context.Background(), agent.CompleteOptions{
		Summary:             "Second summary",
		Description:         "Second details.",
		DetailedDescription: "Detailed PR body.",
	})

	must.NoError(err)
	is.True(completed.Repeated)
	is.Equal(0, gitState.staged)
	is.Equal(0, gitState.committed)
	assertCompletionMatchesFirst(t, completed, first)
	assertRepeatedDiagnostic(t, completed)

	latest, ok, loadErr := fixture.store.LatestRun("alpha", "op-1")
	must.NoError(loadErr)
	must.True(ok)
	must.NotNil(latest.Completion)
	is.Equal("First summary", latest.Completion.Summary)
	is.Equal("First details.", latest.Completion.Description)
	is.Equal("Detailed PR body.", latest.Completion.DetailedDescription)
	is.Equal("abc123", latest.Completion.Commit)
	events, eventsErr := fixture.store.Events("alpha", "op-1")
	must.NoError(eventsErr)
	is.Equal(taskstate.EventCompletionRepeated, events[len(events)-1].Type)
}

func assertCompletionMatchesFirst(
	t *testing.T,
	completed agent.CompleteResult,
	first taskstate.RunAttempt,
) {
	t.Helper()
	is := assert.New(t)
	must := require.New(t)

	must.NotNil(completed.Run.Completion)
	is.Equal(first.Completion.Summary, completed.Run.Completion.Summary)
	is.Equal(first.Completion.Description, completed.Run.Completion.Description)
	is.Equal(first.Completion.DetailedDescription, completed.Run.Completion.DetailedDescription)
	is.Equal(first.Completion.Commit, completed.Run.Completion.Commit)
}

func assertRepeatedDiagnostic(t *testing.T, completed agent.CompleteResult) {
	t.Helper()
	is := assert.New(t)
	must := require.New(t)

	must.NotNil(completed.RepeatedDiagnostic)
	is.Equal(taskstate.EventCompletionRepeated, completed.RepeatedDiagnostic.Type)
	is.Equal("Second summary", completed.RepeatedDiagnostic.RequestedSummary)
	is.Equal("Second details.", completed.RepeatedDiagnostic.RequestedDescription)
	is.Equal("Detailed PR body.", completed.RepeatedDiagnostic.RequestedDetailedDescription)
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
		Git:      &fakeGitState{branch: "main", hasChanges: false},
	}

	_, err = service.Complete(context.Background(), agent.CompleteOptions{
		Summary:             "Done",
		Description:         "Details",
		DetailedDescription: "Detailed PR body.",
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
		Git:      &fakeGitState{branch: "feature", hasChanges: true},
	}

	_, err = service.Complete(context.Background(), agent.CompleteOptions{
		Summary:             "Done",
		Description:         "Details",
		DetailedDescription: "Detailed PR body.",
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
		Git:      &fakeGitState{branchErr: branchErr, hasChanges: true},
	}

	_, err = service.Complete(context.Background(), agent.CompleteOptions{
		Summary:             "Done",
		Description:         "Details",
		DetailedDescription: "Detailed PR body.",
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
