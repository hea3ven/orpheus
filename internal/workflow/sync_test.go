package workflow_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
)

type fakeSyncRunStore struct {
	states map[string]taskstate.TaskState
	err    error
}

type fakeReadBackend struct {
	tasks []task.Task
}

func (b fakeReadBackend) Get(_ context.Context, id string) (task.Task, error) {
	for _, candidate := range b.tasks {
		if candidate.ID == id {
			return candidate.Clone(), nil
		}
	}
	return task.Task{}, task.ErrNotFound
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

type fakePRProvider struct {
	findRequests   []pullrequest.FindOpenByBranchRequest
	createRequests []pullrequest.CreateRequest
	found          pullrequest.PullRequest
	foundOK        bool
	created        pullrequest.PullRequest
	findErr        error
	createErr      error
}

func (p *fakePRProvider) FindOpenByBranch(_ context.Context, req pullrequest.FindOpenByBranchRequest) (pullrequest.PullRequest, bool, error) {
	p.findRequests = append(p.findRequests, req)
	return p.found, p.foundOK, p.findErr
}

func (p *fakePRProvider) Create(_ context.Context, req pullrequest.CreateRequest) (pullrequest.PullRequest, error) {
	p.createRequests = append(p.createRequests, req)
	if p.createErr != nil {
		return pullrequest.PullRequest{}, p.createErr
	}
	return p.created, nil
}

func TestSyncServiceCreatesPRForEligibleWorktreeCompletion(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	worktreePath := targets.WorktreeTeam.Worktree
	taskItem := task.Task{
		ID:       "op-1",
		Title:    "Sync creates PR",
		Status:   task.StatusInProgress,
		Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-1", task.MetadataWorktree: worktreePath},
	}
	service, git, provider := newSyncTestService(t, taskItem, syncTaskState(taskstate.RunAttempt{
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
	}), paths, source)

	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != workflow.SyncStatusPRCreated || result.Branch != "orpheus/op-1" || result.PRURL != "https://github.test/org/repo/pull/42" {
		t.Fatalf("result = %#v, want created PR for orpheus/op-1", result)
	}
	if len(git.pushes) != 1 || git.pushes[0].dir != repoPath || git.pushes[0].branch != "orpheus/op-1" {
		t.Fatalf("pushes = %#v, want repo task branch push", git.pushes)
	}
	if len(provider.findRequests) != 1 {
		t.Fatalf("find requests = %#v, want one", provider.findRequests)
	}
	find := provider.findRequests[0]
	if find.RepositoryPath != repoPath || find.HeadBranch != "orpheus/op-1" || find.BaseBranch != "main" {
		t.Fatalf("find request = %#v, want repo/head/base", find)
	}
	if len(provider.createRequests) != 1 {
		t.Fatalf("create requests = %#v, want one", provider.createRequests)
	}
	create := provider.createRequests[0]
	if create.RepositoryPath != repoPath || create.HeadBranch != "orpheus/op-1" || create.BaseBranch != "main" {
		t.Fatalf("create request = %#v, want repo/head/base", create)
	}
	if !strings.Contains(create.Title, "op-1: Sync creates PR") || !strings.Contains(create.Body, "Created by Orpheus") {
		t.Fatalf("created content title/body = %q/%q, want Orpheus task content", create.Title, create.Body)
	}
}

func TestSyncServiceRecoversExistingPRBeforeCreate(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	worktreePath := targets.WorktreeTeam.Worktree
	service, _, provider := newSyncTestService(t, task.Task{
		ID:       "op-1",
		Status:   task.StatusInProgress,
		Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-1", task.MetadataWorktree: worktreePath},
	}, syncTaskState(taskstate.RunAttempt{
		Attempt:  1,
		Status:   taskstate.RunStatusSucceeded,
		Branch:   "orpheus/op-1",
		Worktree: worktreePath,
		Completion: &taskstate.Completion{
			Summary: "Done",
			Details: "Implemented.",
			Commit:  "abc123",
		},
	}), paths, source)
	provider.found = pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/7"}
	provider.foundOK = true

	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != workflow.SyncStatusPRRecovered || result.PRURL != "https://github.test/org/repo/pull/7" {
		t.Fatalf("result = %#v, want recovered PR", result)
	}
	if len(provider.findRequests) != 1 {
		t.Fatalf("find requests = %#v, want one", provider.findRequests)
	}
	if len(provider.createRequests) != 0 {
		t.Fatalf("create requests = %#v, want none for recovered PR", provider.createRequests)
	}
}

func TestSyncServiceSkipsNonEligibleTasks(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	worktreePath := targets.WorktreeTeam.Worktree
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
			taskItem: task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{task.MetadataBranch: targets.MainSolo.Branch, task.MetadataWorktree: targets.MainSolo.Worktree}},
			state: syncTaskState(taskstate.RunAttempt{
				Attempt:    2,
				Status:     taskstate.RunStatusSucceeded,
				Branch:     targets.MainSolo.Branch,
				Worktree:   targets.MainSolo.Worktree,
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
			service, git, provider := newSyncTestService(t, tt.taskItem, tt.state, paths, source)
			result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
			if err != nil {
				t.Fatalf("sync: %v", err)
			}
			if result.Status != workflow.SyncStatusSkipped || !strings.Contains(result.Reason, tt.wantReason) {
				t.Fatalf("result = %#v, want skipped reason containing %q", result, tt.wantReason)
			}
			if len(git.pushes) != 0 {
				t.Fatalf("pushes = %#v, want no push for skip", git.pushes)
			}
			if len(provider.findRequests) != 0 || len(provider.createRequests) != 0 {
				t.Fatalf("provider calls = %#v/%#v, want no PR calls for skip", provider.findRequests, provider.createRequests)
			}
		})
	}
}

func TestSyncServiceErrorsOnMalformedMetadataAndPushFailure(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	worktreePath := targets.WorktreeTeam.Worktree
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
	service, _, _ := newSyncTestService(t, missingBranch, state, paths, source)
	_, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err == nil || !strings.Contains(err.Error(), "orpheus.branch is missing") {
		t.Fatalf("error = %v, want missing branch metadata", err)
	}

	pushErr := errors.New("remote rejected")
	service, _, _ = newSyncTestService(t, task.Task{
		ID:       "op-1",
		Status:   task.StatusInProgress,
		Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-1", task.MetadataWorktree: worktreePath},
	}, state, paths, source)
	service.Git = &fakeSyncGit{err: pushErr}
	_, err = service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if !errors.Is(err, pushErr) {
		t.Fatalf("error = %v, want push error", err)
	}
}

func TestSyncServicePRProviderFailuresAreHardErrors(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	worktreePath := targets.WorktreeTeam.Worktree
	state := syncTaskState(taskstate.RunAttempt{
		Attempt:  1,
		Status:   taskstate.RunStatusSucceeded,
		Branch:   "orpheus/op-1",
		Worktree: worktreePath,
		Completion: &taskstate.Completion{
			Summary: "Done",
			Details: "Implemented.",
			Commit:  "abc123",
		},
	})
	taskItem := task.Task{
		ID:       "op-1",
		Status:   task.StatusInProgress,
		Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-1", task.MetadataWorktree: worktreePath},
	}

	findErr := errors.New("auth missing")
	service, _, provider := newSyncTestService(t, taskItem, state, paths, source)
	provider.findErr = findErr
	_, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if !errors.Is(err, findErr) {
		t.Fatalf("error = %v, want find error", err)
	}
	if len(provider.createRequests) != 0 {
		t.Fatalf("create requests = %#v, want none after find error", provider.createRequests)
	}

	createErr := errors.New("create failed")
	service, _, provider = newSyncTestService(t, taskItem, state, paths, source)
	provider.createErr = createErr
	_, err = service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if !errors.Is(err, createErr) {
		t.Fatalf("error = %v, want create error", err)
	}
}

func TestBuildSyncPullRequestContent(t *testing.T) {
	content, err := workflow.BuildSyncPullRequestContent(task.Task{
		ID:                 "op-1",
		Title:              "Create PR",
		Description:        "Implement sync PR creation.",
		AcceptanceCriteria: "No duplicate PRs are created.",
	}, taskstate.RunAttempt{
		Completion: &taskstate.Completion{
			Summary: "Ready for review",
			Details: "Pushed the branch and wired the provider.",
		},
	})
	if err != nil {
		t.Fatalf("build content: %v", err)
	}
	for _, want := range []string{
		"op-1: Create PR",
		"Created by Orpheus.",
		"- ID: op-1",
		"Implement sync PR creation.",
		"No duplicate PRs are created.",
		"Ready for review",
		"Pushed the branch and wired the provider.",
	} {
		if !strings.Contains(content.Title+"\n"+content.Body, want) {
			t.Fatalf("content = %#v, want %q", content, want)
		}
	}
	if strings.Contains(strings.ToLower(content.Body), "beads") {
		t.Fatalf("content body = %q, want no backend-specific Beads text", content.Body)
	}
}

func newSyncTestService(
	t *testing.T,
	taskItem task.Task,
	taskState taskstate.TaskState,
	paths state.Paths,
	source task.RepositorySource,
) (workflow.SyncService, *fakeSyncGit, *fakePRProvider) {
	t.Helper()
	git := &fakeSyncGit{}
	provider := &fakePRProvider{created: pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/42"}}
	service := workflow.SyncService{
		Paths:   paths,
		Sources: []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.Getter, error) {
			return fakeReadBackend{tasks: []task.Task{taskItem}}, nil
		},
		RunStore:   fakeSyncRunStore{states: map[string]taskstate.TaskState{"alpha/op-1": taskState}},
		Git:        git,
		PRProvider: provider,
	}
	return service, git, provider
}

func newSyncTestSource(
	t *testing.T,
	repoPath string,
	taskID string,
) (state.Paths, task.RepositorySource, workflow.ExpectedTargets) {
	t.Helper()
	paths, err := state.NewPaths(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatalf("create paths: %v", err)
	}
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
