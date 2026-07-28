package workflow

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
)

//nolint:funlen // The fixture is intentionally explicit about blocked review state.
func TestDispatchValidateStartInfersBlockedReviewFollowUpTarget(t *testing.T) {
	paths := newDispatchTestPaths(t)
	repoPath := filepath.Join(canonicalTestPath(t, t.TempDir()), "repo")
	repo := task.Repository{
		ID:            "alpha",
		Name:          "Alpha",
		Path:          repoPath,
		DefaultBranch: "main",
		TaskIDPrefix:  "op",
	}
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   "main",
			task.MetadataWorktree: repoPath,
		},
	}
	store := fakeDispatchRunStore{
		state: taskstate.TaskState{
			Version: 2,
			RepoID:  repo.ID,
			TaskID:  taskItem.ID,
			Target: taskstate.TaskTarget{
				Branch:   "main",
				Worktree: repoPath,
			},
			Reviews: []taskstate.ReviewAttempt{
				{
					Attempt:  1,
					Status:   taskstate.ReviewStatusBlocked,
					Pipeline: "default",
					Step:     "local-review",
					Findings: []taskstate.ReviewFinding{
						{Type: taskstate.FindingTypeBlocking, Title: "Bug", Description: "Fix it."},
					},
				},
			},
		},
	}
	service := DispatchService{Paths: paths, RunStore: store}

	plan, err := service.validateStart(context.Background(), DispatchStartOptions{
		TaskID: taskItem.ID,
		Source: task.RepositorySource{
			Repository: repo,
		},
		Backend: fakeDispatchBackend{taskItem: taskItem},
	})
	if err != nil {
		t.Fatalf("validate start: %v", err)
	}

	if plan.followUp == nil {
		t.Fatalf("follow-up plan is nil")
	}
	if plan.followUp.targetKind != tasktarget.TargetMainSolo {
		t.Fatalf("follow-up target = %q, want %q", plan.followUp.targetKind, tasktarget.TargetMainSolo)
	}
	if plan.expected.Branch != "main" || plan.expected.WorktreePath != repoPath {
		t.Fatalf("expected target = %#v, want main repo root", plan.expected)
	}
}

func TestDispatchValidateStartRefusesInterruptedAutomatedBlockerDecision(t *testing.T) {
	paths := newDispatchTestPaths(t)
	repoPath := filepath.Join(canonicalTestPath(t, t.TempDir()), "repo")
	repo := task.Repository{
		ID:            "alpha",
		Name:          "Alpha",
		Path:          repoPath,
		DefaultBranch: "main",
		TaskIDPrefix:  "op",
	}
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   "main",
			task.MetadataWorktree: repoPath,
		},
	}
	store := fakeDispatchRunStore{
		state: taskstate.TaskState{
			Version: 2,
			RepoID:  repo.ID,
			TaskID:  taskItem.ID,
			Target: taskstate.TaskTarget{
				Branch:   "main",
				Worktree: repoPath,
			},
			Reviews: []taskstate.ReviewAttempt{
				{
					Attempt:                             1,
					Status:                              taskstate.ReviewStatusBlocked,
					Pipeline:                            "default",
					Step:                                "local-review",
					AutomatedBlockerDecisionInterrupted: true,
					Findings: []taskstate.ReviewFinding{
						{Type: taskstate.FindingTypeBlocking, Title: "Bug", Description: "Fix it."},
					},
				},
			},
		},
	}
	service := DispatchService{Paths: paths, RunStore: store}

	_, err := service.validateStart(context.Background(), DispatchStartOptions{
		TaskID: taskItem.ID,
		Source: task.RepositorySource{
			Repository: repo,
		},
		Backend: fakeDispatchBackend{taskItem: taskItem},
	})

	if err == nil || !strings.Contains(err.Error(), "interrupted automated blocker decisions") {
		t.Fatalf("validate error = %v, want interrupted blocker guidance", err)
	}
	if !strings.Contains(err.Error(), "run `orpheus task review op-1`") {
		t.Fatalf("validate error = %v, want task review guidance", err)
	}
}

func TestDispatchValidateStartRefusesUnkeptAutomatedBlockers(t *testing.T) {
	reviewAttempt := taskstate.ReviewAttempt{
		Attempt:  1,
		Status:   taskstate.ReviewStatusBlocked,
		Pipeline: "default",
		Step:     "unit",
		Steps: []taskstate.ReviewStep{{
			Kind: taskstate.ReviewStepKindCheck,
			Name: "unit",
		}},
		Findings: []taskstate.ReviewFinding{{
			Type:        taskstate.FindingTypeBlocking,
			Step:        "unit",
			Title:       "Check failed",
			Description: "Fix it.",
		}},
	}

	_, err := validateDispatchStartForReview(t, reviewAttempt)

	if err == nil || !strings.Contains(err.Error(), "automated blockers without an explicit keep decision") {
		t.Fatalf("validate error = %v, want explicit-keep guidance", err)
	}
	if !strings.Contains(err.Error(), "run `orpheus task review op-1`") {
		t.Fatalf("validate error = %v, want task review guidance", err)
	}
}

func TestDispatchValidateStartAllowsFollowUpForNormalizedFailedReview(t *testing.T) {
	paths := newDispatchTestPaths(t)
	repoPath := filepath.Join(canonicalTestPath(t, t.TempDir()), "repo")
	repo := task.Repository{
		ID: "alpha", Name: "Alpha", Path: repoPath, DefaultBranch: "main", TaskIDPrefix: "op",
	}
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch: "main", task.MetadataWorktree: repoPath,
		},
	}
	store := taskstate.NewStore(paths)
	run, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Branch: "main", Worktree: repoPath})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := store.FinishRun("alpha", "op-1", run.Attempt, taskstate.RunStatusSucceeded); err != nil {
		t.Fatalf("finish run: %v", err)
	}
	reviewAttempt, err := store.StartReview("alpha", "op-1")
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	if _, err := store.RecordReviewStep("alpha", "op-1", reviewAttempt.Attempt, taskstate.RecordReviewStepOptions{
		Kind: taskstate.ReviewStepKindCheck,
		Name: "local-review",
	}); err != nil {
		t.Fatalf("record review step: %v", err)
	}
	if _, err := store.RecordReviewFinding("alpha", "op-1", reviewAttempt.Attempt, taskstate.ReviewFinding{
		Type: taskstate.FindingTypeBlocking, Step: "local-review", Title: "Failed review blocker", Description: "Fix it.",
	}); err != nil {
		t.Fatalf("record review finding: %v", err)
	}
	if _, err := store.FinishReview("alpha", "op-1", reviewAttempt.Attempt, taskstate.ReviewStatusFailed); err != nil {
		t.Fatalf("finish failed review: %v", err)
	}
	if _, err := store.MarkReviewAutomatedBlockerDecisionKept("alpha", "op-1", reviewAttempt.Attempt); err != nil {
		t.Fatalf("record keep decision: %v", err)
	}
	if _, err := store.PrepareReviewForTargetedFollowUp("alpha", "op-1", reviewAttempt.Attempt); err != nil {
		t.Fatalf("normalize failed review for follow-up: %v", err)
	}

	service := DispatchService{Paths: paths, RunStore: store}
	plan, err := service.validateStart(context.Background(), DispatchStartOptions{
		TaskID: taskItem.ID, Source: task.RepositorySource{Repository: repo}, Backend: fakeDispatchBackend{taskItem: taskItem},
	})
	if err != nil {
		t.Fatalf("validate start: %v", err)
	}
	if plan.followUp == nil || plan.followUp.reviewAttempt != reviewAttempt.Attempt || len(plan.followUp.findingIndexes) != 1 || plan.followUp.findingIndexes[0] != 0 {
		t.Fatalf("follow-up plan = %#v, want normalized failed review finding 0", plan.followUp)
	}
}

func TestDispatchValidateStartAllowsManualBlockersWithoutKeepDecision(t *testing.T) {
	reviewAttempt := taskstate.ReviewAttempt{
		Attempt:  1,
		Status:   taskstate.ReviewStatusBlocked,
		Pipeline: "default",
		Step:     "inspect",
		Steps: []taskstate.ReviewStep{{
			Kind: taskstate.ReviewStepKindManual,
			Name: "inspect",
		}},
		Findings: []taskstate.ReviewFinding{{
			Type:        taskstate.FindingTypeBlocking,
			Step:        "inspect",
			Title:       "Manual issue",
			Description: "Fix it.",
		}},
	}

	plan, err := validateDispatchStartForReview(t, reviewAttempt)
	if err != nil {
		t.Fatalf("validate start: %v", err)
	}
	if plan.followUp == nil || len(plan.followUp.findingIndexes) != 1 || plan.followUp.findingIndexes[0] != 0 {
		t.Fatalf("follow-up plan = %#v, want finding 0", plan.followUp)
	}
}

func TestDispatchValidateStartAllowsKeptBudgetExhaustedAutomatedBlockers(t *testing.T) {
	reviewAttempt := taskstate.ReviewAttempt{
		Attempt:                      1,
		Status:                       taskstate.ReviewStatusBlocked,
		Pipeline:                     "default",
		Step:                         "agent-review",
		AutomatedBlockerDecisionKept: true,
		AutonomousBudgetExhausted:    true,
		Steps: []taskstate.ReviewStep{{
			Kind: taskstate.ReviewStepKindAgentReview,
			Name: "agent-review",
		}},
		Findings: []taskstate.ReviewFinding{{
			Type:        taskstate.FindingTypeBlocking,
			Step:        "agent-review",
			Title:       "Agent blocker",
			Description: "Fix it.",
		}},
	}

	plan, err := validateDispatchStartForReview(t, reviewAttempt)
	if err != nil {
		t.Fatalf("validate start: %v", err)
	}
	if plan.followUp == nil || len(plan.followUp.findingIndexes) != 1 || plan.followUp.findingIndexes[0] != 0 {
		t.Fatalf("follow-up plan = %#v, want finding 0", plan.followUp)
	}
}

func TestDispatchValidateStartRefusesAlreadyTargetedBlockedReview(t *testing.T) {
	paths := newDispatchTestPaths(t)
	repoPath := filepath.Join(canonicalTestPath(t, t.TempDir()), "repo")
	repo := task.Repository{
		ID:            "alpha",
		Name:          "Alpha",
		Path:          repoPath,
		DefaultBranch: "main",
		TaskIDPrefix:  "op",
	}
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   "main",
			task.MetadataWorktree: repoPath,
		},
	}
	store := fakeDispatchRunStore{
		state: taskstate.TaskState{
			Version: 2,
			RepoID:  repo.ID,
			TaskID:  taskItem.ID,
			Target: taskstate.TaskTarget{
				Branch:   "main",
				Worktree: repoPath,
			},
			Reviews: []taskstate.ReviewAttempt{
				{
					Attempt:  1,
					Status:   taskstate.ReviewStatusBlocked,
					Pipeline: "default",
					Step:     "local-review",
					Findings: []taskstate.ReviewFinding{
						{
							Type:                 taskstate.FindingTypeBlocking,
							Title:                "Bug",
							Description:          "Fix it.",
							TargetedByRunAttempt: 2,
						},
					},
				},
			},
		},
	}
	service := DispatchService{Paths: paths, RunStore: store}

	_, err := service.validateStart(context.Background(), DispatchStartOptions{
		TaskID: taskItem.ID,
		Source: task.RepositorySource{
			Repository: repo,
		},
		Backend: fakeDispatchBackend{taskItem: taskItem},
	})

	if err == nil || !strings.Contains(err.Error(), "run `orpheus task review op-1`") {
		t.Fatalf("validate error = %v, want task review guidance", err)
	}
}

func TestDispatchValidateStartRejectsMainModeAfterTargetLock(t *testing.T) {
	paths := newDispatchTestPaths(t)
	repoPath := filepath.Join(canonicalTestPath(t, t.TempDir()), "repo")
	repo := task.Repository{
		ID:            "alpha",
		Name:          "Alpha",
		Path:          repoPath,
		DefaultBranch: "main",
		TaskIDPrefix:  "op",
	}
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   "orpheus/op-1",
			task.MetadataWorktree: filepath.Join(paths.DataRoot, "repos", "alpha", "worktrees", "op-1"),
		},
	}
	store := fakeDispatchRunStore{
		state: taskstate.TaskState{
			Version: 2,
			RepoID:  repo.ID,
			TaskID:  taskItem.ID,
			Target: taskstate.TaskTarget{
				Branch:   taskItem.Metadata[task.MetadataBranch],
				Worktree: taskItem.Metadata[task.MetadataWorktree],
			},
			Runs: []taskstate.RunAttempt{
				{Attempt: 1, Status: taskstate.RunStatusFailed},
			},
		},
	}
	service := DispatchService{Paths: paths, RunStore: store}

	_, err := service.validateStart(context.Background(), DispatchStartOptions{
		TaskID:   taskItem.ID,
		Source:   task.RepositorySource{Repository: repo},
		Backend:  fakeDispatchBackend{taskItem: taskItem},
		MainMode: true,
	})

	if err == nil || !strings.Contains(err.Error(), "retry without --main") {
		t.Fatalf("validate error = %v, want --main rejection after target lock", err)
	}
}

func validateDispatchStartForReview(
	t *testing.T,
	reviewAttempt taskstate.ReviewAttempt,
) (dispatchStartPlan, error) {
	t.Helper()

	paths := newDispatchTestPaths(t)
	repoPath := filepath.Join(canonicalTestPath(t, t.TempDir()), "repo")
	repo := task.Repository{
		ID:            "alpha",
		Name:          "Alpha",
		Path:          repoPath,
		DefaultBranch: "main",
		TaskIDPrefix:  "op",
	}
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   "main",
			task.MetadataWorktree: repoPath,
		},
	}
	store := fakeDispatchRunStore{
		state: taskstate.TaskState{
			Version: 2,
			RepoID:  repo.ID,
			TaskID:  taskItem.ID,
			Target: taskstate.TaskTarget{
				Branch:   "main",
				Worktree: repoPath,
			},
			Reviews: []taskstate.ReviewAttempt{reviewAttempt},
		},
	}
	service := DispatchService{Paths: paths, RunStore: store}

	return service.validateStart(context.Background(), DispatchStartOptions{
		TaskID: taskItem.ID,
		Source: task.RepositorySource{
			Repository: repo,
		},
		Backend: fakeDispatchBackend{taskItem: taskItem},
	})
}

type fakeDispatchBackend struct {
	taskItem task.Task
}

func (b fakeDispatchBackend) Get(context.Context, string) (task.Task, error) {
	return b.taskItem, nil
}

func (b fakeDispatchBackend) List(context.Context) ([]task.Task, error) {
	return []task.Task{b.taskItem}, nil
}

func (b fakeDispatchBackend) MarkInProgress(context.Context, string, string, string) error {
	return nil
}

type fakeDispatchRunStore struct {
	state taskstate.TaskState
}

func (s fakeDispatchRunStore) Path(repoID, taskID string) (string, error) {
	return filepath.Join(repoID, taskID+".yaml"), nil
}

func (s fakeDispatchRunStore) Load(string, string) (taskstate.TaskState, error) {
	return s.state, nil
}

func (s fakeDispatchRunStore) LatestRun(string, string) (taskstate.RunAttempt, bool, error) {
	run, ok := taskstate.LatestRun(s.state)
	return run, ok, nil
}

func (s fakeDispatchRunStore) ActiveRun(string, string) (taskstate.RunAttempt, bool, error) {
	run, ok := taskstate.ActiveRun(s.state)
	return run, ok, nil
}

func (s fakeDispatchRunStore) RecordSetupEvent(string, string, taskstate.EventType, taskstate.SetupEventOptions) (taskstate.Event, error) {
	return taskstate.Event{}, errors.New("not implemented")
}

func (s fakeDispatchRunStore) StartRun(string, string, taskstate.StartRunOptions) (taskstate.RunAttempt, error) {
	return taskstate.RunAttempt{}, errors.New("not implemented")
}

func (s fakeDispatchRunStore) RecordRunUsage(string, string, int, taskstate.RecordRunUsageOptions) (taskstate.RunAttempt, error) {
	return taskstate.RunAttempt{}, errors.New("not implemented")
}

func (s fakeDispatchRunStore) TargetReviewFindings(string, string, int, []int, int) (taskstate.ReviewAttempt, error) {
	return taskstate.ReviewAttempt{}, errors.New("not implemented")
}

func (s fakeDispatchRunStore) FinishRun(string, string, int, taskstate.RunStatus) (taskstate.RunAttempt, error) {
	return taskstate.RunAttempt{}, errors.New("not implemented")
}

func (s fakeDispatchRunStore) FailRunStart(string, string, int, error) (taskstate.RunAttempt, error) {
	return taskstate.RunAttempt{}, errors.New("not implemented")
}

func newDispatchTestPaths(t *testing.T) state.Paths {
	t.Helper()

	root := t.TempDir()
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	return paths
}
