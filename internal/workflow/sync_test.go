package workflow_test

import (
	"context"
	"errors"
	"os"
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
	events     []fakeTaskClosedPRMergedEvent
	recordErr  error
	recordedAt time.Time
}

type fakeSyncBackend struct {
	tasks     []task.Task
	getTasks  []task.Task
	setPRURLs []fakeSetPRURL
	closed    []string
	closeErr  error
	onList    func()
	onGet     func()
}

type fakeSetPRURL struct {
	taskID string
	prURL  string
}

type fakeTaskClosedPRMergedEvent struct {
	repoID string
	taskID string
	opts   taskstate.TaskClosedPRMergedOptions
}

func (b *fakeSyncBackend) Get(_ context.Context, id string) (task.Task, error) {
	if b.onGet != nil {
		b.onGet()
	}
	tasks := b.tasks
	if b.getTasks != nil {
		tasks = b.getTasks
	}
	for _, candidate := range tasks {
		if candidate.ID == id {
			return candidate.Clone(), nil
		}
	}
	return task.Task{}, task.ErrNotFound
}

func (b *fakeSyncBackend) List(_ context.Context) ([]task.Task, error) {
	if b.onList != nil {
		b.onList()
	}
	tasks := make([]task.Task, len(b.tasks))
	for i, taskItem := range b.tasks {
		tasks[i] = taskItem.Clone()
	}
	return tasks, nil
}

func (b *fakeSyncBackend) SetPRURL(_ context.Context, taskID string, prURL string) error {
	b.setPRURLs = append(b.setPRURLs, fakeSetPRURL{taskID: taskID, prURL: prURL})
	return nil
}

func (b *fakeSyncBackend) Close(_ context.Context, taskID string) error {
	b.closed = append(b.closed, taskID)
	if b.closeErr != nil {
		return b.closeErr
	}
	return nil
}

func (s *fakeSyncRunStore) RecordTaskClosedPRMerged(repoID, taskID string, opts taskstate.TaskClosedPRMergedOptions) (taskstate.Event, error) {
	s.events = append(s.events, fakeTaskClosedPRMergedEvent{repoID: repoID, taskID: taskID, opts: opts})
	if s.recordErr != nil {
		return taskstate.Event{}, s.recordErr
	}
	at := s.recordedAt
	if at.IsZero() {
		at = time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)
	}
	return taskstate.Event{
		Type:            taskstate.EventTaskClosedPRMerged,
		At:              at,
		PRURL:           opts.PRURL,
		ObservedPRState: opts.ObservedPRState,
	}, nil
}

type fakePRProvider struct {
	findRequests   []pullrequest.FindOpenByBranchRequest
	createRequests []pullrequest.CreateRequest
	statusRequests []pullrequest.StatusByURLRequest
	found          pullrequest.PullRequest
	foundOK        bool
	created        pullrequest.PullRequest
	status         pullrequest.PullRequestStatus
	statusByURL    map[string]pullrequest.PullRequestStatus
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
	if p.statusByURL != nil {
		status, ok := p.statusByURL[req.URL]
		if ok {
			return status, nil
		}
	}
	return p.status, nil
}

func TestSyncServiceSkipsPRCreationForEligibleWorktreeCompletion(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	worktreePath := targets.WorktreeTeam.Worktree
	taskItem := task.Task{
		ID:       "op-1",
		Title:    "Sync skips missing PR URL",
		Status:   task.StatusInProgress,
		Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-1", task.MetadataWorktree: worktreePath},
	}
	service, provider, backend := newSyncTestService(t, taskItem, syncTaskState(taskstate.RunAttempt{
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
	if result.Status != workflow.SyncStatusSkipped ||
		!strings.Contains(result.Reason, task.MetadataPRURL+" is not set") ||
		result.Branch != "orpheus/op-1" ||
		result.PRURL != "" {
		t.Fatalf("result = %#v, want skipped missing PR URL for orpheus/op-1", result)
	}
	if len(provider.findRequests) != 0 || len(provider.createRequests) != 0 {
		t.Fatalf("find/create requests = %#v/%#v, want none", provider.findRequests, provider.createRequests)
	}
	if len(backend.setPRURLs) != 0 {
		t.Fatalf("set PR URLs = %#v, want none", backend.setPRURLs)
	}
}

func TestSyncServiceDoesNotRecoverBranchPRWithoutRecordedPRURL(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	worktreePath := targets.WorktreeTeam.Worktree
	service, provider, backend := newSyncTestService(t, task.Task{
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
	if result.Status != workflow.SyncStatusSkipped ||
		!strings.Contains(result.Reason, task.MetadataPRURL+" is not set") ||
		result.PRURL != "" {
		t.Fatalf("result = %#v, want skipped missing PR URL", result)
	}
	if len(provider.findRequests) != 0 || len(provider.createRequests) != 0 {
		t.Fatalf("provider requests = %#v/%#v, want none", provider.findRequests, provider.createRequests)
	}
	if len(backend.setPRURLs) != 0 {
		t.Fatalf("set PR URLs = %#v, want none", backend.setPRURLs)
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
	service, provider, backend := newSyncTestService(
		t,
		taskItem,
		taskstate.TaskState{},
		paths,
		source,
	)
	service.RunStore = &fakeSyncRunStore{}
	provider.status = pullrequest.PullRequestStatus{URL: "https://github.test/org/repo/pull/42", State: pullrequest.StateOpen}

	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != workflow.SyncStatusAlreadyInReview || result.PRURL != "https://github.test/org/repo/pull/42" {
		t.Fatalf("result = %#v, want already in review with trimmed PR URL", result)
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

func TestSyncServiceClosesTaskAndRecordsAuditForMergedPR(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, _ := newSyncTestSource(t, repoPath, "op-1")
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataPRURL: "https://github.test/org/repo/pull/42",
		},
	}
	service, provider, backend := newSyncTestService(t, taskItem, taskstate.TaskState{}, paths, source)
	runStore := &fakeSyncRunStore{}
	service.RunStore = runStore
	provider.status = pullrequest.PullRequestStatus{URL: "https://github.test/org/repo/pull/42", State: pullrequest.StateMerged}

	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != workflow.SyncStatusPRMerged ||
		result.Task.Status != task.StatusClosed ||
		!strings.Contains(result.Reason, "backend task was closed") {
		t.Fatalf("result = %#v, want merged close status", result)
	}
	if len(provider.findRequests) != 0 || len(provider.createRequests) != 0 {
		t.Fatalf("provider find/create calls = %#v/%#v, want none", provider.findRequests, provider.createRequests)
	}
	if len(backend.setPRURLs) != 0 {
		t.Fatalf("set PR URLs = %#v, want no metadata write", backend.setPRURLs)
	}
	if len(backend.closed) != 1 || backend.closed[0] != "op-1" {
		t.Fatalf("closed = %#v, want backend close", backend.closed)
	}
	if len(runStore.events) != 1 {
		t.Fatalf("audit events = %#v, want one", runStore.events)
	}
	event := runStore.events[0]
	if event.repoID != "alpha" ||
		event.taskID != "op-1" ||
		event.opts.PRURL != "https://github.test/org/repo/pull/42" ||
		event.opts.ObservedPRState != "merged" {
		t.Fatalf("audit event = %#v, want repo/task/merged PR facts", event)
	}
}

func TestSyncServiceMergedPRCloseAndAuditFailures(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, _ := newSyncTestSource(t, repoPath, "op-1")
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataPRURL: "https://github.test/org/repo/pull/42",
		},
	}

	closeErr := errors.New("close rejected")
	service, provider, backend := newSyncTestService(t, taskItem, taskstate.TaskState{}, paths, source)
	runStore := &fakeSyncRunStore{}
	service.RunStore = runStore
	backend.closeErr = closeErr
	provider.status = pullrequest.PullRequestStatus{URL: "https://github.test/org/repo/pull/42", State: pullrequest.StateMerged}

	_, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if !errors.Is(err, closeErr) || !strings.Contains(err.Error(), "close backend task op-1 after merged PR") {
		t.Fatalf("error = %v, want close error with context", err)
	}
	if len(runStore.events) != 0 {
		t.Fatalf("audit events = %#v, want none after close failure", runStore.events)
	}

	auditErr := errors.New("disk full")
	service, provider, backend = newSyncTestService(t, taskItem, taskstate.TaskState{}, paths, source)
	runStore = &fakeSyncRunStore{
		recordErr: auditErr,
	}
	service.RunStore = runStore
	provider.status = pullrequest.PullRequestStatus{URL: "https://github.test/org/repo/pull/42", State: pullrequest.StateMerged}

	_, err = service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if !errors.Is(err, auditErr) ||
		!strings.Contains(err.Error(), "backend task op-1 was closed") ||
		!strings.Contains(err.Error(), "local task-state audit event failed") {
		t.Fatalf("error = %v, want post-close audit failure", err)
	}
	if len(backend.closed) != 1 || backend.closed[0] != "op-1" {
		t.Fatalf("closed = %#v, want backend close before audit error", backend.closed)
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
	service, provider, backend := newSyncTestService(t, taskItem, taskstate.TaskState{}, paths, source)
	service.RunStore = &fakeSyncRunStore{}
	provider.statusErr = errors.New("provider should not be queried")

	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != workflow.SyncStatusSkipped || !strings.Contains(result.Reason, "task is closed") {
		t.Fatalf("result = %#v, want closed task skip", result)
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
			service, provider, backend := newSyncTestService(t, taskItem, taskstate.TaskState{}, paths, source)
			service.RunStore = &fakeSyncRunStore{}
			provider.status = tt.status
			provider.statusErr = tt.statusErr

			_, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
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
	baseTask := task.Task{
		ID:       "op-1",
		Status:   task.StatusInProgress,
		Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-1", task.MetadataWorktree: worktreePath},
	}

	for _, tt := range syncNonEligibleTaskCases(baseTask, repoPath, worktreePath, targets) {
		t.Run(tt.name, func(t *testing.T) {
			service, provider, backend := newSyncTestService(t, tt.taskItem, tt.state, paths, source)
			result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
			if err != nil {
				t.Fatalf("sync: %v", err)
			}
			if result.Status != workflow.SyncStatusSkipped || !strings.Contains(result.Reason, tt.wantReason) {
				t.Fatalf("result = %#v, want skipped reason containing %q", result, tt.wantReason)
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

type syncNonEligibleTaskCase struct {
	name       string
	taskItem   task.Task
	state      taskstate.TaskState
	wantReason string
}

func syncNonEligibleTaskCases(
	baseTask task.Task,
	repoPath string,
	worktreePath string,
	targets workflow.ExpectedTargets,
) []syncNonEligibleTaskCase {
	cases := syncRunStateSkipCases(baseTask)
	cases = append(cases, syncCompletionSkipCases(baseTask, repoPath, worktreePath, targets)...)
	cases = append(cases, syncNonEligibleTaskCase{
		name:       "closed",
		taskItem:   withSyncStatus(baseTask, task.StatusClosed),
		state:      syncTaskState(syncSucceededRun(worktreePath)),
		wantReason: "task is closed",
	})
	return cases
}

func syncRunStateSkipCases(baseTask task.Task) []syncNonEligibleTaskCase {
	missingPRURL := task.MetadataPRURL + " is not set"
	return []syncNonEligibleTaskCase{
		{name: "no runs", taskItem: baseTask, state: taskstate.TaskState{RepoID: "alpha", TaskID: "op-1"}, wantReason: missingPRURL},
		{name: "running", taskItem: baseTask, state: syncTaskState(taskstate.RunAttempt{Attempt: 2, Status: taskstate.RunStatusRunning}), wantReason: missingPRURL},
		{name: "failed", taskItem: baseTask, state: syncTaskState(taskstate.RunAttempt{Attempt: 2, Status: taskstate.RunStatusFailed}), wantReason: missingPRURL},
		{name: "no completion", taskItem: baseTask, state: syncTaskState(taskstate.RunAttempt{Attempt: 2, Status: taskstate.RunStatusSucceeded}), wantReason: missingPRURL},
	}
}

func syncCompletionSkipCases(
	baseTask task.Task,
	repoPath string,
	worktreePath string,
	targets workflow.ExpectedTargets,
) []syncNonEligibleTaskCase {
	missingPRURL := task.MetadataPRURL + " is not set"
	return []syncNonEligibleTaskCase{
		{name: "missing commit", taskItem: baseTask, state: syncTaskState(syncCompletionRun(worktreePath, "")), wantReason: missingPRURL},
		{name: "commit failed", taskItem: baseTask, state: syncTaskState(syncCommitErrorRun(worktreePath)), wantReason: missingPRURL},
		{name: "main solo", taskItem: syncTaskForTarget(targets.MainSolo), state: syncTaskState(syncCompletionRun(targets.MainSolo.Worktree, targets.MainSolo.Branch)), wantReason: missingPRURL},
		{name: "branch run at repo root", taskItem: syncTaskForBranchWorktree("orpheus/op-1", repoPath), state: syncTaskState(syncCommittedCompletionRun(repoPath, "orpheus/op-1")), wantReason: missingPRURL},
	}
}

func syncSucceededRun(worktreePath string) taskstate.RunAttempt {
	run := syncCommittedCompletionRun(worktreePath, "orpheus/op-1")
	run.Attempt = 1
	run.StartedAt = time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC)
	return run
}

func syncCompletionRun(worktreePath string, branch string) taskstate.RunAttempt {
	if branch == "" {
		branch = "orpheus/op-1"
	}
	return taskstate.RunAttempt{
		Attempt:  2,
		Status:   taskstate.RunStatusSucceeded,
		Branch:   branch,
		Worktree: worktreePath,
		Completion: &taskstate.Completion{
			Summary:             "Done",
			Description:         "Done.",
			DetailedDescription: "Detailed PR body.",
		},
	}
}

func syncCommittedCompletionRun(worktreePath string, branch string) taskstate.RunAttempt {
	run := syncCompletionRun(worktreePath, branch)
	run.Completion.Commit = "abc123"
	return run
}

func syncCommitErrorRun(worktreePath string) taskstate.RunAttempt {
	run := syncCompletionRun(worktreePath, "orpheus/op-1")
	run.Completion.CommitError = "dirty worktree"
	return run
}

func syncTaskForTarget(target workflow.Target) task.Task {
	return syncTaskForBranchWorktree(target.Branch, target.Worktree)
}

func syncTaskForBranchWorktree(branch string, worktree string) task.Task {
	return task.Task{
		ID:       "op-1",
		Status:   task.StatusInProgress,
		Metadata: task.Metadata{task.MetadataBranch: branch, task.MetadataWorktree: worktree},
	}
}

func TestSyncServiceSkipsTasksWithoutPRURLDespiteMalformedMetadata(t *testing.T) {
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
	service, _, _ := newSyncTestService(t, missingBranch, state, paths, source)
	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != workflow.SyncStatusSkipped || !strings.Contains(result.Reason, task.MetadataPRURL+" is not set") {
		t.Fatalf("result = %#v, want skipped missing PR URL", result)
	}

	service, provider, backend := newSyncTestService(t, task.Task{
		ID:       "op-1",
		Status:   task.StatusInProgress,
		Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-1", task.MetadataWorktree: worktreePath},
	}, state, paths, source)
	result, err = service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != workflow.SyncStatusSkipped || !strings.Contains(result.Reason, task.MetadataPRURL+" is not set") {
		t.Fatalf("result = %#v, want skipped missing PR URL", result)
	}
	if len(provider.findRequests) != 0 || len(provider.createRequests) != 0 || len(backend.setPRURLs) != 0 {
		t.Fatalf("mutations provider=%#v/%#v set=%#v, want none", provider.findRequests, provider.createRequests, backend.setPRURLs)
	}
}

func TestSyncServiceDoesNotCallPRProviderForPublicationCandidates(t *testing.T) {
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
	service, provider, _ := newSyncTestService(t, taskItem, state, paths, source)
	provider.findErr = findErr
	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != workflow.SyncStatusSkipped || !strings.Contains(result.Reason, task.MetadataPRURL+" is not set") {
		t.Fatalf("result = %#v, want skipped missing PR URL", result)
	}
	if len(provider.findRequests) != 0 || len(provider.createRequests) != 0 {
		t.Fatalf("provider requests = %#v/%#v, want none", provider.findRequests, provider.createRequests)
	}
}

func TestSyncServiceSyncAllScansPRBoundaryTasksAndContinuesAfterFailures(t *testing.T) {
	paths, alpha, beta := newSyncAllScanSources(t)
	alphaBackend := &fakeSyncBackend{tasks: syncAllScanAlphaTasks(t, paths, alpha)}
	scanErr := errors.New("bd unavailable")
	runStore := &fakeSyncRunStore{}
	provider := newSyncAllScanPRProvider()
	service := workflow.SyncService{
		Paths:   paths,
		Sources: []task.RepositorySource{alpha, beta},
		BackendFactory: func(source task.RepositorySource) (task.SyncBackend, error) {
			if source.Repository.ID == "beta" {
				return nil, scanErr
			}
			return alphaBackend, nil
		},
		RunStore:   runStore,
		PRProvider: provider,
	}

	result, err := service.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("sync all: %v", err)
	}

	if len(result.Results) != 2 {
		t.Fatalf("results = %#v, want open/merged only", result.Results)
	}
	statuses := syncAllStatusesByTask(result.Results)
	if statuses["a-review"] != workflow.SyncStatusAlreadyInReview ||
		statuses["a-merged"] != workflow.SyncStatusPRMerged {
		t.Fatalf("statuses = %#v, want open/merged", statuses)
	}
	if len(result.Failures) != 2 {
		t.Fatalf("failures = %#v, want closed PR and beta scan failures", result.Failures)
	}
	if len(provider.createRequests) != 0 {
		t.Fatalf("create requests = %#v, want none", provider.createRequests)
	}
	if len(provider.statusRequests) != 3 {
		t.Fatalf("status requests = %#v, want only existing PR candidates", provider.statusRequests)
	}
	for _, req := range provider.statusRequests {
		switch req.URL {
		case "https://github.test/org/alpha/pull/10",
			"https://github.test/org/alpha/pull/11",
			"https://github.test/org/alpha/pull/12":
		default:
			t.Fatalf("unexpected status request = %#v", req)
		}
	}
	if len(alphaBackend.closed) != 1 || alphaBackend.closed[0] != "a-merged" {
		t.Fatalf("closed = %#v, want merged task closed", alphaBackend.closed)
	}
	if len(alphaBackend.setPRURLs) != 0 {
		t.Fatalf("set PR URLs = %#v, want none", alphaBackend.setPRURLs)
	}
}

func newSyncAllScanSources(t *testing.T) (state.Paths, task.RepositorySource, task.RepositorySource) {
	t.Helper()
	root := t.TempDir()
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("create paths: %v", err)
	}
	alphaPath := filepath.Join(root, "alpha")
	betaPath := filepath.Join(root, "beta")
	alpha := task.RepositorySource{
		Repository: task.Repository{
			ID:            "alpha",
			Name:          "Alpha",
			TaskIDPrefix:  "a",
			Path:          alphaPath,
			DefaultBranch: "main",
		},
		BackendDir: alphaPath,
	}
	beta := task.RepositorySource{
		Repository: task.Repository{
			ID:            "beta",
			Name:          "Beta",
			TaskIDPrefix:  "b",
			Path:          betaPath,
			DefaultBranch: "main",
		},
		BackendDir: betaPath,
	}
	return paths, alpha, beta
}

func syncAllScanAlphaTasks(t *testing.T, paths state.Paths, alpha task.RepositorySource) []task.Task {
	t.Helper()
	createTargets := mustSyncExpectedTargets(t, alpha.Repository, "a-create", paths)
	mainTargets := mustSyncExpectedTargets(t, alpha.Repository, "a-main", paths)
	return []task.Task{
		syncAllMetadataTask("a-create", task.StatusInProgress, task.IssueTypeTask, task.Metadata{
			task.MetadataBranch:   createTargets.WorktreeTeam.Branch,
			task.MetadataWorktree: createTargets.WorktreeTeam.Worktree,
		}),
		syncAllPRTask("a-review", task.StatusInProgress, task.IssueTypeTask, "https://github.test/org/alpha/pull/10"),
		syncAllPRTask("a-merged", task.StatusInProgress, task.IssueTypeBug, "https://github.test/org/alpha/pull/11"),
		syncAllPRTask("a-closed-pr", task.StatusInProgress, task.IssueTypeChore, "https://github.test/org/alpha/pull/12"),
		syncAllMetadataTask("a-main", task.StatusInProgress, task.IssueTypeTask, task.Metadata{
			task.MetadataBranch:   mainTargets.MainSolo.Branch,
			task.MetadataWorktree: mainTargets.MainSolo.Worktree,
		}),
		{ID: "a-ready", Status: task.StatusOpen, IssueType: task.IssueTypeTask},
		syncAllPRTask("a-epic-review", task.StatusInProgress, task.IssueTypeEpic, "https://github.test/org/alpha/pull/13"),
		syncAllPRTask("a-closed", task.StatusClosed, task.IssueTypeTask, "https://github.test/org/alpha/pull/14"),
	}
}

func mustSyncExpectedTargets(
	t *testing.T,
	repo task.Repository,
	taskID string,
	paths state.Paths,
) workflow.ExpectedTargets {
	t.Helper()
	targets, err := workflow.ExpectedTargetsForTask(repo, taskID, paths)
	if err != nil {
		t.Fatalf("expected targets: %v", err)
	}
	return targets
}

func syncAllPRTask(id string, status task.Status, issueType task.IssueType, prURL string) task.Task {
	return syncAllMetadataTask(id, status, issueType, task.Metadata{task.MetadataPRURL: prURL})
}

func syncAllMetadataTask(id string, status task.Status, issueType task.IssueType, metadata task.Metadata) task.Task {
	return task.Task{ID: id, Status: status, IssueType: issueType, Metadata: metadata}
}

func newSyncAllScanPRProvider() *fakePRProvider {
	return &fakePRProvider{
		created: pullrequest.PullRequest{URL: "https://github.test/org/alpha/pull/99"},
		statusByURL: map[string]pullrequest.PullRequestStatus{
			"https://github.test/org/alpha/pull/10": {
				URL:   "https://github.test/org/alpha/pull/10",
				State: pullrequest.StateOpen,
			},
			"https://github.test/org/alpha/pull/11": {
				URL:   "https://github.test/org/alpha/pull/11",
				State: pullrequest.StateMerged,
			},
			"https://github.test/org/alpha/pull/12": {
				URL:   "https://github.test/org/alpha/pull/12",
				State: pullrequest.StateClosed,
			},
		},
	}
}

func TestSyncServiceSyncAllIgnoresTasksWithoutPRURL(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	worktreePath := targets.WorktreeTeam.Worktree
	listTask := task.Task{
		ID:        "op-1",
		Status:    task.StatusInProgress,
		IssueType: task.IssueTypeTask,
		Metadata: task.Metadata{
			task.MetadataBranch:   "orpheus/op-1",
			task.MetadataWorktree: worktreePath,
		},
	}
	backend := &fakeSyncBackend{
		tasks:    []task.Task{listTask},
		getTasks: []task.Task{withSyncStatus(listTask, task.StatusClosed)},
	}
	service := workflow.SyncService{
		Paths:   paths,
		Sources: []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.SyncBackend, error) {
			return backend, nil
		},
		RunStore:   &fakeSyncRunStore{},
		PRProvider: &fakePRProvider{created: pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/42"}},
	}

	result, err := service.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("sync all: %v", err)
	}
	if len(result.Failures) != 0 {
		t.Fatalf("failures = %#v, want none", result.Failures)
	}
	if len(result.Results) != 0 {
		t.Fatalf("results = %#v, want no candidates without PR URL", result.Results)
	}
}

func TestSyncServiceSyncAllHoldsGlobalLockAcrossScanAndSync(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, _ := newSyncTestSource(t, repoPath, "op-1")
	lockPath, err := paths.GlobalMutationLockPath()
	if err != nil {
		t.Fatalf("lock path: %v", err)
	}
	assertLockHeld := func(phase string) {
		if _, err := os.Stat(lockPath); err != nil {
			t.Fatalf("%s: expected global mutation lock at %s: %v", phase, lockPath, err)
		}
	}

	backend := &fakeSyncBackend{
		tasks: []task.Task{{
			ID:        "op-1",
			Status:    task.StatusInProgress,
			IssueType: task.IssueTypeTask,
			Metadata: task.Metadata{
				task.MetadataPRURL: "https://github.test/org/repo/pull/42",
			},
		}},
		onList: func() { assertLockHeld("scan") },
		onGet:  func() { assertLockHeld("sync") },
	}
	service := workflow.SyncService{
		Paths:   paths,
		Sources: []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.SyncBackend, error) {
			return backend, nil
		},
		RunStore: &fakeSyncRunStore{},
		PRProvider: &fakePRProvider{status: pullrequest.PullRequestStatus{
			URL:   "https://github.test/org/repo/pull/42",
			State: pullrequest.StateOpen,
		}},
	}

	result, err := service.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("sync all: %v", err)
	}
	if len(result.Results) != 1 || result.Results[0].Status != workflow.SyncStatusAlreadyInReview {
		t.Fatalf("results = %#v, want one polled PR", result.Results)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock after sync all: %v, want removed", err)
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

func TestBuildPublicationPullRequestContentUsesTitleTemplate(t *testing.T) {
	content, err := workflow.BuildPublicationPullRequestContent("[{{external_ref}}] {{summary}}", task.Task{
		ID:          "op-1",
		ExternalRef: "\n TREX-1234\t",
	}, taskstate.RunAttempt{
		Completion: &taskstate.Completion{
			Summary:             "Ready for review",
			DetailedDescription: "Detailed PR body.",
		},
	})
	if err != nil {
		t.Fatalf("build content: %v", err)
	}
	if content.Title != "[TREX-1234] Ready for review" {
		t.Fatalf("title = %q, want rendered title", content.Title)
	}
	if content.Body != "Detailed PR body." {
		t.Fatalf("body = %q, want detailed description unchanged", content.Body)
	}
}

func TestBuildPublicationPullRequestContentRejectsMissingRequiredExternalReference(t *testing.T) {
	_, err := workflow.BuildPublicationPullRequestContent("[{{external_ref}}] {{summary}}", task.Task{
		ID: "op-1",
	}, taskstate.RunAttempt{
		Completion: &taskstate.Completion{
			Summary:             "Ready for review",
			DetailedDescription: "Detailed PR body.",
		},
	})

	if err == nil || !strings.Contains(err.Error(), "requires a task external reference") {
		t.Fatalf("error = %v, want missing external reference error", err)
	}
}

func newSyncTestService(
	t *testing.T,
	taskItem task.Task,
	taskState taskstate.TaskState,
	paths state.Paths,
	source task.RepositorySource,
) (workflow.SyncService, *fakePRProvider, *fakeSyncBackend) {
	t.Helper()
	provider := &fakePRProvider{created: pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/42"}}
	backend := &fakeSyncBackend{tasks: []task.Task{taskItem}}
	service := workflow.SyncService{
		Paths:   paths,
		Sources: []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.SyncBackend, error) {
			return backend, nil
		},
		RunStore:   &fakeSyncRunStore{},
		PRProvider: provider,
	}
	return service, provider, backend
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

func syncAllStatusesByTask(results []workflow.SyncResult) map[string]workflow.SyncStatus {
	statuses := make(map[string]workflow.SyncStatus, len(results))
	for _, result := range results {
		statuses[result.Task.ID] = result.Status
	}
	return statuses
}

func withSyncStatus(taskItem task.Task, status task.Status) task.Task {
	taskItem.Status = status
	return taskItem
}
