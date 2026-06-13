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

type fakeSyncBackend struct {
	tasks     []task.Task
	setPRURLs []fakeSetPRURL
}

type fakeSetPRURL struct {
	taskID string
	prURL  string
}

func (b *fakeSyncBackend) Get(_ context.Context, id string) (task.Task, error) {
	for _, candidate := range b.tasks {
		if candidate.ID == id {
			return candidate.Clone(), nil
		}
	}
	return task.Task{}, task.ErrNotFound
}

func (b *fakeSyncBackend) SetPRURL(_ context.Context, taskID string, prURL string) error {
	b.setPRURLs = append(b.setPRURLs, fakeSetPRURL{taskID: taskID, prURL: prURL})
	return nil
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
	statusRequests []pullrequest.StatusByURLRequest
	found          pullrequest.PullRequest
	foundOK        bool
	created        pullrequest.PullRequest
	status         pullrequest.PullRequestStatus
	findErr        error
	createErr      error
	statusErr      error
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

func (p *fakePRProvider) StatusByURL(_ context.Context, req pullrequest.StatusByURLRequest) (pullrequest.PullRequestStatus, error) {
	p.statusRequests = append(p.statusRequests, req)
	if p.statusErr != nil {
		return pullrequest.PullRequestStatus{}, p.statusErr
	}
	return p.status, nil
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
	service, git, provider, backend := newSyncTestService(t, taskItem, syncTaskState(taskstate.RunAttempt{
		Attempt:   1,
		Status:    taskstate.RunStatusSucceeded,
		Branch:    "orpheus/op-1",
		Worktree:  worktreePath,
		StartedAt: time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC),
		Completion: &taskstate.Completion{
			Summary:             "Done",
			Description:         "Implemented.",
			DetailedDescription: "Detailed PR body.",
			Commit:              "abc123",
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
	if create.Title != "Done" || create.Body != "Detailed PR body." {
		t.Fatalf("created content title/body = %q/%q, want completion summary/detailed description", create.Title, create.Body)
	}
	if len(backend.setPRURLs) != 1 || backend.setPRURLs[0].taskID != "op-1" ||
		backend.setPRURLs[0].prURL != "https://github.test/org/repo/pull/42" {
		t.Fatalf("set PR URLs = %#v, want created PR URL stored", backend.setPRURLs)
	}
}

func TestSyncServiceRecoversExistingPRBeforeCreate(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	worktreePath := targets.WorktreeTeam.Worktree
	service, _, provider, backend := newSyncTestService(t, task.Task{
		ID:       "op-1",
		Status:   task.StatusInProgress,
		Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-1", task.MetadataWorktree: worktreePath},
	}, syncTaskState(taskstate.RunAttempt{
		Attempt:  1,
		Status:   taskstate.RunStatusSucceeded,
		Branch:   "orpheus/op-1",
		Worktree: worktreePath,
		Completion: &taskstate.Completion{
			Summary:             "Done",
			Description:         "Implemented.",
			DetailedDescription: "Detailed PR body.",
			Commit:              "abc123",
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
	if len(backend.setPRURLs) != 1 || backend.setPRURLs[0].prURL != "https://github.test/org/repo/pull/7" {
		t.Fatalf("set PR URLs = %#v, want recovered PR URL stored", backend.setPRURLs)
	}
}

func TestSyncServicePollsOpenPRWithoutLocalEligibility(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   "orpheus/op-1",
			task.MetadataWorktree: targets.WorktreeTeam.Worktree,
			task.MetadataPRURL:    " https://github.test/org/repo/pull/42 ",
		},
	}
	service, git, provider, backend := newSyncTestService(
		t,
		taskItem,
		taskstate.TaskState{},
		paths,
		source,
	)
	service.RunStore = fakeSyncRunStore{err: errors.New("run store should not be queried")}
	provider.status = pullrequest.PullRequestStatus{URL: "https://github.test/org/repo/pull/42", State: pullrequest.StateOpen}

	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != workflow.SyncStatusAlreadyInReview || result.PRURL != "https://github.test/org/repo/pull/42" {
		t.Fatalf("result = %#v, want already in review with trimmed PR URL", result)
	}
	if len(git.pushes) != 0 {
		t.Fatalf("pushes = %#v, want no push", git.pushes)
	}
	if len(provider.statusRequests) != 1 || provider.statusRequests[0].URL != "https://github.test/org/repo/pull/42" {
		t.Fatalf("status requests = %#v, want recorded PR URL polling", provider.statusRequests)
	}
	if len(provider.findRequests) != 0 || len(provider.createRequests) != 0 {
		t.Fatalf("provider find/create calls = %#v/%#v, want none", provider.findRequests, provider.createRequests)
	}
	if len(backend.setPRURLs) != 0 {
		t.Fatalf("set PR URLs = %#v, want no metadata write", backend.setPRURLs)
	}
}

func TestSyncServiceReportsMergedPRWithoutMutation(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, _ := newSyncTestSource(t, repoPath, "op-1")
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataPRURL: "https://github.test/org/repo/pull/42",
		},
	}
	service, git, provider, backend := newSyncTestService(t, taskItem, taskstate.TaskState{}, paths, source)
	service.RunStore = fakeSyncRunStore{err: errors.New("run store should not be queried")}
	provider.status = pullrequest.PullRequestStatus{URL: "https://github.test/org/repo/pull/42", State: pullrequest.StateMerged}

	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != workflow.SyncStatusPRMerged ||
		!strings.Contains(result.Reason, "backend close is not implemented") {
		t.Fatalf("result = %#v, want merged read-only status", result)
	}
	if len(git.pushes) != 0 {
		t.Fatalf("pushes = %#v, want no push", git.pushes)
	}
	if len(provider.findRequests) != 0 || len(provider.createRequests) != 0 {
		t.Fatalf("provider find/create calls = %#v/%#v, want none", provider.findRequests, provider.createRequests)
	}
	if len(backend.setPRURLs) != 0 {
		t.Fatalf("set PR URLs = %#v, want no metadata write", backend.setPRURLs)
	}
}

func TestSyncServiceClosedTaskSkipsWithoutPRPolling(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, _ := newSyncTestSource(t, repoPath, "op-1")
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusClosed,
		Metadata: task.Metadata{
			task.MetadataPRURL: "https://github.test/org/repo/pull/42",
		},
	}
	service, git, provider, backend := newSyncTestService(t, taskItem, taskstate.TaskState{}, paths, source)
	service.RunStore = fakeSyncRunStore{err: errors.New("run store should not be queried")}
	provider.statusErr = errors.New("provider should not be queried")

	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != workflow.SyncStatusSkipped || !strings.Contains(result.Reason, "task is closed") {
		t.Fatalf("result = %#v, want closed task skip", result)
	}
	if len(git.pushes) != 0 {
		t.Fatalf("pushes = %#v, want no push", git.pushes)
	}
	if len(provider.statusRequests) != 0 || len(provider.findRequests) != 0 || len(provider.createRequests) != 0 {
		t.Fatalf("provider calls = %#v/%#v/%#v, want none", provider.statusRequests, provider.findRequests, provider.createRequests)
	}
	if len(backend.setPRURLs) != 0 {
		t.Fatalf("set PR URLs = %#v, want no metadata write", backend.setPRURLs)
	}
}

func TestSyncServiceExistingPRFailuresAreHardErrors(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, _ := newSyncTestSource(t, repoPath, "op-1")
	taskItem := task.Task{
		ID:       "op-1",
		Status:   task.StatusInProgress,
		Metadata: task.Metadata{task.MetadataPRURL: "https://github.test/org/repo/pull/42"},
	}

	tests := []struct {
		name      string
		status    pullrequest.PullRequestStatus
		statusErr error
		want      string
	}{
		{
			name:   "closed unmerged",
			status: pullrequest.PullRequestStatus{URL: "https://github.test/org/repo/pull/42", State: pullrequest.StateClosed},
			want:   "closed without merge",
		},
		{
			name:      "provider failure",
			statusErr: errors.New("gh auth missing"),
			want:      "gh auth missing",
		},
		{
			name:   "unsupported state",
			status: pullrequest.PullRequestStatus{URL: "https://github.test/org/repo/pull/42", State: pullrequest.State("draft")},
			want:   "unsupported provider state",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, git, provider, backend := newSyncTestService(t, taskItem, taskstate.TaskState{}, paths, source)
			service.RunStore = fakeSyncRunStore{err: errors.New("run store should not be queried")}
			provider.status = tt.status
			provider.statusErr = tt.statusErr

			_, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
			if len(git.pushes) != 0 {
				t.Fatalf("pushes = %#v, want no push", git.pushes)
			}
			if len(provider.findRequests) != 0 || len(provider.createRequests) != 0 {
				t.Fatalf("provider find/create calls = %#v/%#v, want none", provider.findRequests, provider.createRequests)
			}
			if len(backend.setPRURLs) != 0 {
				t.Fatalf("set PR URLs = %#v, want no metadata write", backend.setPRURLs)
			}
		})
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
			Summary:             "Done",
			Description:         "Implemented.",
			DetailedDescription: "Detailed PR body.",
			Commit:              "abc123",
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
				Attempt:  2,
				Status:   taskstate.RunStatusSucceeded,
				Branch:   "orpheus/op-1",
				Worktree: worktreePath,
				Completion: &taskstate.Completion{
					Summary:             "Done",
					Description:         "Done.",
					DetailedDescription: "Detailed PR body.",
				},
			}),
			wantReason: "commit is missing",
		},
		{
			name:     "commit failed",
			taskItem: baseTask,
			state: syncTaskState(taskstate.RunAttempt{
				Attempt:  2,
				Status:   taskstate.RunStatusSucceeded,
				Branch:   "orpheus/op-1",
				Worktree: worktreePath,
				Completion: &taskstate.Completion{Summary: "Done", Description: "Done.",
					DetailedDescription: "Detailed PR body.", CommitError: "dirty worktree"},
			}),
			wantReason: "completion commit failed",
		},
		{
			name:     "main solo",
			taskItem: task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{task.MetadataBranch: targets.MainSolo.Branch, task.MetadataWorktree: targets.MainSolo.Worktree}},
			state: syncTaskState(taskstate.RunAttempt{
				Attempt:  2,
				Status:   taskstate.RunStatusSucceeded,
				Branch:   targets.MainSolo.Branch,
				Worktree: targets.MainSolo.Worktree,
				Completion: &taskstate.Completion{
					Summary:             "Done",
					Description:         "Done.",
					DetailedDescription: "Detailed PR body.",
				},
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
					Summary:             "Done",
					Description:         "Done.",
					DetailedDescription: "Detailed PR body.",
					Commit:              "abc123",
				},
			}),
			wantReason: "registered repo root",
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
			service, git, provider, backend := newSyncTestService(t, tt.taskItem, tt.state, paths, source)
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
			if len(provider.statusRequests) != 0 || len(provider.findRequests) != 0 || len(provider.createRequests) != 0 {
				t.Fatalf("provider calls = %#v/%#v/%#v, want no PR calls for skip", provider.statusRequests, provider.findRequests, provider.createRequests)
			}
			if len(backend.setPRURLs) != 0 {
				t.Fatalf("set PR URLs = %#v, want no metadata writes for skip", backend.setPRURLs)
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
			Summary:             "Done",
			Description:         "Implemented.",
			DetailedDescription: "Detailed PR body.",
			Commit:              "abc123",
		},
	})

	missingBranch := task.Task{
		ID:       "op-1",
		Status:   task.StatusInProgress,
		Metadata: task.Metadata{task.MetadataWorktree: worktreePath},
	}
	service, _, _, _ := newSyncTestService(t, missingBranch, state, paths, source)
	_, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err == nil || !strings.Contains(err.Error(), "orpheus.branch is missing") {
		t.Fatalf("error = %v, want missing branch metadata", err)
	}

	pushErr := errors.New("remote rejected")
	service, _, _, _ = newSyncTestService(t, task.Task{
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
			Summary:             "Done",
			Description:         "Implemented.",
			DetailedDescription: "Detailed PR body.",
			Commit:              "abc123",
		},
	})
	taskItem := task.Task{
		ID:       "op-1",
		Status:   task.StatusInProgress,
		Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-1", task.MetadataWorktree: worktreePath},
	}

	findErr := errors.New("auth missing")
	service, _, provider, _ := newSyncTestService(t, taskItem, state, paths, source)
	provider.findErr = findErr
	_, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if !errors.Is(err, findErr) {
		t.Fatalf("error = %v, want find error", err)
	}
	if len(provider.createRequests) != 0 {
		t.Fatalf("create requests = %#v, want none after find error", provider.createRequests)
	}

	createErr := errors.New("create failed")
	service, _, provider, _ = newSyncTestService(t, taskItem, state, paths, source)
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
			Summary:             "Ready for review",
			Description:         "Pushed the branch and wired the provider.",
			DetailedDescription: "Detailed PR body.",
		},
	})
	if err != nil {
		t.Fatalf("build content: %v", err)
	}
	if content.Title != "Ready for review" {
		t.Fatalf("title = %q, want completion summary", content.Title)
	}
	if content.Body != "Detailed PR body." {
		t.Fatalf("body = %q, want detailed description exactly", content.Body)
	}
	for _, unwanted := range []string{
		"op-1",
		"Create PR",
		"Implement sync PR creation.",
		"No duplicate PRs are created.",
		"Created by Orpheus",
		"Summary",
		"Details",
	} {
		if strings.Contains(content.Body, unwanted) {
			t.Fatalf("body = %q, should not contain %q", content.Body, unwanted)
		}
	}
}

func newSyncTestService(
	t *testing.T,
	taskItem task.Task,
	taskState taskstate.TaskState,
	paths state.Paths,
	source task.RepositorySource,
) (workflow.SyncService, *fakeSyncGit, *fakePRProvider, *fakeSyncBackend) {
	t.Helper()
	git := &fakeSyncGit{}
	provider := &fakePRProvider{created: pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/42"}}
	backend := &fakeSyncBackend{tasks: []task.Task{taskItem}}
	service := workflow.SyncService{
		Paths:   paths,
		Sources: []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.SyncBackend, error) {
			return backend, nil
		},
		RunStore:   fakeSyncRunStore{states: map[string]taskstate.TaskState{"alpha/op-1": taskState}},
		Git:        git,
		PRProvider: provider,
	}
	return service, git, provider, backend
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
