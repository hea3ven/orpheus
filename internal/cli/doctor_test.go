package cli_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//nolint:funlen // The end-to-end recovery fixture covers implementation, review, and stats together.
func TestDoctorRecoversCodexUsageForImplementationAndReviewAgent(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")
	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[{"id":"op-1","title":"Doctor","status":"in_progress","priority":1,"issue_type":"task"}]`},
	})
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

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

	writeDoctorCodexSessionLog(
		t,
		codexHome,
		"run-session",
		repoDir,
		time.Date(2026, 7, 7, 10, 1, 0, 0, time.UTC),
		190,
	)
	writeDoctorCodexSessionLog(
		t,
		codexHome,
		"wrong-cwd-session",
		filepath.Join(t.TempDir(), "other-repo"),
		time.Date(2026, 7, 7, 10, 1, 30, 0, time.UTC),
		999,
	)
	writeDoctorCodexSessionLog(
		t,
		codexHome,
		"review-session",
		repoDir,
		time.Date(2026, 7, 7, 10, 11, 0, 0, time.UTC),
		50,
	)

	stdout, stderr := executeCommand(t, []string{"doctor"})
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

	stdout, stderr = executeCommand(t, []string{"doctor", "--fix"})
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

	statsOut, statsErr := executeCommand(t, []string{"task", "stats", "op-1"})
	is.Empty(statsErr)
	is.Contains(statsOut, "total=190")
	is.Contains(statsOut, "total=50")
	is.Contains(statsOut, "UNKNOWN_USAGE")
	is.Contains(statsOut, "combined")
}

func TestDoctorFallsBackToRegisteredRepoRootWhenTaskTargetIsMissing(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

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
	writeDoctorCodexSessionLog(
		t,
		codexHome,
		"repo-root-session",
		repoDir,
		time.Date(2026, 7, 7, 10, 1, 0, 0, time.UTC),
		42,
	)
	writeDoctorCodexSessionLog(
		t,
		codexHome,
		"wrong-repo-session",
		filepath.Join(t.TempDir(), "wrong-repo"),
		time.Date(2026, 7, 7, 10, 1, 30, 0, time.UTC),
		99,
	)

	stdout, stderr := executeCommand(t, []string{"doctor"})
	is.Empty(stderr)
	is.Contains(stdout, "would_recover")
	is.Contains(stdout, "repo-root-session")
	is.NotContains(stdout, "wrong-repo-session")
}

func TestDoctorRecoversPiUsageAndReportedCost(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")
	piSessionDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", piSessionDir)

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
	writeDoctorPiSessionLog(
		t,
		piSessionDir,
		"pi-session",
		"(op-pi) Pi task",
		repoDir,
		time.Date(2026, 7, 7, 10, 1, 0, 0, time.UTC),
	)

	stdout, stderr := executeCommand(t, []string{"doctor", "--fix"})
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

func TestDoctorRecoversPiUsageWhenMatchedSessionHasNoReportedCost(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")
	piSessionDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", piSessionDir)

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
	writeDoctorPiSessionLogWithoutReportedCost(
		t,
		piSessionDir,
		"pi-usage-no-cost",
		"(op-pi-usage-no-cost) Pi task",
		repoDir,
		time.Date(2026, 7, 7, 10, 1, 0, 0, time.UTC),
	)

	stdout, stderr := executeCommand(t, []string{"doctor", "--fix"})
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

func TestDoctorDoesNotRecoverPiCostWhenMatchedSessionHasNoReportedCost(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")
	piSessionDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", piSessionDir)

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
	writeDoctorPiSessionLogWithoutReportedCost(
		t,
		piSessionDir,
		"pi-no-cost",
		"(op-pi-no-cost) Pi task",
		repoDir,
		time.Date(2026, 7, 7, 10, 1, 0, 0, time.UTC),
	)

	stdout, stderr := executeCommand(t, []string{"doctor", "--fix"})
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

func TestDoctorRecoversUsageForUnfinishedExecution(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

	store := taskstate.NewStoreWithClock(paths, clockSequence(
		time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	))
	_, err := store.StartRun("alpha", "op-running", doctorCodexStartOptions(repoDir))
	must.NoError(err)
	writeDoctorCodexSessionLog(
		t,
		codexHome,
		"running-session",
		repoDir,
		time.Date(2026, 7, 7, 10, 1, 0, 0, time.UTC),
		42,
	)

	stdout, stderr := executeCommand(t, []string{"doctor", "--fix"})
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

func TestDoctorReportsAmbiguousAndNoMatchWithoutMutating(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

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

	writeDoctorCodexSessionLog(
		t,
		codexHome,
		"ambiguous-one",
		repoDir,
		time.Date(2026, 7, 7, 11, 1, 0, 0, time.UTC),
		10,
	)
	writeDoctorCodexSessionLog(
		t,
		codexHome,
		"ambiguous-two",
		repoDir,
		time.Date(2026, 7, 7, 11, 2, 0, 0, time.UTC),
		10,
	)

	stdout, stderr := executeCommand(t, []string{"doctor", "--fix"})
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

func TestDoctorReportsAmbiguousPiMatchesWithoutMutating(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")
	piSessionDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", piSessionDir)

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
	writeDoctorPiSessionLog(t, piSessionDir, "pi-one", "Pi one", repoDir, time.Date(2026, 7, 7, 11, 1, 0, 0, time.UTC))
	writeDoctorPiSessionLog(t, piSessionDir, "pi-two", "Pi two", repoDir, time.Date(2026, 7, 7, 11, 2, 0, 0, time.UTC))

	stdout, stderr := executeCommand(t, []string{"doctor", "--fix"})
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

func TestDoctorTraversesAllRegisteredRepos(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	root := t.TempDir()
	alphaDir := filepath.Join(root, "alpha")
	betaDir := filepath.Join(root, "beta")
	must.NoError(os.MkdirAll(alphaDir, 0o755))
	must.NoError(os.MkdirAll(betaDir, 0o755))
	must.NoError(registry.NewStore(paths).Save(registry.Registry{Repos: []registry.Repo{
		{ID: "alpha", Name: "Alpha", Path: alphaDir, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "op"},
		{ID: "beta", Name: "Beta", Path: betaDir, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "bt"},
	}}))
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

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
	writeDoctorCodexSessionLog(t, codexHome, "alpha-session", alphaDir, time.Date(2026, 7, 7, 13, 1, 0, 0, time.UTC), 20)
	writeDoctorCodexSessionLog(t, codexHome, "beta-session", betaDir, time.Date(2026, 7, 7, 14, 1, 0, 0, time.UTC), 30)

	stdout, stderr := executeCommand(t, []string{"doctor"})
	is.Empty(stderr)
	is.Contains(stdout, "alpha")
	is.Contains(stdout, "op-1")
	is.Contains(stdout, "alpha-session")
	is.Contains(stdout, "beta")
	is.Contains(stdout, "bt-1")
	is.Contains(stdout, "beta-session")
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

func writeDoctorCodexSessionLog(
	t *testing.T,
	codexHome string,
	sessionID string,
	cwd string,
	startedAt time.Time,
	totalTokens int,
) {
	t.Helper()

	sessionDir := filepath.Join(codexHome, "sessions", startedAt.Format("2006"), startedAt.Format("01"), startedAt.Format("02"))
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	path := filepath.Join(sessionDir, sessionID+".jsonl")
	timestamp := startedAt.UTC().Format(time.RFC3339Nano)
	content := strings.Join([]string{
		`{"timestamp":"` + timestamp + `","type":"session_meta","payload":{"session_id":"` + sessionID + `","id":"` + sessionID + `","timestamp":"` + timestamp + `","cwd":"` + cwd + `","model":"gpt-5"}}`,
		`{"timestamp":"` + timestamp + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":` + "1" + `,"cached_input_tokens":2,"output_tokens":` + "3" + `,"reasoning_output_tokens":4,"total_tokens":` + strconv.Itoa(totalTokens) + `}}}}`,
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func doctorPiSessionLogPath(sessionRoot string, cwd string, sessionID string) string {
	trimmedCWD := strings.Trim(cwd, string(os.PathSeparator))
	pathComponent := strings.ReplaceAll(trimmedCWD, string(os.PathSeparator), "-")
	sessionDir := filepath.Join(sessionRoot, pathComponent)
	return filepath.Join(sessionDir, sessionID+".jsonl")
}

func writeDoctorPiSessionLog(
	t *testing.T,
	sessionRoot string,
	sessionID string,
	sessionName string,
	cwd string,
	startedAt time.Time,
) {
	t.Helper()
	writeDoctorPiSessionLogWithCost(t, sessionRoot, sessionID, sessionName, cwd, startedAt, true)
}

func writeDoctorPiSessionLogWithoutReportedCost(
	t *testing.T,
	sessionRoot string,
	sessionID string,
	sessionName string,
	cwd string,
	startedAt time.Time,
) {
	t.Helper()
	writeDoctorPiSessionLogWithCost(t, sessionRoot, sessionID, sessionName, cwd, startedAt, false)
}

func writeDoctorPiSessionLogWithCost(
	t *testing.T,
	sessionRoot string,
	sessionID string,
	sessionName string,
	cwd string,
	startedAt time.Time,
	includeCost bool,
) {
	t.Helper()

	sessionDir := filepath.Join(sessionRoot, strings.ReplaceAll(strings.Trim(cwd, string(os.PathSeparator)), string(os.PathSeparator), "-"))
	require.NoError(t, os.MkdirAll(sessionDir, 0o755))
	path := filepath.Join(sessionDir, sessionID+".jsonl")
	timestamp := startedAt.UTC().Format(time.RFC3339Nano)
	nameField := ""
	if sessionName != "" {
		nameField = `,"name":"` + sessionName + `"`
	}
	firstUsageCost := ""
	secondUsageCost := ""
	if includeCost {
		firstUsageCost = `,"cost":{"total":0.001234}`
		secondUsageCost = `,"cost":{"total":0.000006}`
	}
	content := strings.Join([]string{
		`{"type":"session","version":3,"id":"` + sessionID + `","timestamp":"` + timestamp + `","cwd":"` + cwd + `"` + nameField + `}`,
		`{"type":"model_change","id":"model","timestamp":"` + timestamp + `","provider":"openai-codex","modelId":"gpt-5.5"}`,
		`{"type":"message","id":"assistant-1","timestamp":"` + timestamp + `","message":{"role":"assistant"},"usage":{"input":100,"output":20,"cacheRead":10,"cacheWrite":3,"reasoning":5,"totalTokens":120` + firstUsageCost + `}}`,
		`{"type":"message","id":"assistant-2","timestamp":"` + timestamp + `","message":{"role":"assistant"},"usage":{"input":50,"output":10,"cacheRead":7,"cacheWrite":0,"reasoning":0,"totalTokens":60` + secondUsageCost + `}}`,
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
