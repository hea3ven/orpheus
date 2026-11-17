package workflow_test

import (
	"context"
	"errors"
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
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/require"
)

type fakeSyncRunStore struct {
	states map[string]taskstate.TaskState
}

type fakeSyncGit struct {
	requests     []gitmeta.TaskBranchSyncOptions
	result       gitmeta.TaskBranchSyncResult
	head, remote string
}

type fakeSyncBackend struct {
	tasks         []task.Task
	setPRURLs     []fakeSetPRURL
	closed        []string
	closeErr      error
	onList, onGet func()
}

type fakeSetPRURL struct{ taskID, prURL string }

func (g *fakeSyncGit) SyncTaskBranchWithDefault(_ context.Context, opts gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchSyncResult, error) {
	g.requests = append(g.requests, opts)
	result := g.result
	if result.Status == "" {
		result.Status = gitmeta.TaskBranchSyncAlreadyCurrent
	}
	return result, nil
}

func (g *fakeSyncGit) BeginTaskBranchConflictResolution(ctx context.Context, opts gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchSyncResult, error) {
	result, err := g.SyncTaskBranchWithDefault(ctx, opts)
	if result.Head == "" {
		result.Head = "fixture-output"
	}
	g.head = result.Head
	return result, err
}

func (*fakeSyncGit) CompleteTaskBranchConflictResolution(context.Context, gitmeta.TaskBranchSyncOptions, []string) (gitmeta.TaskBranchSyncResult, error) {
	return gitmeta.TaskBranchSyncResult{}, errors.New("unexpected conflict completion")
}

func (b *fakeSyncBackend) Get(_ context.Context, id string) (task.Task, error) {
	if b.onGet != nil {
		b.onGet()
	}
	for _, candidate := range b.tasks {
		if candidate.ID == id {
			return candidate.Clone(), nil
		}
	}
	return task.Task{}, task.ErrNotFound
}

func (b *fakeSyncBackend) List(context.Context) ([]task.Task, error) {
	if b.onList != nil {
		b.onList()
	}
	tasks := make([]task.Task, len(b.tasks))
	for i, candidate := range b.tasks {
		tasks[i] = candidate.Clone()
	}
	return tasks, nil
}

func (b *fakeSyncBackend) SetPRURL(_ context.Context, taskID, prURL string) error {
	b.setPRURLs = append(b.setPRURLs, fakeSetPRURL{taskID: taskID, prURL: prURL})
	return nil
}

func (b *fakeSyncBackend) Close(_ context.Context, id string) error {
	b.closed = append(b.closed, id)
	if b.closeErr != nil {
		return b.closeErr
	}
	for i := range b.tasks {
		if b.tasks[i].ID == id {
			b.tasks[i].Status = task.StatusClosed
			return nil
		}
	}
	return task.ErrNotFound
}

func (s *fakeSyncRunStore) Load(repoID, taskID string) (taskstate.TaskState, error) {
	if value, ok := s.states[repoID+"/"+taskID]; ok {
		return value, nil
	}
	return taskstate.TaskState{RepoID: repoID, TaskID: taskID}, nil
}

type fakePRProvider struct {
	findRequests   []pullrequest.FindByBranchRequest
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

func (p *fakePRProvider) FindByBranch(_ context.Context, req pullrequest.FindByBranchRequest) (pullrequest.PullRequest, bool, error) {
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

func TestSyncServiceSyncAllHoldsGlobalLockAcrossScanAndSync(t *testing.T) {
	repoPath := filepath.Join(testutil.CanonicalTempDir(t), "repo")
	paths, source, _ := newSyncTestSource(t, repoPath, "op-1")
	lockPath, err := paths.DataPath(filepath.Join("locks", "mutation.lock"))
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
	view := deliveredSyncView(t, repoPath, "orpheus/op-1")
	_, err = workstate.NewStore(paths).Save(*view.Work, 0)
	require.NoError(t, err)
	service := workflow.SyncService{
		Paths:   paths,
		Sources: []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.SyncBackend, error) {
			return backend, nil
		},
		RunStore: &fakeSyncRunStore{states: map[string]taskstate.TaskState{"alpha/op-1": view}},
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

func TestSyncServiceUpdatesOpenPRBranchFromRecordedDestination(t *testing.T) {
	repoPath := filepath.Join(testutil.CanonicalTempDir(t), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	taskItem := task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{
		task.MetadataBranch: targets.WorktreeTeam.Branch, task.MetadataWorktree: targets.WorktreeTeam.Worktree,
		task.MetadataPRURL: "https://github.test/org/repo/pull/42",
	}}
	service, provider, _ := newSyncTestService(t, taskItem, taskstate.TaskState{}, paths, source)
	snapshot, err := testutil.DeliveredPRWork("alpha", "op-1", targets.WorktreeTeam.Worktree, targets.WorktreeTeam.Branch, taskItem.Metadata[task.MetadataPRURL])
	require.NoError(t, err)
	w := &snapshot.Workflows[0]
	delivery := w.Phases[3].(*work.PublicationPhase).Delivery
	w.Phases = w.Phases[:4]
	w.Phases[3].(*work.PublicationPhase).Delivery = nil
	w.Phases[2].(*work.ReviewPhase).Scope.Destination.Branch = "release/next"
	w.Phases[3].(*work.PublicationPhase).Destination.Branch = "release/next"
	w.Phases[3].(*work.PublicationPhase).Consent = nil
	grant := delivery.(*work.PullRequestDelivery).ObservedAt.Add(-time.Second)
	require.NoError(t, snapshot.AuthorizePublication(0, work.PublicationRef{Phase: 3}, grant, "Explicit synthetic fixture grant for named destination"))
	delivery.(*work.PullRequestDelivery).ObservedAt = grant.Add(time.Second)
	w.Phases[3].(*work.PublicationPhase).Delivery = delivery
	w.Phases = append(w.Phases, &work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: 3}})
	_, err = workstate.NewStore(paths).Save(snapshot, 0)
	require.NoError(t, err)
	service.RunStore = taskstate.NewStore(paths)

	gitState := &fakeSyncGit{result: gitmeta.TaskBranchSyncResult{Status: gitmeta.TaskBranchSyncUpdated}}
	service.Git = gitState
	provider.status = pullrequest.PullRequestStatus{URL: "https://github.test/org/repo/pull/42", State: pullrequest.StateOpen}

	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if result.Status != workflow.SyncStatusBranchUpdated || !strings.Contains(result.Reason, "merged release/next into") {
		t.Fatalf("result = %#v, want named destination synchronization", result)
	}
	if len(gitState.requests) != 1 || gitState.requests[0].DefaultBranch != "release/next" {
		t.Fatalf("sync request = %#v, want release/next", gitState.requests)
	}
}

func (g *fakeSyncGit) InspectTaskBranchConflictCheckpoint(context.Context, gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchConflictCheckpoint, error) {
	if g.head == "" {
		g.head = "fixture-output"
	}
	if g.remote == "" {
		g.remote = "fixture-output"
	}
	return gitmeta.TaskBranchConflictCheckpoint{LocalHead: g.head, RemoteHead: g.remote, MergeSource: "destination-head"}, nil
}
func (g *fakeSyncGit) InspectRemoteTaskBranchHead(context.Context, gitmeta.TaskBranchSyncOptions) (string, error) {
	return g.remote, nil
}
func (g *fakeSyncGit) InspectTaskBranchConflictRollbackEligibility(context.Context, gitmeta.TaskBranchSyncOptions, gitmeta.TaskBranchConflictCheckpoint, string) error {
	return errors.New("unexpected rollback")
}
func (g *fakeSyncGit) RollbackTaskBranchConflictResolution(context.Context, gitmeta.TaskBranchSyncOptions, gitmeta.TaskBranchConflictCheckpoint) error {
	return errors.New("unexpected rollback")
}
func (g *fakeSyncGit) VerifyTaskBranchConflictRollback(context.Context, gitmeta.TaskBranchSyncOptions, gitmeta.TaskBranchConflictCheckpoint) error {
	return errors.New("unexpected rollback")
}
func (g *fakeSyncGit) CommitTaskBranchConflictResolution(context.Context, gitmeta.TaskBranchSyncOptions, []string) (gitmeta.TaskBranchSyncResult, error) {
	return gitmeta.TaskBranchSyncResult{}, errors.New("unexpected resolver")
}
func (g *fakeSyncGit) PushCommittedTaskBranchConflictResolution(context.Context, gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchSyncResult, error) {
	g.remote = g.head
	return gitmeta.TaskBranchSyncResult{Head: g.head, Status: gitmeta.TaskBranchSyncUpdated}, nil
}

func deliveredSyncView(t *testing.T, dir, branch string) taskstate.TaskState {
	t.Helper()
	snapshot, err := testutil.DeliveredPRWork("alpha", "op-1", dir, branch, "https://github.test/org/repo/pull/42")
	require.NoError(t, err)
	view, err := taskstate.Project(snapshot)
	require.NoError(t, err)
	return view
}

func (*fakePRProvider) Update(context.Context, pullrequest.UpdateRequest) error {
	return errors.New("unexpected PR update")
}

type syncCleanupGit struct {
	inspection gitmeta.ClosedTaskWorktreeInspection
	removal    gitmeta.ClosedTaskWorktreeRemoval
	inspects   int
	removes    int
}

func (g *syncCleanupGit) InspectClosedTaskWorktree(
	context.Context,
	gitmeta.ClosedTaskWorktreeOptions,
) gitmeta.ClosedTaskWorktreeInspection {
	g.inspects++
	return g.inspection
}

func (g *syncCleanupGit) RemoveClosedTaskWorktree(
	context.Context,
	gitmeta.ClosedTaskWorktreeOptions,
) gitmeta.ClosedTaskWorktreeRemoval {
	g.removes++
	return g.removal
}
func newSyncTestService(
	t *testing.T,
	taskItem task.Task,
	taskState taskstate.TaskState,
	paths state.Paths,
	source task.RepositorySource,
) (workflow.SyncService, *fakePRProvider, *fakeSyncBackend) {
	t.Helper()
	if taskState.Work == nil {
		dir := taskItem.Metadata[task.MetadataWorktree]
		if dir == "" {
			dir = source.Repository.Path
		}
		branch := taskItem.Metadata[task.MetadataBranch]
		if branch == "" {
			branch = "orpheus/" + taskItem.ID
		}
		taskState = deliveredSyncView(t, dir, branch)
	}
	provider := &fakePRProvider{created: pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/42"}}
	backend := &fakeSyncBackend{tasks: []task.Task{taskItem}}
	service := workflow.SyncService{
		Paths:   paths,
		Sources: []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.SyncBackend, error) {
			return backend, nil
		},
		RunStore:   &fakeSyncRunStore{states: map[string]taskstate.TaskState{source.Repository.ID + "/" + taskItem.ID: taskState}},
		Git:        &fakeSyncGit{},
		CleanupGit: &syncCleanupGit{inspection: gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeAbsent}},
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
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
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
