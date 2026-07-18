package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/workflow"
)

type fakeSyncRunStore struct {
	events           []fakeTaskClosedEvent
	conflictEvents   []fakeConflictEvent
	recordErr        error
	conflictEventErr error
	recordedAt       time.Time
}

type fakeSyncGit struct {
	requests         []gitmeta.TaskBranchSyncOptions
	beginRequests    []gitmeta.TaskBranchSyncOptions
	completeRequests []gitmeta.TaskBranchSyncOptions
	completeFiles    [][]string
	result           gitmeta.TaskBranchSyncResult
	beginResult      gitmeta.TaskBranchSyncResult
	completeResult   gitmeta.TaskBranchSyncResult
	resultsByBranch  map[string]gitmeta.TaskBranchSyncResult
	err              error
	beginErr         error
	completeErr      error
	errsByBranch     map[string]error
}

type fakeSyncConflictResolver struct {
	requests       []workflow.SyncConflictResolutionOptions
	execution      taskstate.AgentExecution
	usage          taskstate.RecordRunUsageOptions
	usageExecution taskstate.AgentExecution
	usageErr       error
	err            error
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

type fakeTaskClosedEvent struct {
	repoID string
	taskID string
	opts   taskstate.TaskClosedOptions
}

type fakeConflictEvent struct {
	eventType taskstate.EventType
	repoID    string
	taskID    string
	opts      taskstate.SyncConflictResolutionEventOptions
	err       error
}

func (g *fakeSyncGit) SyncTaskBranchWithDefault(
	_ context.Context,
	opts gitmeta.TaskBranchSyncOptions,
) (gitmeta.TaskBranchSyncResult, error) {
	g.requests = append(g.requests, opts)
	if err, ok := g.errsByBranch[opts.Branch]; ok {
		return gitmeta.TaskBranchSyncResult{}, err
	}
	if g.err != nil {
		return gitmeta.TaskBranchSyncResult{}, g.err
	}
	if result, ok := g.resultsByBranch[opts.Branch]; ok {
		return result, nil
	}
	result := g.result
	if result.Status == "" {
		result.Status = gitmeta.TaskBranchSyncAlreadyCurrent
	}
	return result, nil
}

func (g *fakeSyncGit) BeginTaskBranchConflictResolution(
	_ context.Context,
	opts gitmeta.TaskBranchSyncOptions,
) (gitmeta.TaskBranchSyncResult, error) {
	g.beginRequests = append(g.beginRequests, opts)
	if g.beginErr != nil {
		return gitmeta.TaskBranchSyncResult{}, g.beginErr
	}
	result := g.beginResult
	if result.Status == "" {
		result.Status = gitmeta.TaskBranchSyncConflicted
	}
	return result, nil
}

func (g *fakeSyncGit) CompleteTaskBranchConflictResolution(
	_ context.Context,
	opts gitmeta.TaskBranchSyncOptions,
	conflictFiles []string,
) (gitmeta.TaskBranchSyncResult, error) {
	g.completeRequests = append(g.completeRequests, opts)
	g.completeFiles = append(g.completeFiles, append([]string{}, conflictFiles...))
	if g.completeErr != nil {
		return gitmeta.TaskBranchSyncResult{}, g.completeErr
	}
	result := g.completeResult
	if result.Status == "" {
		result.Status = gitmeta.TaskBranchSyncUpdated
	}
	return result, nil
}

func (r *fakeSyncConflictResolver) PrepareSyncConflictResolution(
	_ context.Context,
	opts workflow.SyncConflictResolutionOptions,
) (workflow.PreparedSyncConflictResolution, error) {
	r.requests = append(r.requests, opts)
	execution := r.execution
	if execution.Agent == "" {
		execution = taskstate.AgentExecution{
			Purpose:     taskstate.AgentExecutionPurposeSyncConflictResolution,
			Status:      taskstate.RunStatusRunning,
			Agent:       "codex",
			Profile:     "codex",
			Harness:     "codex",
			Model:       "gpt-5",
			Command:     "codex",
			Args:        []string{"exec"},
			SessionName: "sync-conflict-" + opts.Task.ID,
		}
	}
	return workflow.PreparedSyncConflictResolution{
		Execution: execution,
		Resolve: func(context.Context) error {
			return r.err
		},
		CaptureUsage: func(execution taskstate.AgentExecution, err error) taskstate.RecordRunUsageOptions {
			r.usageExecution = execution
			r.usageErr = err
			return r.usage
		},
	}, nil
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

func (s *fakeSyncRunStore) RecordTaskClosed(repoID, taskID string, opts taskstate.TaskClosedOptions) (taskstate.Event, error) {
	s.events = append(s.events, fakeTaskClosedEvent{repoID: repoID, taskID: taskID, opts: opts})
	if s.recordErr != nil {
		return taskstate.Event{}, s.recordErr
	}
	at := s.recordedAt
	if at.IsZero() {
		at = time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)
	}
	return taskstate.Event{
		Type:            taskstate.EventTaskClosed,
		At:              at,
		CloseReason:     opts.Reason,
		PRURL:           opts.PRURL,
		ObservedPRState: opts.ObservedPRState,
	}, nil
}

func (s *fakeSyncRunStore) RecordSyncConflictResolutionStarted(
	repoID,
	taskID string,
	opts taskstate.SyncConflictResolutionEventOptions,
) (taskstate.Event, error) {
	return s.recordConflictEvent(taskstate.EventSyncConflictStarted, repoID, taskID, opts, nil)
}

func (s *fakeSyncRunStore) RecordSyncConflictResolutionFinished(
	repoID,
	taskID string,
	opts taskstate.SyncConflictResolutionEventOptions,
) (taskstate.Event, error) {
	return s.recordConflictEvent(taskstate.EventSyncConflictFinished, repoID, taskID, opts, nil)
}

func (s *fakeSyncRunStore) RecordSyncConflictResolutionFailed(
	repoID,
	taskID string,
	opts taskstate.SyncConflictResolutionEventOptions,
	err error,
) (taskstate.Event, error) {
	return s.recordConflictEvent(taskstate.EventSyncConflictFailed, repoID, taskID, opts, err)
}

func (s *fakeSyncRunStore) recordConflictEvent(
	eventType taskstate.EventType,
	repoID string,
	taskID string,
	opts taskstate.SyncConflictResolutionEventOptions,
	err error,
) (taskstate.Event, error) {
	s.conflictEvents = append(s.conflictEvents, fakeConflictEvent{
		eventType: eventType,
		repoID:    repoID,
		taskID:    taskID,
		opts:      opts,
		err:       err,
	})
	if s.conflictEventErr != nil {
		return taskstate.Event{}, s.conflictEventErr
	}
	return taskstate.Event{Type: eventType}, nil
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
		Attempt: 1,
		Status:  taskstate.RunStatusSucceeded,
		Execution: syncRunExecution(
			taskstate.RunStatusSucceeded,
			time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC),
		),
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
		Attempt: 1,
		Status:  taskstate.RunStatusSucceeded,
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

func TestSyncServiceUpdatesOpenPRBranchFromDefault(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   targets.WorktreeTeam.Branch,
			task.MetadataWorktree: targets.WorktreeTeam.Worktree,
			task.MetadataPRURL:    "https://github.test/org/repo/pull/42",
		},
	}
	service, provider, backend := newSyncTestService(t, taskItem, taskstate.TaskState{}, paths, source)
	gitState := &fakeSyncGit{
		result: gitmeta.TaskBranchSyncResult{Status: gitmeta.TaskBranchSyncUpdated},
	}
	service.Git = gitState
	provider.status = pullrequest.PullRequestStatus{URL: "https://github.test/org/repo/pull/42", State: pullrequest.StateOpen}

	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != workflow.SyncStatusBranchUpdated ||
		!strings.Contains(result.Reason, "merged main into "+targets.WorktreeTeam.Branch) {
		t.Fatalf("result = %#v, want branch updated result", result)
	}
	if len(gitState.requests) != 1 {
		t.Fatalf("git requests = %#v, want one branch sync", gitState.requests)
	}
	req := gitState.requests[0]
	if req.RepoPath != repoPath ||
		req.DefaultBranch != "main" ||
		req.Branch != targets.WorktreeTeam.Branch ||
		req.Worktree != targets.WorktreeTeam.Worktree {
		t.Fatalf("git request = %#v, want repo/default/task branch metadata", req)
	}
	if len(backend.closed) != 0 || len(backend.setPRURLs) != 0 {
		t.Fatalf("backend closed=%#v set=%#v, want no backend mutation", backend.closed, backend.setPRURLs)
	}
}

func TestSyncServiceReportsOpenPRBranchAlreadyCurrent(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   targets.WorktreeTeam.Branch,
			task.MetadataWorktree: targets.WorktreeTeam.Worktree,
			task.MetadataPRURL:    "https://github.test/org/repo/pull/42",
		},
	}
	service, provider, _ := newSyncTestService(t, taskItem, taskstate.TaskState{}, paths, source)
	gitState := &fakeSyncGit{
		result: gitmeta.TaskBranchSyncResult{Status: gitmeta.TaskBranchSyncAlreadyCurrent},
	}
	service.Git = gitState
	provider.status = pullrequest.PullRequestStatus{URL: "https://github.test/org/repo/pull/42", State: pullrequest.StateOpen}

	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != workflow.SyncStatusAlreadyInReview ||
		!strings.Contains(result.Reason, "already includes main") {
		t.Fatalf("result = %#v, want already-current open PR branch", result)
	}
	if len(gitState.requests) != 1 {
		t.Fatalf("git requests = %#v, want one branch sync", gitState.requests)
	}
}

//nolint:funlen // The successful conflict-repair workflow is clearer as one integrated fixture.
func TestSyncServiceResolvesOpenPRBranchConflictWithAgent(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   targets.WorktreeTeam.Branch,
			task.MetadataWorktree: targets.WorktreeTeam.Worktree,
			task.MetadataPRURL:    "https://github.test/org/repo/pull/42",
		},
	}
	service, provider, _ := newSyncTestService(t, taskItem, taskstate.TaskState{}, paths, source)
	gitState := &fakeSyncGit{
		err: fmt.Errorf("%w: conflict.txt", gitmeta.ErrMergeConflict),
		beginResult: gitmeta.TaskBranchSyncResult{
			Status:        gitmeta.TaskBranchSyncConflicted,
			Branch:        targets.WorktreeTeam.Branch,
			DefaultBranch: "main",
			ConflictFiles: []string{"conflict.txt"},
		},
		completeResult: gitmeta.TaskBranchSyncResult{Status: gitmeta.TaskBranchSyncUpdated, Head: "merge123"},
	}
	resolver := &fakeSyncConflictResolver{
		usage: taskstate.RecordRunUsageOptions{
			Session: &taskstate.AgentSession{ID: "session-123"},
			Usage: &taskstate.AgentUsage{
				InputTokens:  100,
				OutputTokens: 50,
				TotalTokens:  150,
			},
			UsageCapture: taskstate.AgentUsageCapture{
				Status:         taskstate.UsageCaptureCaptured,
				Reason:         "matched_codex_session",
				CandidateCount: 1,
			},
			Model: "gpt-5",
		},
	}
	service.Git = gitState
	service.ConflictResolver = resolver
	provider.status = pullrequest.PullRequestStatus{URL: "https://github.test/org/repo/pull/42", State: pullrequest.StateOpen}
	runStore := service.RunStore.(*fakeSyncRunStore)

	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != workflow.SyncStatusBranchUpdated ||
		!strings.Contains(result.Reason, "resolved merge conflicts with the configured agent") {
		t.Fatalf("result = %#v, want conflict-resolved branch update", result)
	}
	if len(gitState.requests) != 1 ||
		len(gitState.beginRequests) != 1 ||
		len(gitState.completeRequests) != 1 {
		t.Fatalf(
			"git calls sync=%#v begin=%#v complete=%#v, want one call each",
			gitState.requests,
			gitState.beginRequests,
			gitState.completeRequests,
		)
	}
	if len(gitState.completeFiles) != 1 ||
		len(gitState.completeFiles[0]) != 1 ||
		gitState.completeFiles[0][0] != "conflict.txt" {
		t.Fatalf("complete files = %#v, want conflict file forwarded", gitState.completeFiles)
	}
	if len(resolver.requests) != 1 {
		t.Fatalf("resolver requests = %#v, want one conflict repair", resolver.requests)
	}
	assertSyncConflictResolverRequest(t, resolver.requests[0], targets.WorktreeTeam)
	if resolver.usageErr != nil ||
		resolver.usageExecution.Agent != "codex" ||
		resolver.usageExecution.SessionName != "sync-conflict-op-1" {
		t.Fatalf(
			"usage capture got execution=%#v err=%v, want successful conflict execution context",
			resolver.usageExecution,
			resolver.usageErr,
		)
	}
	if len(runStore.conflictEvents) != 2 ||
		runStore.conflictEvents[0].eventType != taskstate.EventSyncConflictStarted ||
		runStore.conflictEvents[1].eventType != taskstate.EventSyncConflictFinished {
		t.Fatalf("conflict events = %#v, want started and finished", runStore.conflictEvents)
	}
	assertSyncConflictAuditEvent(t, runStore.conflictEvents[0], taskstate.EventSyncConflictStarted, targets.WorktreeTeam)
	assertSyncConflictAuditEvent(t, runStore.conflictEvents[1], taskstate.EventSyncConflictFinished, targets.WorktreeTeam)
	assertSyncConflictUsage(t, runStore.conflictEvents[1])
	if runStore.conflictEvents[1].opts.Commit != "merge123" {
		t.Fatalf("finished commit = %q, want merge123", runStore.conflictEvents[1].opts.Commit)
	}
}

func assertSyncConflictResolverRequest(
	t *testing.T,
	req workflow.SyncConflictResolutionOptions,
	target tasktarget.Target,
) {
	t.Helper()

	if req.Repository.ID != "alpha" ||
		req.Task.ID != "op-1" ||
		req.Branch != target.Branch ||
		req.Worktree != target.Worktree ||
		req.DefaultBranch != "main" ||
		req.PRURL != "https://github.test/org/repo/pull/42" ||
		len(req.ConflictFiles) != 1 ||
		req.ConflictFiles[0] != "conflict.txt" {
		t.Fatalf("resolver request = %#v, want repo/task/target/conflict context", req)
	}
}

func assertSyncConflictUsage(t *testing.T, event fakeConflictEvent) {
	t.Helper()

	if event.opts.Usage.Session == nil ||
		event.opts.Usage.Session.ID != "session-123" ||
		event.opts.Usage.Usage == nil ||
		event.opts.Usage.Usage.TotalTokens != 150 ||
		event.opts.Usage.UsageCapture.Status != taskstate.UsageCaptureCaptured ||
		event.opts.Usage.Model != "gpt-5" {
		t.Fatalf("conflict usage = %#v, want captured session and token telemetry", event.opts.Usage)
	}
}

func assertSyncConflictAuditEvent(
	t *testing.T,
	event fakeConflictEvent,
	eventType taskstate.EventType,
	target tasktarget.Target,
) {
	t.Helper()

	if event.eventType != eventType ||
		event.repoID != "alpha" ||
		event.taskID != "op-1" ||
		event.opts.Execution.Agent != "codex" ||
		event.opts.Execution.SessionName != "sync-conflict-op-1" ||
		event.opts.Branch != target.Branch ||
		event.opts.DefaultBranch != "main" ||
		event.opts.Worktree != target.Worktree ||
		event.opts.PRURL != "https://github.test/org/repo/pull/42" ||
		len(event.opts.ConflictFiles) != 1 ||
		event.opts.ConflictFiles[0] != "conflict.txt" {
		t.Fatalf("conflict audit event = %#v, want persisted agent/branch/conflict facts", event)
	}
}

func TestSyncServiceReportsConflictAgentFailure(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   targets.WorktreeTeam.Branch,
			task.MetadataWorktree: targets.WorktreeTeam.Worktree,
			task.MetadataPRURL:    "https://github.test/org/repo/pull/42",
		},
	}
	service, provider, _ := newSyncTestService(t, taskItem, taskstate.TaskState{}, paths, source)
	agentErr := errors.New("agent exited 1")
	gitState := &fakeSyncGit{
		err: fmt.Errorf("%w: conflict.txt", gitmeta.ErrMergeConflict),
		beginResult: gitmeta.TaskBranchSyncResult{
			Status:        gitmeta.TaskBranchSyncConflicted,
			ConflictFiles: []string{"conflict.txt"},
		},
	}
	resolver := &fakeSyncConflictResolver{
		err: agentErr,
		usage: taskstate.RecordRunUsageOptions{
			UsageCapture: taskstate.AgentUsageCapture{
				Status: taskstate.UsageCaptureUnknown,
				Reason: "agent process failed before usage capture",
			},
		},
	}
	service.Git = gitState
	service.ConflictResolver = resolver
	provider.status = pullrequest.PullRequestStatus{URL: "https://github.test/org/repo/pull/42", State: pullrequest.StateOpen}
	runStore := service.RunStore.(*fakeSyncRunStore)

	_, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if !errors.Is(err, agentErr) || !strings.Contains(err.Error(), "resolve merge conflicts for task op-1 with agent") {
		t.Fatalf("error = %v, want agent failure with context", err)
	}
	if len(gitState.completeRequests) != 0 {
		t.Fatalf("complete requests = %#v, want none after agent failure", gitState.completeRequests)
	}
	if len(runStore.conflictEvents) != 2 ||
		runStore.conflictEvents[0].eventType != taskstate.EventSyncConflictStarted ||
		runStore.conflictEvents[1].eventType != taskstate.EventSyncConflictFailed ||
		!errors.Is(runStore.conflictEvents[1].err, agentErr) {
		t.Fatalf("conflict events = %#v, want started and failed with agent error", runStore.conflictEvents)
	}
	if !errors.Is(resolver.usageErr, agentErr) {
		t.Fatalf("usage capture error = %v, want agent error", resolver.usageErr)
	}
	if runStore.conflictEvents[1].opts.Usage.Usage != nil ||
		runStore.conflictEvents[1].opts.Usage.UsageCapture.Status != taskstate.UsageCaptureUnknown {
		t.Fatalf("failed usage = %#v, want unknown capture without usage tokens", runStore.conflictEvents[1].opts.Usage)
	}
}

func TestSyncServiceReportsUnresolvedConflictAfterAgent(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   targets.WorktreeTeam.Branch,
			task.MetadataWorktree: targets.WorktreeTeam.Worktree,
			task.MetadataPRURL:    "https://github.test/org/repo/pull/42",
		},
	}
	service, provider, _ := newSyncTestService(t, taskItem, taskstate.TaskState{}, paths, source)
	completeErr := errors.New("unresolved merge conflicts remain: conflict.txt")
	gitState := &fakeSyncGit{
		err: fmt.Errorf("%w: conflict.txt", gitmeta.ErrMergeConflict),
		beginResult: gitmeta.TaskBranchSyncResult{
			Status:        gitmeta.TaskBranchSyncConflicted,
			ConflictFiles: []string{"conflict.txt"},
		},
		completeErr: completeErr,
	}
	service.Git = gitState
	service.ConflictResolver = &fakeSyncConflictResolver{}
	provider.status = pullrequest.PullRequestStatus{URL: "https://github.test/org/repo/pull/42", State: pullrequest.StateOpen}
	runStore := service.RunStore.(*fakeSyncRunStore)

	_, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if !errors.Is(err, completeErr) || !strings.Contains(err.Error(), "complete resolved merge for task op-1") {
		t.Fatalf("error = %v, want unresolved conflict completion failure", err)
	}
	if len(runStore.conflictEvents) != 2 ||
		runStore.conflictEvents[0].eventType != taskstate.EventSyncConflictStarted ||
		runStore.conflictEvents[1].eventType != taskstate.EventSyncConflictFailed ||
		!errors.Is(runStore.conflictEvents[1].err, completeErr) {
		t.Fatalf("conflict events = %#v, want started and failed with completion error", runStore.conflictEvents)
	}
}

func TestSyncServiceSkipsOpenPRBranchUpdateWithoutManagedMetadata(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "repo")
	paths, source, _ := newSyncTestSource(t, repoPath, "op-1")
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataPRURL: "https://github.test/org/repo/pull/42",
		},
	}
	service, provider, _ := newSyncTestService(t, taskItem, taskstate.TaskState{}, paths, source)
	gitState := &fakeSyncGit{
		err: errors.New("git should not be called"),
	}
	service.Git = gitState
	provider.status = pullrequest.PullRequestStatus{URL: "https://github.test/org/repo/pull/42", State: pullrequest.StateOpen}

	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != workflow.SyncStatusAlreadyInReview ||
		!strings.Contains(result.Reason, "branch update skipped") {
		t.Fatalf("result = %#v, want open PR with branch update skipped", result)
	}
	if len(gitState.requests) != 0 {
		t.Fatalf("git requests = %#v, want none", gitState.requests)
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
		event.opts.Reason != taskstate.CloseReasonPRMerged ||
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
	targets tasktarget.ExpectedTargets,
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
	targets tasktarget.ExpectedTargets,
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
	run.Execution.StartedAt = time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC)
	return run
}

func syncCompletionRun(_ string, _ string) taskstate.RunAttempt {
	return taskstate.RunAttempt{
		Attempt: 2,
		Status:  taskstate.RunStatusSucceeded,
		Execution: syncRunExecution(
			taskstate.RunStatusSucceeded,
			time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC),
		),
		Completion: &taskstate.Completion{
			Summary:             "Done",
			Description:         "Done.",
			DetailedDescription: "Detailed PR body.",
		},
	}
}

func syncRunExecution(status taskstate.RunStatus, startedAt time.Time) taskstate.AgentExecution {
	finishedAt := startedAt.Add(time.Minute)
	return taskstate.AgentExecution{
		Purpose:        taskstate.AgentExecutionPurposeImplementation,
		Status:         status,
		Agent:          "recorder",
		Profile:        "recorder",
		StartedAt:      startedAt,
		FinishedAt:     &finishedAt,
		DurationMillis: finishedAt.Sub(startedAt).Milliseconds(),
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

func syncTaskForTarget(target tasktarget.Target) task.Task {
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
		Attempt: 1,
		Status:  taskstate.RunStatusSucceeded,
		Execution: syncRunExecution(
			taskstate.RunStatusSucceeded,
			time.Date(2026, 6, 8, 10, 0, 0, 0, time.UTC),
		),
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
		Attempt: 1,
		Status:  taskstate.RunStatusSucceeded,
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

//nolint:funlen // The batch continuation scenario is clearer as one integrated workflow fixture.
func TestSyncServiceSyncAllContinuesAfterOpenPRBranchUpdateFailure(t *testing.T) {
	paths, source, targets := newSyncTestSource(t, filepath.Join(t.TempDir(), "repo"), "op-fail")
	currentTargets := mustSyncExpectedTargets(t, source.Repository, "op-current", paths)
	backend := &fakeSyncBackend{
		tasks: []task.Task{
			{
				ID:        "op-fail",
				Status:    task.StatusInProgress,
				IssueType: task.IssueTypeTask,
				Metadata: task.Metadata{
					task.MetadataBranch:   targets.WorktreeTeam.Branch,
					task.MetadataWorktree: targets.WorktreeTeam.Worktree,
					task.MetadataPRURL:    "https://github.test/org/repo/pull/1",
				},
			},
			{
				ID:        "op-current",
				Status:    task.StatusInProgress,
				IssueType: task.IssueTypeTask,
				Metadata: task.Metadata{
					task.MetadataBranch:   currentTargets.WorktreeTeam.Branch,
					task.MetadataWorktree: currentTargets.WorktreeTeam.Worktree,
					task.MetadataPRURL:    "https://github.test/org/repo/pull/2",
				},
			},
		},
	}
	provider := &fakePRProvider{
		statusByURL: map[string]pullrequest.PullRequestStatus{
			"https://github.test/org/repo/pull/1": {
				URL:   "https://github.test/org/repo/pull/1",
				State: pullrequest.StateOpen,
			},
			"https://github.test/org/repo/pull/2": {
				URL:   "https://github.test/org/repo/pull/2",
				State: pullrequest.StateOpen,
			},
		},
	}
	gitErr := errors.New("merge conflict")
	gitState := &fakeSyncGit{
		errsByBranch: map[string]error{
			targets.WorktreeTeam.Branch: gitErr,
		},
		resultsByBranch: map[string]gitmeta.TaskBranchSyncResult{
			currentTargets.WorktreeTeam.Branch: {Status: gitmeta.TaskBranchSyncAlreadyCurrent},
		},
	}
	service := workflow.SyncService{
		Paths:   paths,
		Sources: []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.SyncBackend, error) {
			return backend, nil
		},
		RunStore:   &fakeSyncRunStore{},
		Git:        gitState,
		PRProvider: provider,
	}

	result, err := service.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("sync all: %v", err)
	}
	if len(result.Results) != 1 ||
		result.Results[0].Task.ID != "op-current" ||
		result.Results[0].Status != workflow.SyncStatusAlreadyInReview {
		t.Fatalf("results = %#v, want op-current already in review", result.Results)
	}
	if len(result.Failures) != 1 ||
		result.Failures[0].TaskID != "op-fail" ||
		!errors.Is(result.Failures[0].Err, gitErr) {
		t.Fatalf("failures = %#v, want op-fail git failure", result.Failures)
	}
	if len(gitState.requests) != 2 {
		t.Fatalf("git requests = %#v, want both open PR candidates attempted", gitState.requests)
	}
}

//nolint:funlen // The batch conflict-repair path needs repository, PR, Git, and telemetry fixtures together.
func TestSyncServiceSyncAllRecordsConflictResolutionTelemetry(t *testing.T) {
	paths, source, targets := newSyncTestSource(t, filepath.Join(t.TempDir(), "repo"), "op-conflict")
	backend := &fakeSyncBackend{
		tasks: []task.Task{{
			ID:        "op-conflict",
			Status:    task.StatusInProgress,
			IssueType: task.IssueTypeTask,
			Metadata: task.Metadata{
				task.MetadataBranch:   targets.WorktreeTeam.Branch,
				task.MetadataWorktree: targets.WorktreeTeam.Worktree,
				task.MetadataPRURL:    "https://github.test/org/repo/pull/1",
			},
		}},
	}
	provider := &fakePRProvider{
		statusByURL: map[string]pullrequest.PullRequestStatus{
			"https://github.test/org/repo/pull/1": {
				URL:   "https://github.test/org/repo/pull/1",
				State: pullrequest.StateOpen,
			},
		},
	}
	gitState := &fakeSyncGit{
		err: fmt.Errorf("%w: conflict.txt", gitmeta.ErrMergeConflict),
		beginResult: gitmeta.TaskBranchSyncResult{
			Status:        gitmeta.TaskBranchSyncConflicted,
			ConflictFiles: []string{"conflict.txt"},
		},
		completeResult: gitmeta.TaskBranchSyncResult{
			Status: gitmeta.TaskBranchSyncUpdated,
			Head:   "merge456",
		},
	}
	resolver := &fakeSyncConflictResolver{
		usage: taskstate.RecordRunUsageOptions{
			Session: &taskstate.AgentSession{ID: "session-456"},
			Usage: &taskstate.AgentUsage{
				InputTokens:  200,
				OutputTokens: 75,
				TotalTokens:  275,
			},
			UsageCapture: taskstate.AgentUsageCapture{
				Status:         taskstate.UsageCaptureCaptured,
				Reason:         "matched_codex_session",
				CandidateCount: 1,
			},
			Model: "gpt-5",
		},
	}
	runStore := &fakeSyncRunStore{}
	service := workflow.SyncService{
		Paths:   paths,
		Sources: []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.SyncBackend, error) {
			return backend, nil
		},
		RunStore:         runStore,
		Git:              gitState,
		ConflictResolver: resolver,
		PRProvider:       provider,
	}

	result, err := service.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("sync all: %v", err)
	}
	if len(result.Results) != 1 ||
		result.Results[0].Task.ID != "op-conflict" ||
		result.Results[0].Status != workflow.SyncStatusBranchUpdated {
		t.Fatalf("results = %#v, want conflict-resolved branch update", result.Results)
	}
	if len(result.Failures) != 0 {
		t.Fatalf("failures = %#v, want none", result.Failures)
	}
	if len(runStore.conflictEvents) != 2 ||
		runStore.conflictEvents[0].eventType != taskstate.EventSyncConflictStarted ||
		runStore.conflictEvents[1].eventType != taskstate.EventSyncConflictFinished {
		t.Fatalf("conflict events = %#v, want started and finished", runStore.conflictEvents)
	}
	if runStore.conflictEvents[1].opts.Commit != "merge456" {
		t.Fatalf("finished commit = %q, want merge456", runStore.conflictEvents[1].opts.Commit)
	}
	if runStore.conflictEvents[1].opts.Usage.Usage == nil ||
		runStore.conflictEvents[1].opts.Usage.Usage.TotalTokens != 275 ||
		runStore.conflictEvents[1].opts.Usage.Session == nil ||
		runStore.conflictEvents[1].opts.Usage.Session.ID != "session-456" {
		t.Fatalf("finished usage = %#v, want sync-all conflict telemetry", runStore.conflictEvents[1].opts.Usage)
	}
}

//nolint:funlen // The batch conflict-agent failure is clearer as one integrated fixture.
func TestSyncServiceSyncAllContinuesAfterConflictAgentFailure(t *testing.T) {
	paths, source, targets := newSyncTestSource(t, filepath.Join(t.TempDir(), "repo"), "op-conflict")
	currentTargets := mustSyncExpectedTargets(t, source.Repository, "op-current", paths)
	backend := &fakeSyncBackend{
		tasks: []task.Task{
			{
				ID:        "op-conflict",
				Status:    task.StatusInProgress,
				IssueType: task.IssueTypeTask,
				Metadata: task.Metadata{
					task.MetadataBranch:   targets.WorktreeTeam.Branch,
					task.MetadataWorktree: targets.WorktreeTeam.Worktree,
					task.MetadataPRURL:    "https://github.test/org/repo/pull/1",
				},
			},
			{
				ID:        "op-current",
				Status:    task.StatusInProgress,
				IssueType: task.IssueTypeTask,
				Metadata: task.Metadata{
					task.MetadataBranch:   currentTargets.WorktreeTeam.Branch,
					task.MetadataWorktree: currentTargets.WorktreeTeam.Worktree,
					task.MetadataPRURL:    "https://github.test/org/repo/pull/2",
				},
			},
		},
	}
	provider := &fakePRProvider{
		statusByURL: map[string]pullrequest.PullRequestStatus{
			"https://github.test/org/repo/pull/1": {
				URL:   "https://github.test/org/repo/pull/1",
				State: pullrequest.StateOpen,
			},
			"https://github.test/org/repo/pull/2": {
				URL:   "https://github.test/org/repo/pull/2",
				State: pullrequest.StateOpen,
			},
		},
	}
	gitState := &fakeSyncGit{
		errsByBranch: map[string]error{
			targets.WorktreeTeam.Branch: fmt.Errorf("%w: conflict.txt", gitmeta.ErrMergeConflict),
		},
		beginResult: gitmeta.TaskBranchSyncResult{
			Status:        gitmeta.TaskBranchSyncConflicted,
			ConflictFiles: []string{"conflict.txt"},
		},
		resultsByBranch: map[string]gitmeta.TaskBranchSyncResult{
			currentTargets.WorktreeTeam.Branch: {Status: gitmeta.TaskBranchSyncAlreadyCurrent},
		},
	}
	agentErr := errors.New("agent exited 1")
	service := workflow.SyncService{
		Paths:   paths,
		Sources: []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.SyncBackend, error) {
			return backend, nil
		},
		RunStore:         &fakeSyncRunStore{},
		Git:              gitState,
		ConflictResolver: &fakeSyncConflictResolver{err: agentErr},
		PRProvider:       provider,
	}

	result, err := service.SyncAll(context.Background())
	if err != nil {
		t.Fatalf("sync all: %v", err)
	}
	if len(result.Results) != 1 ||
		result.Results[0].Task.ID != "op-current" ||
		result.Results[0].Status != workflow.SyncStatusAlreadyInReview {
		t.Fatalf("results = %#v, want op-current already in review", result.Results)
	}
	if len(result.Failures) != 1 ||
		result.Failures[0].TaskID != "op-conflict" ||
		!errors.Is(result.Failures[0].Err, agentErr) {
		t.Fatalf("failures = %#v, want op-conflict agent failure", result.Failures)
	}
	if len(gitState.completeRequests) != 0 {
		t.Fatalf("complete requests = %#v, want none after agent failure", gitState.completeRequests)
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
) tasktarget.ExpectedTargets {
	t.Helper()
	targets, err := tasktarget.ExpectedTargetsForTask(repo, taskID, paths)
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

//nolint:funlen // The review process fixture is easier to verify as one content example.
func TestBuildPublicationPullRequestContentFromStateFormatsReviewProcess(t *testing.T) {
	finishedAt := time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC)
	content, err := workflow.BuildPublicationPullRequestContentFromState("", task.Task{
		ID: "op-1",
	}, taskstate.TaskState{
		Runs: []taskstate.RunAttempt{
			{
				Attempt: 1,
				Completion: &taskstate.Completion{
					Summary:             "Original summary",
					DetailedDescription: "Original PR body.",
				},
			},
			{
				Attempt: 2,
				Completion: &taskstate.Completion{
					Summary:             "Fix review findings",
					Description:         "Addressed review feedback.",
					DetailedDescription: "Fix run detailed body must be omitted.",
				},
				ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1},
			},
		},
		Reviews: []taskstate.ReviewAttempt{
			{
				Attempt:    1,
				Status:     taskstate.ReviewStatusPassed,
				Pipeline:   "default",
				Step:       "manual-review",
				StartedAt:  time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
				FinishedAt: &finishedAt,
				Steps: []taskstate.ReviewStep{
					{Kind: "manual", Name: "manual-review"},
				},
				Findings: []taskstate.ReviewFinding{
					{
						Type:                 taskstate.FindingTypeBlocking,
						Title:                "Fixed blocker",
						Description:          "Finding details must be omitted.",
						Step:                 "manual-review",
						TargetedByRunAttempt: 2,
					},
					{
						Type:   taskstate.FindingTypeBlocking,
						Title:  "Waived blocker",
						Step:   "manual-review",
						Waiver: "Accepted for this task.",
					},
					{
						Type:  taskstate.FindingTypeBlocking,
						Title: "Unfixed blocker",
						Step:  "manual-review",
					},
					{
						Type:  taskstate.FindingTypeAdvisory,
						Title: "Advisory note",
						Step:  "manual-review",
					},
					{
						Type:          taskstate.FindingTypeSeparateTask,
						Title:         "Future cleanup",
						Step:          "manual-review",
						CreatedTaskID: "op-2",
					},
				},
			},
			{
				Attempt:    2,
				Status:     taskstate.ReviewStatusFailed,
				Pipeline:   "default",
				Step:       "lint",
				StartedAt:  time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
				FinishedAt: &finishedAt,
				Steps: []taskstate.ReviewStep{
					{Kind: "check", Name: "lint"},
				},
			},
			{
				Attempt:    3,
				Status:     taskstate.ReviewStatusPassed,
				Pipeline:   "default",
				Step:       "agent-review",
				StartedAt:  time.Date(2026, 6, 10, 13, 0, 0, 0, time.UTC),
				FinishedAt: &finishedAt,
				Steps: []taskstate.ReviewStep{
					{Kind: "agent_review", Name: "agent-review"},
				},
				Findings: []taskstate.ReviewFinding{
					{
						Type:                 taskstate.FindingTypeBlocking,
						Title:                "Resolved blocker",
						Step:                 "agent-review",
						TargetedByRunAttempt: 2,
					},
					{
						Type:            taskstate.FindingTypeAdvisory,
						Title:           "Downgraded blocker",
						Step:            "agent-review",
						DowngradeReason: "False positive for this task.",
					},
					{
						Type:   taskstate.FindingTypeBlocking,
						Title:  "Waived automated blocker",
						Step:   "agent-review",
						Waiver: "Accepted for this task.",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build content: %v", err)
	}
	if content.Title != "Original summary" || !strings.HasPrefix(content.Body, "Original PR body.") {
		t.Fatalf("content = %#v, want original completion first", content)
	}
	for _, want := range []string{
		"### Review attempt 1 — passed",
		"- ❌ `manual-review`",
		"  - **Blocking:** Fixed blocker",
		"    - Fixed by run attempt 2",
		"  - **Blocking (waived):** Waived blocker",
		"    - Waived.",
		"  - **Blocking:** Unfixed blocker",
		"    - No targeted fix run recorded.",
		"  - **Advisory:** Advisory note",
		"  - **Separate task:** Future cleanup",
		"    - Created task: op-2",
		"  **Fix run attempt 2**",
		"  - Summary: `Fix review findings`",
		"  - Description: Addressed review feedback.",
		"### Review attempt 2 — failed",
		"- ⚠️ `lint`",
		"### Review attempt 3 — passed",
		"- ✅ `agent-review`",
		"  - **Blocking:** Resolved blocker",
		"  - **Advisory (downgraded):** Downgraded blocker",
		"    - Downgraded to advisory.",
		"  - **Blocking (waived):** Waived automated blocker",
	} {
		if !strings.Contains(content.Body, want) {
			t.Fatalf("body missing %q:\n%s", want, content.Body)
		}
	}
	for _, unwanted := range []string{
		"Finding details must be omitted.",
		"Fix run detailed body must be omitted.",
		"Accepted for this task.",
	} {
		if strings.Contains(content.Body, unwanted) {
			t.Fatalf("body = %q, should not contain %q", content.Body, unwanted)
		}
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
		Git:        &fakeSyncGit{},
		PRProvider: provider,
	}
	return service, provider, backend
}

func newSyncTestSource(
	t *testing.T,
	repoPath string,
	taskID string,
) (state.Paths, task.RepositorySource, tasktarget.ExpectedTargets) {
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
	targets, err := tasktarget.ExpectedTargetsForTask(source.Repository, taskID, paths)
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
