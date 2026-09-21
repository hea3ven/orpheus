//go:build integration

package cli_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/registry"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowDoctorRecoversCodexUsageForImplementationAndReviewAgent(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	paths := fixture.paths
	repoDir := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repoDir: {tasks: []taskmodel.Task{{ID: "op-1", Title: "Doctor", Status: taskmodel.StatusInProgress, IssueType: taskmodel.IssueTypeTask}}}})

	store := taskstate.NewStoreWithClock(paths, clockSequence(
		time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 10, 2, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 10, 10, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 10, 13, 0, 0, time.UTC),
	))
	run, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:    "codex-profile",
		Profile:  "codex-profile",
		Harness:  "codex",
		Command:  "codex",
		Args:     []string{"exec"},
		Branch:   "main",
		Worktree: repoDir,
	})
	must.NoError(err)
	_, err = store.FinishRun("alpha", "op-1", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)

	reviewAttempt, err := store.StartReviewWithOptions("alpha", "op-1", taskstate.StartReviewOptions{
		Pipeline: "standard",
		Step:     "ai-review",
	})
	must.NoError(err)
	_, err = store.RecordReviewStep("alpha", "op-1", reviewAttempt.Attempt, taskstate.RecordReviewStepOptions{
		Kind: "agent_review",
		Name: "ai-review",
		Execution: &taskstate.AgentExecution{
			Purpose:   taskstate.AgentExecutionPurposeReview,
			Status:    taskstate.RunStatusRunning,
			Agent:     "reviewer",
			Profile:   "reviewer",
			Harness:   "codex",
			Command:   "codex",
			Args:      []string{"exec", "review"},
			StartedAt: time.Date(2026, 7, 7, 10, 10, 0, 0, time.UTC),
		},
	})
	must.NoError(err)
	_, err = store.FinishReviewStepExecution(
		"alpha",
		"op-1",
		reviewAttempt.Attempt,
		"ai-review",
		taskstate.FinishReviewStepExecutionOptions{
			Status:     taskstate.RunStatusSucceeded,
			FinishedAt: time.Date(2026, 7, 7, 10, 12, 0, 0, time.UTC),
			UsageCapture: taskstate.AgentUsageCapture{
				Status: taskstate.UsageCaptureUnknown,
				Reason: "no_matching_codex_session",
			},
		},
	)
	must.NoError(err)
	_, err = store.FinishReview("alpha", "op-1", reviewAttempt.Attempt, taskstate.ReviewStatusPassed)
	must.NoError(err)

	withDoctorCapture(t, fixture, func(opts agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
		require.Equal(t, "codex", opts.Harness)
		require.Equal(t, []string{repoDir}, opts.ExecutionDirs)
		switch opts.StartedAt.Format("15:04") {
		case "10:00":
			return doctorCapturedUsage("codex", "run-session", 190)
		case "10:10":
			return doctorCapturedUsage("codex", "review-session", 50)
		default:
			t.Fatalf("unexpected capture start %v", opts.StartedAt)
			return taskstate.RecordRunUsageOptions{}
		}
	})

	stdout, stderr := fixture.mustExecute("doctor")
	is.Empty(stderr)
	is.Contains(stdout, "would_recover")
	is.Contains(stdout, "run-session")
	is.Contains(stdout, "review-session")
	is.NotContains(stdout, "wrong-cwd-session")
	is.Contains(stdout, "CHECKED")

	loaded, err := store.Load("alpha", "op-1")
	must.NoError(err)
	is.Nil(loaded.Runs[0].Execution.Usage)
	is.Nil(loaded.Reviews[0].Steps[0].Execution.Usage)

	stdout, stderr = fixture.mustExecute("doctor", "--fix")
	is.Empty(stderr)
	is.Contains(stdout, "recovered")

	loaded, err = store.Load("alpha", "op-1")
	must.NoError(err)
	must.NotNil(loaded.Runs[0].Execution.Session)
	must.NotNil(loaded.Runs[0].Execution.Usage)
	is.Equal("run-session", loaded.Runs[0].Execution.Session.ID)
	is.NotEmpty(loaded.Runs[0].Execution.Session.LogPath)
	is.Equal("gpt-5", loaded.Runs[0].Execution.Model)
	is.Equal(190, loaded.Runs[0].Execution.Usage.TotalTokens)
	is.Equal(taskstate.UsageCaptureCaptured, loaded.Runs[0].Execution.UsageCapture.Status)
	reviewExecution := loaded.Reviews[0].Steps[0].Execution
	must.NotNil(reviewExecution)
	must.NotNil(reviewExecution.Session)
	must.NotNil(reviewExecution.Usage)
	is.Equal("review-session", reviewExecution.Session.ID)
	is.NotEmpty(reviewExecution.Session.LogPath)
	is.Equal(50, reviewExecution.Usage.TotalTokens)
	is.Equal(taskstate.UsageCaptureCaptured, reviewExecution.UsageCapture.Status)

	statsOut, statsErr := fixture.mustExecute("task", "stats", "op-1")
	is.Empty(statsErr)
	is.Contains(statsOut, "total=190")
	is.Contains(statsOut, "total=50")
	is.Contains(statsOut, "UNKNOWN_USAGE")
	is.Contains(statsOut, "combined")
}

func TestIntegrationWorkflowDoctorDoesNotOverwriteExistingCodexCostWhenRecoveringSession(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	paths := fixture.paths
	repoDir := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")

	startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	store := taskstate.NewStoreWithClock(paths, func() time.Time { return startedAt })
	run, err := store.StartRun("alpha", "op-immutable-cost", taskstate.StartRunOptions{
		Harness: "codex", Model: "gpt-5", Command: "codex", Branch: "main", Worktree: repoDir,
	})
	must.NoError(err)
	oldCost := taskstate.AgentUsageCost{
		Kind:           agent.UsageCostKindEstimatedAPIEquivalent,
		Currency:       "USD",
		AmountMicroUSD: 42,
		Pricing: &taskstate.AgentUsagePricing{
			Provider: "openai", Model: "gpt-5", ServiceTier: "standard", Source: "old pricing snapshot",
		},
	}
	_, err = store.RecordRunUsage("alpha", "op-immutable-cost", run.Attempt, taskstate.RecordRunUsageOptions{
		Model: "gpt-5", Usage: &taskstate.AgentUsage{InputTokens: 100, OutputTokens: 50}, UsageCost: &oldCost,
	})
	must.NoError(err)

	supplyDoctorUsage(t, fixture, "codex", repoDir, doctorCapturedUsage("codex", "replacement-session", 100))

	stdout, stderr := fixture.mustExecute("doctor", "--fix")
	is.Empty(stderr)
	is.Contains(stdout, "recovered")
	is.Contains(stdout, "$0.000042")
	loaded, err := store.Load("alpha", "op-immutable-cost")
	must.NoError(err)
	must.NotNil(loaded.Runs[0].Execution.Session)
	must.NotNil(loaded.Runs[0].Execution.UsageCost)
	is.Equal(int64(42), loaded.Runs[0].Execution.UsageCost.AmountMicroUSD)
	must.NotNil(loaded.Runs[0].Execution.UsageCost.Pricing)
	is.Equal("old pricing snapshot", loaded.Runs[0].Execution.UsageCost.Pricing.Source)
}

func TestIntegrationWorkflowDoctorLeavesTotalOnlyCodexUsageCostUnknown(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	paths := fixture.paths
	repoDir := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")

	store := taskstate.NewStoreWithClock(paths, func() time.Time {
		return time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	})
	run, err := store.StartRun("alpha", "op-total-only-cost", taskstate.StartRunOptions{
		Harness: "codex", Model: "gpt-5", Command: "codex", Branch: "main", Worktree: repoDir,
	})
	must.NoError(err)
	_, err = store.RecordRunUsage("alpha", "op-total-only-cost", run.Attempt, taskstate.RecordRunUsageOptions{
		Model: "gpt-5", Usage: &taskstate.AgentUsage{TotalTokens: 100},
	})
	must.NoError(err)

	rejectDoctorCapture(t, fixture)

	stdout, stderr := fixture.mustExecute("doctor", "--fix")
	is.Empty(stderr)
	is.Contains(stdout, agent.UsageCostUnknownBillableUsageMissing)
	loaded, err := store.Load("alpha", "op-total-only-cost")
	must.NoError(err)
	is.Nil(loaded.Runs[0].Execution.UsageCost)
}

func TestIntegrationWorkflowDoctorStampsStoredCodexCostsWithoutSessionLogRecorrelation(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	paths := fixture.paths
	repoDir := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")

	store := taskstate.NewStoreWithClock(paths, func() time.Time {
		return time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	})
	usage := taskstate.AgentUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}

	run, err := store.StartRun("alpha", "op-stored-cost", taskstate.StartRunOptions{
		Harness: "codex", Model: "gpt-5", Command: "codex", Branch: "main", Worktree: repoDir,
	})
	must.NoError(err)
	_, err = store.RecordRunUsage("alpha", "op-stored-cost", run.Attempt, taskstate.RecordRunUsageOptions{
		Model: "gpt-5", Usage: &usage,
	})
	must.NoError(err)

	review, err := store.StartReviewWithOptions("alpha", "op-stored-cost", taskstate.StartReviewOptions{
		Pipeline: "standard", Step: "ai-review",
	})
	must.NoError(err)
	_, err = store.RecordReviewStep("alpha", "op-stored-cost", review.Attempt, taskstate.RecordReviewStepOptions{
		Kind: "agent_review", Name: "ai-review", Execution: &taskstate.AgentExecution{
			Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning,
			Harness: "codex", Model: "gpt-5", Command: "codex", StartedAt: time.Date(2026, 7, 7, 10, 1, 0, 0, time.UTC),
		},
	})
	must.NoError(err)
	_, err = store.FinishReviewStepExecution("alpha", "op-stored-cost", review.Attempt, "ai-review", taskstate.FinishReviewStepExecutionOptions{
		Status: taskstate.RunStatusSucceeded, FinishedAt: time.Date(2026, 7, 7, 10, 2, 0, 0, time.UTC), Model: "gpt-5", Usage: &usage,
	})
	must.NoError(err)

	syncOpts := taskstate.SyncConflictResolutionEventOptions{
		Execution:     taskstate.AgentExecution{Harness: "codex", Model: "gpt-5", Command: "codex"},
		Branch:        "main",
		DefaultBranch: "main",
		Worktree:      repoDir,
	}
	_, err = store.RecordSyncConflictResolutionStarted("alpha", "op-stored-cost", syncOpts)
	must.NoError(err)
	syncOpts.Usage = taskstate.RecordRunUsageOptions{Model: "gpt-5", Usage: &usage}
	_, err = store.RecordSyncConflictResolutionFinished("alpha", "op-stored-cost", syncOpts)
	must.NoError(err)

	rejectDoctorCapture(t, fixture)

	stdout, stderr := fixture.mustExecute("doctor", "--fix")
	is.Empty(stderr)
	is.Contains(stdout, "recovered")
	is.NotContains(stdout, "codex_home_unavailable")
	loaded, err := store.Load("alpha", "op-stored-cost")
	must.NoError(err)
	assertDoctorCodexCostSnapshot(t, loaded.Runs[0].Execution.UsageCost)
	assertDoctorCodexCostSnapshot(t, loaded.Reviews[0].Steps[0].Execution.UsageCost)
	for _, event := range loaded.Events {
		if event.Type == taskstate.EventSyncConflictFinished {
			must.NotNil(event.Execution)
			assertDoctorCodexCostSnapshot(t, event.Execution.UsageCost)
			return
		}
	}
	t.Fatal("missing finished sync-conflict execution")
}

func TestIntegrationWorkflowDoctorBackfillSelectsPricingByExecutionStart(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	paths := fixture.paths
	repoDir := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")

	startedAt := time.Date(2026, 7, 29, 23, 59, 59, 0, time.UTC)
	store := taskstate.NewStoreWithClock(paths, func() time.Time { return startedAt })
	run, err := store.StartRun("alpha", "op-effective-price", taskstate.StartRunOptions{
		Harness: "codex", Model: "gpt-5.6-luna", Command: "codex", Branch: "main", Worktree: repoDir,
	})
	must.NoError(err)
	_, err = store.RecordRunUsage("alpha", "op-effective-price", run.Attempt, taskstate.RecordRunUsageOptions{
		Model: "gpt-5.6-luna", Usage: &taskstate.AgentUsage{InputTokens: 1_000_000},
	})
	must.NoError(err)

	rejectDoctorCapture(t, fixture)

	stdout, stderr := fixture.mustExecute("doctor", "--fix")
	is.Empty(stderr)
	is.Contains(stdout, "recovered")
	loaded, err := store.Load("alpha", "op-effective-price")
	must.NoError(err)
	cost := loaded.Runs[0].Execution.UsageCost
	must.NotNil(cost)
	must.NotNil(cost.Pricing)
	is.Equal(int64(1_000_000), cost.AmountMicroUSD)
	is.Equal("2026-07-09", cost.Pricing.EffectiveDate)
	is.Equal("1", cost.Pricing.InputUSDPerMillionTokens)
	is.NotEmpty(cost.Pricing.Source)
}

func assertDoctorCodexCostSnapshot(t *testing.T, cost *taskstate.AgentUsageCost) {
	t.Helper()
	must := require.New(t)
	must.NotNil(cost)
	must.NotNil(cost.Pricing)
	must.Equal(agent.UsageCostKindEstimatedAPIEquivalent, cost.Kind)
	must.Equal("gpt-5", cost.Pricing.Model)
	must.NotEmpty(cost.Pricing.Source)
}

func TestIntegrationWorkflowDoctorFallsBackToRegisteredRepoRootWhenTaskTargetIsMissing(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	paths := fixture.paths
	repoDir := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")

	store := taskstate.NewStoreWithClock(paths, clockSequence(
		time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 10, 2, 0, 0, time.UTC),
	))
	run, err := store.StartRun("alpha", "op-legacy", taskstate.StartRunOptions{
		Agent:   "codex-profile",
		Profile: "codex-profile",
		Harness: "codex",
		Command: "codex",
		Args:    []string{"exec"},
	})
	must.NoError(err)
	_, err = store.FinishRun("alpha", "op-legacy", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)

	supplyDoctorUsage(t, fixture, "codex", repoDir, doctorCapturedUsage("codex", "repo-root-session", 42))

	stdout, stderr := fixture.mustExecute("doctor")
	is.Empty(stderr)
	is.Contains(stdout, "would_recover")
	is.Contains(stdout, "repo-root-session")
	is.NotContains(stdout, "wrong-repo-session")
}

func TestIntegrationWorkflowDoctorRecoversPiUsageAndReportedCost(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	paths := fixture.paths
	repoDir := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")

	store := taskstate.NewStoreWithClock(paths, clockSequence(
		time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 10, 2, 0, 0, time.UTC),
	))
	run, err := store.StartRun("alpha", "op-pi", taskstate.StartRunOptions{
		Agent:       "pi-profile",
		Profile:     "pi-profile",
		Harness:     "pi",
		Command:     "pi",
		Args:        []string{"--model", "openai-codex/gpt-5.5"},
		SessionName: "(op-pi) Pi task",
		Branch:      "main",
		Worktree:    repoDir,
	})
	must.NoError(err)
	_, err = store.FinishRun("alpha", "op-pi", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)

	supplyDoctorUsage(t, fixture, "pi", repoDir, doctorCapturedUsage("pi", "pi-session", 180))

	dryRunStdout, dryRunStderr := fixture.mustExecute("doctor")
	is.Empty(dryRunStderr)
	is.Contains(dryRunStdout, "Agent usage telemetry")
	is.Contains(dryRunStdout, "would_recover")
	is.Contains(dryRunStdout, "pi-session")
	is.Contains(dryRunStdout, "$0.001240")

	stdout, stderr := fixture.mustExecute("doctor", "--fix")
	is.Empty(stderr)
	is.Contains(stdout, "Agent usage telemetry")
	is.Contains(stdout, "recovered")
	is.Contains(stdout, "pi-session")
	is.Contains(stdout, "$0.001240")

	loaded, err := store.Load("alpha", "op-pi")
	must.NoError(err)
	execution := loaded.Runs[0].Execution
	must.NotNil(execution.Session)
	must.NotNil(execution.Usage)
	must.NotNil(execution.UsageCost)
	is.Equal("pi-session", execution.Session.ID)
	is.Equal("openai-codex/gpt-5.5", execution.Model)
	is.Equal(180, execution.Usage.TotalTokens)
	is.Equal(int64(1240), execution.UsageCost.AmountMicroUSD)
	is.Equal("pi_reported_estimated", execution.UsageCost.Kind)
	is.Equal(taskstate.UsageCaptureCaptured, execution.UsageCapture.Status)
}

func TestIntegrationWorkflowDoctorRefreshesStoredPiReportedCost(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	paths := fixture.paths
	repoDir := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")

	startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	store := taskstate.NewStoreWithClock(paths, func() time.Time { return startedAt })
	sessionName := "(op-pi-refresh) Pi task"
	run, err := store.StartRun("alpha", "op-pi-refresh", doctorPiStartOptions(repoDir, sessionName))
	must.NoError(err)
	oldCost := agent.PiReportedUsageCost(1)
	_, err = store.RecordRunUsage("alpha", "op-pi-refresh", run.Attempt, taskstate.RecordRunUsageOptions{
		Model: "openai-codex/gpt-5.5", Usage: &taskstate.AgentUsage{InputTokens: 1, TotalTokens: 1}, UsageCost: &oldCost,
	})
	must.NoError(err)

	supplyDoctorUsage(t, fixture, "pi", repoDir, doctorCapturedUsage("pi", "pi-refresh-session", 180))

	stdout, stderr := fixture.mustExecute("doctor", "--fix")
	is.Empty(stderr)
	is.Contains(stdout, "recovered")
	is.Contains(stdout, "$0.001240")
	loaded, err := store.Load("alpha", "op-pi-refresh")
	must.NoError(err)
	must.NotNil(loaded.Runs[0].Execution.Session)
	must.NotNil(loaded.Runs[0].Execution.UsageCost)
	is.Equal(int64(1240), loaded.Runs[0].Execution.UsageCost.AmountMicroUSD)
	is.Equal(agent.UsageCostKindPiReportedEstimated, loaded.Runs[0].Execution.UsageCost.Kind)
}

func TestIntegrationWorkflowDoctorBoundsDelayedResumedPiRecoveryAtNextLaunch(t *testing.T) {
	t.Parallel()
	t.Run("with complete boundary", func(t *testing.T) {
		fixture := newCommandWorkflow(t)
		costBaseline := int64(1561)
		wantCost := int64(321)
		testDoctorBoundsDelayedResumedPiRecoveryAtNextLaunch(t, fixture, &costBaseline, &wantCost)
	})
	t.Run("without cost boundary", func(t *testing.T) {
		fixture := newCommandWorkflow(t)
		testDoctorBoundsDelayedResumedPiRecoveryAtNextLaunch(t, fixture, nil, nil)
	})
}

func testDoctorBoundsDelayedResumedPiRecoveryAtNextLaunch(
	t *testing.T, fixture *workflowFixture,
	costBaseline *int64,
	wantCost *int64,
) {
	t.Helper()

	is := assert.New(t)
	must := require.New(t)

	piSessionDir := workflowDirectory(t)
	paths := fixture.paths
	repoDir := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")

	startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	sessionID := "reused-pi-session"
	sessionName := "(op-resumed) Pi task"

	session := taskstate.AgentSession{
		ID:      sessionID,
		LogPath: doctorPiSessionLogPath(piSessionDir, repoDir, sessionID),
	}

	store := taskstate.NewStoreWithClock(paths, clockSequence(
		startedAt,
		startedAt.Add(time.Minute),
		startedAt.Add(2*time.Minute),
		startedAt.Add(3*time.Minute),
		startedAt.Add(4*time.Minute),
		startedAt.Add(5*time.Minute),
		startedAt.Add(6*time.Minute),
		startedAt.Add(7*time.Minute),
	))
	firstUsage := taskstate.AgentUsage{
		InputTokens: 150, CachedInputTokens: 17, OutputTokens: 30,
		ReasoningOutputTokens: 5, TotalTokens: 180,
	}
	firstCost := int64(1240)
	first, err := store.StartRun("alpha", "op-resumed", doctorPiStartOptions(repoDir, sessionName))
	must.NoError(err)
	_, err = store.RecordRunUsage("alpha", "op-resumed", first.Attempt, taskstate.RecordRunUsageOptions{
		Session: &session, Model: "gpt-5.5", Usage: &firstUsage,
		UsageCost:    agentUsageCost(firstCost),
		UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: "matched_pi_session"},
	})
	must.NoError(err)
	_, err = store.FinishRun("alpha", "op-resumed", first.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)

	secondLaunch := &taskstate.AgentLaunch{
		Mode: taskstate.AgentLaunchResumed, SourceRunAttempt: first.Attempt, SourceSession: &session,
		UsageBaseline: &firstUsage, CostBaseline: &firstCost,
	}
	secondOpts := doctorPiStartOptions(repoDir, sessionName)
	secondOpts.Launch = secondLaunch
	second, err := store.StartRun("alpha", "op-resumed", secondOpts)
	must.NoError(err)
	_, err = store.RecordRunUsage("alpha", "op-resumed", second.Attempt, taskstate.RecordRunUsageOptions{
		UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureUnknown, Reason: "read_resumed_session_failed"},
	})
	must.NoError(err)
	_, err = store.FinishRun("alpha", "op-resumed", second.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)

	thirdBaseline := taskstate.AgentUsage{
		InputTokens: 161, CachedInputTokens: 20, OutputTokens: 37,
		ReasoningOutputTokens: 7, TotalTokens: 198,
	}
	thirdLaunch := &taskstate.AgentLaunch{
		Mode: taskstate.AgentLaunchResumed, SourceRunAttempt: second.Attempt, SourceSession: &session,
		UsageBaseline: &thirdBaseline, CostBaseline: costBaseline,
	}
	thirdOpts := doctorPiStartOptions(repoDir, sessionName)
	thirdOpts.Launch = thirdLaunch
	third, err := store.StartRun("alpha", "op-resumed", thirdOpts)
	must.NoError(err)
	thirdUsage := taskstate.AgentUsage{
		InputTokens: 20, CachedInputTokens: 4, OutputTokens: 10,
		ReasoningOutputTokens: 3, TotalTokens: 30,
	}
	_, err = store.RecordRunUsage("alpha", "op-resumed", third.Attempt, taskstate.RecordRunUsageOptions{
		Session: &session, Model: "gpt-5.5", Usage: &thirdUsage,
		UsageCost:    agentUsageCost(500),
		UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: "matched_resumed_pi_session"},
	})
	must.NoError(err)
	_, err = store.FinishRun("alpha", "op-resumed", third.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)

	fixture.options.Dependencies.DoctorEffects.SameSession = func(left, right *taskstate.AgentSession) (bool, error) {
		require.Equal(t, &session, left)
		require.Equal(t, &session, right)
		return true, nil
	}
	withDoctorCapture(t, fixture, func(opts agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
		require.Equal(t, "pi", opts.Harness)
		require.Equal(t, secondLaunch, opts.Launch)
		require.NotNil(t, opts.ResumeBoundary)
		require.Equal(t, &thirdBaseline, opts.ResumeBoundary.Usage)
		require.Equal(t, costBaseline, opts.ResumeBoundary.CostMicroUSD)
		result := doctorCapturedUsage("pi", sessionID, 18)
		result.UsageCost = nil
		if wantCost != nil {
			result.UsageCost = agentUsageCost(*wantCost)
		}
		return result
	})

	stdout, stderr := fixture.mustExecute("doctor", "--fix")
	is.Empty(stderr)
	is.Contains(stdout, "recovered")

	loaded, err := store.Load("alpha", "op-resumed")
	must.NoError(err)
	must.NotNil(loaded.Runs[1].Execution.Usage)
	is.Equal(18, loaded.Runs[1].Execution.Usage.TotalTokens)
	is.Equal(taskstate.UsageCaptureCaptured, loaded.Runs[1].Execution.UsageCapture.Status)
	if wantCost == nil {
		is.Nil(loaded.Runs[1].Execution.UsageCost)
	} else {
		must.NotNil(loaded.Runs[1].Execution.UsageCost)
		is.Equal(*wantCost, loaded.Runs[1].Execution.UsageCost.AmountMicroUSD)
	}
	is.Equal(30, loaded.Runs[2].Execution.Usage.TotalTokens)
	is.Equal(int64(500), loaded.Runs[2].Execution.UsageCost.AmountMicroUSD)
}

func agentUsageCost(amount int64) *taskstate.AgentUsageCost {
	cost := agent.PiReportedUsageCost(amount)
	return &cost
}

func TestIntegrationWorkflowDoctorRecoversPiUsageWhenMatchedSessionHasNoReportedCost(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	paths := fixture.paths
	repoDir := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")

	store := taskstate.NewStoreWithClock(paths, clockSequence(
		time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 10, 2, 0, 0, time.UTC),
	))
	run, err := store.StartRun("alpha", "op-pi-usage-no-cost", doctorPiStartOptions(
		repoDir,
		"(op-pi-usage-no-cost) Pi task",
	))
	must.NoError(err)
	_, err = store.FinishRun("alpha", "op-pi-usage-no-cost", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)

	capture := doctorCapturedUsage("pi", "pi-usage-no-cost", 180)
	capture.UsageCost = nil
	supplyDoctorUsage(t, fixture, "pi", repoDir, capture)

	stdout, stderr := fixture.mustExecute("doctor", "--fix")
	is.Empty(stderr)
	is.Contains(stdout, "recovered")
	is.Contains(stdout, "pi-usage-no-cost")
	is.Contains(stdout, "180")
	is.NotContains(stdout, "matching_pi_session_has_no_reported_cost")

	loaded, err := store.Load("alpha", "op-pi-usage-no-cost")
	must.NoError(err)
	execution := loaded.Runs[0].Execution
	must.NotNil(execution.Session)
	must.NotNil(execution.Usage)
	is.Nil(execution.UsageCost)
	is.Equal("pi-usage-no-cost", execution.Session.ID)
	is.Equal("openai-codex/gpt-5.5", execution.Model)
	is.Equal(180, execution.Usage.TotalTokens)
	is.Equal(taskstate.UsageCaptureCaptured, execution.UsageCapture.Status)
}

func TestIntegrationWorkflowDoctorDoesNotRecoverPiCostWhenMatchedSessionHasNoReportedCost(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	piSessionDir := workflowDirectory(t)
	paths := fixture.paths
	repoDir := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")

	store := taskstate.NewStoreWithClock(paths, clockSequence(
		time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 10, 2, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 10, 3, 0, 0, time.UTC),
	))
	run, err := store.StartRun("alpha", "op-pi-no-cost", doctorPiStartOptions(repoDir, "(op-pi-no-cost) Pi task"))
	must.NoError(err)
	_, err = store.FinishRun("alpha", "op-pi-no-cost", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)
	_, err = store.RecordRunUsage("alpha", "op-pi-no-cost", run.Attempt, taskstate.RecordRunUsageOptions{
		Session: &taskstate.AgentSession{
			ID:      "existing-pi-session",
			LogPath: filepath.Join(piSessionDir, "existing-pi-session.jsonl"),
		},
		Usage: &taskstate.AgentUsage{
			TotalTokens: 180,
		},
		UsageCapture: taskstate.AgentUsageCapture{
			Status: taskstate.UsageCaptureCaptured,
			Reason: "matched_pi_session",
		},
		Model: "openai-codex/gpt-5.5",
	})
	must.NoError(err)

	capture := doctorCapturedUsage("pi", "pi-no-cost", 180)
	capture.UsageCost = nil
	supplyDoctorUsage(t, fixture, "pi", repoDir, capture)

	stdout, stderr := fixture.mustExecute("doctor", "--fix")
	is.Empty(stderr)
	is.Contains(stdout, "unknown")
	is.Contains(stdout, "matching_pi_session_has_no_reported_cost")
	is.NotContains(stdout, "recovered")

	loaded, err := store.Load("alpha", "op-pi-no-cost")
	must.NoError(err)
	execution := loaded.Runs[0].Execution
	must.NotNil(execution.Session)
	must.NotNil(execution.Usage)
	is.Nil(execution.UsageCost)
	is.Equal("existing-pi-session", execution.Session.ID)
	is.Equal(180, execution.Usage.TotalTokens)
}

func TestIntegrationWorkflowDoctorRecoversUsageForUnfinishedExecution(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	paths := fixture.paths
	repoDir := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")

	store := taskstate.NewStoreWithClock(paths, clockSequence(
		time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	))
	_, err := store.StartRun("alpha", "op-running", doctorCodexStartOptions(repoDir))
	must.NoError(err)

	supplyDoctorUsage(t, fixture, "codex", repoDir, doctorCapturedUsage("codex", "running-session", 42))

	stdout, stderr := fixture.mustExecute("doctor", "--fix")
	is.Empty(stderr)
	is.Contains(stdout, "recovered")
	is.Contains(stdout, "running-session")
	is.NotContains(stdout, "execution_not_finished")

	loaded, err := store.Load("alpha", "op-running")
	must.NoError(err)
	must.NotNil(loaded.Runs[0].Execution.Session)
	must.NotNil(loaded.Runs[0].Execution.Usage)
	is.Equal("running-session", loaded.Runs[0].Execution.Session.ID)
	is.Equal(42, loaded.Runs[0].Execution.Usage.TotalTokens)
	is.Equal(taskstate.UsageCaptureCaptured, loaded.Runs[0].Execution.UsageCapture.Status)
}

func TestIntegrationWorkflowDoctorReportsAmbiguousAndNoMatchWithoutMutating(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	paths := fixture.paths
	repoDir := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")

	store := taskstate.NewStoreWithClock(paths, clockSequence(
		time.Date(2026, 7, 7, 11, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 11, 5, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 12, 5, 0, 0, time.UTC),
	))
	ambiguousRun, err := store.StartRun("alpha", "op-ambiguous", doctorCodexStartOptions(repoDir))
	must.NoError(err)
	_, err = store.FinishRun("alpha", "op-ambiguous", ambiguousRun.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)
	missingRun, err := store.StartRun("alpha", "op-missing", doctorCodexStartOptions(repoDir))
	must.NoError(err)
	_, err = store.FinishRun("alpha", "op-missing", missingRun.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)

	withDoctorCapture(t, fixture, func(opts agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
		require.Equal(t, "codex", opts.Harness)
		require.Equal(t, []string{repoDir}, opts.ExecutionDirs)
		if opts.StartedAt.Equal(ambiguousRun.Execution.StartedAt) {
			return taskstate.RecordRunUsageOptions{UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureAmbiguous, Reason: "multiple_matching_codex_sessions", CandidateCount: 2}}
		}
		require.Equal(t, missingRun.Execution.StartedAt, opts.StartedAt)
		return taskstate.RecordRunUsageOptions{UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureUnknown, Reason: "no_matching_codex_session"}}
	})

	stdout, stderr := fixture.mustExecute("doctor", "--fix")
	is.Empty(stderr)
	is.Contains(stdout, "ambiguous")
	is.Contains(stdout, "multiple_matching_codex_sessions")
	is.Contains(stdout, "unknown")
	is.Contains(stdout, "no_matching_codex_session")

	ambiguousState, err := store.Load("alpha", "op-ambiguous")
	must.NoError(err)
	is.Nil(ambiguousState.Runs[0].Execution.Usage)
	missingState, err := store.Load("alpha", "op-missing")
	must.NoError(err)
	is.Nil(missingState.Runs[0].Execution.Usage)
}

func TestIntegrationWorkflowDoctorReportsAmbiguousPiMatchesWithoutMutating(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	piSessionDir := workflowDirectory(t)
	paths := fixture.paths
	repoDir := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")

	store := taskstate.NewStoreWithClock(paths, clockSequence(
		time.Date(2026, 7, 7, 11, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 11, 5, 0, 0, time.UTC),
	))
	run, err := store.StartRun("alpha", "op-pi-ambiguous", taskstate.StartRunOptions{
		Agent:    "pi-profile",
		Profile:  "pi-profile",
		Harness:  "pi",
		Command:  "pi",
		Branch:   "main",
		Worktree: repoDir,
	})
	must.NoError(err)
	_, err = store.FinishRun("alpha", "op-pi-ambiguous", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)

	supplyDoctorUsage(t, fixture, "pi", repoDir, taskstate.RecordRunUsageOptions{
		UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureAmbiguous, Reason: "multiple_matching_pi_sessions", CandidateCount: 2},
		Candidates: []taskstate.UsageCaptureCandidate{
			{SessionID: "pi-one", SessionName: "Pi one", StartedAt: workflowTime("2026-07-07T11:01:00Z"), StartOffsetMillis: 60000, CWD: repoDir, Model: "openai-codex/gpt-5.5", LogPath: doctorPiSessionLogPath(piSessionDir, repoDir, "pi-one")},
			{SessionID: "pi-two", SessionName: "Pi two", StartedAt: workflowTime("2026-07-07T11:02:00Z"), StartOffsetMillis: 120000, CWD: repoDir, Model: "openai-codex/gpt-5.5", LogPath: doctorPiSessionLogPath(piSessionDir, repoDir, "pi-two")},
		},
	})

	stdout, stderr := fixture.mustExecute("doctor", "--fix")
	is.Empty(stderr)
	is.Contains(stdout, "ambiguous")
	is.Contains(stdout, "multiple_matching_pi_sessions")
	is.Contains(stdout, "CANDIDATE_DETAILS")
	is.Contains(stdout, "id=pi-one")
	is.Contains(stdout, "name=Pi one")
	is.Contains(stdout, "started=2026-07-07T11:01:00Z")
	is.Contains(stdout, "offset=1m0s")
	is.Contains(stdout, "cwd="+repoDir)
	is.Contains(stdout, "model=openai-codex/gpt-5.5")
	is.Contains(stdout, "log="+doctorPiSessionLogPath(piSessionDir, repoDir, "pi-one"))
	is.Contains(stdout, "id=pi-two")
	is.Contains(stdout, "name=Pi two")
	is.Contains(stdout, "started=2026-07-07T11:02:00Z")
	is.Contains(stdout, "offset=2m0s")
	is.Contains(stdout, "log="+doctorPiSessionLogPath(piSessionDir, repoDir, "pi-two"))

	loaded, err := store.Load("alpha", "op-pi-ambiguous")
	must.NoError(err)
	is.Nil(loaded.Runs[0].Execution.Usage)
	is.Nil(loaded.Runs[0].Execution.UsageCost)
}

func TestIntegrationWorkflowDoctorRecoversSyncConflictTerminalUsage(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	paths := fixture.paths
	repoDir := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repoDir: {tasks: []taskmodel.Task{{ID: "op-sync", Title: "Sync conflict", Status: taskmodel.StatusInProgress, IssueType: taskmodel.IssueTypeTask}}}})

	store := taskstate.NewStoreWithClock(paths, clockSequence(
		time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 10, 5, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 11, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 11, 5, 0, 0, time.UTC),
	))
	codexOpts := doctorSyncConflictOptions(repoDir, taskstate.AgentExecution{
		Agent:       "sync-codex",
		Profile:     "sync-codex",
		Harness:     "codex",
		Command:     "codex",
		Args:        []string{"exec", "resolve"},
		SessionName: "sync-conflict-op-sync",
		StartedAt:   time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	})
	_, err := store.RecordSyncConflictResolutionStarted("alpha", "op-sync", codexOpts)
	must.NoError(err)
	codexOpts.Commit = "codex-merge"
	_, err = store.RecordSyncConflictResolutionFinished("alpha", "op-sync", codexOpts)
	must.NoError(err)

	piOpts := doctorSyncConflictOptions(repoDir, taskstate.AgentExecution{
		Agent:       "sync-pi",
		Profile:     "sync-pi",
		Harness:     "pi",
		Command:     "pi",
		Args:        []string{"--model", "openai-codex/gpt-5.5"},
		SessionName: "sync-conflict-op-sync-pi",
		StartedAt:   time.Date(2026, 7, 7, 11, 0, 0, 0, time.UTC),
	})
	_, err = store.RecordSyncConflictResolutionStarted("alpha", "op-sync", piOpts)
	must.NoError(err)
	_, err = store.RecordSyncConflictResolutionFailed("alpha", "op-sync", piOpts, assert.AnError)
	must.NoError(err)

	withDoctorCapture(t, fixture, func(opts agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
		require.Equal(t, []string{repoDir}, opts.ExecutionDirs)
		if opts.Harness == "codex" {
			require.Equal(t, codexOpts.Execution.StartedAt, opts.StartedAt)
			return doctorCapturedUsage("codex", "sync-codex-session", 165)
		}
		require.Equal(t, "pi", opts.Harness)
		require.Equal(t, piOpts.Execution.StartedAt, opts.StartedAt)
		return doctorCapturedUsage("pi", "sync-pi-session", 180)
	})

	dryRunStdout, dryRunStderr := fixture.mustExecute("doctor")
	is.Empty(dryRunStderr)
	is.Contains(dryRunStdout, "sync-conflict-resolution")
	is.Contains(dryRunStdout, "would_recover")
	is.Contains(dryRunStdout, "sync-codex-session")
	is.Contains(dryRunStdout, "sync-pi-session")

	loaded, err := store.Load("alpha", "op-sync")
	must.NoError(err)
	must.Len(loaded.Events, 4)
	is.Nil(loaded.Events[1].Execution.Usage)
	is.Nil(loaded.Events[3].Execution.Usage)

	beforeEvents := loaded.Events
	stdout, stderr := fixture.mustExecute("doctor", "--fix")
	is.Empty(stderr)
	is.Contains(stdout, "recovered")
	is.Contains(stdout, "sync-conflict-resolution")

	loaded, err = store.Load("alpha", "op-sync")
	must.NoError(err)
	must.Len(loaded.Events, 4)
	is.Equal(taskstate.EventSyncConflictStarted, loaded.Events[0].Type)
	is.Equal(taskstate.EventSyncConflictFinished, loaded.Events[1].Type)
	is.Equal(taskstate.EventSyncConflictStarted, loaded.Events[2].Type)
	is.Equal(taskstate.EventSyncConflictFailed, loaded.Events[3].Type)
	is.Equal(beforeEvents[1].At, loaded.Events[1].At)
	is.Equal(beforeEvents[3].Error, loaded.Events[3].Error)
	is.Nil(loaded.Events[0].Execution.Usage)
	is.Nil(loaded.Events[2].Execution.Usage)

	codexExecution := loaded.Events[1].Execution
	must.NotNil(codexExecution.Session)
	must.NotNil(codexExecution.Usage)
	is.Equal("sync-codex-session", codexExecution.Session.ID)
	is.Equal("gpt-5", codexExecution.Model)
	is.Equal(165, codexExecution.Usage.TotalTokens)
	is.Equal(taskstate.UsageCaptureCaptured, codexExecution.UsageCapture.Status)

	piExecution := loaded.Events[3].Execution
	must.NotNil(piExecution.Session)
	must.NotNil(piExecution.Usage)
	must.NotNil(piExecution.UsageCost)
	is.Equal("sync-pi-session", piExecution.Session.ID)
	is.Equal("openai-codex/gpt-5.5", piExecution.Model)
	is.Equal(180, piExecution.Usage.TotalTokens)
	is.Equal(int64(1240), piExecution.UsageCost.AmountMicroUSD)
	is.Equal(taskstate.UsageCaptureCaptured, piExecution.UsageCapture.Status)

	secondStdout, secondStderr := fixture.mustExecute("doctor")
	is.Empty(secondStderr)
	is.NotContains(secondStdout, "sync-codex-session")
	is.NotContains(secondStdout, "sync-pi-session")

	statsOut, statsErr := fixture.mustExecute("task", "stats", "op-sync")
	is.Empty(statsErr)
	is.Contains(statsOut, "sync-conflict-resolution")
	is.Contains(statsOut, "total=165")
	is.Contains(statsOut, "total=180")
	is.Regexp(`(?m)^sync-conflict-resolution\s+2\s+10m0s\s+345\s+151\s+22\s+33\s+9\s+\$[0-9.]+\s+0\s+0$`, statsOut)
}

func TestIntegrationWorkflowDoctorPrefersRecordedSyncConflictWorktreeBeforeFallbackDirs(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	paths := fixture.paths
	registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")

	targetWorktree := filepath.Join(workflowDirectory(t), "target-worktree")
	eventWorktree := filepath.Join(workflowDirectory(t), "event-worktree")

	startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	store := taskstate.NewStoreWithClock(paths, clockSequence(
		time.Date(2026, 7, 7, 9, 50, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 10, 5, 0, 0, time.UTC),
	))
	_, err := store.StartRun("alpha", "op-sync-priority", taskstate.StartRunOptions{
		Agent:    "raw-profile",
		Profile:  "raw-profile",
		Harness:  "raw",
		Command:  "sh",
		Args:     []string{"-c", "true"},
		Branch:   "main",
		Worktree: targetWorktree,
	})
	must.NoError(err)
	_, err = store.RecordSyncConflictResolutionFinished(
		"alpha",
		"op-sync-priority",
		doctorSyncConflictOptions(eventWorktree, taskstate.AgentExecution{
			Agent:       "sync-codex",
			Profile:     "sync-codex",
			Harness:     "codex",
			Command:     "codex",
			Args:        []string{"exec", "resolve"},
			SessionName: "sync-conflict-op-sync-priority",
			StartedAt:   startedAt,
		}),
	)
	must.NoError(err)

	supplyDoctorUsage(t, fixture, "codex", eventWorktree, doctorCapturedUsage("codex", "recorded-worktree-session", 111))

	stdout, stderr := fixture.mustExecute("doctor", "--fix")
	is.Empty(stderr)
	is.Contains(stdout, "sync-conflict-resolution")
	is.Contains(stdout, "recorded-worktree-session")
	is.NotContains(stdout, "fallback-worktree-session")

	loaded, err := store.Load("alpha", "op-sync-priority")
	must.NoError(err)
	must.Len(loaded.Events, 2)
	syncEvent := loaded.Events[1]
	is.Equal(taskstate.EventSyncConflictFinished, syncEvent.Type)
	execution := syncEvent.Execution
	must.NotNil(execution)
	must.NotNil(execution.Session)
	must.NotNil(execution.Usage)
	is.Equal("recorded-worktree-session", execution.Session.ID)
	is.Equal(111, execution.Usage.TotalTokens)
	is.Equal(eventWorktree, syncEvent.Worktree)
}

func TestIntegrationWorkflowDoctorTraversesAllRegisteredRepos(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	paths := fixture.paths
	root := workflowDirectory(t)
	alphaDir := filepath.Join(root, "alpha")
	betaDir := filepath.Join(root, "beta")

	must.NoError(registry.NewStore(paths).Save(registry.Registry{Repos: []registry.Repo{
		{ID: "alpha", Name: "Alpha", Path: alphaDir, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "op"},
		{ID: "beta", Name: "Beta", Path: betaDir, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "bt"},
	}}))

	store := taskstate.NewStoreWithClock(paths, clockSequence(
		time.Date(2026, 7, 7, 13, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 13, 5, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 14, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 7, 14, 5, 0, 0, time.UTC),
	))
	alphaRun, err := store.StartRun("alpha", "op-1", doctorCodexStartOptions(alphaDir))
	must.NoError(err)
	_, err = store.FinishRun("alpha", "op-1", alphaRun.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)
	betaRun, err := store.StartRun("beta", "bt-1", doctorCodexStartOptions(betaDir))
	must.NoError(err)
	_, err = store.FinishRun("beta", "bt-1", betaRun.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)

	withDoctorCapture(t, fixture, func(opts agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
		require.Equal(t, "codex", opts.Harness)
		if opts.StartedAt.Equal(alphaRun.Execution.StartedAt) {
			require.Equal(t, []string{alphaDir}, opts.ExecutionDirs)
			return doctorCapturedUsage("codex", "alpha-session", 20)
		}
		require.Equal(t, betaRun.Execution.StartedAt, opts.StartedAt)
		require.Equal(t, []string{betaDir}, opts.ExecutionDirs)
		return doctorCapturedUsage("codex", "beta-session", 30)
	})

	stdout, stderr := fixture.mustExecute("doctor")
	is.Empty(stderr)
	is.Contains(stdout, "alpha")
	is.Contains(stdout, "op-1")
	is.Contains(stdout, "alpha-session")
	is.Contains(stdout, "beta")
	is.Contains(stdout, "bt-1")
	is.Contains(stdout, "beta-session")
}

func doctorSyncConflictOptions(
	worktree string,
	execution taskstate.AgentExecution,
) taskstate.SyncConflictResolutionEventOptions {
	return taskstate.SyncConflictResolutionEventOptions{
		Execution:     execution,
		Branch:        "orpheus/op-sync",
		DefaultBranch: "main",
		Worktree:      worktree,
		PRURL:         "https://github.test/org/repo/pull/42",
		ConflictFiles: []string{"conflict.txt"},
	}
}

func doctorCodexStartOptions(worktree string) taskstate.StartRunOptions {
	return taskstate.StartRunOptions{
		Agent:    "codex-profile",
		Profile:  "codex-profile",
		Harness:  "codex",
		Command:  "codex",
		Args:     []string{"exec"},
		Branch:   "main",
		Worktree: worktree,
	}
}

func doctorPiStartOptions(worktree string, sessionName string) taskstate.StartRunOptions {
	return taskstate.StartRunOptions{
		Agent:       "pi-profile",
		Profile:     "pi-profile",
		Harness:     "pi",
		Command:     "pi",
		SessionName: sessionName,
		Branch:      "main",
		Worktree:    worktree,
	}
}
