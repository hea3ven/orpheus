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

type fakeFinalizationBackend struct {
	tasks     []task.Task
	closed    []string
	setPRURLs []fakeFinalizationSetPRURL
}

type fakeFinalizationSetPRURL struct {
	taskID string
	prURL  string
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

func (b *fakeFinalizationBackend) SetPRURL(_ context.Context, taskID string, prURL string) error {
	b.setPRURLs = append(b.setPRURLs, fakeFinalizationSetPRURL{taskID: taskID, prURL: prURL})
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

func (s *fakeFinalizationRunStore) RecordFinalizationClose(
	repoID string,
	taskID string,
	opts taskstate.FinalizationCloseOptions,
) (taskstate.Finalization, error) {
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
	branch     string
	hasChanges bool
	commit     string
	pushErr    error
	staged     bool
	messages   []string
	pushes     []string
	taskPushes []string
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

func (g *fakeFinalizationGit) Commit(_ context.Context, _ string, message string) (string, error) {
	g.messages = append(g.messages, message)
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

func TestFinalizeRequiresConfirmationForRunningCompletion(t *testing.T) {
	service, _, store, backend := newFinalizationTestService(t, []task.Task{
		finalizationMainTask("op-1", "/tmp/repo"),
	}, map[string]taskstate.TaskState{
		"alpha/op-1": finalizationTaskState("op-1", taskstate.RunAttempt{
			Attempt:  1,
			Status:   taskstate.RunStatusRunning,
			Branch:   "main",
			Worktree: "/tmp/repo",
			Completion: &taskstate.Completion{
				Summary:             "Done",
				Description:         "Implemented.",
				DetailedDescription: "Detailed PR body.",
			},
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
			Attempt:  1,
			Status:   taskstate.RunStatusRunning,
			Branch:   "main",
			Worktree: "/tmp/repo",
			Completion: &taskstate.Completion{
				Summary:             "Done",
				Description:         "Implemented.",
				DetailedDescription: "Detailed PR body.",
			},
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
	if events := store.states["alpha/op-1"].Events; len(events) != 2 ||
		events[0].PushTarget != taskstate.PushTargetMain ||
		events[1].CloseReason != taskstate.CloseReasonDefaultBranchPublished {
		t.Fatalf("events = %#v, want main push and default-branch close", events)
	}
	latest, _ := taskstate.LatestRun(store.states["alpha/op-1"])
	if latest.Status != taskstate.RunStatusRunning {
		t.Fatalf("latest status = %q, want still running", latest.Status)
	}
}

//nolint:funlen // The retry workflow is clearer as one linear scenario.
func TestFinalizeRecordsPublicationFailureAndRetriesWithPassedReview(t *testing.T) {
	taskState := finalizationTaskState("op-1", taskstate.RunAttempt{
		Attempt:  1,
		Status:   taskstate.RunStatusSucceeded,
		Branch:   "main",
		Worktree: "/tmp/repo",
		Completion: &taskstate.Completion{
			Summary:             "Done",
			Description:         "Commit reviewed repo-root changes.",
			DetailedDescription: "Detailed PR body.",
		},
	})
	finishedAt := time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC)
	taskState.Reviews = []taskstate.ReviewAttempt{{
		Attempt:    1,
		Status:     taskstate.ReviewStatusPassed,
		Pipeline:   "default",
		Step:       "local-review",
		StartedAt:  time.Date(2026, 6, 10, 10, 30, 0, 0, time.UTC),
		FinishedAt: &finishedAt,
	}}
	service, git, store, backend := newFinalizationTestService(t, []task.Task{
		finalizationMainTask("op-1", "/tmp/repo"),
	}, map[string]taskstate.TaskState{
		"alpha/op-1": taskState,
	})
	git.pushErr = errors.New("push failed")

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{
		TaskID:              "op-1",
		RequirePassedReview: true,
	})
	if err == nil || !strings.Contains(err.Error(), "push failed") {
		t.Fatalf("first finalize error = %v, want push failure", err)
	}
	stateAfterFailure := store.states["alpha/op-1"]
	if latestReview, ok := taskstate.LatestReview(stateAfterFailure); !ok || latestReview.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("latest review = %#v/%v, want passed review preserved", latestReview, ok)
	}
	if failure, ok := taskstate.LatestFinalizationFailure(stateAfterFailure); !ok || !strings.Contains(failure.Error, "push failed") {
		t.Fatalf("latest finalization failure = %#v/%v, want push failure event", failure, ok)
	}
	if len(backend.closed) != 0 {
		t.Fatalf("closed after failed push = %#v, want none", backend.closed)
	}

	git.pushErr = nil
	git.hasChanges = false
	result, err := service.Finalize(context.Background(), workflow.FinalizeOptions{
		TaskID:              "op-1",
		RequirePassedReview: true,
	})
	if err != nil {
		t.Fatalf("retry finalize: %v", err)
	}
	if result.Finalization.Commit != "commit123" || result.Finalization.PushedAt == nil || result.Finalization.ClosedAt == nil {
		t.Fatalf("retry finalization = %#v, want committed, pushed, and closed", result.Finalization)
	}
	if len(git.messages) != 1 {
		t.Fatalf("commit messages = %#v, want one commit reused across retry", git.messages)
	}
	if len(git.pushes) != 2 || len(backend.closed) != 1 || backend.closed[0] != "op-1" {
		t.Fatalf("pushes=%#v closed=%#v, want failed push retried and task closed", git.pushes, backend.closed)
	}
}

func TestFinalizeDoesNotRequestRunningConfirmationWhenOtherChecksFail(t *testing.T) {
	service, git, _, _ := newFinalizationTestService(t, []task.Task{
		finalizationMainTask("op-1", "/tmp/repo"),
	}, map[string]taskstate.TaskState{
		"alpha/op-1": finalizationTaskState("op-1", taskstate.RunAttempt{
			Attempt:  1,
			Status:   taskstate.RunStatusRunning,
			Branch:   "main",
			Worktree: "/tmp/repo",
			Completion: &taskstate.Completion{
				Summary:             "Done",
				Description:         "Implemented.",
				DetailedDescription: "Detailed PR body.",
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

func TestFinalizeInfersSingleRunningCompletionCandidate(t *testing.T) {
	service, _, _, _ := newFinalizationTestService(t, []task.Task{
		finalizationMainTask("op-1", "/tmp/repo"),
	}, map[string]taskstate.TaskState{
		"alpha/op-1": finalizationTaskState("op-1", taskstate.RunAttempt{
			Attempt:  2,
			Status:   taskstate.RunStatusRunning,
			Branch:   "main",
			Worktree: "/tmp/repo",
			Completion: &taskstate.Completion{
				Summary:             "Done",
				Description:         "Implemented.",
				DetailedDescription: "Detailed PR body.",
			},
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
			Attempt:  1,
			Status:   taskstate.RunStatusRunning,
			Branch:   targets.WorktreeTeam.Branch,
			Worktree: targets.WorktreeTeam.Worktree,
			Completion: &taskstate.Completion{Summary: "Done", Description: "Implemented.",
				DetailedDescription: "Detailed PR body.", Commit: "abc123"},
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

//nolint:funlen // This end-to-end workflow is clearer as a linear scenario.
func TestFinalizePublishesFeatureBranchPRWithoutClosingTask(t *testing.T) {
	paths, source, targets := newFinalizationTestSource(t, "/tmp/repo", "op-1")
	source.Repository.TitleTemplate = "[{{external_ref}}] {{summary}}"
	worktree := targets.WorktreeTeam.Worktree
	taskItem := task.Task{
		ID:          "op-1",
		ExternalRef: " \nTREX-1234\t",
		Status:      task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   targets.WorktreeTeam.Branch,
			task.MetadataWorktree: worktree,
		},
	}
	service, git, store, backend := newFinalizationTestServiceForSource(t, paths, source, []task.Task{taskItem}, map[string]taskstate.TaskState{
		"alpha/op-1": finalizationTaskState("op-1", taskstate.RunAttempt{
			Attempt:  1,
			Status:   taskstate.RunStatusSucceeded,
			Branch:   targets.WorktreeTeam.Branch,
			Worktree: worktree,
			Completion: &taskstate.Completion{
				Summary:             "Publish branch",
				Description:         "Commit reviewed feature work.",
				DetailedDescription: "Detailed PR body.",
			},
		}),
	})
	git.branch = targets.WorktreeTeam.Branch
	provider := &fakePRProvider{created: pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/42"}}
	service.PRProvider = provider

	result, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if result.PRURL != "https://github.test/org/repo/pull/42" || result.PRRecovered {
		t.Fatalf("result = %#v, want created PR URL", result)
	}
	if result.Branch != targets.WorktreeTeam.Branch {
		t.Fatalf("branch = %q, want %q", result.Branch, targets.WorktreeTeam.Branch)
	}
	if !git.staged || len(git.taskPushes) != 1 || git.taskPushes[0] != targets.WorktreeTeam.Branch {
		t.Fatalf("staged=%v taskPushes=%#v, want staged branch push", git.staged, git.taskPushes)
	}
	if len(git.pushes) != 0 {
		t.Fatalf("default branch pushes = %#v, want none", git.pushes)
	}
	if len(provider.findRequests) != 1 || len(provider.createRequests) != 1 {
		t.Fatalf("provider find/create = %#v/%#v, want one each", provider.findRequests, provider.createRequests)
	}
	assertFeatureBranchPublicationTitles(t, git, provider, "[TREX-1234] Publish branch")
	if len(backend.setPRURLs) != 1 || backend.setPRURLs[0].prURL != "https://github.test/org/repo/pull/42" {
		t.Fatalf("set PR URLs = %#v, want created URL", backend.setPRURLs)
	}
	if len(backend.closed) != 0 {
		t.Fatalf("closed = %#v, want backend task left open", backend.closed)
	}
	facts := taskstate.FinalizationFacts(store.states["alpha/op-1"])
	if facts.Commit != "commit123" || facts.CommittedAt == nil || facts.PushedAt == nil || facts.ClosedAt != nil {
		t.Fatalf("finalization facts = %#v, want commit/push without close", facts)
	}
	if events := store.states["alpha/op-1"].Events; len(events) != 2 ||
		events[0].Type != taskstate.EventChangesPushed || events[1].Type != taskstate.EventPRCreated {
		t.Fatalf("events = %#v, want branch push and created PR", events)
	}
}

func TestFinalizeRejectsMissingExternalReferenceBeforeFeatureBranchPublication(t *testing.T) {
	paths, source, targets := newFinalizationTestSource(t, "/tmp/repo", "op-1")
	source.Repository.TitleTemplate = "[{{external_ref}}] {{summary}}"
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
			Attempt:  1,
			Status:   taskstate.RunStatusSucceeded,
			Branch:   targets.WorktreeTeam.Branch,
			Worktree: worktree,
			Completion: &taskstate.Completion{
				Summary:             "Publish branch",
				Description:         "Commit reviewed feature work.",
				DetailedDescription: "Detailed PR body.",
			},
		}),
	})
	git.branch = targets.WorktreeTeam.Branch
	provider := &fakePRProvider{created: pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/42"}}
	service.PRProvider = provider

	_, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})

	if err == nil || !strings.Contains(err.Error(), "requires a task external reference") {
		t.Fatalf("error = %v, want missing external reference error", err)
	}
	if git.staged || len(git.messages) != 0 || len(git.taskPushes) != 0 {
		t.Fatalf("git staged=%v messages=%#v task pushes=%#v, want no publication writes", git.staged, git.messages, git.taskPushes)
	}
	if len(provider.findRequests) != 0 || len(provider.createRequests) != 0 {
		t.Fatalf("provider requests = %#v/%#v, want no PR operations", provider.findRequests, provider.createRequests)
	}
	if len(backend.setPRURLs) != 0 {
		t.Fatalf("set PR URLs = %#v, want none", backend.setPRURLs)
	}
}

func TestFinalizePublishesRepoRootFeatureBranchPRWithoutClosingTask(t *testing.T) {
	paths, source, targets := newFinalizationTestSource(t, "/tmp/repo", "op-1")
	target := targets.RepoRootTeam
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   target.Branch,
			task.MetadataWorktree: target.Worktree,
		},
	}
	service, git, store, backend := newFinalizationTestServiceForSource(
		t,
		paths,
		source,
		[]task.Task{taskItem},
		map[string]taskstate.TaskState{
			"alpha/op-1": finalizationTaskState("op-1", taskstate.RunAttempt{
				Attempt:  1,
				Status:   taskstate.RunStatusSucceeded,
				Branch:   target.Branch,
				Worktree: target.Worktree,
				Completion: &taskstate.Completion{
					Summary:             "Publish repo-root branch",
					Description:         "Commit reviewed feature work.",
					DetailedDescription: "Detailed PR body.",
				},
			}),
		},
	)
	git.branch = target.Branch
	provider := &fakePRProvider{created: pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/42"}}
	service.PRProvider = provider

	result, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if result.Branch != target.Branch || result.PRURL != "https://github.test/org/repo/pull/42" || result.PRRecovered {
		t.Fatalf("result = %#v, want published repo-root feature branch", result)
	}
	if !git.staged || len(git.taskPushes) != 1 || git.taskPushes[0] != target.Branch {
		t.Fatalf("staged=%v taskPushes=%#v, want staged feature branch push", git.staged, git.taskPushes)
	}
	if len(git.pushes) != 0 {
		t.Fatalf("default branch pushes = %#v, want none", git.pushes)
	}
	if len(provider.findRequests) != 1 || len(provider.createRequests) != 1 {
		t.Fatalf("provider find/create = %#v/%#v, want one each", provider.findRequests, provider.createRequests)
	}
	assertFeatureBranchPublicationTitles(t, git, provider, "Publish repo-root branch")
	if len(backend.setPRURLs) != 1 || len(backend.closed) != 0 {
		t.Fatalf("backend set=%#v closed=%#v, want PR recorded and task left open", backend.setPRURLs, backend.closed)
	}
	facts := taskstate.FinalizationFacts(store.states["alpha/op-1"])
	if facts.Commit != "commit123" || facts.CommittedAt == nil || facts.PushedAt == nil || facts.ClosedAt != nil {
		t.Fatalf("finalization facts = %#v, want commit/push without close", facts)
	}
}

func TestFinalizeRecoversExistingFeatureBranchPR(t *testing.T) {
	paths, source, targets := newFinalizationTestSource(t, "/tmp/repo", "op-1")
	worktree := targets.WorktreeTeam.Worktree
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   targets.WorktreeTeam.Branch,
			task.MetadataWorktree: worktree,
		},
	}
	service, git, store, backend := newFinalizationTestServiceForSource(t, paths, source, []task.Task{taskItem}, map[string]taskstate.TaskState{
		"alpha/op-1": finalizationTaskState("op-1", taskstate.RunAttempt{
			Attempt:  1,
			Status:   taskstate.RunStatusSucceeded,
			Branch:   targets.WorktreeTeam.Branch,
			Worktree: worktree,
			Completion: &taskstate.Completion{
				Summary:             "Publish branch",
				Description:         "Commit reviewed feature work.",
				DetailedDescription: "Detailed PR body.",
			},
		}),
	})
	git.branch = targets.WorktreeTeam.Branch
	provider := &fakePRProvider{
		found:   pullrequest.PullRequest{URL: "https://github.test/org/repo/pull/7"},
		foundOK: true,
	}
	service.PRProvider = provider

	result, err := service.Finalize(context.Background(), workflow.FinalizeOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}

	if result.PRURL != "https://github.test/org/repo/pull/7" || !result.PRRecovered {
		t.Fatalf("result = %#v, want recovered PR URL", result)
	}
	if len(provider.createRequests) != 0 {
		t.Fatalf("create requests = %#v, want none", provider.createRequests)
	}
	if len(backend.setPRURLs) != 1 || backend.setPRURLs[0].prURL != "https://github.test/org/repo/pull/7" {
		t.Fatalf("set PR URLs = %#v, want recovered URL", backend.setPRURLs)
	}
	if events := store.states["alpha/op-1"].Events; len(events) != 2 || events[1].Type != taskstate.EventPRRecovered {
		t.Fatalf("events = %#v, want recovered PR event", events)
	}
}

//nolint:funlen // The review follow-up scenario is easier to verify as one fixture.
func TestFinalizePublishesOriginalCompletionAfterReviewFollowUp(t *testing.T) {
	paths, source, targets := newFinalizationTestSource(t, "/tmp/repo", "op-1")
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
			Attempt:  1,
			Status:   taskstate.RunStatusSucceeded,
			Branch:   targets.WorktreeTeam.Branch,
			Worktree: worktree,
			Completion: &taskstate.Completion{
				Summary:             "Implement original feature",
				Description:         "Commit the original implementation.",
				DetailedDescription: "## Original PR body\n\nThe primary implementation details.",
			},
		},
		taskstate.RunAttempt{
			Attempt:  2,
			Status:   taskstate.RunStatusSucceeded,
			Branch:   targets.WorktreeTeam.Branch,
			Worktree: worktree,
			Completion: &taskstate.Completion{
				Summary:             "Fix review blocker",
				Description:         "Addressed review-only follow-up work.",
				DetailedDescription: "## Fix run PR body\n\nThis must not become the PR body.",
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
		"- ❌ `agent-review`",
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

func TestFinalizeRefusesFeatureBranchPublicationWithoutReviewedChanges(t *testing.T) {
	paths, source, targets := newFinalizationTestSource(t, "/tmp/repo", "op-1")
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
			Attempt:  1,
			Status:   taskstate.RunStatusSucceeded,
			Branch:   targets.WorktreeTeam.Branch,
			Worktree: worktree,
			Completion: &taskstate.Completion{
				Summary:             "Publish branch",
				Description:         "Commit reviewed feature work.",
				DetailedDescription: "Detailed PR body.",
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

func newFinalizationTestService(
	t *testing.T,
	tasks []task.Task,
	states map[string]taskstate.TaskState,
) (workflow.FinalizationService, *fakeFinalizationGit, *fakeFinalizationRunStore, *fakeFinalizationBackend) {
	t.Helper()
	paths, source, _ := newFinalizationTestSource(t, "/tmp/repo", "op-1")
	return newFinalizationTestServiceForSource(t, paths, source, tasks, states)
}

func assertFeatureBranchPublicationTitles(
	t *testing.T,
	git *fakeFinalizationGit,
	provider *fakePRProvider,
	title string,
) {
	t.Helper()
	if got := git.messages; len(got) != 1 || got[0] != title+"\n\nCommit reviewed feature work." {
		t.Fatalf("commit messages = %#v, want title with unchanged body", got)
	}
	if got := provider.createRequests[0]; got.Title != title || got.Body != "Detailed PR body." {
		t.Fatalf("PR request = %#v, want title with unchanged body", got)
	}
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
