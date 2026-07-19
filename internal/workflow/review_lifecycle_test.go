package workflow_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
)

//nolint:funlen // The test sets up a full lifecycle fixture without Cobra.
func TestReviewLifecycleRunRecordsBlockedOutcomeWithoutCobra(t *testing.T) {
	t.Parallel()

	paths := testPaths(t)
	repoPath := testRepoWithCandidateChange(t)
	store := taskstate.NewStore(paths)
	_, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:       "implementer",
		Profile:     "implementer",
		Command:     "agent",
		SessionName: "Implementing op-1",
		Branch:      "main",
		Worktree:    repoPath,
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}

	taskItem := task.Task{
		ID:     "op-1",
		Title:  "Lifecycle task",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   "main",
			task.MetadataWorktree: repoPath,
		},
	}
	frontend := &recordingReviewFrontend{}
	service := workflow.ReviewLifecycleService{
		Paths: paths,
		Sources: []task.RepositorySource{{Repository: task.Repository{
			ID: "alpha", Name: "Alpha", TaskIDPrefix: "op", Path: repoPath, DefaultBranch: "main",
		}}},
		RunStore: store,
		BackendFactory: func(task.RepositorySource) (workflow.ReviewLifecycleBackend, error) {
			return &fakeReviewLifecycleBackend{task: taskItem}, nil
		},
		Frontend: frontend,
		PipelineRunner: func(opts review.PipelineRunOptions) (review.PipelineOutcome, error) {
			if _, err := opts.Store.Load("alpha", "op-1"); err != nil {
				t.Fatalf("pipeline store cannot load task state: %v", err)
			}
			if opts.RepoID != "alpha" || opts.TaskID != "op-1" {
				t.Fatalf("pipeline core target = repo:%q task:%q, want alpha/op-1", opts.RepoID, opts.TaskID)
			}
			if opts.Branch != "main" || opts.Workdir != repoPath || opts.Attempt.Attempt == 0 || opts.Pipeline.Name != "default" {
				t.Fatalf(
					"pipeline core review options = branch:%q workdir:%q attempt:%d pipeline:%q",
					opts.Branch,
					opts.Workdir,
					opts.Attempt.Attempt,
					opts.Pipeline.Name,
				)
			}
			if opts.SessionName != taskItem.ReviewSessionName() {
				t.Fatalf("pipeline session = %q, want %q", opts.SessionName, taskItem.ReviewSessionName())
			}
			return review.PipelineOutcome{Status: taskstate.ReviewStatusBlocked}, nil
		},
	}

	outcome, err := service.Run(context.Background(), workflow.ReviewLifecycleOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("run lifecycle: %v", err)
	}
	if outcome.Kind != workflow.ReviewLifecycleOutcomeBlocked {
		t.Fatalf("outcome kind = %q, want %q", outcome.Kind, workflow.ReviewLifecycleOutcomeBlocked)
	}
	if frontend.pipelineCalls != 1 {
		t.Fatalf("pipeline calls = %d, want 1", frontend.pipelineCalls)
	}
	taskState, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load task state: %v", err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok {
		t.Fatal("latest review missing")
	}
	if latest.Status != taskstate.ReviewStatusBlocked {
		t.Fatalf("latest review status = %q, want blocked", latest.Status)
	}
}

//nolint:funlen // The test sets up a full lifecycle fixture without Cobra.
func TestReviewLifecycleRunLogsGitDiagnosticsForGatingFailure(t *testing.T) {
	t.Parallel()

	paths := testPaths(t)
	repoPath := testRepoWithCandidateChange(t)
	runGit(t, repoPath, "add", "candidate.txt")
	store := taskstate.NewStore(paths)
	_, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:       "implementer",
		Profile:     "implementer",
		Command:     "agent",
		SessionName: "Implementing op-1",
		Branch:      "main",
		Worktree:    repoPath,
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}

	taskItem := task.Task{
		ID:     "op-1",
		Title:  "Lifecycle task",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   "main",
			task.MetadataWorktree: repoPath,
		},
	}
	var diagnostics bytes.Buffer
	service := workflow.ReviewLifecycleService{
		Paths: paths,
		Sources: []task.RepositorySource{{Repository: task.Repository{
			ID: "alpha", Name: "Alpha", TaskIDPrefix: "op", Path: repoPath, DefaultBranch: "main",
		}}},
		RunStore: store,
		BackendFactory: func(task.RepositorySource) (workflow.ReviewLifecycleBackend, error) {
			return &fakeReviewLifecycleBackend{task: taskItem}, nil
		},
		Logger:   logging.New(&diagnostics, logging.Config{Verbose: true}),
		Frontend: &recordingReviewFrontend{},
		PipelineRunner: func(review.PipelineRunOptions) (review.PipelineOutcome, error) {
			t.Fatal("pipeline should not run after review gating failure")
			return review.PipelineOutcome{}, nil
		},
	}

	outcome, err := service.Run(context.Background(), workflow.ReviewLifecycleOptions{TaskID: "op-1"})

	if err == nil {
		t.Fatal("run lifecycle succeeded, want review gating failure")
	}
	if !strings.Contains(err.Error(), "review requires a clean Git index") {
		t.Fatalf("run lifecycle error = %v, want clean index failure", err)
	}
	if outcome.Kind != workflow.ReviewLifecycleOutcomeOperationalFail {
		t.Fatalf("outcome kind = %q, want %q", outcome.Kind, workflow.ReviewLifecycleOutcomeOperationalFail)
	}
	logs := diagnostics.String()
	for _, want := range []string{
		`component=git operation=diff_cached`,
		`component=git operation=status`,
		`exit_code=1`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("diagnostics missing %q:\n%s", want, logs)
		}
	}
}

//nolint:funlen // The test sets up a full lifecycle fixture without Cobra.
func TestReviewLifecycleManualPromptPersistsFindingsThroughWorkflowRecorder(t *testing.T) {
	t.Parallel()

	paths := testPaths(t)
	repoPath := testRepoWithCandidateChange(t)
	store := taskstate.NewStore(paths)
	run, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:       "implementer",
		Profile:     "implementer",
		Command:     "agent",
		SessionName: "Implementing op-1",
		Branch:      "main",
		Worktree:    repoPath,
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := store.CompleteRun("alpha", "op-1", run.Attempt, taskstate.CompleteRunOptions{
		Summary:              "Implementation complete",
		Description:          "Ready for local review.",
		DetailedDescription:  "Ready for publication.",
		TechnicalExplanation: "Technical explanation.",
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}

	taskItem := task.Task{
		ID:     "op-1",
		Title:  "Lifecycle task",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   "main",
			task.MetadataWorktree: repoPath,
		},
	}
	frontend := &recordingReviewFrontend{
		manualRender: func(workflow.ReviewManualStepContext) error { return nil },
		manualPrompt: func(prompt workflow.ReviewManualStepPrompt) (review.ManualResult, error) {
			_, err := prompt.Recorder.RecordFinding(taskstate.ReviewFinding{
				Type:        taskstate.FindingTypeBlocking,
				Title:       "Manual blocker",
				Description: "Recorded through workflow recorder.",
			})
			return review.ManualResult{Status: taskstate.ReviewStatusBlocked, Stop: true}, err
		},
	}
	var diagnostics bytes.Buffer
	service := workflow.ReviewLifecycleService{
		Paths: paths,
		Sources: []task.RepositorySource{{Repository: task.Repository{
			ID: "alpha", Name: "Alpha", TaskIDPrefix: "op", Path: repoPath, DefaultBranch: "main",
		}}},
		RunStore: store,
		BackendFactory: func(task.RepositorySource) (workflow.ReviewLifecycleBackend, error) {
			return &fakeReviewLifecycleBackend{task: taskItem}, nil
		},
		Logger:   logging.New(&diagnostics, logging.Config{Verbose: true}),
		Frontend: frontend,
		PipelineRunner: func(opts review.PipelineRunOptions) (review.PipelineOutcome, error) {
			step := review.Step{Name: "local-review"}
			if err := opts.RenderManualStep(step); err != nil {
				return review.PipelineOutcome{}, err
			}
			result, err := opts.PromptManualStep(review.ManualStep{Step: step})
			return review.PipelineOutcome{Status: result.Status}, err
		},
	}

	outcome, err := service.Run(context.Background(), workflow.ReviewLifecycleOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("run lifecycle: %v", err)
	}
	if outcome.Kind != workflow.ReviewLifecycleOutcomeBlocked {
		t.Fatalf("outcome kind = %q, want %q", outcome.Kind, workflow.ReviewLifecycleOutcomeBlocked)
	}
	taskState, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load task state: %v", err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || len(latest.Findings) != 1 {
		t.Fatalf("latest review findings = %#v, want one finding", latest.Findings)
	}
	if latest.Findings[0].Step != "local-review" {
		t.Fatalf("manual finding step = %q, want local-review", latest.Findings[0].Step)
	}
	logs := diagnostics.String()
	for _, want := range []string{
		`component=git operation=diff_cached`,
		`component=git operation=candidate_status`,
		`component=git operation=status`,
	} {
		if !strings.Contains(logs, want) {
			t.Fatalf("diagnostics missing %q:\n%s", want, logs)
		}
	}
}

//nolint:funlen // The test drives the full passed review/finalization path.
func TestReviewLifecycleConfirmsRunningCompletionBeforeFinalizing(t *testing.T) {
	t.Parallel()

	paths := testPaths(t)
	repoPath := testRepoWithLocalOriginAndCandidateChange(t)
	store := taskstate.NewStore(paths)
	run, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:    "implementer",
		Profile:  "implementer",
		Branch:   "main",
		Worktree: repoPath,
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := store.CompleteRun("alpha", "op-1", run.Attempt, taskstate.CompleteRunOptions{
		Summary:              "Implementation complete",
		Description:          "Ready for local review.",
		DetailedDescription:  "Ready for publication.",
		TechnicalExplanation: "Technical explanation.",
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}

	taskItem := task.Task{
		ID:     "op-1",
		Title:  "Lifecycle task",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   "main",
			task.MetadataWorktree: repoPath,
		},
	}
	frontend := &recordingReviewFrontend{confirmRunningCompletion: true}
	backend := &fakeReviewLifecycleBackend{task: taskItem}
	service := workflow.ReviewLifecycleService{
		Paths: paths,
		Sources: []task.RepositorySource{{Repository: task.Repository{
			ID: "alpha", Name: "Alpha", TaskIDPrefix: "op", Path: repoPath, DefaultBranch: "main",
		}}},
		RunStore: store,
		BackendFactory: func(task.RepositorySource) (workflow.ReviewLifecycleBackend, error) {
			return backend, nil
		},
		Frontend: frontend,
		PipelineRunner: func(review.PipelineRunOptions) (review.PipelineOutcome, error) {
			return review.PipelineOutcome{Status: taskstate.ReviewStatusPassed}, nil
		},
	}

	outcome, err := service.Run(context.Background(), workflow.ReviewLifecycleOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("run lifecycle: %v", err)
	}
	if outcome.Kind != workflow.ReviewLifecycleOutcomePassed {
		t.Fatalf("outcome kind = %q, want %q", outcome.Kind, workflow.ReviewLifecycleOutcomePassed)
	}
	if frontend.runningConfirmations != 1 {
		t.Fatalf("running confirmations = %d, want 1", frontend.runningConfirmations)
	}
	if !backend.closed {
		t.Fatal("backend task was not closed")
	}
	taskState, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load task state: %v", err)
	}
	latestRun, ok := taskstate.LatestRun(taskState)
	if !ok {
		t.Fatal("latest run missing")
	}
	if latestRun.Status != taskstate.RunStatusRunning {
		t.Fatalf("latest run status = %q, want running", latestRun.Status)
	}
	if taskstate.FinalizationFacts(taskState).Commit == "" {
		t.Fatal("finalization commit was not recorded")
	}
}

//nolint:funlen // The test asserts preflight outcome and persisted review state.
func TestReviewLifecyclePreparesPipelineBeforeFreshReviewTransition(t *testing.T) {
	t.Parallel()

	paths := testPaths(t)
	repoPath := testRepoWithCandidateChange(t)
	store := taskstate.NewStore(paths)
	_, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:    "implementer",
		Profile:  "implementer",
		Branch:   "main",
		Worktree: repoPath,
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	taskItem := task.Task{
		ID:     "op-1",
		Title:  "Lifecycle task",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   "main",
			task.MetadataWorktree: repoPath,
		},
	}
	writeAgentReviewPipelineConfig(t, paths)
	frontend := &recordingReviewFrontend{}
	service := workflow.ReviewLifecycleService{
		Paths: paths,
		Sources: []task.RepositorySource{{Repository: task.Repository{
			ID: "alpha", Name: "Alpha", TaskIDPrefix: "op", Path: repoPath, DefaultBranch: "main",
		}}},
		RunStore: store,
		BackendFactory: func(task.RepositorySource) (workflow.ReviewLifecycleBackend, error) {
			return &fakeReviewLifecycleBackend{task: taskItem}, nil
		},
		Frontend: frontend,
		PipelineRunner: func(review.PipelineRunOptions) (review.PipelineOutcome, error) {
			t.Fatal("pipeline should not run when preparation fails")
			return review.PipelineOutcome{}, nil
		},
	}

	outcome, err := service.Run(context.Background(), workflow.ReviewLifecycleOptions{TaskID: "op-1"})
	if err == nil || !strings.Contains(err.Error(), "load agent profiles") {
		t.Fatalf("run lifecycle error = %v, want invalid agent config", err)
	}
	if outcome.Kind != workflow.ReviewLifecycleOutcomeOperationalFail {
		t.Fatalf("outcome kind = %q, want %q", outcome.Kind, workflow.ReviewLifecycleOutcomeOperationalFail)
	}
	if !errors.Is(outcome.Err, err) {
		t.Fatalf("outcome error = %v, want returned error %v", outcome.Err, err)
	}
	if outcome.Context.TaskID() != "op-1" {
		t.Fatalf("outcome context task ID = %q, want op-1", outcome.Context.TaskID())
	}
	if frontend.pipelineCalls != 0 {
		t.Fatalf("pipeline calls = %d, want 0", frontend.pipelineCalls)
	}
	taskState, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load task state: %v", err)
	}
	if _, ok := taskstate.LatestReview(taskState); ok {
		t.Fatal("fresh preparation failure persisted a review attempt")
	}
}

//nolint:funlen // The test needs a paused review fixture to assert no resume mutation.
func TestReviewLifecyclePreparesPipelineBeforeResumeReviewTransition(t *testing.T) {
	t.Parallel()

	paths := testPaths(t)
	repoPath := testRepoWithCandidateChange(t)
	store := taskstate.NewStore(paths)
	_, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:    "implementer",
		Profile:  "implementer",
		Branch:   "main",
		Worktree: repoPath,
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	writeAgentReviewPipelineConfig(t, paths)
	attempt, err := store.StartReviewWithOptions("alpha", "op-1", taskstate.StartReviewOptions{
		Pipeline: "agent-review",
		Step:     "automated-review",
	})
	if err != nil {
		t.Fatalf("start paused review: %v", err)
	}
	if _, err := store.PauseReviewForManual("alpha", "op-1", attempt.Attempt, "automated-review"); err != nil {
		t.Fatalf("pause review: %v", err)
	}
	taskItem := task.Task{
		ID:     "op-1",
		Title:  "Lifecycle task",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   "main",
			task.MetadataWorktree: repoPath,
		},
	}
	frontend := &recordingReviewFrontend{}
	service := workflow.ReviewLifecycleService{
		Paths: paths,
		Sources: []task.RepositorySource{{Repository: task.Repository{
			ID: "alpha", Name: "Alpha", TaskIDPrefix: "op", Path: repoPath, DefaultBranch: "main",
		}}},
		RunStore: store,
		BackendFactory: func(task.RepositorySource) (workflow.ReviewLifecycleBackend, error) {
			return &fakeReviewLifecycleBackend{task: taskItem}, nil
		},
		Frontend: frontend,
		PipelineRunner: func(review.PipelineRunOptions) (review.PipelineOutcome, error) {
			t.Fatal("pipeline should not run when preparation fails")
			return review.PipelineOutcome{}, nil
		},
	}

	_, err = service.Run(context.Background(), workflow.ReviewLifecycleOptions{TaskID: "op-1"})
	if err == nil || !strings.Contains(err.Error(), "load agent profiles") {
		t.Fatalf("run lifecycle error = %v, want invalid agent config", err)
	}
	taskState, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load task state: %v", err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok {
		t.Fatal("latest review missing")
	}
	if latest.Status != taskstate.ReviewStatusWaitingForManual {
		t.Fatalf("latest review status = %q, want waiting_for_manual", latest.Status)
	}
	if latest.Attempt != attempt.Attempt {
		t.Fatalf("latest attempt = %d, want %d", latest.Attempt, attempt.Attempt)
	}
}

func TestReviewLifecycleRejectsClosedTaskBeforeStartingReview(t *testing.T) {
	t.Parallel()

	paths := testPaths(t)
	taskItem := task.Task{
		ID:        "op-closed",
		Title:     "Closed lifecycle task",
		Status:    task.StatusClosed,
		IssueType: task.IssueTypeEpic,
	}
	frontend := &recordingReviewFrontend{}
	service := workflow.ReviewLifecycleService{
		Paths: paths,
		Sources: []task.RepositorySource{{Repository: task.Repository{
			ID: "alpha", Name: "Alpha", TaskIDPrefix: "op", Path: t.TempDir(), DefaultBranch: "main",
		}}},
		RunStore: taskstate.NewStore(paths),
		BackendFactory: func(task.RepositorySource) (workflow.ReviewLifecycleBackend, error) {
			return &fakeReviewLifecycleBackend{task: taskItem}, nil
		},
		Frontend: frontend,
		PipelineRunner: func(review.PipelineRunOptions) (review.PipelineOutcome, error) {
			t.Fatal("pipeline should not run for closed task")
			return review.PipelineOutcome{}, nil
		},
	}

	_, err := service.Run(context.Background(), workflow.ReviewLifecycleOptions{TaskID: "op-closed"})
	if err == nil {
		t.Fatal("run lifecycle succeeded, want scope error")
	}
	if !strings.Contains(err.Error(), "item is out of scope for M2 task views") {
		t.Fatalf("error = %v, want M2 scope error", err)
	}
	if !strings.Contains(err.Error(), "issue_type=epic status=closed") {
		t.Fatalf("error = %v, want issue type and status", err)
	}
	if frontend.pipelineCalls != 0 {
		t.Fatalf("pipeline calls = %d, want 0", frontend.pipelineCalls)
	}
}

//nolint:funlen // The test exercises the autonomous follow-up transition guard.
func TestReviewLifecycleMissingAgentRunnerDoesNotStartFollowUpRun(t *testing.T) {
	t.Parallel()

	paths := testPaths(t)
	repoPath := testRepoWithCandidateChange(t)
	store := taskstate.NewStore(paths)
	_, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:    "implementer",
		Profile:  "implementer",
		Branch:   "main",
		Worktree: repoPath,
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	taskItem := task.Task{
		ID:     "op-1",
		Title:  "Lifecycle task",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   "main",
			task.MetadataWorktree: repoPath,
		},
	}
	frontend := &recordingReviewFrontend{}
	service := workflow.ReviewLifecycleService{
		Paths: paths,
		Sources: []task.RepositorySource{{Repository: task.Repository{
			ID: "alpha", Name: "Alpha", TaskIDPrefix: "op", Path: repoPath, DefaultBranch: "main",
		}}},
		RunStore: store,
		BackendFactory: func(task.RepositorySource) (workflow.ReviewLifecycleBackend, error) {
			return &fakeReviewLifecycleBackend{task: taskItem}, nil
		},
		Frontend: frontend,
		PipelineRunner: func(opts review.PipelineRunOptions) (review.PipelineOutcome, error) {
			if _, err := store.RecordReviewStep("alpha", "op-1", opts.Attempt.Attempt, taskstate.RecordReviewStepOptions{
				Kind: taskstate.ReviewStepKindAgentReview,
				Name: "automated-review",
			}); err != nil {
				t.Fatalf("record review step: %v", err)
			}
			if _, err := store.RecordReviewFinding("alpha", "op-1", opts.Attempt.Attempt, taskstate.ReviewFinding{
				Type:        taskstate.FindingTypeBlocking,
				Title:       "Blocking review finding",
				Description: "Needs an autonomous follow-up.",
				Step:        "automated-review",
			}); err != nil {
				t.Fatalf("record review finding: %v", err)
			}
			if _, err := store.MarkReviewAutomatedBlockerDecisionKept("alpha", "op-1", opts.Attempt.Attempt); err != nil {
				t.Fatalf("mark automated blocker kept: %v", err)
			}
			return review.PipelineOutcome{Status: taskstate.ReviewStatusBlocked}, nil
		},
	}

	outcome, err := service.Run(context.Background(), workflow.ReviewLifecycleOptions{TaskID: "op-1"})
	if err == nil || !strings.Contains(err.Error(), "review lifecycle agent runner is required") {
		t.Fatalf("run lifecycle error = %v, want missing agent runner", err)
	}
	if outcome.Kind != workflow.ReviewLifecycleOutcomeOperationalFail {
		t.Fatalf("outcome kind = %q, want %q", outcome.Kind, workflow.ReviewLifecycleOutcomeOperationalFail)
	}
	if !errors.Is(outcome.Err, err) {
		t.Fatalf("outcome error = %v, want returned error %v", outcome.Err, err)
	}
	if frontend.autonomousFollowUps != 0 {
		t.Fatalf("autonomous follow-up callbacks = %d, want 0", frontend.autonomousFollowUps)
	}
	taskState, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load task state: %v", err)
	}
	if len(taskState.Runs) != 1 {
		t.Fatalf("run attempts = %d, want 1", len(taskState.Runs))
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok {
		t.Fatal("latest review missing")
	}
	if latest.Status != taskstate.ReviewStatusBlocked {
		t.Fatalf("latest review status = %q, want blocked", latest.Status)
	}
}

//nolint:funlen // The fixture drives the blocked-review follow-up transition.
func TestReviewLifecycleUsageErrorDoesNotFailSuccessfulAutonomousFollowUp(t *testing.T) {
	t.Parallel()

	paths := testPaths(t)
	repoPath := testRepoWithLocalOriginAndCandidateChange(t)
	store := taskstate.NewStore(paths)
	run, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:    "implementer",
		Profile:  "implementer",
		Branch:   "main",
		Worktree: repoPath,
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := store.FinishRun("alpha", "op-1", run.Attempt, taskstate.RunStatusSucceeded); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	taskItem := task.Task{
		ID:     "op-1",
		Title:  "Lifecycle task",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   "main",
			task.MetadataWorktree: repoPath,
		},
	}
	usageErr := errors.New("usage store unavailable")
	service := workflow.ReviewLifecycleService{
		Paths: paths,
		Sources: []task.RepositorySource{{Repository: task.Repository{
			ID: "alpha", Name: "Alpha", TaskIDPrefix: "op", Path: repoPath, DefaultBranch: "main",
		}}},
		RunStore: store,
		BackendFactory: func(task.RepositorySource) (workflow.ReviewLifecycleBackend, error) {
			return &fakeReviewLifecycleBackend{task: taskItem}, nil
		},
		Frontend:    &recordingReviewFrontend{},
		AgentRunner: &recordingReviewAgentRunner{usageErr: usageErr},
		ResolveCommand: func(workflow.DispatchCommandContext, string) (workflow.DispatchCommand, string, error) {
			return workflow.DispatchCommand{AgentName: "implementer", Command: "agent"}, "prompt", nil
		},
		PipelineRunner: func(opts review.PipelineRunOptions) (review.PipelineOutcome, error) {
			recordAutonomousReviewBlocker(t, store, opts.Attempt.Attempt)
			return review.PipelineOutcome{Status: taskstate.ReviewStatusBlocked}, nil
		},
	}

	outcome, err := service.Run(context.Background(), workflow.ReviewLifecycleOptions{
		TaskID:            "op-1",
		DispatchAgentName: "implementer",
	})
	if err == nil || !strings.Contains(err.Error(), "record run usage") {
		t.Fatalf("run lifecycle error = %v, want usage recording error", err)
	}
	if outcome.Kind != workflow.ReviewLifecycleOutcomeOperationalFail {
		t.Fatalf("outcome kind = %q, want %q", outcome.Kind, workflow.ReviewLifecycleOutcomeOperationalFail)
	}
	taskState, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load task state: %v", err)
	}
	latest, ok := taskstate.LatestRun(taskState)
	if !ok {
		t.Fatal("latest run missing")
	}
	if latest.Attempt != 2 {
		t.Fatalf("latest run attempt = %d, want 2", latest.Attempt)
	}
	if latest.Status != taskstate.RunStatusSucceeded {
		t.Fatalf("latest run status = %q, want succeeded", latest.Status)
	}
}

//nolint:funlen // The fixture proves task-run continuation is reusable without Cobra.
func TestReviewLifecycleRunAfterCompletedRunStartsReviewAndPropagatesAgent(t *testing.T) {
	t.Parallel()

	paths := testPaths(t)
	repoPath := testRepoWithLocalOriginAndCandidateChange(t)
	store := taskstate.NewStore(paths)
	run, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:    "implementer",
		Profile:  "implementer",
		Branch:   "main",
		Worktree: repoPath,
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := store.CompleteRun("alpha", "op-1", run.Attempt, taskstate.CompleteRunOptions{
		Summary:              "Implementation complete",
		Description:          "Ready for local review.",
		DetailedDescription:  "Ready for publication.",
		TechnicalExplanation: "Technical explanation.",
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	if _, err := store.FinishRun("alpha", "op-1", run.Attempt, taskstate.RunStatusSucceeded); err != nil {
		t.Fatalf("finish run: %v", err)
	}

	taskItem := task.Task{
		ID:     "op-1",
		Title:  "Lifecycle task",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   "main",
			task.MetadataWorktree: repoPath,
		},
	}
	var capturedAgentName string
	pipelineCalls := 0
	service := workflow.ReviewLifecycleService{
		Paths: paths,
		Sources: []task.RepositorySource{{Repository: task.Repository{
			ID: "alpha", Name: "Alpha", TaskIDPrefix: "op", Path: repoPath, DefaultBranch: "main",
		}}},
		RunStore: store,
		BackendFactory: func(task.RepositorySource) (workflow.ReviewLifecycleBackend, error) {
			return &fakeReviewLifecycleBackend{task: taskItem}, nil
		},
		Frontend: &recordingReviewFrontend{},
		AgentRunner: &recordingReviewAgentRunner{
			store:    store,
			complete: true,
		},
		ResolveCommand: func(_ workflow.DispatchCommandContext, agentName string) (workflow.DispatchCommand, string, error) {
			capturedAgentName = agentName
			return workflow.DispatchCommand{AgentName: agentName, Command: "agent"}, "prompt", nil
		},
		ResolveFollowUpCommand: func(_ workflow.DispatchCommandContext, agentName string) (workflow.DispatchCommand, string, error) {
			capturedAgentName = agentName
			return workflow.DispatchCommand{AgentName: agentName, Command: "agent"}, "prompt", nil
		},
		PipelineRunner: func(opts review.PipelineRunOptions) (review.PipelineOutcome, error) {
			pipelineCalls++
			if pipelineCalls == 1 {
				recordAutonomousReviewBlocker(t, store, opts.Attempt.Attempt)
				return review.PipelineOutcome{Status: taskstate.ReviewStatusBlocked}, nil
			}
			return review.PipelineOutcome{Status: taskstate.ReviewStatusWaitingForManual}, nil
		},
	}

	outcome, reviewed, err := service.RunAfterCompletedRun(context.Background(), workflow.ReviewAfterRunCompletionOptions{
		RepoID:                    "alpha",
		TaskID:                    "op-1",
		RunAttempt:                run.Attempt,
		SelectedDispatchAgentName: "selected-implementer",
		FallbackDispatchAgentName: "fallback-implementer",
	})
	if err != nil {
		t.Fatalf("continue review after run: %v", err)
	}
	if !reviewed {
		t.Fatal("review continuation was skipped")
	}
	if outcome.Kind != workflow.ReviewLifecycleOutcomeWaitingForManual {
		t.Fatalf("outcome kind = %q, want %q", outcome.Kind, workflow.ReviewLifecycleOutcomeWaitingForManual)
	}
	if capturedAgentName != "selected-implementer" {
		t.Fatalf("captured agent name = %q, want selected-implementer", capturedAgentName)
	}
	if pipelineCalls != 2 {
		t.Fatalf("pipeline calls = %d, want 2", pipelineCalls)
	}
}

func TestReviewLifecycleRunAfterCompletedRunSkipsReviewWithoutCompletion(t *testing.T) {
	t.Parallel()

	paths := testPaths(t)
	store := taskstate.NewStore(paths)
	run, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "implementer"})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := store.FinishRun("alpha", "op-1", run.Attempt, taskstate.RunStatusSucceeded); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	service := workflow.ReviewLifecycleService{RunStore: store}

	_, reviewed, err := service.RunAfterCompletedRun(context.Background(), workflow.ReviewAfterRunCompletionOptions{
		RepoID:     "alpha",
		TaskID:     "op-1",
		RunAttempt: run.Attempt,
	})
	if err != nil {
		t.Fatalf("continue review after run: %v", err)
	}
	if reviewed {
		t.Fatal("review continuation started without a run completion")
	}
}

func recordAutonomousReviewBlocker(t *testing.T, store taskstate.Store, attempt int) {
	t.Helper()
	if _, err := store.RecordReviewStep("alpha", "op-1", attempt, taskstate.RecordReviewStepOptions{
		Kind: taskstate.ReviewStepKindAgentReview,
		Name: "automated-review",
	}); err != nil {
		t.Fatalf("record review step: %v", err)
	}
	if _, err := store.RecordReviewFinding("alpha", "op-1", attempt, taskstate.ReviewFinding{
		Type:        taskstate.FindingTypeBlocking,
		Title:       "Blocking review finding",
		Description: "Needs an autonomous follow-up.",
		Step:        "automated-review",
	}); err != nil {
		t.Fatalf("record review finding: %v", err)
	}
	if _, err := store.MarkReviewAutomatedBlockerDecisionKept("alpha", "op-1", attempt); err != nil {
		t.Fatalf("mark automated blocker kept: %v", err)
	}
}

func TestSelectSeparateTaskCandidatesParsesSelection(t *testing.T) {
	t.Parallel()

	candidates := []workflow.SeparateTaskCandidate{{Index: 2}, {Index: 4}, {Index: 8}}
	selected, err := workflow.SelectSeparateTaskCandidates(candidates, "3, 1, 3")
	if err != nil {
		t.Fatalf("select candidates: %v", err)
	}
	if len(selected) != 2 || selected[0].Index != 8 || selected[1].Index != 2 {
		t.Fatalf("selected = %#v, want candidate indexes 8 and 2", selected)
	}
}

type recordingReviewFrontend struct {
	pipelineCalls            int
	autonomousFollowUps      int
	confirmRunningCompletion bool
	runningConfirmations     int
	manualRender             func(workflow.ReviewManualStepContext) error
	manualPrompt             func(workflow.ReviewManualStepPrompt) (review.ManualResult, error)
}

func (f *recordingReviewFrontend) PipelinePresentation(workflow.ReviewAttemptContext) (workflow.ReviewPipelinePresentation, error) {
	f.pipelineCalls++
	return workflow.ReviewPipelinePresentation{RenderManualStep: f.manualRender, PromptManualStep: f.manualPrompt}, nil
}

func (f *recordingReviewFrontend) ReviewResumed(workflow.ReviewAttemptContext) error { return nil }
func (f *recordingReviewFrontend) AutonomousFollowUp(workflow.ReviewAttemptContext, int, []int) error {
	f.autonomousFollowUps++
	return nil
}
func (f *recordingReviewFrontend) AutonomousBudgetExhausted(workflow.ReviewAttemptContext, int, int) error {
	return nil
}
func (f *recordingReviewFrontend) FollowUpRunIncomplete(workflow.ReviewAttemptContext, int) error {
	return nil
}
func (f *recordingReviewFrontend) SelectSeparateTaskCandidates(
	workflow.ReviewAttemptContext,
	[]workflow.SeparateTaskCandidate,
) ([]workflow.SeparateTaskCandidate, error) {
	return nil, nil
}
func (f *recordingReviewFrontend) SeparateTaskCreated(
	workflow.ReviewAttemptContext,
	workflow.SeparateTaskCandidate,
	task.Task,
) error {
	return nil
}
func (f *recordingReviewFrontend) ContinueAfterFollowUpCreationFailure(
	workflow.ReviewAttemptContext,
	workflow.SeparateTaskCandidate,
	error,
) (bool, error) {
	return false, nil
}
func (f *recordingReviewFrontend) ConfirmRunningCompletionFinalization(
	workflow.ReviewAttemptContext,
	workflow.RunningCompletionConfirmation,
) (bool, error) {
	f.runningConfirmations++
	return f.confirmRunningCompletion, nil
}

type recordingReviewAgentRunner struct {
	store    taskstate.Store
	usageErr error
	complete bool
}

func (r *recordingReviewAgentRunner) RunReviewLifecycleAgent(
	_ context.Context,
	run workflow.ReviewLifecycleAgentRun,
) (workflow.ReviewLifecycleAgentRunResult, error) {
	if r.complete {
		if _, err := r.store.CompleteRun(run.RepoID, run.TaskID, run.Start.Attempt.Attempt, taskstate.CompleteRunOptions{
			Summary:              "Follow-up complete",
			Description:          "Review blockers were addressed.",
			DetailedDescription:  "Ready for another review.",
			TechnicalExplanation: "Technical explanation.",
		}); err != nil {
			return workflow.ReviewLifecycleAgentRunResult{}, err
		}
	}
	return workflow.ReviewLifecycleAgentRunResult{UsageError: r.usageErr}, nil
}

func (r *recordingReviewAgentRunner) IsStartError(error) bool { return false }

type fakeReviewLifecycleBackend struct {
	task   task.Task
	closed bool
}

func (b *fakeReviewLifecycleBackend) Get(context.Context, string) (task.Task, error) {
	return b.task, nil
}
func (b *fakeReviewLifecycleBackend) List(context.Context) ([]task.Task, error) {
	return []task.Task{b.task}, nil
}
func (b *fakeReviewLifecycleBackend) MarkInProgress(context.Context, string, string, string) error {
	return nil
}
func (b *fakeReviewLifecycleBackend) SetPRURL(context.Context, string, string) error { return nil }
func (b *fakeReviewLifecycleBackend) Close(context.Context, string) error {
	b.closed = true
	return nil
}
func (b *fakeReviewLifecycleBackend) Create(context.Context, task.CreateOptions) (task.Task, error) {
	return task.Task{}, errors.New("unexpected create")
}

func writeAgentReviewPipelineConfig(t *testing.T, paths state.Paths) {
	t.Helper()
	if err := paths.WriteConfigYAML(review.ConfigFile, map[string]any{
		"reviews": map[string]any{
			"default_pipeline": "agent-review",
			"pipelines": map[string]any{
				"agent-review": map[string]any{
					"steps": []map[string]any{{
						"kind": "agent_review",
						"name": "automated-review",
					}},
				},
			},
		},
	}); err != nil {
		t.Fatalf("write review config: %v", err)
	}
}

func testPaths(t *testing.T) state.Paths {
	t.Helper()
	root := t.TempDir()
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	return paths
}

func testRepoWithCandidateChange(t *testing.T) string {
	t.Helper()
	return testRepoWithCandidateChangeOptions(t, false)
}

func testRepoWithLocalOriginAndCandidateChange(t *testing.T) string {
	t.Helper()
	return testRepoWithCandidateChangeOptions(t, true)
}

func testRepoWithCandidateChangeOptions(t *testing.T, withOrigin bool) string {
	t.Helper()
	repoPath := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("create repo dir: %v", err)
	}
	runGit(t, repoPath, "init", "-b", "main")
	runGit(t, repoPath, "config", "user.email", "test@example.com")
	runGit(t, repoPath, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repoPath, "candidate.txt"), []byte("before\n"), 0o644); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "initial")
	if withOrigin {
		originPath := filepath.Join(t.TempDir(), "origin.git")
		runGit(t, "", "init", "--bare", originPath)
		runGit(t, repoPath, "remote", "add", "origin", originPath)
		runGit(t, repoPath, "push", "-u", "origin", "main")
	}
	if err := os.WriteFile(filepath.Join(repoPath, "candidate.txt"), []byte("after\n"), 0o644); err != nil {
		t.Fatalf("modify candidate: %v", err)
	}
	return repoPath
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
