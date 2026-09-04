package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestDispatchPrepareFollowUpResumeHonorsStrictFeatureFlag(t *testing.T) {
	workdir := testutil.CanonicalTempDir(t)
	sessionPath := filepath.Join(testutil.CanonicalTempDir(t), "session.jsonl")
	content := `{"type":"session","version":3,"id":"session-1","timestamp":"2026-07-07T10:00:00Z","cwd":"` + workdir + `"}
{"type":"message","id":"assistant","timestamp":"2026-07-07T10:00:01Z","message":{"role":"assistant","usage":{"input":10,"output":5,"totalTokens":15}}}
`
	if err := os.WriteFile(sessionPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	store := fakeDispatchRunStore{state: taskstate.TaskState{Runs: []taskstate.RunAttempt{{
		Attempt: 1,
		Status:  taskstate.RunStatusSucceeded,
		Completion: &taskstate.Completion{
			Summary: "implemented",
		},
		Execution: taskstate.AgentExecution{
			Purpose: taskstate.AgentExecutionPurposeImplementation,
			Profile: "implementer",
			Harness: "pi",
			Session: &taskstate.AgentSession{ID: "session-1", LogPath: sessionPath},
		},
	}}}}
	service := DispatchService{RunStore: store}
	plan := dispatchStartPlan{expected: gitmeta.TaskWorktreeSetupResult{WorktreePath: workdir}}
	command := NewDispatchCommand(agent.CommandSnapshot{
		AgentName:   "implementer",
		Command:     "pi",
		Harness:     "pi",
		Args:        []string{"--model", "gpt-5", "prompt"},
		Interactive: true,
	})
	opts := DispatchStartOptions{TaskID: "op-1", Source: task.RepositorySource{Repository: task.Repository{ID: "alpha"}}}

	t.Setenv("ORPHEUS_RESUME_SESSIONS", "0")
	fresh, launch, err := service.prepareFollowUpResume(opts, plan, command)
	if err != nil {
		t.Fatalf("prepare disabled follow-up: %v", err)
	}
	if launch.Mode != taskstate.AgentLaunchFresh || !equalStrings(fresh.Args, command.Args) {
		t.Fatalf("disabled result = %#v, %#v", fresh.Args, launch)
	}
	if !fresh.Interactive {
		t.Fatal("disabled follow-up lost interactive launch behavior")
	}

	t.Setenv("ORPHEUS_RESUME_SESSIONS", "1")
	resumed, launch, err := service.prepareFollowUpResume(opts, plan, command)
	if err != nil {
		t.Fatalf("prepare enabled follow-up: %v", err)
	}
	if launch.Mode != taskstate.AgentLaunchResumed || launch.SourceRunAttempt != 1 {
		t.Fatalf("enabled launch = %#v", launch)
	}
	if len(resumed.Args) < 3 || resumed.Args[0] != "--session" || resumed.Args[1] != sessionPath {
		t.Fatalf("resumed args = %#v", resumed.Args)
	}
	if !resumed.Interactive {
		t.Fatal("resumed follow-up lost interactive launch behavior")
	}
}

func TestDispatchPrepareFollowUpResumeUsesActiveCodexHome(t *testing.T) {
	workdir := testutil.CanonicalTempDir(t)
	sourceHome := testutil.CanonicalTempDir(t)
	sessionPath := filepath.Join(sourceHome, "sessions", "source.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatalf("create source sessions: %v", err)
	}
	content := `{"timestamp":"2026-07-07T10:00:00Z","type":"session_meta","payload":{"session_id":"session-1","id":"session-1","timestamp":"2026-07-07T10:00:00Z","cwd":"` + workdir + `","model":"gpt-5"}}
{"timestamp":"2026-07-07T10:01:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}}
`
	if err := os.WriteFile(sessionPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write source session: %v", err)
	}
	store := fakeDispatchRunStore{state: taskstate.TaskState{Runs: []taskstate.RunAttempt{{
		Attempt: 1, Status: taskstate.RunStatusSucceeded, Completion: &taskstate.Completion{Summary: "implemented"},
		Execution: taskstate.AgentExecution{
			Purpose: taskstate.AgentExecutionPurposeImplementation, Profile: "implementer", Harness: "codex",
			Session: &taskstate.AgentSession{ID: "session-1", LogPath: sessionPath},
		},
	}}}}
	service := DispatchService{RunStore: store}
	plan := dispatchStartPlan{expected: gitmeta.TaskWorktreeSetupResult{WorktreePath: workdir}}
	opts := DispatchStartOptions{TaskID: "op-1", Source: task.RepositorySource{Repository: task.Repository{ID: "alpha"}}}
	activeHome := testutil.CanonicalTempDir(t)
	if err := os.MkdirAll(filepath.Join(activeHome, "sessions"), 0o755); err != nil {
		t.Fatalf("create active sessions: %v", err)
	}
	t.Setenv("CODEX_HOME", activeHome)
	t.Setenv("ORPHEUS_RESUME_SESSIONS", "1")

	for _, nonInteractive := range []bool{false, true} {
		name := "interactive"
		args := []string{"--model", "gpt-5", "prompt"}
		if nonInteractive {
			name = "exec"
			args = append([]string{"exec"}, args...)
		}
		t.Run(name, func(t *testing.T) {
			command := NewDispatchCommand(agent.CommandSnapshot{
				AgentName: "implementer", Command: "codex", Harness: "codex", Args: args,
			})
			fresh, launch, err := service.prepareFollowUpResume(opts, plan, command)
			if err != nil {
				t.Fatalf("prepare Codex follow-up: %v", err)
			}
			if launch.Mode != taskstate.AgentLaunchFresh || !equalStrings(fresh.Args, command.Args) {
				t.Fatalf("changed CODEX_HOME result = %#v, %#v", fresh.Args, launch)
			}
			if !strings.Contains(launch.FallbackReason, "active Codex sessions root") {
				t.Fatalf("fallback reason = %q", launch.FallbackReason)
			}
		})
	}
}

func equalStrings(left []string, right []string) bool {
	return strings.Join(left, "\x00") == strings.Join(right, "\x00")
}

func TestDispatchValidateStartInfersBlockedReviewFollowUpTarget(t *testing.T) {
	paths := newDispatchTestPaths(t)
	repoPath := filepath.Join(testutil.CanonicalTempDir(t), "repo")
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
			Target: taskstate.GitFacts{
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
	repoPath := filepath.Join(testutil.CanonicalTempDir(t), "repo")
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
			Target: taskstate.GitFacts{
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
	if !strings.Contains(err.Error(), "run `orpheus task run op-1`") {
		t.Fatalf("validate error = %v, want task run guidance", err)
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
	if !strings.Contains(err.Error(), "run `orpheus task run op-1`") {
		t.Fatalf("validate error = %v, want task run guidance", err)
	}
}

func TestDispatchValidateStartAllowsFollowUpForNormalizedFailedReview(t *testing.T) {
	paths := newDispatchTestPaths(t)
	repoPath := filepath.Join(testutil.CanonicalTempDir(t), "repo")
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

func TestDispatchValidateStartScopesEligibleAdvisoriesWithBlockers(t *testing.T) {
	reviewAttempt := taskstate.ReviewAttempt{
		Attempt:  1,
		Status:   taskstate.ReviewStatusBlocked,
		Pipeline: "default",
		Step:     "inspect",
		Steps: []taskstate.ReviewStep{
			{Kind: taskstate.ReviewStepKindManual, Name: "inspect"},
			{Kind: taskstate.ReviewStepKindAgentReview, Name: "interrupted", Execution: &taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusInterrupted, InterruptionReason: "supervisor disappeared"}},
		},
		Findings: []taskstate.ReviewFinding{
			{Type: taskstate.FindingTypeBlocking, Step: "inspect", Title: "Must fix", Description: "Required repair."},
			{Type: taskstate.FindingTypeAdvisory, Step: "inspect", Title: "Ordinary advisory", Description: "Could improve."},
			{Type: taskstate.FindingTypeAdvisory, Step: "inspect", Title: "Downgraded advisory", Description: "Safe to defer.", DowngradeReason: "Not required."},
			{Type: taskstate.FindingTypeAdvisory, Step: "inspect", Title: "Waived advisory", Description: "Excluded.", Waiver: "Accepted."},
			{Type: taskstate.FindingTypeAdvisory, Step: "inspect", Title: "Manual advisory", Description: "Excluded.", AddressedManually: "Done."},
			{Type: taskstate.FindingTypeSeparateTask, Step: "inspect", Title: "Separate work", Description: "Excluded."},
			{Type: taskstate.FindingTypeAdvisory, Step: "interrupted", Title: "Audit-only advisory", Description: "Excluded."},
		},
	}

	plan, err := validateDispatchStartForReview(t, reviewAttempt)
	if err != nil {
		t.Fatalf("validate start: %v", err)
	}
	if plan.followUp == nil || !slices.Equal(plan.followUp.findingIndexes, []int{0}) || !slices.Equal(plan.followUp.advisoryFindingIndexes, []int{1, 2}) {
		t.Fatalf("follow-up plan = %#v, want required [0] and advisory [1 2]", plan.followUp)
	}
}

func TestDispatchValidateStartDoesNotStartForAdvisoriesOnly(t *testing.T) {
	reviewAttempt := taskstate.ReviewAttempt{
		Attempt: 1, Status: taskstate.ReviewStatusBlocked, Pipeline: "default", Step: "inspect",
		Steps:    []taskstate.ReviewStep{{Kind: taskstate.ReviewStepKindManual, Name: "inspect"}},
		Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeAdvisory, Step: "inspect", Title: "Only advisory", Description: "No implementation run."}},
	}

	_, err := validateDispatchStartForReview(t, reviewAttempt)
	if err == nil || !strings.Contains(err.Error(), "no untargeted blocking findings") {
		t.Fatalf("validate error = %v, want advisory-only review rejection", err)
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

func TestDispatchValidateStartRetriesFailedFollowUpTargets(t *testing.T) {
	reviewAttempt := taskstate.ReviewAttempt{
		Attempt:  1,
		Status:   taskstate.ReviewStatusBlocked,
		Pipeline: "default",
		Step:     "inspect",
		Steps:    []taskstate.ReviewStep{{Kind: taskstate.ReviewStepKindManual, Name: "inspect"}},
		Findings: []taskstate.ReviewFinding{
			{
				Type:                 taskstate.FindingTypeBlocking,
				Step:                 "inspect",
				Title:                "Bug",
				Description:          "Fix it.",
				TargetedByRunAttempt: 2,
			},
			{Type: taskstate.FindingTypeAdvisory, Step: "inspect", Title: "Keep considering", Description: "Best effort."},
		},
	}

	plan, err := validateDispatchStartForReviewWithRuns(t, reviewAttempt, []taskstate.RunAttempt{{
		Attempt:        2,
		Status:         taskstate.RunStatusFailed,
		ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{0}, AdvisoryFindingIndexes: []int{1}},
	}})
	if err != nil {
		t.Fatalf("validate retry start: %v", err)
	}
	if plan.followUp == nil || len(plan.followUp.findingIndexes) != 1 || plan.followUp.findingIndexes[0] != 0 || !slices.Equal(plan.followUp.advisoryFindingIndexes, []int{1}) {
		t.Fatalf("follow-up plan = %#v, want failed target and preserved advisory scope", plan.followUp)
	}
}

func TestDispatchValidateStartRefusesAlreadyTargetedBlockedReview(t *testing.T) {
	paths := newDispatchTestPaths(t)
	repoPath := filepath.Join(testutil.CanonicalTempDir(t), "repo")
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
			Target: taskstate.GitFacts{
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

	if err == nil || !strings.Contains(err.Error(), "rerun `orpheus task run op-1`") {
		t.Fatalf("validate error = %v, want task run guidance", err)
	}
}

func TestDispatchValidateStartRejectsMainModeAfterTargetLock(t *testing.T) {
	paths := newDispatchTestPaths(t)
	repoPath := filepath.Join(testutil.CanonicalTempDir(t), "repo")
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
			Target: taskstate.GitFacts{
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

	if err == nil || !strings.Contains(err.Error(), "--main is no longer supported") {
		t.Fatalf("validate error = %v, want --main migration guidance", err)
	}
}

func validateDispatchStartForReview(
	t *testing.T,
	reviewAttempt taskstate.ReviewAttempt,
) (dispatchStartPlan, error) {
	t.Helper()
	return validateDispatchStartForReviewWithRuns(t, reviewAttempt, nil)
}

func validateDispatchStartForReviewWithRuns(
	t *testing.T,
	reviewAttempt taskstate.ReviewAttempt,
	runs []taskstate.RunAttempt,
) (dispatchStartPlan, error) {
	t.Helper()

	paths := newDispatchTestPaths(t)
	repoPath := filepath.Join(testutil.CanonicalTempDir(t), "repo")
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
			Target: taskstate.GitFacts{
				Branch:   "main",
				Worktree: repoPath,
			},
			Runs:    runs,
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

func TestDispatchValidateStartRejectsMissingTemplateValueBeforeSetup(t *testing.T) {
	paths := newDispatchTestPaths(t)
	repo := task.Repository{ID: "alpha", Name: "Alpha", Path: filepath.Join(testutil.CanonicalTempDir(t), "repo"), DefaultBranch: "main", BranchTemplate: "feature/{{external_ref}}"}
	taskItem := task.Task{ID: "op-1", Title: "Missing reference", Status: task.StatusOpen}
	service := DispatchService{Paths: paths, RunStore: fakeDispatchRunStore{}}

	_, err := service.validateStart(context.Background(), DispatchStartOptions{
		TaskID: taskItem.ID, Source: task.RepositorySource{Repository: repo}, Backend: fakeDispatchBackend{taskItem: taskItem},
	})
	if err == nil || !strings.Contains(err.Error(), "external reference") {
		t.Fatalf("validate start error = %v, want missing external reference", err)
	}
}

func TestDispatchValidateStartRejectsReservedBranchTemplateBeforeSetup(t *testing.T) {
	paths := newDispatchTestPaths(t)
	repo := task.Repository{
		ID: "alpha", Name: "Alpha", Path: filepath.Join(testutil.CanonicalTempDir(t), "repo"), DefaultBranch: "main",
		BranchTemplate: "HEAD",
	}
	taskItem := task.Task{ID: "op-1", Status: task.StatusOpen}
	service := DispatchService{Paths: paths, RunStore: fakeDispatchRunStore{}}

	_, err := service.validateStart(context.Background(), DispatchStartOptions{
		TaskID: taskItem.ID, Source: task.RepositorySource{Repository: repo}, Backend: fakeDispatchBackend{taskItem: taskItem},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid Git ref syntax") {
		t.Fatalf("validate start error = %v, want reserved branch rejection", err)
	}
}

func TestDispatchValidateStartRejectsDefaultBranchTemplateForAllDispatchModes(t *testing.T) {
	paths := newDispatchTestPaths(t)
	tests := []struct {
		name         string
		template     string
		taskItem     task.Task
		repoRootMode bool
	}{
		{
			name:     "worktree literal",
			template: "main",
			taskItem: task.Task{ID: "op-1", Status: task.StatusOpen},
		},
		{
			name:         "repo root task title",
			template:     "{{task_title}}",
			taskItem:     task.Task{ID: "op-1", Title: "main", Status: task.StatusOpen},
			repoRootMode: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := task.Repository{
				ID: "alpha", Name: "Alpha", Path: filepath.Join(testutil.CanonicalTempDir(t), "repo"), DefaultBranch: "main",
				BranchTemplate: tt.template,
			}
			service := DispatchService{Paths: paths, RunStore: fakeDispatchRunStore{}}
			_, err := service.validateStart(context.Background(), DispatchStartOptions{
				TaskID: tt.taskItem.ID, Source: task.RepositorySource{Repository: repo},
				Backend: fakeDispatchBackend{taskItem: tt.taskItem}, RepoRootMode: tt.repoRootMode,
			})
			if err == nil || !strings.Contains(err.Error(), "matches registered default branch") {
				t.Fatalf("validate start error = %v, want default branch rejection", err)
			}
		})
	}
}

func TestDispatchValidateStartRejectsTaskBranchOwnedByAnotherTask(t *testing.T) {
	paths := newDispatchTestPaths(t)
	repoPath := filepath.Join(testutil.CanonicalTempDir(t), "repo")
	repo := task.Repository{ID: "alpha", Name: "Alpha", Path: repoPath, DefaultBranch: "main", BranchTemplate: "feature/{{task_title}}"}
	taskItem := task.Task{ID: "op-1", Title: "Same title", Status: task.StatusOpen}
	other := task.Task{ID: "op-2", Status: task.StatusInProgress, Metadata: task.Metadata{task.MetadataBranch: "feature/Same-title"}}
	service := DispatchService{Paths: paths, RunStore: fakeDispatchRunStore{}}

	_, err := service.validateStart(context.Background(), DispatchStartOptions{
		TaskID: taskItem.ID, Source: task.RepositorySource{Repository: repo}, Backend: fakeDispatchBackend{taskItem: taskItem, tasks: []task.Task{taskItem, other}},
	})
	if err == nil || !strings.Contains(err.Error(), "already recorded for task op-2") {
		t.Fatalf("validate start error = %v, want collision", err)
	}
}

func TestDispatchValidateStartRejectsCompatibilityBranchCollisionAfterNormalization(t *testing.T) {
	paths := newDispatchTestPaths(t)
	repoPath := filepath.Join(testutil.CanonicalTempDir(t), "repo")
	repo := task.Repository{ID: "alpha", Name: "Alpha", Path: repoPath, DefaultBranch: "main"}
	taskItem := task.Task{ID: "op.1", Status: task.StatusOpen}
	other := task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-1"}}
	service := DispatchService{Paths: paths, RunStore: fakeDispatchRunStore{}}

	_, err := service.validateStart(context.Background(), DispatchStartOptions{
		TaskID: taskItem.ID, Source: task.RepositorySource{Repository: repo}, Backend: fakeDispatchBackend{taskItem: taskItem, tasks: []task.Task{taskItem, other}},
	})
	if err == nil || !strings.Contains(err.Error(), "already recorded for task op-1") {
		t.Fatalf("validate start error = %v, want compatibility branch collision", err)
	}
}

func TestDispatchValidateStartPreservesRecordedBranchAfterTemplateChange(t *testing.T) {
	paths := newDispatchTestPaths(t)
	repoPath := filepath.Join(testutil.CanonicalTempDir(t), "repo")
	worktree := filepath.Join(paths.DataRoot, "repos", "alpha", "worktrees", "op-1")
	repo := task.Repository{ID: "alpha", Name: "Alpha", Path: repoPath, DefaultBranch: "main", BranchTemplate: "changed/{{task_title}}"}
	taskItem := task.Task{ID: "op-1", Title: "New template", Status: task.StatusInProgress, Metadata: task.Metadata{task.MetadataBranch: "recorded/branch", task.MetadataWorktree: worktree}}
	store := fakeDispatchRunStore{state: taskstate.TaskState{Target: taskstate.GitFacts{Branch: "recorded/branch", Worktree: worktree}}}
	service := DispatchService{Paths: paths, RunStore: store}

	plan, err := service.validateStart(context.Background(), DispatchStartOptions{
		TaskID: taskItem.ID, Source: task.RepositorySource{Repository: repo}, Backend: fakeDispatchBackend{taskItem: taskItem},
	})
	if err != nil {
		t.Fatalf("validate start error = %v", err)
	}
	if plan.expected.Branch != "recorded/branch" {
		t.Fatalf("expected branch = %q, want recorded branch", plan.expected.Branch)
	}
}

func TestDispatchValidateStartRecoversBackendRecordedBranchWithoutLocalGitFacts(t *testing.T) {
	paths := newDispatchTestPaths(t)
	repoPath := filepath.Join(testutil.CanonicalTempDir(t), "repo")
	worktree := filepath.Join(paths.DataRoot, "repos", "alpha", "worktrees", "op-1")
	repo := task.Repository{ID: "alpha", Name: "Alpha", Path: repoPath, DefaultBranch: "main", BranchTemplate: "changed/{{external_ref}}"}
	taskItem := task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{task.MetadataBranch: "recorded/branch", task.MetadataWorktree: worktree}}
	service := DispatchService{Paths: paths, RunStore: fakeDispatchRunStore{}}

	plan, err := service.validateStart(context.Background(), DispatchStartOptions{
		TaskID: taskItem.ID, Source: task.RepositorySource{Repository: repo}, Backend: fakeDispatchBackend{taskItem: taskItem},
	})
	if err != nil {
		t.Fatalf("validate start error = %v", err)
	}
	if plan.expected.Branch != "recorded/branch" || plan.expected.WorktreePath != worktree {
		t.Fatalf("expected target = %#v, want recorded backend target", plan.expected)
	}
}

type fakeDispatchBackend struct {
	taskItem task.Task
	tasks    []task.Task
}

func (b fakeDispatchBackend) Get(context.Context, string) (task.Task, error) {
	return b.taskItem, nil
}

func (b fakeDispatchBackend) List(context.Context) ([]task.Task, error) {
	if b.tasks != nil {
		return b.tasks, nil
	}
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

func (s fakeDispatchRunStore) RecordRunChildPID(string, string, int, int) (taskstate.RunAttempt, error) {
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

	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	return paths
}
