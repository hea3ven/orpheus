package agent_test

import (
	"context"
	"errors"
	workmodel "github.com/hea3ven/orpheus/internal/work"
	"sync"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeGitState struct {
	branch     string
	hasChanges bool
	branchErr  error
	changesErr error
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
	return nil
}

func (g *fakeGitState) Commit(ctx context.Context, dir string, message string) (string, error) {
	g.committed++
	if g.commitErr != nil {
		return "", g.commitErr
	}
	return g.commit, nil
}

func TestChildPIDPersistenceAndImmediateCompletionPreserveBothFacts(t *testing.T) {
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-main")
	taskItem := mainTask("op-main", fixture.repoPath)
	run, err := fixture.store.StartRun("alpha", "op-main", taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: fixture.repoPath, TaskBranch: "main", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}},
		Agent: "recorder", Branch: "main", Worktree: fixture.repoPath, SupervisorPID: 123,
	})
	must.NoError(err)
	completion := agent.CompletionService{
		Paths:    fixture.paths,
		Resolver: fixture.resolver(taskItem, mainEnv("op-main", fixture.repoPath), fixture.repoPath),
		RunStore: fixture.store,
		Git:      &fakeGitState{branch: "main", hasChanges: true},
	}
	dispatch := workflow.DispatchService{Paths: fixture.paths, RunStore: fixture.store}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		errs <- dispatch.RecordChildPID(run.Address, 456)
	}()
	go func() {
		defer wait.Done()
		<-start
		_, completeErr := completion.Complete(context.Background(), agent.CompleteOptions{
			Summary:              "Implemented main completion",
			Description:          "Recorded local review completion data.",
			DetailedDescription:  "Detailed PR body.",
			TechnicalExplanation: "Technical explanation.",
		})
		errs <- completeErr
	}()
	close(start)
	wait.Wait()
	close(errs)
	for concurrentErr := range errs {
		must.NoError(concurrentErr)
	}

	latest, ok, err := fixture.store.LatestRun("alpha", "op-main")
	must.NoError(err)
	must.True(ok)
	must.Equal(456, latest.Execution.ChildPID)
	must.NotNil(latest.Completion)
}

func TestCompletionServiceCancelsWhileMutationLockIsHeld(t *testing.T) {
	fixture := newActiveContextFixture(t, "op-main")
	completion := agent.CompletionService{Paths: fixture.paths, RunStore: fixture.store}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := state.WithGlobalMutationLock(fixture.paths, "hold completion lock", func() error {
		_, err := completion.Complete(ctx, agent.CompleteOptions{})
		return err
	})

	require.ErrorIs(t, err, context.Canceled)
}

func TestCompletionServiceWorktreeHandoffIsImmutableAndDoesNotCommit(t *testing.T) {
	fixture := newActiveContextFixture(t, "op-1")
	worktree := fixture.expectedWorktree(t, "op-1")
	taskItem := fixture.worktreeTask("op-1", worktree)
	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: worktree, TaskBranch: "orpheus/op-1", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}}, Branch: "orpheus/op-1", Worktree: worktree})
	require.NoError(t, err)
	gitState := &fakeGitState{branch: "orpheus/op-1", hasChanges: true, commitErr: errors.New("completion must not commit")}
	service := agent.CompletionService{
		Paths: fixture.paths, Resolver: fixture.resolver(taskItem, worktreeEnv("op-1", worktree), worktree),
		RunStore: fixture.store, Git: gitState,
	}
	payload := agent.CompleteOptions{Summary: "Done", Description: "Details", DetailedDescription: "Detailed PR body.", TechnicalExplanation: "Technical explanation."}

	first, err := service.Complete(context.Background(), payload)
	require.NoError(t, err)
	require.NotNil(t, first.Run.Completion)
	assert.Equal(t, taskstate.RunStatusRunning, first.Run.Status)
	assert.False(t, first.Repeated)
	assert.Empty(t, first.Run.Completion.Commit)
	assert.Empty(t, first.Run.Completion.CommitError)
	assert.NoError(t, first.CommitError)

	gitState.branchErr = errors.New("repeat must not inspect Git")
	gitState.changesErr = errors.New("repeat must not inspect changes")
	repeated, err := service.Complete(context.Background(), payload)

	require.NoError(t, err)
	assert.True(t, repeated.Repeated)
	assert.Equal(t, first.Run.Completion, repeated.Run.Completion)
	assert.Zero(t, gitState.staged)
	assert.Zero(t, gitState.committed)
	latest, ok, err := fixture.store.LatestRun("alpha", "op-1")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, taskstate.RunStatusRunning, latest.Status)
	assert.Equal(t, first.Run.Completion, latest.Completion)
}

func TestCompletionServiceRepeatedWorktreeCompletionWithDifferentPayloadIsNoop(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	worktree := fixture.expectedWorktree(t, "op-1")
	taskItem := fixture.worktreeTask("op-1", worktree)
	attempt, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: worktree, TaskBranch: "orpheus/op-1", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}},
		Agent:    "recorder",
		Branch:   "orpheus/op-1",
		Worktree: worktree,
	})
	must.NoError(err)
	first, err := fixture.store.CompleteImplementation(attempt.Address, taskstate.CompleteRunOptions{
		Summary:              "First summary",
		Description:          "First details.",
		DetailedDescription:  "Detailed PR body.",
		TechnicalExplanation: "Technical explanation.",
		Commit:               "abc123",
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
		Summary:              "Second summary",
		Description:          "Second details.",
		DetailedDescription:  "Detailed PR body.",
		TechnicalExplanation: "Technical explanation.",
	})

	must.NoError(err)
	is.True(completed.Repeated)
	is.Equal(0, gitState.staged)
	is.Equal(0, gitState.committed)
	assertCompletionMatchesFirst(t, completed, first)
	is.Nil(completed.RepeatedDiagnostic, "repeated handoff is a no-op, not a generic operation journal")

	latest, ok, loadErr := fixture.store.LatestRun("alpha", "op-1")
	must.NoError(loadErr)
	must.True(ok)
	must.NotNil(latest.Completion)
	is.Equal("First summary", latest.Completion.Summary)
	is.Equal("First details.", latest.Completion.Description)
	is.Equal("Detailed PR body.", latest.Completion.DetailedDescription)
	is.Equal("Technical explanation.", latest.Completion.TechnicalExplanation)
	is.Empty(latest.Completion.Commit, "an agent-provided SHA cannot become captured output identity")
	events, eventsErr := fixture.store.Events("alpha", "op-1")
	must.NoError(eventsErr)
	must.Len(events, 3)
	is.Equal(taskstate.EventWorkPrepared, events[0].Type)
	is.Equal(taskstate.EventRunStarted, events[1].Type)
	is.Equal(taskstate.EventCompletionRecorded, events[2].Type)
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
	is.Equal(first.Completion.TechnicalExplanation, completed.Run.Completion.TechnicalExplanation)
	is.Equal(first.Completion.Commit, completed.Run.Completion.Commit)
}

func TestCompletionServiceRejectsMissingTechnicalExplanation(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-main")
	taskItem := mainTask("op-main", fixture.repoPath)
	_, err := fixture.store.StartRun("alpha", "op-main", taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: fixture.repoPath, TaskBranch: "main", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}},
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

	_, err = service.Complete(context.Background(), agent.CompleteOptions{
		Summary:             "Done",
		Description:         "Details",
		DetailedDescription: "Detailed PR body.",
	})

	must.Error(err)
	is.Contains(err.Error(), "completion technical explanation is required")
	latest, ok, loadErr := fixture.store.LatestRun("alpha", "op-main")
	must.NoError(loadErr)
	must.True(ok)
	is.Nil(latest.Completion)
}

func TestCompletionServiceRejectsCurrentBranchMismatch(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-main")
	taskItem := mainTask("op-main", fixture.repoPath)
	_, err := fixture.store.StartRun("alpha", "op-main", taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: fixture.repoPath, TaskBranch: "main", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}},
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
		Summary:              "Done",
		Description:          "Details",
		DetailedDescription:  "Detailed PR body.",
		TechnicalExplanation: "Technical explanation.",
	})

	must.Error(err)
	is.Contains(err.Error(), `current Git branch is "feature"`)
}

func TestCompletionServiceGitInspectionFailureDoesNotWriteCompletion(t *testing.T) {
	cause := errors.New("Git inspection unavailable")
	for _, tc := range []struct {
		name string
		git  *fakeGitState
		want string
	}{
		{"branch", &fakeGitState{branchErr: cause, hasChanges: true}, "inspect current Git branch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newActiveContextFixture(t, "op-main")
			_, err := fixture.store.StartRun("alpha", "op-main", taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: fixture.repoPath, TaskBranch: "main", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}}, Branch: "main", Worktree: fixture.repoPath})
			require.NoError(t, err)
			service := agent.CompletionService{
				Paths: fixture.paths, Resolver: fixture.resolver(mainTask("op-main", fixture.repoPath), mainEnv("op-main", fixture.repoPath), fixture.repoPath),
				RunStore: fixture.store, Git: tc.git,
			}

			_, err = service.Complete(context.Background(), agent.CompleteOptions{Summary: "Done", Description: "Details", DetailedDescription: "PR body", TechnicalExplanation: "Rationale"})

			require.ErrorIs(t, err, cause)
			require.ErrorContains(t, err, tc.want)
			latest, ok, err := fixture.store.LatestRun("alpha", "op-main")
			require.NoError(t, err)
			require.True(t, ok)
			assert.Nil(t, latest.Completion)
			assert.Equal(t, taskstate.RunStatusRunning, latest.Status)
			assert.Zero(t, tc.git.staged)
			assert.Zero(t, tc.git.committed)
		})
	}
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
