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
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
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

type cleanupReloadFailureStore struct{ workflow.WorkStore }

func (s cleanupReloadFailureStore) Load(repoID, taskID string) (work.State, error) {
	snapshot, err := s.WorkStore.Load(repoID, taskID)
	if err == nil && len(snapshot.Workflows) > 0 {
		if final, ok := snapshot.Workflows[0].LastPhase().(*work.FinalizationPhase); ok && final.Closure != nil {
			return work.State{}, errPublicationPersistence
		}
	}
	return snapshot, err
}

func TestSyncServiceMergedPRReportsWorktreeWhenCleanupStateReloadFails(t *testing.T) {
	repoPath := filepath.Join(testutil.CanonicalTempDir(t), "repo")
	paths, source, targets := newSyncTestSource(t, repoPath, "op-1")
	worktree := targets.WorktreeTeam.Worktree
	item := task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{task.MetadataBranch: targets.WorktreeTeam.Branch, task.MetadataWorktree: worktree, task.MetadataPRURL: "https://github.test/org/repo/pull/42"}}
	view := deliveredSyncView(t, worktree, targets.WorktreeTeam.Branch)
	store := workstate.NewStore(paths)
	_, err := store.Save(*view.Work, 0)
	require.NoError(t, err)
	service, provider, backend := newSyncTestService(t, item, view, paths, source)
	service.RunStore = taskstate.NewStore(paths)
	service.Store = cleanupReloadFailureStore{WorkStore: store}
	provider.status = pullrequest.PullRequestStatus{URL: item.Metadata[task.MetadataPRURL], State: pullrequest.StateMerged}

	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: item.ID})

	require.NoError(t, err)
	require.NotNil(t, result.WorktreeCleanup)
	assert.Equal(t, workflow.WorktreeCleanupUnsafe, result.WorktreeCleanup.Outcome)
	assert.Equal(t, worktree, result.WorktreeCleanup.Worktree)
	assert.Contains(t, result.WorktreeCleanup.Reason, "reload workflow after closure")
	assert.Contains(t, result.WorktreeCleanup.Reason, errPublicationPersistence.Error())
	assert.Equal(t, []string{item.ID}, backend.closed)
	saved, err := store.Load("alpha", item.ID)
	require.NoError(t, err)
	require.NotNil(t, saved.Workflows[0].LastPhase().(*work.FinalizationPhase).Closure)
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
	service := workflow.SyncService{
		Paths:   paths,
		Sources: []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.SyncBackend, error) {
			return backend, nil
		},
		RunStore: &fakeSyncRunStore{states: map[string]taskstate.TaskState{"alpha/op-1": deliveredSyncView(t, repoPath, "orpheus/op-1")}},
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

func TestBuildPublicationPullRequestContentUsesCompletionAndValidatesTitlePolicy(t *testing.T) {
	for _, tt := range []struct{ name, template, externalRef, title, wantError string }{
		{name: "default title", title: "Ready for review"},
		{name: "custom title", template: "[{{external_ref}}] {{summary}}", externalRef: "\n TREX-1234\t", title: "[TREX-1234] Ready for review"},
		{name: "missing required reference", template: "[{{external_ref}}] {{summary}}", wantError: "requires a task external reference"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item := task.Task{ID: "op-1", Title: "Create PR", Description: "Implement publication.", AcceptanceCriteria: "No duplicate PRs.", ExternalRef: tt.externalRef}
			run := taskstate.RunAttempt{Completion: &taskstate.Completion{Summary: "Ready for review", Description: "Pushed the branch.", DetailedDescription: "Detailed PR body.", TechnicalExplanation: "Technical explanation."}}

			content, err := workflow.BuildPublicationPullRequestContent(tt.template, item, run)

			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("build error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if content.Title != tt.title || content.Body != "Detailed PR body." {
				t.Fatalf("content = %#v, want title %q and exact completion body", content, tt.title)
			}
		})
	}
}

func TestBuildPublicationPullRequestContentFromStateFormatsReviewProcess(t *testing.T) {
	finishedAt := time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC)
	snapshot := taskstate.TaskState{
		Runs: []taskstate.RunAttempt{
			{
				Attempt: 1,
				Completion: &taskstate.Completion{
					Summary:              "Original summary",
					DetailedDescription:  "Original PR body.",
					TechnicalExplanation: "Technical explanation.",
				},
			},
			{
				Attempt: 2,
				Completion: &taskstate.Completion{
					Summary:              "Fix review findings",
					Description:          "Addressed review feedback.",
					DetailedDescription:  "Fix run detailed body must be omitted.",
					TechnicalExplanation: "Technical explanation.",
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
						Type:              taskstate.FindingTypeBlocking,
						Title:             "Manually addressed blocker",
						Step:              "manual-review",
						AddressedManually: "Verified direct repair.",
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
	}
	history := testutil.ReviewableWork("alpha", "op-1", "/fixture/repo", "orpheus/op-1")
	snapshot.Work = &history
	snapshot.CurrentWorkflow = 0
	snapshot.Runs[0].Address = work.AttemptRef{RepositoryID: "alpha", TaskID: "op-1", Workflow: 0, Phase: 1}
	snapshot.Runs[1].Address = work.AttemptRef{RepositoryID: "alpha", TaskID: "op-1", Workflow: 0, Phase: 3}

	for _, include := range []bool{true, false} {
		t.Run(fmt.Sprintf("include review=%t", include), func(t *testing.T) {
			content, err := workflow.BuildPublicationPullRequestContentFromStateWithOptions(workflow.PublicationOptions{IncludeReviewProcess: include}, task.Task{ID: "op-1"}, snapshot)

			require.NoError(t, err)
			assert.Equal(t, "Original summary", content.Title)
			if !include {
				assert.Equal(t, "Original PR body.", content.Body, "omitting review process must preserve the exact completion body")
				return
			}
			assert.True(t, strings.HasPrefix(content.Body, "Original PR body."), "completion must appear before the review history")
			assertPublicationReviewProcess(t, content.Body)
		})
	}
}

func assertPublicationReviewProcess(t *testing.T, body string) {
	t.Helper()
	for _, want := range []string{
		"### Review attempt 1 — passed",
		"- ❌ `manual-review`",
		"  - **Blocking:** Fixed blocker",
		"    - Fixed by run attempt 2",
		"  - **Blocking (waived):** Waived blocker",
		"    - Waived.",
		"  - **Blocking (addressed manually):** Manually addressed blocker",
		"    - Addressed manually.",
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
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{
		"Finding details must be omitted.",
		"Fix run detailed body must be omitted.",
		"Accepted for this task.",
	} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("body = %q, should not contain %q", body, unwanted)
		}
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
		CleanupGit: &fakeClosedTaskWorktreeGit{inspection: gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeAbsent}},
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
	w.Phases[3].(*work.PublicationPhase).Plan.Destination.Branch = "release/next"
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
