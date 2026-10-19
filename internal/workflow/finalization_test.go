package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/stretchr/testify/require"
)

type fakeFinalizationBackend struct {
	tasks            []task.Task
	closed           []string
	setPRURLs        []fakeFinalizationSetPRURL
	getWhenClosedErr error
}

type fakeFinalizationSetPRURL struct {
	taskID string
	prURL  string
}

func (b *fakeFinalizationBackend) Get(_ context.Context, id string) (task.Task, error) {
	for _, candidate := range b.tasks {
		if candidate.ID == id {
			if candidate.Status == task.StatusClosed && b.getWhenClosedErr != nil {
				return task.Task{}, b.getWhenClosedErr
			}
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
	for i := range b.tasks {
		if b.tasks[i].ID == id {
			b.tasks[i].Status = task.StatusClosed
			return nil
		}
	}
	return task.ErrNotFound
}

func (b *fakeFinalizationBackend) SetPRURL(_ context.Context, taskID string, prURL string) error {
	b.setPRURLs = append(b.setPRURLs, fakeFinalizationSetPRURL{taskID: taskID, prURL: prURL})
	for i := range b.tasks {
		if b.tasks[i].ID != taskID {
			continue
		}
		if b.tasks[i].Metadata == nil {
			b.tasks[i].Metadata = task.Metadata{}
		}
		b.tasks[i].Metadata[task.MetadataPRURL] = prURL
		return nil
	}
	return task.ErrNotFound
}

type fakeFinalizationRunStore struct {
	states                      map[string]taskstate.TaskState
	recordFinalizationCommitErr error
	recordFinalizationCloseErr  error
	recordFeatureBranchPRErr    error
	cleanupLoadErr              error
	worktreeCleanups            []taskstate.WorktreeCleanupOptions
}

func (s *fakeFinalizationRunStore) Load(repoID, taskID string) (taskstate.TaskState, error) {
	state, ok := s.states[repoID+"/"+taskID]
	if !ok {
		return taskstate.TaskState{RepoID: repoID, TaskID: taskID}, nil
	}
	if s.cleanupLoadErr != nil && taskstate.FinalizationFacts(state).ClosedAt != nil {
		return taskstate.TaskState{}, s.cleanupLoadErr
	}
	return state, nil
}

func (s *fakeFinalizationRunStore) SetFinalizationIntegrationFlow(repoID, taskID string, flow publication.IntegrationFlow) (taskstate.Finalization, error) {
	state := s.states[repoID+"/"+taskID]
	finalization := taskstate.FinalizationFacts(state)
	if finalization.IntegrationFlow != "" && finalization.IntegrationFlow != flow && finalization.PublicationStartedAt != nil {
		return taskstate.Finalization{}, taskstate.ErrFinalizationConflict
	}
	finalization.IntegrationFlow = flow
	state.Finalization = &finalization
	s.states[repoID+"/"+taskID] = state
	return finalization, nil
}

func (s *fakeFinalizationRunStore) SetFinalizationDestination(repoID, taskID, destination string) (taskstate.Finalization, error) {
	state := s.states[repoID+"/"+taskID]
	finalization := taskstate.FinalizationFacts(state)
	finalization.DestinationBranch = destination
	state.Finalization = &finalization
	s.states[repoID+"/"+taskID] = state
	return finalization, nil
}

func (s *fakeFinalizationRunStore) RecordFinalizationPublicationStart(repoID, taskID string) (taskstate.Finalization, error) {
	state := s.states[repoID+"/"+taskID]
	finalization := taskstate.FinalizationFacts(state)
	if finalization.PublicationStartedAt == nil {
		now := time.Date(2026, 6, 10, 11, 59, 0, 0, time.UTC)
		finalization.PublicationStartedAt = &now
		state.Finalization = &finalization
		s.states[repoID+"/"+taskID] = state
	}
	return finalization, nil
}

func (s *fakeFinalizationRunStore) RecordFinalizationCommitIntent(
	repoID string,
	taskID string,
	parent string,
	message string,
) (taskstate.Finalization, error) {
	state := s.states[repoID+"/"+taskID]
	finalization := taskstate.FinalizationFacts(state)
	intent := taskstate.FinalizationCommitIntent{Parent: parent, Message: message}
	finalization.PendingCommit = &intent
	state.Finalization = &finalization
	s.states[repoID+"/"+taskID] = state
	return finalization, nil
}

func (s *fakeFinalizationRunStore) RecordFinalizationCommit(repoID, taskID string, commit string) (taskstate.Finalization, error) {
	if s.recordFinalizationCommitErr != nil {
		return taskstate.Finalization{}, s.recordFinalizationCommitErr
	}
	state := s.states[repoID+"/"+taskID]
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	finalization := taskstate.FinalizationFacts(state)
	finalization.Commit = commit
	finalization.PendingCommit = nil
	finalization.CommittedAt = &now
	state.Finalization = &finalization
	s.states[repoID+"/"+taskID] = state
	return finalization, nil
}

func (s *fakeFinalizationRunStore) RecordFinalizationMerge(repoID, taskID, commit string) (taskstate.Finalization, error) {
	state := s.states[repoID+"/"+taskID]
	finalization := taskstate.FinalizationFacts(state)
	finalization.MergeCommit = commit
	state.Finalization = &finalization
	s.states[repoID+"/"+taskID] = state
	return finalization, nil
}

func (s *fakeFinalizationRunStore) RecordFinalizationPush(
	repoID string,
	taskID string,
	opts taskstate.FinalizationPushOptions,
) (taskstate.Finalization, error) {
	state := s.states[repoID+"/"+taskID]
	now := time.Date(2026, 6, 10, 12, 1, 0, 0, time.UTC)
	finalization := taskstate.FinalizationFacts(state)
	finalization.PushedAt = &now
	state.Finalization = &finalization
	state.Events = append(state.Events, taskstate.Event{
		Type:       taskstate.EventChangesPushed,
		At:         now,
		Branch:     opts.Branch,
		PushTarget: opts.PushTarget,
	})
	s.states[repoID+"/"+taskID] = state
	return finalization, nil
}

func (s *fakeFinalizationRunStore) RecordWorktreeCleanup(repoID, taskID string, opts taskstate.WorktreeCleanupOptions) (taskstate.Event, error) {
	s.worktreeCleanups = append(s.worktreeCleanups, opts)
	return taskstate.Event{Type: taskstate.EventWorktreeRemoved, Worktree: opts.Worktree}, nil
}

func (s *fakeFinalizationRunStore) RecordFinalizationClose(
	repoID string,
	taskID string,
	opts taskstate.FinalizationCloseOptions,
) (taskstate.Finalization, error) {
	if s.recordFinalizationCloseErr != nil {
		return taskstate.Finalization{}, s.recordFinalizationCloseErr
	}
	state := s.states[repoID+"/"+taskID]
	now := time.Date(2026, 6, 10, 12, 2, 0, 0, time.UTC)
	finalization := taskstate.FinalizationFacts(state)
	finalization.ClosedAt = &now
	state.Finalization = &finalization
	state.Events = append(state.Events, taskstate.Event{
		Type:        taskstate.EventTaskClosed,
		At:          now,
		CloseReason: opts.Reason,
	})
	s.states[repoID+"/"+taskID] = state
	return finalization, nil
}

func (s *fakeFinalizationRunStore) RecordFinalizationFailure(
	repoID string,
	taskID string,
	cause error,
) (taskstate.Event, error) {
	state := s.states[repoID+"/"+taskID]
	event := taskstate.Event{
		Type:  taskstate.EventFinalizationFailed,
		At:    time.Date(2026, 6, 10, 12, 3, 0, 0, time.UTC),
		Error: cause.Error(),
	}
	state.Events = append(state.Events, event)
	s.states[repoID+"/"+taskID] = state
	return event, nil
}

func (s *fakeFinalizationRunStore) RecordFeatureBranchPR(
	repoID string,
	taskID string,
	opts taskstate.FeatureBranchPROptions,
) (taskstate.Event, error) {
	if s.recordFeatureBranchPRErr != nil {
		return taskstate.Event{}, s.recordFeatureBranchPRErr
	}
	state := s.states[repoID+"/"+taskID]
	eventType := taskstate.EventPRCreated
	if opts.WasRecovered {
		eventType = taskstate.EventPRRecovered
	}
	event := taskstate.Event{
		Type:   eventType,
		At:     time.Date(2026, 6, 10, 12, 2, 0, 0, time.UTC),
		Branch: opts.Branch,
		PRURL:  opts.PRURL,
	}
	state.Events = append(state.Events, event)
	s.states[repoID+"/"+taskID] = state
	return event, nil
}

type fakeFinalizationGit struct {
	branch                           string
	hasChanges                       bool
	commit                           string
	commitResult                     string
	verifyCommitCalls                int
	verifiedCommit                   string
	verifiedParent                   string
	verifiedMessage                  string
	verifyCommitErr                  error
	pushErr                          error
	directMergeLocalCommit           string
	directMergeRemoteContains        bool
	validateRecordedDirectMergeCalls int
	validateRecordedDirectMergeErr   error
	verifyRemoteDestinationErr       error
	verifiedDestinations             []string
	mergeDestinations                []string
	validatedMergeDestinations       []string
	staged                           bool
	messages                         []string
	pushes                           []string
	taskPushes                       []string
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

func (g *fakeFinalizationGit) VerifyCommit(_ context.Context, _ string, commit string, parent string, message string) error {
	g.verifyCommitCalls++
	g.verifiedCommit = commit
	g.verifiedParent = parent
	g.verifiedMessage = message
	return g.verifyCommitErr
}

func (g *fakeFinalizationGit) StageAll(context.Context, string) error {
	g.staged = true
	return nil
}

func (g *fakeFinalizationGit) Commit(_ context.Context, _ string, message string) (string, error) {
	g.messages = append(g.messages, message)
	if g.commitResult != "" {
		g.commit = g.commitResult
	}
	g.hasChanges = false
	return g.commit, nil
}

func (g *fakeFinalizationGit) PushDefaultBranch(_ context.Context, _ string, branch string) error {
	g.pushes = append(g.pushes, branch)
	if g.pushErr != nil {
		return g.pushErr
	}
	return nil
}

func (g *fakeFinalizationGit) PushTaskBranch(_ context.Context, _ string, branch string) error {
	g.taskPushes = append(g.taskPushes, branch)
	return nil
}

func (g *fakeFinalizationGit) VerifyRemoteDestination(_ context.Context, _ task.Repository, destination string) error {
	g.verifiedDestinations = append(g.verifiedDestinations, destination)
	return g.verifyRemoteDestinationErr
}

func (g *fakeFinalizationGit) MergeTaskBranchIntoDestination(_ context.Context, _ task.Repository, destination string, _ string) (string, error) {
	g.mergeDestinations = append(g.mergeDestinations, destination)
	mergeCommit := "merge-" + g.commit
	g.directMergeLocalCommit = mergeCommit
	return mergeCommit, nil
}

func (g *fakeFinalizationGit) ValidateRecordedDirectMerge(_ context.Context, _ task.Repository, destination string, mergeCommit string) (bool, error) {
	g.validatedMergeDestinations = append(g.validatedMergeDestinations, destination)
	g.validateRecordedDirectMergeCalls++
	if g.validateRecordedDirectMergeErr != nil {
		return false, g.validateRecordedDirectMergeErr
	}
	if g.directMergeRemoteContains {
		return true, nil
	}
	if g.directMergeLocalCommit != mergeCommit {
		return false, fmt.Errorf("local default branch does not match recorded merge")
	}
	return false, nil
}

func TestFinalizeDoesNotRequestRunningConfirmationWhenOtherChecksFail(t *testing.T) {
	service, git, _, _ := newFinalizationTestService(t, []task.Task{
		finalizationMainTask("op-1", "/fixture/repo"),
	}, map[string]taskstate.TaskState{
		"alpha/op-1": finalizationTaskState("op-1", taskstate.RunAttempt{
			Attempt: 1,
			Status:  taskstate.RunStatusRunning,
			Completion: &taskstate.Completion{
				Summary:              "Done",
				Description:          "Implemented.",
				DetailedDescription:  "Detailed PR body.",
				TechnicalExplanation: "Technical explanation.",
			},
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

func TestFinalizeInferenceSkipsIrrelevantTasksBeforeRenderingBranchTemplates(t *testing.T) {
	tests := []struct {
		name          string
		status        task.Status
		currentBranch string
	}{
		{name: "closed", status: task.StatusClosed, currentBranch: "main"},
		{name: "different current branch", status: task.StatusInProgress, currentBranch: "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths, source, _ := newFinalizationTestSource(t, filepath.Join(testutil.CanonicalTempDir(t), "repo"), "op-1")
			source.Repository.BranchTemplate = "feature/{{external_ref}}"
			taskItem := finalizationMainTask("op-1", source.Repository.Path)
			taskItem.Status = tt.status
			state := taskstate.TaskState{
				RepoID: source.Repository.ID, TaskID: taskItem.ID,
				Target: taskstate.GitFacts{Branch: "main", Worktree: source.Repository.Path},
			}
			service, git, _, _ := newFinalizationTestServiceForSource(t, paths, source, []task.Task{taskItem}, map[string]taskstate.TaskState{"alpha/op-1": state})
			git.branch = tt.currentBranch

			_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{CWD: source.Repository.Path})
			if err == nil || !strings.Contains(err.Error(), "no non-closed ready task owns the current branch") {
				t.Fatalf("inferred finalize error = %v, want no candidate", err)
			}
			if strings.Contains(err.Error(), "external reference") {
				t.Fatalf("inferred finalize error = %v, want irrelevant task skipped before template rendering", err)
			}
		})
	}
}

func TestFinalizeInfersFromTaskStateTargetBeforeMetadataMirrorValidation(t *testing.T) {
	service, _, _, _ := newFinalizationTestService(t, []task.Task{
		{
			ID:     "op-1",
			Status: task.StatusInProgress,
			Metadata: task.Metadata{
				task.MetadataBranch:   "main",
				task.MetadataWorktree: "/fixture/stale-worktree",
			},
		},
	}, map[string]taskstate.TaskState{
		"alpha/op-1": {
			RepoID: "alpha",
			TaskID: "op-1",
			Target: taskstate.GitFacts{
				Branch:   "main",
				Worktree: "/fixture/repo",
			},
			Runs: []taskstate.RunAttempt{
				{
					Attempt: 1,
					Status:  taskstate.RunStatusSucceeded,
					Completion: &taskstate.Completion{
						Summary:              "Done",
						Description:          "Implemented.",
						DetailedDescription:  "Detailed PR body.",
						TechnicalExplanation: "Technical explanation.",
					},
				},
			},
		},
	})

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{CWD: "/fixture/repo"})

	if err == nil {
		t.Fatal("error = nil, want stale metadata mirror error")
	}
	if strings.Contains(err.Error(), "no non-closed ready task owns the current branch") {
		t.Fatalf("error = %v, want inferred task before metadata validation", err)
	}
	if !strings.Contains(err.Error(), "metadata target") {
		t.Fatalf("error = %v, want metadata mirror validation error", err)
	}
}

func TestFinalizeDoesNotOfferRunningEscapeHatchForInvalidTargets(t *testing.T) {
	paths, source, targets := newFinalizationTestSource(t, "/fixture/repo", "op-1")
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
			Attempt: 1,
			Status:  taskstate.RunStatusRunning,
			Completion: &taskstate.Completion{
				Summary:              "Done",
				Description:          "Implemented.",
				DetailedDescription:  "Detailed PR body.",
				TechnicalExplanation: "Technical explanation.",
				Commit:               "abc123",
			},
		}),
	})

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})

	if _, ok := workflow.RunningCompletionConfirmationFromError(err); ok {
		t.Fatalf("error = %v, did not want confirmation bypass for worktree/team target", err)
	}
	if err == nil || !strings.Contains(err.Error(), `expected "succeeded"`) {
		t.Fatalf("error = %v, want feature-branch running run error", err)
	}
}

func TestFinalizeRepoRootPublicationFailureLocksFlowInDurableStore(t *testing.T) {
	paths, source, _ := newFinalizationTestSource(t, "/fixture/repo", "op-1")
	store := taskstate.NewStore(paths)
	run, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Branch: "orpheus/op-1", Worktree: source.Repository.Path, WorkDirectory: source.Repository.Path})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CompleteRun("alpha", "op-1", run.Attempt, taskstate.CompleteRunOptions{Summary: "Publish", Description: "Reviewed", DetailedDescription: "Body", TechnicalExplanation: "Rationale"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishRun("alpha", "op-1", run.Attempt, taskstate.RunStatusSucceeded); err != nil {
		t.Fatal(err)
	}
	item := finalizationMainTask("op-1", source.Repository.Path)
	item.Metadata[task.MetadataBranch] = "orpheus/op-1"
	service, git, _, backend := newFinalizationTestServiceForSource(t, paths, source, []task.Task{item}, nil)
	git.branch = "orpheus/op-1"
	service.RunStore = store

	_, err = service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err == nil || !strings.Contains(err.Error(), "task done PR provider is required") {
		t.Fatalf("finalize error = %v, want provider failure", err)
	}
	persisted, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.GitFacts.Branch != "orpheus/op-1" || persisted.GitFacts.Worktree != source.Repository.Path {
		t.Fatalf("Git facts = %#v, want prepared root branch", persisted.GitFacts)
	}
	if _, err := store.SetFinalizationIntegrationFlow("alpha", "op-1", publication.IntegrationFlowDirectMerge); !errors.Is(err, taskstate.ErrFinalizationConflict) {
		t.Fatalf("flow change error = %v, want conflict from real store", err)
	}

	service.PRProvider = &fakePRProvider{created: pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/42"}}
	result, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Branch != "orpheus/op-1" || len(git.taskPushes) != 1 {
		t.Fatalf("result/Git = %#v/%#v, want publication on the prepared branch", result, git)
	}
	if backend.tasks[0].OrpheusMetadata().Branch != "orpheus/op-1" {
		t.Fatalf("backend task = %#v, want matching prepared branch", backend.tasks[0])
	}
}

func TestFinalizeRepoRootRetriesAfterPublicationCommitRecordFailure(t *testing.T) {
	paths, source, _ := newFinalizationTestSource(t, "/fixture/repo", "op-1")
	state := modernRepoRootFinalizationState("op-1", source.Repository.Path)
	service, git, store, _ := newFinalizationTestServiceForSource(
		t,
		paths,
		source,
		[]task.Task{preparedRepoRootTask("op-1", source.Repository.Path)},
		map[string]taskstate.TaskState{"alpha/op-1": state},
	)
	git.branch = "orpheus/op-1"
	service.PRProvider = &fakePRProvider{created: pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/42"}}
	git.commit = "reviewed-default123"
	git.commitResult = "publication456"
	store.recordFinalizationCommitErr = errors.New("local state unavailable")

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err == nil || !strings.Contains(err.Error(), "record publication commit") {
		t.Fatalf("first finalize error = %v, want publication commit persistence failure", err)
	}
	facts := taskstate.FinalizationFacts(store.states["alpha/op-1"])
	if facts.Commit != "" || facts.PendingCommit == nil || facts.PendingCommit.Parent != "reviewed-default123" {
		t.Fatalf("first finalization facts = %#v, want durable unrecorded commit intent", facts)
	}
	if len(git.messages) != 1 || git.messages[0] == "" || git.commit != "publication456" {
		t.Fatalf("first publication git = %#v, want exactly one completed commit", git)
	}

	store.recordFinalizationCommitErr = nil
	result, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("retry finalize: %v", err)
	}
	if result.Finalization.Commit != "publication456" || result.PRURL == "" || len(git.messages) != 1 {
		t.Fatalf("retry result/git = %#v/%#v, want recovered commit and PR without a second commit", result, git)
	}
	if git.verifyCommitCalls != 1 || git.verifiedCommit != "publication456" ||
		git.verifiedParent != "reviewed-default123" || git.verifiedMessage != git.messages[0] || len(git.taskPushes) != 1 {
		t.Fatalf("retry verification/pushes = %#v/%#v, want verified recovery and one push", git, git.taskPushes)
	}
	facts = taskstate.FinalizationFacts(store.states["alpha/op-1"])
	if facts.Commit != "publication456" || facts.PendingCommit != nil || facts.PushedAt == nil {
		t.Fatalf("retried finalization facts = %#v, want persisted recovered commit and push", facts)
	}
}

func TestFinalizeRepoRootRetriesAfterPRRecordFailure(t *testing.T) {
	paths, source, _ := newFinalizationTestSource(t, "/fixture/repo", "op-1")
	state := modernRepoRootFinalizationState("op-1", source.Repository.Path)
	service, git, store, backend := newFinalizationTestServiceForSource(t, paths, source, []task.Task{preparedRepoRootTask("op-1", source.Repository.Path)}, map[string]taskstate.TaskState{"alpha/op-1": state})
	provider := &fakePRProvider{created: pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/42"}}
	git.branch = "orpheus/op-1"
	service.PRProvider = provider
	store.recordFeatureBranchPRErr = errors.New("local state unavailable")

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err == nil || !strings.Contains(err.Error(), "record feature branch PR") {
		t.Fatalf("first finalize error = %v, want PR record failure", err)
	}
	if metadata := backend.tasks[0].OrpheusMetadata(); metadata.PRURL != "https://github.test/org/repo/pull/42" {
		t.Fatalf("backend metadata = %#v, want persisted PR URL", metadata)
	}

	git.hasChanges = false
	provider.found = pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/42"}
	provider.foundOK = true
	store.recordFeatureBranchPRErr = nil
	result, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("retry finalize: %v", err)
	}
	if result.PRURL != "https://github.test/org/repo/pull/42" || !result.PRRecovered {
		t.Fatalf("retry result = %#v, want recovered PR", result)
	}

	if len(backend.setPRURLs) != 1 {
		t.Fatalf("PR URL writes = %#v, want only the initial successful write", backend.setPRURLs)
	}
}

func TestFinalizeLegacyMainTargetRetainsDirectPublicationAfterTemplateChange(t *testing.T) {
	paths, source, _ := newFinalizationTestSource(t, filepath.Join(testutil.CanonicalTempDir(t), "repo"), "op-1")
	source.Repository.BranchTemplate = "feature/{{external_ref}}"
	state := finalizationTaskState("op-1", taskstate.RunAttempt{
		Attempt: 1,
		Status:  taskstate.RunStatusSucceeded,
		Completion: &taskstate.Completion{
			Summary: "Original implementation", Description: "Initial work.", DetailedDescription: "Initial PR body.", TechnicalExplanation: "Initial explanation.",
		},
	}, taskstate.RunAttempt{
		Attempt: 2,
		Status:  taskstate.RunStatusSucceeded,
		Completion: &taskstate.Completion{
			Summary: "Review follow-up", Description: "Addressed review feedback.", DetailedDescription: "Updated PR body.", TechnicalExplanation: "Follow-up explanation.",
		},
		ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{0}},
	})
	state.Target = taskstate.GitFacts{Branch: "main", Worktree: source.Repository.Path}
	service, git, _, backend := newFinalizationTestServiceForSource(t, paths, source, []task.Task{finalizationMainTask("op-1", source.Repository.Path)}, map[string]taskstate.TaskState{"alpha/op-1": state})

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("finalize legacy main target: %v", err)
	}
	if len(git.pushes) != 1 || git.pushes[0] != source.Repository.DefaultBranch {
		t.Fatalf("git = %#v, want direct default-branch publication", git)
	}
	if len(backend.closed) != 1 || backend.closed[0] != "op-1" {
		t.Fatalf("closed = %#v, want legacy task closed after direct publication", backend.closed)
	}
}

func modernRepoRootFinalizationState(taskID string, repoPath string) taskstate.TaskState {
	state := finalizationTaskState(taskID, taskstate.RunAttempt{
		Attempt: 1,
		Status:  taskstate.RunStatusSucceeded,
		Completion: &taskstate.Completion{
			Summary: "Publish repository-root work", Description: "Commit reviewed root changes.", DetailedDescription: "Detailed PR body.", TechnicalExplanation: "Technical explanation.",
		},
	})
	state.WorkDirectory = taskstate.WorkDirectory{Path: repoPath}
	state.GitFacts = taskstate.GitFacts{Branch: "orpheus/" + taskID, Worktree: repoPath}
	return state
}

func TestFinalizePublishesOriginalCompletionAfterReviewFollowUp(t *testing.T) {
	paths, source, targets := newFinalizationTestSource(t, "/fixture/repo", "op-1")
	source.Repository.TitleTemplate = "[{{external_ref}}] {{summary}}"
	worktree := targets.WorktreeTeam.Worktree
	taskItem := task.Task{
		ID:          "op-1",
		ExternalRef: "TREX-1234",
		Status:      task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   targets.WorktreeTeam.Branch,
			task.MetadataWorktree: worktree,
		},
	}
	finishedAt := time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC)
	taskState := finalizationTaskState(
		"op-1",
		taskstate.RunAttempt{
			Attempt: 1,
			Status:  taskstate.RunStatusSucceeded,
			Completion: &taskstate.Completion{
				Summary:              "Implement original feature",
				Description:          "Commit the original implementation.",
				DetailedDescription:  "## Original PR body\n\nThe primary implementation details.",
				TechnicalExplanation: "Technical explanation.",
			},
		},
		taskstate.RunAttempt{
			Attempt: 2,
			Status:  taskstate.RunStatusSucceeded,
			Completion: &taskstate.Completion{
				Summary:              "Fix review blocker",
				Description:          "Addressed review-only follow-up work.",
				DetailedDescription:  "## Fix run PR body\n\nThis must not become the PR body.",
				TechnicalExplanation: "Technical explanation.",
			},
			ReviewFollowUp: &taskstate.ReviewFollowUp{
				ReviewAttempt:  1,
				FindingIndexes: []int{0, 1},
			},
		},
	)
	taskState.Reviews = []taskstate.ReviewAttempt{
		{
			Attempt:    1,
			Status:     taskstate.ReviewStatusBlocked,
			Pipeline:   "default",
			Step:       "agent-review",
			StartedAt:  time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
			FinishedAt: &finishedAt,
			Steps: []taskstate.ReviewStep{
				{Kind: "agent_review", Name: "agent-review"},
			},
			Findings: []taskstate.ReviewFinding{
				{
					Type:                 taskstate.FindingTypeBlocking,
					Title:                "Preserve original PR title",
					Description:          "Long description must be omitted.",
					Step:                 "agent-review",
					SuggestedAction:      "Suggested action must be omitted.",
					TargetedByRunAttempt: 2,
				},
				{
					Type:                 taskstate.FindingTypeBlocking,
					Title:                "Preserve original PR body",
					Step:                 "agent-review",
					TargetedByRunAttempt: 2,
				},
			},
		},
		{
			Attempt:    2,
			Status:     taskstate.ReviewStatusPassed,
			Pipeline:   "default",
			Step:       "agent-review",
			StartedAt:  time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
			FinishedAt: &finishedAt,
			Steps: []taskstate.ReviewStep{
				{Kind: "agent_review", Name: "agent-review"},
			},
		},
	}
	service, git, _, _ := newFinalizationTestServiceForSource(
		t,
		paths,
		source,
		[]task.Task{taskItem},
		map[string]taskstate.TaskState{"alpha/op-1": taskState},
	)
	git.branch = targets.WorktreeTeam.Branch
	provider := &fakePRProvider{created: pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/42"}}
	git.branch = "orpheus/op-1"
	service.PRProvider = provider

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if len(git.messages) != 1 ||
		git.messages[0] != "[TREX-1234] Implement original feature\n\nCommit the original implementation." {
		t.Fatalf("commit messages = %#v, want original implementation completion", git.messages)
	}
	if len(provider.createRequests) != 1 {
		t.Fatalf("create requests = %#v, want one PR creation", provider.createRequests)
	}
	request := provider.createRequests[0]
	if request.Title != "[TREX-1234] Implement original feature" {
		t.Fatalf("PR title = %q, want original implementation title", request.Title)
	}
	if !strings.HasPrefix(request.Body, "## Original PR body\n\nThe primary implementation details.") {
		t.Fatalf("PR body = %q, want original detailed description first", request.Body)
	}
	if strings.Contains(request.Body, "Fix run PR body") ||
		strings.Contains(request.Body, "Long description must be omitted") ||
		strings.Contains(request.Body, "Suggested action must be omitted") {
		t.Fatalf("PR body = %q, want concise review process without fix body or finding details", request.Body)
	}
	for _, want := range []string{
		"## Review process",
		"### Review attempt 1 — blocked",
		"- ✅ `agent-review`",
		"  - **Blocking:** Preserve original PR title",
		"    - Fixed by run attempt 2",
		"  - **Blocking:** Preserve original PR body",
		"  **Fix run attempt 2**",
		"  - Summary: `Fix review blocker`",
		"  - Description: Addressed review-only follow-up work.",
		"### Review attempt 2 — passed",
		"- ✅ `agent-review`",
	} {
		if !strings.Contains(request.Body, want) {
			t.Fatalf("PR body missing %q:\n%s", want, request.Body)
		}
	}
	if strings.Count(request.Body, "**Fix run attempt 2**") != 1 {
		t.Fatalf("PR body = %q, want one fix run summary", request.Body)
	}
}

func TestFinalizeFeatureBranchPRReviewProcessPublicationPolicy(t *testing.T) {
	tests := []struct {
		name         string
		global       *bool
		repoOverride *bool
		wantReview   bool
	}{
		{
			name:       "global omit",
			global:     workflowTestBool(false),
			wantReview: false,
		},
		{
			name:         "repo include overrides global omit",
			global:       workflowTestBool(false),
			repoOverride: workflowTestBool(true),
			wantReview:   true,
		},
		{
			name:         "repo omit overrides default include",
			repoOverride: workflowTestBool(false),
			wantReview:   false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertFeatureBranchPRReviewProcessPolicy(t, test.global, test.repoOverride, test.wantReview)
		})
	}
}

func TestFinalizeRefusesFeatureBranchPublicationWithoutReviewedChanges(t *testing.T) {
	paths, source, targets := newFinalizationTestSource(t, "/fixture/repo", "op-1")
	worktree := targets.WorktreeTeam.Worktree
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   targets.WorktreeTeam.Branch,
			task.MetadataWorktree: worktree,
		},
	}
	service, git, _, backend := newFinalizationTestServiceForSource(t, paths, source, []task.Task{taskItem}, map[string]taskstate.TaskState{
		"alpha/op-1": finalizationTaskState("op-1", taskstate.RunAttempt{
			Attempt: 1,
			Status:  taskstate.RunStatusSucceeded,
			Completion: &taskstate.Completion{
				Summary:              "Publish branch",
				Description:          "Commit reviewed feature work.",
				DetailedDescription:  "Detailed PR body.",
				TechnicalExplanation: "Technical explanation.",
			},
		}),
	})
	git.branch = targets.WorktreeTeam.Branch
	git.hasChanges = false
	service.PRProvider = &fakePRProvider{created: pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/42"}}

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})

	if err == nil || !strings.Contains(err.Error(), "no reviewed local changes to commit") {
		t.Fatalf("error = %v, want no reviewed changes error", err)
	}
	if git.staged || len(git.taskPushes) != 0 {
		t.Fatalf("staged=%v taskPushes=%#v, want no git mutation", git.staged, git.taskPushes)
	}
	if len(backend.setPRURLs) != 0 || len(backend.closed) != 0 {
		t.Fatalf("backend set=%#v closed=%#v, want no backend mutation", backend.setPRURLs, backend.closed)
	}
}

func TestFinalizeDirectMergeReportsWorktreeWhenCleanupStateReloadFails(t *testing.T) {
	paths, source, targets := newFinalizationTestSource(t, "/fixture/repo", "op-1")
	source.Repository.IntegrationFlow = string(publication.IntegrationFlowDirectMerge)
	worktree := targets.WorktreeTeam.Worktree
	taskItem := task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{
		task.MetadataBranch: targets.WorktreeTeam.Branch, task.MetadataWorktree: worktree,
	}}
	state := finalizationTaskState("op-1", taskstate.RunAttempt{Attempt: 1, Status: taskstate.RunStatusSucceeded, Completion: &taskstate.Completion{
		Summary: "Direct merge", Description: "Merge directly.", DetailedDescription: "PR body.", TechnicalExplanation: "Rationale.",
	}})
	service, git, store, _ := newFinalizationTestServiceForSource(t, paths, source, []task.Task{taskItem}, map[string]taskstate.TaskState{"alpha/op-1": state})
	git.branch = targets.WorktreeTeam.Branch
	store.cleanupLoadErr = errors.New("state storage unavailable")

	result, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("finalize direct merge: %v", err)
	}
	if result.WorktreeCleanup == nil || result.WorktreeCleanup.Outcome != workflow.WorktreeCleanupUnsafe || result.WorktreeCleanup.Worktree != worktree || !strings.Contains(result.WorktreeCleanup.Reason, "state storage unavailable") {
		t.Fatalf("worktree cleanup = %#v, want unsafe result with worktree path", result.WorktreeCleanup)
	}
}

func TestFinalizeRepositoryRootNeverCleansItsCheckoutAfterClosureLookupFailure(t *testing.T) {
	for _, directMerge := range []bool{false, true} {
		name := "main publication"
		if directMerge {
			name = "direct merge"
		}
		t.Run(name, func(t *testing.T) {
			paths, source, targets := newFinalizationTestSource(t, "/fixture/repo", "op-1")
			item := finalizationMainTask("op-1", source.Repository.Path)
			branch := "main"
			if directMerge {
				source.Repository.IntegrationFlow = string(publication.IntegrationFlowDirectMerge)
				branch = targets.RepoRootTeam.Branch
				item.Metadata[task.MetadataBranch] = branch
			}
			state := finalizationTaskState("op-1", taskstate.RunAttempt{Attempt: 1, Status: taskstate.RunStatusSucceeded, Completion: &taskstate.Completion{Summary: "Publish root", Description: "Reviewed changes", DetailedDescription: "Body", TechnicalExplanation: "Rationale"}})
			service, git, store, backend := newFinalizationTestServiceForSource(t, paths, source, []task.Task{item}, map[string]taskstate.TaskState{"alpha/op-1": state})
			git.branch = branch
			backend.getWhenClosedErr = errors.New("task source unavailable after close")
			cleanup := &fakeClosedTaskWorktreeGit{}
			service.CleanupGit = cleanup

			result, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})

			if err != nil {
				t.Fatal(err)
			}
			if result.WorktreeCleanup != nil || cleanup.removes != 0 || len(store.worktreeCleanups) != 0 {
				t.Fatalf("root cleanup = %#v, removes=%d audits=%d, want no cleanup", result.WorktreeCleanup, cleanup.removes, len(store.worktreeCleanups))
			}
			if len(backend.closed) != 1 || backend.closed[0] != "op-1" || taskstate.FinalizationFacts(store.states["alpha/op-1"]).ClosedAt == nil {
				t.Fatalf("closure = %#v/%#v, want durable closure", backend.closed, store.states["alpha/op-1"].Finalization)
			}
		})
	}
}

func TestFinalizeDirectMergeRetryRefusesChangedLocalDefaultBeforePush(t *testing.T) {
	paths, source, targets := newFinalizationTestSource(t, "/fixture/repo", "op-1")
	source.Repository.IntegrationFlow = string(publication.IntegrationFlowDirectMerge)
	worktree := targets.WorktreeTeam.Worktree
	taskItem := task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{
		task.MetadataBranch: targets.WorktreeTeam.Branch, task.MetadataWorktree: worktree,
	}}
	state := finalizationTaskState("op-1", taskstate.RunAttempt{Attempt: 1, Status: taskstate.RunStatusSucceeded, Completion: &taskstate.Completion{
		Summary: "Direct merge", Description: "Merge directly.", DetailedDescription: "PR body.", TechnicalExplanation: "Rationale.",
	}})
	service, git, store, backend := newFinalizationTestServiceForSource(t, paths, source, []task.Task{taskItem}, map[string]taskstate.TaskState{"alpha/op-1": state})
	git.branch = targets.WorktreeTeam.Branch
	git.pushErr = errors.New("push unavailable")

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if !errors.Is(err, git.pushErr) {
		t.Fatalf("first finalize error = %v, want push error", err)
	}
	mergeCommit := taskstate.FinalizationFacts(store.states["alpha/op-1"]).MergeCommit
	if mergeCommit == "" {
		t.Fatal("first finalization did not record the merge checkpoint")
	}

	git.pushErr = nil
	git.directMergeLocalCommit = "unrelated-local-main"
	_, err = service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err == nil || !strings.Contains(err.Error(), "local default branch does not match recorded merge") {
		t.Fatalf("retry error = %v, want recorded merge validation failure", err)
	}
	if len(git.pushes) != 1 {
		t.Fatalf("pushes = %#v, want only failed initial push", git.pushes)
	}
	if len(backend.closed) != 0 {
		t.Fatalf("closed = %#v, want task left open", backend.closed)
	}
	if git.validateRecordedDirectMergeCalls != 2 {
		t.Fatalf("recorded merge validations = %d, want 2", git.validateRecordedDirectMergeCalls)
	}
}

func TestFinalizeDirectMergeRetriesAfterBackendCloseBeforeCloseFactPersistence(t *testing.T) {
	paths, source, targets := newFinalizationTestSource(t, "/fixture/repo", "op-1")
	source.Repository.IntegrationFlow = string(publication.IntegrationFlowDirectMerge)
	worktree := targets.WorktreeTeam.Worktree
	taskItem := task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{
		task.MetadataBranch: targets.WorktreeTeam.Branch, task.MetadataWorktree: worktree,
	}}
	state := finalizationTaskState("op-1", taskstate.RunAttempt{Attempt: 1, Status: taskstate.RunStatusSucceeded, Completion: &taskstate.Completion{
		Summary: "Direct merge", Description: "Merge directly.", DetailedDescription: "PR body.", TechnicalExplanation: "Rationale.",
	}})
	service, git, store, backend := newFinalizationTestServiceForSource(t, paths, source, []task.Task{taskItem}, map[string]taskstate.TaskState{"alpha/op-1": state})
	git.branch = targets.WorktreeTeam.Branch
	store.recordFinalizationCloseErr = errors.New("local state unavailable")

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err == nil || !strings.Contains(err.Error(), "record finalization close") {
		t.Fatalf("first finalize error = %v, want close fact persistence failure", err)
	}
	facts := taskstate.FinalizationFacts(store.states["alpha/op-1"])
	if facts.IntegrationFlow != publication.IntegrationFlowDirectMerge || facts.MergeCommit == "" || facts.PushedAt == nil || facts.ClosedAt != nil {
		t.Fatalf("first finalization = %#v, want completed direct merge without close fact", facts)
	}
	if backend.tasks[0].Status != task.StatusClosed || len(backend.closed) != 1 {
		t.Fatalf("backend task/close calls = %#v/%#v, want closed once", backend.tasks[0], backend.closed)
	}

	store.recordFinalizationCloseErr = nil
	result, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("retry finalize: %v", err)
	}
	if result.Finalization.ClosedAt == nil || len(backend.closed) != 1 {
		t.Fatalf("retry result/close calls = %#v/%#v, want recorded close without a second backend close", result, backend.closed)
	}
	if len(git.pushes) != 1 {
		t.Fatalf("default pushes = %#v, want original push only", git.pushes)
	}
}

func newFinalizationTestService(
	t *testing.T,
	tasks []task.Task,
	states map[string]taskstate.TaskState,
) (workflow.FinalizationService, *fakeFinalizationGit, *fakeFinalizationRunStore, *fakeFinalizationBackend) {
	t.Helper()
	paths, source, _ := newFinalizationTestSource(t, "/fixture/repo", "op-1")
	return newFinalizationTestServiceForSource(t, paths, source, tasks, states)
}

func assertFeatureBranchPRReviewProcessPolicy(t *testing.T, global, repoOverride *bool, wantReview bool) {
	t.Helper()
	paths, source, targets := newFinalizationTestSource(t, "/fixture/repo", "op-1")
	if global != nil {
		if err := testutil.WriteConfigYAML(paths, "config.yaml", map[string]any{
			"reviews": map[string]any{"include_pr_review_process": *global},
		}); err != nil {
			t.Fatalf("write review config: %v", err)
		}
	}
	source.Repository.IncludePRReviewProcess = repoOverride
	worktree := targets.WorktreeTeam.Worktree
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   targets.WorktreeTeam.Branch,
			task.MetadataWorktree: worktree,
		},
	}
	taskState := reviewProcessPolicyTaskState()
	service, git, _, _ := newFinalizationTestServiceForSource(
		t,
		paths,
		source,
		[]task.Task{taskItem},
		map[string]taskstate.TaskState{"alpha/op-1": taskState},
	)
	git.branch = targets.WorktreeTeam.Branch
	provider := &fakePRProvider{created: pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/42"}}
	git.branch = "orpheus/op-1"
	service.PRProvider = provider

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if len(provider.createRequests) != 1 {
		t.Fatalf("create requests = %#v, want one PR creation", provider.createRequests)
	}
	body := provider.createRequests[0].Body
	if !strings.HasPrefix(body, "Original PR body.") {
		t.Fatalf("PR body = %q, want original detailed description first", body)
	}
	hasReview := strings.Contains(body, "## Review process")
	if hasReview != wantReview {
		t.Fatalf("PR body = %q, review process present = %t, want %t", body, hasReview, wantReview)
	}
}

func reviewProcessPolicyTaskState() taskstate.TaskState {
	taskState := finalizationTaskState("op-1", taskstate.RunAttempt{
		Attempt: 1,
		Status:  taskstate.RunStatusSucceeded,
		Completion: &taskstate.Completion{
			Summary:              "Implement original feature",
			Description:          "Commit the original implementation.",
			DetailedDescription:  "Original PR body.",
			TechnicalExplanation: "Technical explanation.",
		},
	})
	taskState.Reviews = []taskstate.ReviewAttempt{{
		Attempt: 1,
		Status:  taskstate.ReviewStatusPassed,
		Step:    "manual-review",
		Steps: []taskstate.ReviewStep{{
			Kind: "manual",
			Name: "manual-review",
		}},
	}}
	return taskState
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
	for key, state := range states {
		if !state.GitFacts.IsZero() || !state.Target.IsZero() {
			continue
		}
		taskID := strings.TrimPrefix(key, source.Repository.ID+"/")
		taskItem, ok := finalizationTaskByID(tasks, taskID)
		if !ok {
			continue
		}
		metadata := taskItem.OrpheusMetadata()
		if metadata.HasBranch && metadata.HasWorktree {
			state.GitFacts = taskstate.GitFacts{
				Branch:   metadata.Branch,
				Worktree: metadata.Worktree,
			}
			states[key] = state
		}
	}
	store := &fakeFinalizationRunStore{states: states}
	git := &fakeFinalizationGit{branch: source.Repository.DefaultBranch, hasChanges: true, commit: "commit123"}
	service := workflow.FinalizationService{
		Paths:   paths,
		Sources: []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (workflow.FinalizationBackend, error) {
			return backend, nil
		},
		RunStore:   store,
		Git:        git,
		CleanupGit: &fakeClosedTaskWorktreeGit{inspection: gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeAbsent}},
	}
	return service, git, store, backend
}

func finalizationTaskByID(tasks []task.Task, taskID string) (task.Task, bool) {
	for _, taskItem := range tasks {
		if taskItem.ID == taskID {
			return taskItem, true
		}
	}
	return task.Task{}, false
}

func newFinalizationTestSource(
	t *testing.T,
	repoPath string,
	taskID string,
) (state.Paths, task.RepositorySource, tasktarget.ExpectedTargets) {
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
	targets, err := tasktarget.ExpectedTargetsForTask(source.Repository, taskID, paths)
	if err != nil {
		t.Fatalf("expected targets: %v", err)
	}
	return paths, source, targets
}

func mustFinalizationTestPaths(t *testing.T) state.Paths {
	t.Helper()
	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(
		filepath.Join(root, "config", state.AppName),
		filepath.Join(root, "data", state.AppName),
	)
	if err != nil {
		t.Fatalf("create paths: %v", err)
	}
	return paths
}

func workflowTestBool(value bool) *bool {
	return &value
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

func TestFinalizeNamedDestinationUsesItForPullRequest(t *testing.T) {
	paths, source, targets := newFinalizationTestSource(t, "/fixture/repo", "op-1")
	taskItem := task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{
		task.MetadataBranch: targets.WorktreeTeam.Branch, task.MetadataWorktree: targets.WorktreeTeam.Worktree,
	}}
	state := finalizationTaskState("op-1", taskstate.RunAttempt{Attempt: 1, Status: taskstate.RunStatusSucceeded, Completion: &taskstate.Completion{
		Summary: "Publish", Description: "Reviewed.", DetailedDescription: "Body.", TechnicalExplanation: "Why.",
	}})
	state.Finalization = &taskstate.Finalization{DestinationBranch: "release/next"}
	service, git, store, _ := newFinalizationTestServiceForSource(t, paths, source, []task.Task{taskItem}, map[string]taskstate.TaskState{"alpha/op-1": state})
	git.branch = targets.WorktreeTeam.Branch
	provider := &fakePRProvider{created: pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/42"}}
	git.branch = "orpheus/op-1"
	service.PRProvider = provider

	result, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if result.Branch != targets.WorktreeTeam.Branch || len(provider.createRequests) != 1 || provider.createRequests[0].BaseBranch != "release/next" {
		t.Fatalf("result/PR request = %#v/%#v, want PR based on release/next", result, provider.createRequests)
	}
	facts := taskstate.FinalizationFacts(store.states["alpha/op-1"])
	if facts.DestinationBranch != "release/next" || facts.PublicationStartedAt == nil {
		t.Fatalf("finalization facts = %#v, want locked named destination and publication start", facts)
	}
	if got := git.verifiedDestinations; len(got) != 1 || got[0] != "release/next" {
		t.Fatalf("verified destinations = %#v, want release/next", got)
	}
}

func TestFinalizeRejectsMissingNamedDestinationBeforePublication(t *testing.T) {
	paths, source, targets := newFinalizationTestSource(t, "/fixture/repo", "op-1")
	taskItem := task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{
		task.MetadataBranch: targets.WorktreeTeam.Branch, task.MetadataWorktree: targets.WorktreeTeam.Worktree,
	}}
	state := finalizationTaskState("op-1", taskstate.RunAttempt{Attempt: 1, Status: taskstate.RunStatusSucceeded, Completion: &taskstate.Completion{
		Summary: "Publish", Description: "Reviewed.", DetailedDescription: "Body.", TechnicalExplanation: "Why.",
	}})
	state.Finalization = &taskstate.Finalization{DestinationBranch: "release/missing"}
	service, git, store, _ := newFinalizationTestServiceForSource(t, paths, source, []task.Task{taskItem}, map[string]taskstate.TaskState{"alpha/op-1": state})
	git.branch = targets.WorktreeTeam.Branch
	git.verifyRemoteDestinationErr = errors.New("branch not found")
	service.PRProvider = &fakePRProvider{}

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if !errors.Is(err, git.verifyRemoteDestinationErr) || !strings.Contains(err.Error(), "verify selected integration destination") {
		t.Fatalf("finalize error = %v, want remote destination verification failure", err)
	}
	facts := taskstate.FinalizationFacts(store.states["alpha/op-1"])
	if facts.PublicationStartedAt != nil || git.staged || len(git.taskPushes) != 0 {
		t.Fatalf("facts/git = %#v/%#v, want no publication mutation", facts, git)
	}
}

func TestFinalizeRejectsPublicationBranchAsIntegrationDestinationBeforeEffects(t *testing.T) {
	for _, tc := range []struct {
		name             string
		repoRoot, direct bool
	}{
		{"prepared root branch", true, false}, {"worktree pull request", false, false}, {"worktree direct merge", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths, source, targets := newFinalizationTestSource(t, "/fixture/repo", "op-1")
			target := targets.WorktreeTeam
			if tc.direct {
				source.Repository.IntegrationFlow = string(publication.IntegrationFlowDirectMerge)
			}
			item := task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{task.MetadataBranch: target.Branch, task.MetadataWorktree: target.Worktree}}
			state := finalizationTaskState("op-1", taskstate.RunAttempt{Attempt: 1, Status: taskstate.RunStatusSucceeded, Completion: &taskstate.Completion{Summary: "Publish", Description: "Reviewed", DetailedDescription: "Body", TechnicalExplanation: "Why"}})
			if tc.repoRoot {
				item = preparedRepoRootTask("op-1", source.Repository.Path)
				state = modernRepoRootFinalizationState("op-1", source.Repository.Path)
			}
			state.Finalization = &taskstate.Finalization{DestinationBranch: target.Branch}
			service, git, store, backend := newFinalizationTestServiceForSource(t, paths, source, []task.Task{item}, map[string]taskstate.TaskState{"alpha/op-1": state})
			if !tc.repoRoot {
				git.branch = target.Branch
			}
			provider := &fakePRProvider{}
			service.PRProvider = provider

			_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})

			if err == nil || !strings.Contains(err.Error(), "is the task publication branch") {
				t.Fatalf("finalize error = %v, want destination rejection", err)
			}
			facts := taskstate.FinalizationFacts(store.states["alpha/op-1"])
			if facts.PublicationStartedAt != nil || git.staged ||
				len(git.taskPushes) != 0 || len(git.pushes) != 0 || len(git.mergeDestinations) != 0 ||
				len(provider.findRequests) != 0 || len(provider.createRequests) != 0 || len(backend.closed) != 0 {
				t.Fatalf("rejected target caused publication effects: facts=%#v Git=%#v backend=%#v PR=%#v", facts, git, backend, provider)
			}
		})
	}
}

func TestFinalizeDirectMergePushesNamedDestination(t *testing.T) {
	paths, source, targets := newFinalizationTestSource(t, "/fixture/repo", "op-1")
	source.Repository.IntegrationFlow = string(publication.IntegrationFlowDirectMerge)
	taskItem := task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{
		task.MetadataBranch: targets.WorktreeTeam.Branch, task.MetadataWorktree: targets.WorktreeTeam.Worktree,
	}}
	state := finalizationTaskState("op-1", taskstate.RunAttempt{Attempt: 1, Status: taskstate.RunStatusSucceeded, Completion: &taskstate.Completion{
		Summary: "Publish", Description: "Reviewed.", DetailedDescription: "Body.", TechnicalExplanation: "Why.",
	}})
	state.Finalization = &taskstate.Finalization{DestinationBranch: "release/next"}
	service, git, _, backend := newFinalizationTestServiceForSource(t, paths, source, []task.Task{taskItem}, map[string]taskstate.TaskState{"alpha/op-1": state})
	git.branch = targets.WorktreeTeam.Branch

	result, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if result.Branch != "release/next" || len(git.mergeDestinations) != 1 || git.mergeDestinations[0] != "release/next" || len(git.pushes) != 1 || git.pushes[0] != "release/next" {
		t.Fatalf("result/git = %#v/%#v, want merge and push to release/next", result, git)
	}
	if len(backend.closed) != 1 {
		t.Fatalf("closed = %#v, want close only after named push", backend.closed)
	}
}

func preparedRepoRootTask(taskID, repoPath string) task.Task {
	item := finalizationMainTask(taskID, repoPath)
	item.Metadata[task.MetadataBranch] = "orpheus/" + taskID
	return item
}

func TestFinalizeRefusesUnpreparedRepoRootContextWithoutChangingFacts(t *testing.T) {
	paths, source, _ := newFinalizationTestSource(t, "/fixture/repo", "op-1")
	state := modernRepoRootFinalizationState("op-1", source.Repository.Path)
	state.GitFacts.Branch = "main"
	state.BaselineCommit = "starting-sha"
	item := finalizationMainTask("op-1", source.Repository.Path)
	service, git, store, backend := newFinalizationTestServiceForSource(t, paths, source, []task.Task{item}, map[string]taskstate.TaskState{"alpha/op-1": state})

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})

	require.ErrorContains(t, err, "must be prepared on its task branch before implementation")
	require.Equal(t, state, store.states["alpha/op-1"])
	require.Equal(t, item, backend.tasks[0])
	require.Equal(t, "main", git.branch)
	require.True(t, git.hasChanges)
	require.False(t, git.staged)
	require.Empty(t, git.messages)
	require.Empty(t, git.pushes)
	require.Empty(t, git.taskPushes)
}
