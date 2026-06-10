package workflow_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
)

type fakeFinalizationBackend struct {
	tasks  []task.Task
	closed []string
}

func (b *fakeFinalizationBackend) Get(_ context.Context, id string) (task.Task, error) {
	for _, candidate := range b.tasks {
		if candidate.ID == id {
			return candidate.Clone(), nil
		}
	}
	return task.Task{}, task.ErrNotFound
}

func (b *fakeFinalizationBackend) List(context.Context) ([]task.Task, error) {
	tasks := make([]task.Task, 0, len(b.tasks))
	for _, taskItem := range b.tasks {
		tasks = append(tasks, taskItem.Clone())
	}
	return tasks, nil
}

func (b *fakeFinalizationBackend) Close(_ context.Context, id string) error {
	b.closed = append(b.closed, id)
	return nil
}

type fakeFinalizationRunStore struct {
	states map[string]taskstate.TaskState
}

func (s *fakeFinalizationRunStore) Load(repoID, taskID string) (taskstate.TaskState, error) {
	state, ok := s.states[repoID+"/"+taskID]
	if !ok {
		return taskstate.TaskState{RepoID: repoID, TaskID: taskID}, nil
	}
	return state, nil
}

func (s *fakeFinalizationRunStore) RecordFinalizationCommit(repoID, taskID string, commit string) (taskstate.Finalization, error) {
	state := s.states[repoID+"/"+taskID]
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	finalization := taskstate.FinalizationFacts(state)
	finalization.Commit = commit
	finalization.CommittedAt = &now
	state.Finalization = &finalization
	s.states[repoID+"/"+taskID] = state
	return finalization, nil
}

func (s *fakeFinalizationRunStore) RecordFinalizationPush(repoID, taskID string) (taskstate.Finalization, error) {
	state := s.states[repoID+"/"+taskID]
	now := time.Date(2026, 6, 10, 12, 1, 0, 0, time.UTC)
	finalization := taskstate.FinalizationFacts(state)
	finalization.PushedAt = &now
	state.Finalization = &finalization
	s.states[repoID+"/"+taskID] = state
	return finalization, nil
}

func (s *fakeFinalizationRunStore) RecordFinalizationClose(repoID, taskID string) (taskstate.Finalization, error) {
	state := s.states[repoID+"/"+taskID]
	now := time.Date(2026, 6, 10, 12, 2, 0, 0, time.UTC)
	finalization := taskstate.FinalizationFacts(state)
	finalization.ClosedAt = &now
	state.Finalization = &finalization
	s.states[repoID+"/"+taskID] = state
	return finalization, nil
}

type fakeFinalizationGit struct {
	branch     string
	hasChanges bool
	commit     string
	staged     bool
	pushes     []string
}

func (g *fakeFinalizationGit) CurrentBranch(context.Context, string) (string, error) {
	return g.branch, nil
}

func (g *fakeFinalizationGit) HasWorkingTreeChanges(context.Context, string) (bool, error) {
	return g.hasChanges, nil
}

func (g *fakeFinalizationGit) HeadCommit(context.Context, string) (string, error) {
	return g.commit, nil
}

func (g *fakeFinalizationGit) StageAll(context.Context, string) error {
	g.staged = true
	return nil
}

func (g *fakeFinalizationGit) Commit(context.Context, string, string) (string, error) {
	return g.commit, nil
}

func (g *fakeFinalizationGit) PushDefaultBranch(_ context.Context, _ string, branch string) error {
	g.pushes = append(g.pushes, branch)
	return nil
}

func TestFinalizeRequiresConfirmationForRunningCompletion(t *testing.T) {
	service, _, store, backend := newFinalizationTestService(t, []task.Task{
		finalizationMainTask("op-1", "/tmp/repo"),
	}, map[string]taskstate.TaskState{
		"alpha/op-1": finalizationTaskState("op-1", taskstate.RunAttempt{
			Attempt:    1,
			Status:     taskstate.RunStatusRunning,
			Branch:     "main",
			Worktree:   "/tmp/repo",
			Completion: &taskstate.Completion{Summary: "Done", Details: "Implemented."},
		}),
	})

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})

	var confirmationErr *workflow.RunningCompletionConfirmationError
	if !errors.As(err, &confirmationErr) {
		t.Fatalf("error = %v, want RunningCompletionConfirmationError", err)
	}
	if confirmationErr.Confirmation.TaskID != "op-1" || confirmationErr.Confirmation.Attempt != 1 {
		t.Fatalf("confirmation = %#v, want op-1 attempt 1", confirmationErr.Confirmation)
	}
	if taskstate.FinalizationFacts(store.states["alpha/op-1"]).Commit != "" {
		t.Fatalf("finalization = %#v, want unchanged", store.states["alpha/op-1"].Finalization)
	}
	if len(backend.closed) != 0 {
		t.Fatalf("closed = %#v, want no backend close", backend.closed)
	}
}

func TestFinalizeAllowsConfirmedRunningCompletionWithoutMutatingRunStatus(t *testing.T) {
	service, git, store, backend := newFinalizationTestService(t, []task.Task{
		finalizationMainTask("op-1", "/tmp/repo"),
	}, map[string]taskstate.TaskState{
		"alpha/op-1": finalizationTaskState("op-1", taskstate.RunAttempt{
			Attempt:    1,
			Status:     taskstate.RunStatusRunning,
			Branch:     "main",
			Worktree:   "/tmp/repo",
			Completion: &taskstate.Completion{Summary: "Done", Details: "Implemented."},
		}),
	})

	result, err := service.Finalize(context.Background(), workflow.FinalizeOptions{
		TaskID:                "op-1",
		AllowRunningCompleted: true,
	})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if result.Finalization.Commit != "commit123" || !git.staged || len(git.pushes) != 1 || git.pushes[0] != "main" {
		t.Fatalf("result = %#v staged=%v pushes=%#v, want commit and push", result, git.staged, git.pushes)
	}
	if len(backend.closed) != 1 || backend.closed[0] != "op-1" {
		t.Fatalf("closed = %#v, want op-1", backend.closed)
	}
	latest, _ := taskstate.LatestRun(store.states["alpha/op-1"])
	if latest.Status != taskstate.RunStatusRunning {
		t.Fatalf("latest status = %q, want still running", latest.Status)
	}
}

func TestFinalizeDoesNotRequestRunningConfirmationWhenOtherChecksFail(t *testing.T) {
	service, git, _, _ := newFinalizationTestService(t, []task.Task{
		finalizationMainTask("op-1", "/tmp/repo"),
	}, map[string]taskstate.TaskState{
		"alpha/op-1": finalizationTaskState("op-1", taskstate.RunAttempt{
			Attempt:    1,
			Status:     taskstate.RunStatusRunning,
			Branch:     "main",
			Worktree:   "/tmp/repo",
			Completion: &taskstate.Completion{Summary: "Done", Details: "Implemented."},
		}),
	})
	git.hasChanges = false

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})

	if _, ok := workflow.RunningCompletionConfirmationFromError(err); ok {
		t.Fatalf("error = %v, did not want confirmation when no reviewed changes exist", err)
	}
	if err == nil || !strings.Contains(err.Error(), "has no changes to commit") {
		t.Fatalf("error = %v, want no changes error", err)
	}
}

func TestFinalizeInfersSingleRunningCompletionCandidate(t *testing.T) {
	service, _, _, _ := newFinalizationTestService(t, []task.Task{
		finalizationMainTask("op-1", "/tmp/repo"),
	}, map[string]taskstate.TaskState{
		"alpha/op-1": finalizationTaskState("op-1", taskstate.RunAttempt{
			Attempt:    2,
			Status:     taskstate.RunStatusRunning,
			Branch:     "main",
			Worktree:   "/tmp/repo",
			Completion: &taskstate.Completion{Summary: "Done", Details: "Implemented."},
		}),
	})

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{CWD: "/tmp/repo"})

	var confirmationErr *workflow.RunningCompletionConfirmationError
	if !errors.As(err, &confirmationErr) || confirmationErr.Confirmation.TaskID != "op-1" {
		t.Fatalf("error = %v, want confirmation for inferred op-1", err)
	}

	result, err := service.Finalize(context.Background(), workflow.FinalizeOptions{
		CWD:                   "/tmp/repo",
		AllowRunningCompleted: true,
	})
	if err != nil {
		t.Fatalf("confirmed inferred finalize: %v", err)
	}
	if result.Task.ID != "op-1" {
		t.Fatalf("result task = %q, want op-1", result.Task.ID)
	}
}

func TestFinalizeDoesNotOfferRunningEscapeHatchForInvalidTargets(t *testing.T) {
	paths, source, targets := newFinalizationTestSource(t, "/tmp/repo", "op-1")
	service, _, _, _ := newFinalizationTestServiceForSource(t, paths, source, []task.Task{
		{
			ID:     "op-1",
			Status: task.StatusInProgress,
			Metadata: task.Metadata{
				task.MetadataBranch:   targets.WorktreeTeam.Branch,
				task.MetadataWorktree: targets.WorktreeTeam.Worktree,
			},
		},
	}, map[string]taskstate.TaskState{
		"alpha/op-1": finalizationTaskState("op-1", taskstate.RunAttempt{
			Attempt:    1,
			Status:     taskstate.RunStatusRunning,
			Branch:     targets.WorktreeTeam.Branch,
			Worktree:   targets.WorktreeTeam.Worktree,
			Completion: &taskstate.Completion{Summary: "Done", Details: "Implemented.", Commit: "abc123"},
		}),
	})

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})

	if _, ok := workflow.RunningCompletionConfirmationFromError(err); ok {
		t.Fatalf("error = %v, did not want confirmation bypass for worktree/team target", err)
	}
	if err == nil || !strings.Contains(err.Error(), "expected registered default branch") {
		t.Fatalf("error = %v, want metadata branch error", err)
	}
}

func newFinalizationTestService(
	t *testing.T,
	tasks []task.Task,
	states map[string]taskstate.TaskState,
) (workflow.FinalizationService, *fakeFinalizationGit, *fakeFinalizationRunStore, *fakeFinalizationBackend) {
	t.Helper()
	paths, source, _ := newFinalizationTestSource(t, "/tmp/repo", "op-1")
	return newFinalizationTestServiceForSource(t, paths, source, tasks, states)
}

func newFinalizationTestServiceForSource(
	t *testing.T,
	paths state.Paths,
	source task.RepositorySource,
	tasks []task.Task,
	states map[string]taskstate.TaskState,
) (workflow.FinalizationService, *fakeFinalizationGit, *fakeFinalizationRunStore, *fakeFinalizationBackend) {
	t.Helper()
	backend := &fakeFinalizationBackend{tasks: tasks}
	store := &fakeFinalizationRunStore{states: states}
	git := &fakeFinalizationGit{branch: source.Repository.DefaultBranch, hasChanges: true, commit: "commit123"}
	service := workflow.FinalizationService{
		Paths:   paths,
		Sources: []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (workflow.FinalizationBackend, error) {
			return backend, nil
		},
		RunStore: store,
		Git:      git,
	}
	return service, git, store, backend
}

func newFinalizationTestSource(
	t *testing.T,
	repoPath string,
	taskID string,
) (state.Paths, task.RepositorySource, workflow.ExpectedTargets) {
	t.Helper()
	paths := mustFinalizationTestPaths(t)
	source := task.RepositorySource{
		Repository: task.Repository{
			ID:            "alpha",
			Name:          "Alpha",
			TaskIDPrefix:  "op",
			Path:          repoPath,
			DefaultBranch: "main",
		},
		BackendDir: repoPath,
	}
	targets, err := workflow.ExpectedTargetsForTask(source.Repository, taskID, paths)
	if err != nil {
		t.Fatalf("expected targets: %v", err)
	}
	return paths, source, targets
}

func mustFinalizationTestPaths(t *testing.T) state.Paths {
	t.Helper()
	paths, err := state.NewPaths(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("create paths: %v", err)
	}
	return paths
}

func finalizationMainTask(taskID string, repoPath string) task.Task {
	return task.Task{
		ID:     taskID,
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   "main",
			task.MetadataWorktree: repoPath,
		},
	}
}

func finalizationTaskState(taskID string, runs ...taskstate.RunAttempt) taskstate.TaskState {
	return taskstate.TaskState{RepoID: "alpha", TaskID: taskID, Runs: runs}
}
