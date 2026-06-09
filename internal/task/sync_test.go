package task_test

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
)

type fakeSyncRunStore struct {
	states map[string]taskstate.TaskState
	err    error
}

func (s fakeSyncRunStore) Load(repoID, taskID string) (taskstate.TaskState, error) {
	if s.err != nil {
		return taskstate.TaskState{}, s.err
	}
	state, ok := s.states[repoID+"/"+taskID]
	if !ok {
		return taskstate.TaskState{RepoID: repoID, TaskID: taskID}, nil
	}
	return state, nil
}

type fakeSyncGit struct {
	pushes []fakeSyncPush
	err    error
}

type fakeSyncPush struct {
	dir    string
	branch string
}

func (g *fakeSyncGit) PushTaskBranch(_ context.Context, dir string, branch string) error {
	g.pushes = append(g.pushes, fakeSyncPush{dir: dir, branch: branch})
	return g.err
}

func TestSyncServicePushesEligibleWorktreeCompletion(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	worktreePath := filepath.Join(t.TempDir(), "worktree")
	service, git := newSyncTestService(t, task.Task{
		ID:       "op-1",
		Status:   task.StatusInProgress,
		Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-1", task.MetadataWorktree: worktreePath},
	}, syncTaskState(taskstate.RunAttempt{
		Attempt:   1,
		Status:    taskstate.RunStatusSucceeded,
		Branch:    "orpheus/op-1",
		Worktree:  worktreePath,
		StartedAt: time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC),
		Completion: &taskstate.Completion{
			Summary: "Done",
			Details: "Implemented.",
			Commit:  "abc123",
		},
	}), repoPath)

	result, err := service.Sync(context.Background(), task.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != task.SyncStatusPushed || result.Branch != "orpheus/op-1" {
		t.Fatalf("result = %#v, want pushed orpheus/op-1", result)
	}
	if len(git.pushes) != 1 || git.pushes[0].dir != repoPath || git.pushes[0].branch != "orpheus/op-1" {
		t.Fatalf("pushes = %#v, want repo task branch push", git.pushes)
	}
}

func TestSyncServiceSkipsNonEligibleTasks(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	worktreePath := filepath.Join(t.TempDir(), "worktree")
	succeeded := taskstate.RunAttempt{
		Attempt:   1,
		Status:    taskstate.RunStatusSucceeded,
		Branch:    "orpheus/op-1",
		Worktree:  worktreePath,
		StartedAt: time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC),
		Completion: &taskstate.Completion{
			Summary: "Done",
			Details: "Implemented.",
			Commit:  "abc123",
		},
	}
	baseTask := task.Task{
		ID:       "op-1",
		Status:   task.StatusInProgress,
		Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-1", task.MetadataWorktree: worktreePath},
	}

	tests := []struct {
		name       string
		taskItem   task.Task
		state      taskstate.TaskState
		wantReason string
	}{
		{
			name:       "no runs",
			taskItem:   baseTask,
			state:      taskstate.TaskState{RepoID: "alpha", TaskID: "op-1"},
			wantReason: "no Orpheus run attempts",
		},
		{
			name:       "running",
			taskItem:   baseTask,
			state:      syncTaskState(taskstate.RunAttempt{Attempt: 2, Status: taskstate.RunStatusRunning}),
			wantReason: "still running",
		},
		{
			name:       "failed",
			taskItem:   baseTask,
			state:      syncTaskState(taskstate.RunAttempt{Attempt: 2, Status: taskstate.RunStatusFailed}),
			wantReason: "failed",
		},
		{
			name:       "no completion",
			taskItem:   baseTask,
			state:      syncTaskState(taskstate.RunAttempt{Attempt: 2, Status: taskstate.RunStatusSucceeded}),
			wantReason: "without a completion block",
		},
		{
			name:     "missing commit",
			taskItem: baseTask,
			state: syncTaskState(taskstate.RunAttempt{
				Attempt:    2,
				Status:     taskstate.RunStatusSucceeded,
				Branch:     "orpheus/op-1",
				Worktree:   worktreePath,
				Completion: &taskstate.Completion{Summary: "Done", Details: "Done."},
			}),
			wantReason: "commit is missing",
		},
		{
			name:     "commit failed",
			taskItem: baseTask,
			state: syncTaskState(taskstate.RunAttempt{
				Attempt:    2,
				Status:     taskstate.RunStatusSucceeded,
				Branch:     "orpheus/op-1",
				Worktree:   worktreePath,
				Completion: &taskstate.Completion{Summary: "Done", Details: "Done.", CommitError: "dirty worktree"},
			}),
			wantReason: "completion commit failed",
		},
		{
			name:     "main solo",
			taskItem: task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{task.MetadataBranch: "main", task.MetadataWorktree: repoPath}},
			state: syncTaskState(taskstate.RunAttempt{
				Attempt:    2,
				Status:     taskstate.RunStatusSucceeded,
				Branch:     "main",
				Worktree:   repoPath,
				Completion: &taskstate.Completion{Summary: "Done", Details: "Done."},
			}),
			wantReason: "main/solo",
		},
		{
			name:     "branch run at repo root",
			taskItem: task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-1", task.MetadataWorktree: repoPath}},
			state: syncTaskState(taskstate.RunAttempt{
				Attempt:  2,
				Status:   taskstate.RunStatusSucceeded,
				Branch:   "orpheus/op-1",
				Worktree: repoPath,
				Completion: &taskstate.Completion{
					Summary: "Done",
					Details: "Done.",
					Commit:  "abc123",
				},
			}),
			wantReason: "registered repo root",
		},
		{
			name:       "pr url",
			taskItem:   withSyncMetadata(baseTask, task.MetadataPRURL, "https://example.test/pr/1"),
			state:      syncTaskState(succeeded),
			wantReason: "orpheus.pr_url is already set",
		},
		{
			name:       "closed",
			taskItem:   withSyncStatus(baseTask, task.StatusClosed),
			state:      syncTaskState(succeeded),
			wantReason: "task is closed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, git := newSyncTestService(t, tt.taskItem, tt.state, repoPath)
			result, err := service.Sync(context.Background(), task.SyncOptions{TaskID: "op-1"})
			if err != nil {
				t.Fatalf("sync: %v", err)
			}
			if result.Status != task.SyncStatusSkipped || !strings.Contains(result.Reason, tt.wantReason) {
				t.Fatalf("result = %#v, want skipped reason containing %q", result, tt.wantReason)
			}
			if len(git.pushes) != 0 {
				t.Fatalf("pushes = %#v, want no push for skip", git.pushes)
			}
		})
	}
}

func TestSyncServiceErrorsOnMalformedMetadataAndPushFailure(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	worktreePath := filepath.Join(t.TempDir(), "worktree")
	state := syncTaskState(taskstate.RunAttempt{
		Attempt:   1,
		Status:    taskstate.RunStatusSucceeded,
		Branch:    "orpheus/op-1",
		Worktree:  worktreePath,
		StartedAt: time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC),
		Completion: &taskstate.Completion{
			Summary: "Done",
			Details: "Implemented.",
			Commit:  "abc123",
		},
	})

	missingBranch := task.Task{
		ID:       "op-1",
		Status:   task.StatusInProgress,
		Metadata: task.Metadata{task.MetadataWorktree: worktreePath},
	}
	service, _ := newSyncTestService(t, missingBranch, state, repoPath)
	_, err := service.Sync(context.Background(), task.SyncOptions{TaskID: "op-1"})
	if err == nil || !strings.Contains(err.Error(), "orpheus.branch is missing") {
		t.Fatalf("error = %v, want missing branch metadata", err)
	}

	pushErr := errors.New("remote rejected")
	service, _ = newSyncTestService(t, task.Task{
		ID:       "op-1",
		Status:   task.StatusInProgress,
		Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-1", task.MetadataWorktree: worktreePath},
	}, state, repoPath)
	service.Git = &fakeSyncGit{err: pushErr}
	_, err = service.Sync(context.Background(), task.SyncOptions{TaskID: "op-1"})
	if !errors.Is(err, pushErr) {
		t.Fatalf("error = %v, want push error", err)
	}
}

func newSyncTestService(
	t *testing.T,
	taskItem task.Task,
	taskState taskstate.TaskState,
	repoPath string,
) (task.SyncService, *fakeSyncGit) {
	t.Helper()
	paths, err := state.NewPaths(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("create paths: %v", err)
	}
	git := &fakeSyncGit{}
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
	service := task.SyncService{
		Paths:   paths,
		Sources: []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.Getter, error) {
			return fakeReadBackend{tasks: []task.Task{taskItem}}, nil
		},
		RunStore: fakeSyncRunStore{states: map[string]taskstate.TaskState{"alpha/op-1": taskState}},
		Git:      git,
	}
	return service, git
}

func syncTaskState(runs ...taskstate.RunAttempt) taskstate.TaskState {
	return taskstate.TaskState{RepoID: "alpha", TaskID: "op-1", Runs: runs}
}

func withSyncStatus(taskItem task.Task, status task.Status) task.Task {
	taskItem.Status = status
	return taskItem
}

func withSyncMetadata(taskItem task.Task, key string, value string) task.Task {
	taskItem.Metadata = taskItem.Metadata.Clone()
	taskItem.Metadata[key] = value
	return taskItem
}
