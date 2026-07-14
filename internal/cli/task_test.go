package cli_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/registry"
	reviewconfig "github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeBDTaskResponse struct {
	stdout   string
	stderr   string
	exitCode int
}

func registerLocalTaskTestRepo(t *testing.T, id string, name string, prefix string) string {
	t.Helper()

	must := require.New(t)
	store := registry.NewStore(currentTestPaths(t))
	repoDir := filepath.Join(t.TempDir(), id)
	must.NoError(os.MkdirAll(repoDir, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:          id,
		Name:        name,
		Path:        repoDir,
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: prefix,
	}}}))
	return repoDir
}

type localManagedTaskRepos struct {
	localDir   string
	managedDir string
}

func registerLocalManagedTaskTestRepos(t *testing.T) localManagedTaskRepos {
	t.Helper()

	must := require.New(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)
	localDir := filepath.Join(t.TempDir(), "local-alpha")
	managedRepoPath := filepath.Join(t.TempDir(), "managed-beta")
	managedDir, err := store.ManagedBeadsDir("managed-beta")
	must.NoError(err)
	must.NoError(os.MkdirAll(localDir, 0o755))
	must.NoError(os.MkdirAll(managedRepoPath, 0o755))
	must.NoError(os.MkdirAll(managedDir, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{
		{ID: "local-alpha", Name: "Local Alpha", Path: localDir, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "la"},
		{ID: "managed-beta", Name: "Managed Beta", Path: managedRepoPath, BeadsMode: registry.BeadsModeManaged, BeadsPrefix: "mb"},
	}}))
	return localManagedTaskRepos{localDir: localDir, managedDir: managedDir}
}

func TestTaskListListsActiveTasksAcrossRegisteredReposWithDefaultAndDetailedTables(t *testing.T) {
	is := assert.New(t)
	newTestState(t)
	repos := registerLocalManagedTaskTestRepos(t)

	logPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repos.localDir: {stdout: `[
			{"id":"la-1","title":"Local active","external_ref":"TREX-1234","status":"open","priority":2,"issue_type":"task","metadata":{"orpheus.branch":"task/la-1","orpheus.worktree":"/tmp/la-1"}},
			{"id":"la-closed","title":"Closed local task","status":"closed","priority":1,"issue_type":"task"},
			{"id":"la-bug","title":"Local bug","status":"open","priority":1,"issue_type":"bug"}
		]`},
		repos.managedDir: {stdout: `[
			{"id":"mb-1","title":"Managed active","status":"in_progress","priority":3,"issue_type":"task","metadata":{"orpheus.pr_url":"https://example.test/pr/1"}}
		]`},
	})

	stdout, stderr := executeCommand(t, []string{"task", "list"})

	is.Empty(stderr)
	for _, want := range []string{
		"REPO", "TASK_ID", "STATUS", "P", "TITLE",
		"Local Alpha", "la-1", "open", "2", "Local active",
		"Local Alpha", "la-bug", "open", "1", "Local bug",
		"Managed Beta", "mb-1", "in_progress", "3", "Managed active",
	} {
		is.Contains(stdout, want)
	}
	for _, hidden := range []string{
		"REPO_ID", "TASK_PREFIX", "ORPHEUS", "local-alpha", "managed-beta", "branch=task/la-1", "worktree=/tmp/la-1", "pr=https://example.test/pr/1",
		"la-closed", "orpheus.branch",
	} {
		is.NotContains(stdout, hidden)
	}

	detailedStdout, detailedStderr := executeCommand(t, []string{"task", "list", "--details"})

	is.Empty(detailedStderr)
	for _, want := range []string{
		"REPO_ID", "REPO", "TASK_PREFIX", "TASK_ID", "STATUS", "P", "BRANCH", "WORKTREE", "PR", "EXTERNAL_REF", "TITLE",
		"local-alpha", "Local Alpha", "la", "la-1", "open", "2", "task/la-1", "/tmp/la-1", "TREX-1234", "Local active",
		"local-alpha", "Local Alpha", "la", "la-bug", "open", "1", "Local bug",
		"managed-beta", "Managed Beta", "mb", "mb-1", "in_progress", "3", "https://example.test/pr/1", "Managed active",
	} {
		is.Contains(detailedStdout, want)
	}
	localDetail := regexp.MustCompile(`(?m)^local-alpha\s+Local Alpha\s+la\s+la-1\s+open\s+2\s+task/la-1\s+/tmp/la-1\s+-\s+TREX-1234\s+Local active$`)
	managedDetail := regexp.MustCompile(`(?m)^managed-beta\s+Managed Beta\s+mb\s+mb-1\s+in_progress\s+3\s+-\s+-\s+https://example\.test/pr/1\s+-\s+Managed active$`)
	is.True(localDetail.MatchString(detailedStdout), "local detail row should show absent PR metadata as -")
	is.True(managedDetail.MatchString(detailedStdout), "managed detail row should show absent branch/worktree metadata as -")
	is.NotContains(detailedStdout, "branch=task/la-1")
	is.NotContains(detailedStdout, "worktree=/tmp/la-1")
	is.NotContains(detailedStdout, "pr=https://example.test/pr/1")
	is.NotContains(detailedStdout, "la-closed")
	is.NotContains(detailedStdout, "orpheus.branch")

	assertTaskListBDLog(t, logPath, repos)
}

func assertTaskListBDLog(t *testing.T, logPath string, repos localManagedTaskRepos) {
	t.Helper()

	is := assert.New(t)
	log := readFileString(t, logPath)
	is.Contains(log, repos.localDir)
	is.Contains(log, repos.managedDir)
	is.Equal(4, strings.Count(log, "--json --readonly --sandbox list --all --limit 0"))
}

func TestTaskReadyListsReadyTasksAcrossRegisteredRepos(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	repos := registerLocalManagedTaskTestRepos(t)

	logPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repos.localDir: {stdout: `[
			{"id":"la-1","title":"Local ready","status":"open","priority":2,"issue_type":"task"},
			{"id":"la-bug","title":"Local bug ready","status":"open","priority":1,"issue_type":"bug"},
			{"id":"la-chore","title":"Local chore ready","status":"open","priority":3,"issue_type":"chore"},
			{"id":"la-epic","title":"Local epic ready","status":"open","priority":1,"issue_type":"epic"},
			{"id":"la-closed","title":"Closed local task","status":"closed","priority":1,"issue_type":"task"}
		]`},
		repos.managedDir: {stdout: `[
			{"id":"mb-1","title":"Managed ready","status":"open","priority":3,"issue_type":"task"},
			{"id":"mb-review","title":"Managed in review","status":"open","priority":2,"issue_type":"task","metadata":{"orpheus.pr_url":"https://example.test/pr/7"}}
		]`},
	})

	stdout, stderr := executeCommand(t, []string{"task", "ready"})

	is.Empty(stderr)
	for _, want := range []string{
		"REPO", "TASK_ID", "STATUS", "P", "TITLE",
		"Local Alpha", "la-1", "open", "2", "Local ready",
		"Local Alpha", "la-bug", "open", "1", "Local bug ready",
		"Local Alpha", "la-chore", "open", "3", "Local chore ready",
		"Local Alpha", "la-epic", "open", "1", "Local epic ready",
		"Managed Beta", "mb-1", "open", "3", "Managed ready",
	} {
		is.Contains(stdout, want)
	}
	for _, hidden := range []string{"REPO_ID", "TASK_PREFIX", "local-alpha", "managed-beta"} {
		is.NotContains(stdout, hidden)
	}
	is.NotContains(stdout, "la-closed")
	is.NotContains(stdout, "mb-review")

	logData, err := os.ReadFile(logPath)
	must.NoError(err)
	log := string(logData)
	is.Contains(log, repos.localDir)
	is.Contains(log, repos.managedDir)
	is.Equal(2, strings.Count(log, "--json --readonly --sandbox list --all --limit 0"))
	is.NotContains(log, "--json --readonly --sandbox ready")
}

func TestTaskReadyExcludesTasksMissingRequiredExternalReference(t *testing.T) {
	is := assert.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)
	repoDir := filepath.Join(t.TempDir(), "gated")
	require.NoError(t, os.MkdirAll(repoDir, 0o755))
	require.NoError(t, store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "gated",
		Name:          "Gated",
		Path:          repoDir,
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "gt",
		TitleTemplate: "[{{external_ref}}] {{summary}}",
	}}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[
			{"id":"gt-missing","title":"Missing external reference","status":"open","priority":1,"issue_type":"task"},
			{"id":"gt-set","title":"External reference set","external_ref":"TREX-1234","status":"open","priority":1,"issue_type":"task"}
		]`},
	})

	stdout, stderr := executeCommand(t, []string{"task", "ready"})

	is.Empty(stderr)
	is.Contains(stdout, "gt-set")
	is.Contains(stdout, "External reference set")
	is.NotContains(stdout, "gt-missing")
	is.NotContains(stdout, "Missing external reference")
}

func TestTaskListReportsPartialRepoFailures(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	brokenDir := filepath.Join(t.TempDir(), "broken")
	okDir := filepath.Join(t.TempDir(), "ok")
	must.NoError(os.MkdirAll(brokenDir, 0o755))
	must.NoError(os.MkdirAll(okDir, 0o755))

	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{
		{
			ID:          "broken",
			Name:        "Broken Repo",
			Path:        brokenDir,
			BeadsMode:   registry.BeadsModeLocal,
			BeadsPrefix: "br",
		},
		{
			ID:          "ok",
			Name:        "OK Repo",
			Path:        okDir,
			BeadsMode:   registry.BeadsModeLocal,
			BeadsPrefix: "ok",
		},
	}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		brokenDir: {stderr: "bd exploded", exitCode: 7},
		okDir: {stdout: `[
			{"id":"ok-1","title":"Listed despite another repo failure","status":"open","priority":1,"issue_type":"task"}
		]`},
	})

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "list"})

	must.Error(err)
	is.ErrorContains(err, "task list completed with 1 repo failure")
	is.Contains(stdout, "REPO")
	is.Contains(stdout, "OK Repo")
	is.Contains(stdout, "ok-1")
	is.Contains(stdout, "Listed despite another repo failure")
	is.NotContains(stdout, "Broken Repo")
	is.Contains(stderr, "task list: repo broken")
	is.Contains(stderr, "needs attention")
	is.Contains(stderr, "Broken Repo")
	is.Contains(stderr, "prefix br")
	is.Contains(stderr, "bd exploded")
}

func TestTaskReadyReportsPartialRepoFailures(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	brokenDir := filepath.Join(t.TempDir(), "broken")
	okDir := filepath.Join(t.TempDir(), "ok")
	must.NoError(os.MkdirAll(brokenDir, 0o755))
	must.NoError(os.MkdirAll(okDir, 0o755))

	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{
		{
			ID:          "broken",
			Name:        "Broken Repo",
			Path:        brokenDir,
			BeadsMode:   registry.BeadsModeLocal,
			BeadsPrefix: "br",
		},
		{
			ID:          "ok",
			Name:        "OK Repo",
			Path:        okDir,
			BeadsMode:   registry.BeadsModeLocal,
			BeadsPrefix: "ok",
		},
	}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		brokenDir: {stderr: "bd exploded", exitCode: 7},
		okDir: {stdout: `[
			{"id":"ok-1","title":"Ready despite another repo failure","status":"open","priority":1,"issue_type":"task"}
		]`},
	})

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "ready"})

	must.Error(err)
	is.ErrorContains(err, "task ready completed with 1 repo failure")
	is.Contains(stdout, "REPO")
	is.Contains(stdout, "OK Repo")
	is.Contains(stdout, "ok-1")
	is.Contains(stdout, "Ready despite another repo failure")
	is.NotContains(stdout, "Broken Repo")
	is.Contains(stderr, "task ready: repo broken")
	is.Contains(stderr, "Broken Repo")
	is.Contains(stderr, "prefix br")
	is.Contains(stderr, "bd exploded")
}

func TestTaskShowResolvesPrefixQueriesOnlyResolvedRepoAndRendersDetails(t *testing.T) {
	is := assert.New(t)
	newTestState(t)
	repos := registerLocalManagedTaskTestRepos(t)

	logPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repos.localDir: {stdout: `[
			{
				"id":"la-42",
				"title":"Implement local task show",
				"external_ref":"TREX-1234",
				"description":"Render a backend-neutral detail view.\nKeep it read-only.",
				"design":"Use prefix resolution and the task backend.",
				"acceptance_criteria":"Only the resolved repo is queried.",
				"status":"in_progress",
				"priority":2,
				"issue_type":"task",
				"labels":["m2","task-show"],
				"metadata":{"orpheus.branch":"task/la-42","orpheus.worktree":"/tmp/la-42","orpheus.pr_url":"https://example.test/pr/42"}
			}
		]`},
		repos.managedDir: {stderr: "managed repo should not be queried", exitCode: 70},
	})

	stdout, stderr := executeCommand(t, []string{"task", "show", "la-42"})

	is.Empty(stderr)
	for _, want := range []string{
		"Repository:",
		"ID: local-alpha",
		"Name: Local Alpha",
		"Task prefix: la",
		"Task:",
		"ID: la-42",
		"Title: Implement local task show",
		"External reference: TREX-1234",
		"Status: in_progress",
		"Priority: 2",
		"Type: task",
		"Labels: m2, task-show",
		"Description:",
		"Render a backend-neutral detail view.",
		"Keep it read-only.",
		"Design: Use prefix resolution and the task backend.",
		"Acceptance criteria: Only the resolved repo is queried.",
		"Orpheus metadata:",
		"Branch: task/la-42",
		"Worktree: /tmp/la-42",
		"PR: https://example.test/pr/42",
		"History:",
		"  -",
	} {
		is.Contains(stdout, want)
	}
	is.NotContains(stdout, "orpheus.branch")
	is.NotContains(stdout, "managed-beta")

	assertTaskShowBDLog(t, logPath, repos)
}

func assertTaskShowBDLog(t *testing.T, logPath string, repos localManagedTaskRepos) {
	t.Helper()

	is := assert.New(t)
	log := readFileString(t, logPath)
	is.Contains(log, repos.localDir)
	is.NotContains(log, repos.managedDir)
	is.Contains(log, "--json --readonly --sandbox show --id la-42")
	is.NotContains(log, "--json --readonly --sandbox list")
	is.NotContains(log, "--json --readonly --sandbox ready")
}

func TestTaskShowReportsMalformedAndUnknownPrefixes(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "show", "notprefixed"})
	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "malformed task id")
	is.ErrorContains(err, "expected <prefix>-<number>")

	stdout, stderr, err = executeCommandWithError(t, []string{"task", "show", "zz-1"})
	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "unknown task id prefix")
	is.ErrorContains(err, "orpheus repo list")
	is.ErrorContains(err, "register the repo")
}

//nolint:funlen // The stats fixture is clearer with implementation, review, and totals assertions together.
func TestTaskStatsRendersImplementationExecutionUsage(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[{"id":"op-1","title":"Stats","status":"in_progress","priority":1,"issue_type":"task"}]`},
	})

	stateStore := taskstate.NewStoreWithClock(
		paths,
		clockSequence(
			time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
			time.Date(2026, 7, 7, 10, 1, 0, 0, time.UTC),
			time.Date(2026, 7, 7, 10, 2, 0, 0, time.UTC),
			time.Date(2026, 7, 7, 10, 3, 0, 0, time.UTC),
			time.Date(2026, 7, 7, 10, 4, 0, 0, time.UTC),
			time.Date(2026, 7, 7, 10, 5, 0, 0, time.UTC),
		),
	)
	run, err := stateStore.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:    "codex-profile",
		Profile:  "codex-profile",
		Harness:  "codex",
		Model:    "gpt-5",
		Command:  "codex",
		Args:     []string{"exec", "--model", "gpt-5"},
		Branch:   "main",
		Worktree: repoDir,
	})
	must.NoError(err)
	_, err = stateStore.RecordRunUsage("alpha", "op-1", run.Attempt, taskstate.RecordRunUsageOptions{
		Session: &taskstate.AgentSession{ID: "session-123", LogPath: "/tmp/codex.jsonl"},
		Usage: &taskstate.AgentUsage{
			InputTokens:           123,
			CachedInputTokens:     45,
			OutputTokens:          67,
			ReasoningOutputTokens: 8,
			TotalTokens:           190,
		},
		UsageCapture: taskstate.AgentUsageCapture{
			Status:         taskstate.UsageCaptureCaptured,
			Reason:         "matched_codex_session",
			CandidateCount: 1,
		},
	})
	must.NoError(err)
	_, err = stateStore.FinishRun("alpha", "op-1", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)
	reviewAttempt, err := stateStore.StartReviewWithOptions("alpha", "op-1", taskstate.StartReviewOptions{
		Pipeline: "standard",
		Step:     "ai-review",
	})
	must.NoError(err)
	_, err = stateStore.RecordReviewStep("alpha", "op-1", reviewAttempt.Attempt, taskstate.RecordReviewStepOptions{
		Kind: "agent_review",
		Name: "ai-review",
		Execution: &taskstate.AgentExecution{
			Purpose:   taskstate.AgentExecutionPurposeReview,
			Status:    taskstate.RunStatusRunning,
			Agent:     "reviewer",
			Profile:   "reviewer",
			Harness:   "codex",
			Model:     "gpt-5",
			Command:   "codex",
			Args:      []string{"exec", "--model", "gpt-5", "review"},
			StartedAt: time.Date(2026, 7, 7, 10, 3, 0, 0, time.UTC),
		},
	})
	must.NoError(err)
	_, err = stateStore.FinishReviewStepExecution("alpha", "op-1", reviewAttempt.Attempt, "ai-review", taskstate.FinishReviewStepExecutionOptions{
		Status:  taskstate.RunStatusSucceeded,
		Session: &taskstate.AgentSession{ID: "review-session-123", LogPath: "/tmp/codex-review.jsonl"},
		Usage: &taskstate.AgentUsage{
			InputTokens:           20,
			CachedInputTokens:     5,
			OutputTokens:          30,
			ReasoningOutputTokens: 7,
			TotalTokens:           50,
		},
		UsageCapture: taskstate.AgentUsageCapture{
			Status:         taskstate.UsageCaptureCaptured,
			Reason:         "matched_codex_session",
			CandidateCount: 1,
		},
	})
	must.NoError(err)
	_, err = stateStore.FinishReview("alpha", "op-1", reviewAttempt.Attempt, taskstate.ReviewStatusPassed)
	must.NoError(err)

	stdout, stderr := executeCommand(t, []string{"task", "stats", "op-1"})

	is.Empty(stderr)
	for _, want := range []string{
		"Executions", "Estimated API-equivalent cost",
		"TYPE", "ATTEMPT", "STEP", "PROFILE", "HARNESS", "MODEL", "COMMAND", "STARTED", "FINISHED", "DURATION", "STATUS", "SESSION", "USAGE", "ESTIMATED_API_EQUIVALENT_COST",
		"implementation", "1", "codex-profile", "codex", "gpt-5", `"codex" "exec" "--model" "gpt-5"`,
		"2026-07-07T10:00:00Z", "2026-07-07T10:02:00Z", "2m0s", "succeeded", "session-123",
		"total=190 input=123 cached_input=45 output=67 reasoning_output=8",
		"estimated API-equivalent cost=$0.000773", "kind=estimated_api_equivalent", "pricing=openai/gpt-5/standard",
		"review-agent", "ai-review", `"codex" "exec" "--model" "gpt-5" "review"`,
		"2026-07-07T10:03:00Z", "2026-07-07T10:04:00Z", "1m0s", "review-session-123",
		"total=50 input=20 cached_input=5 output=30 reasoning_output=7",
		"estimated API-equivalent cost=$0.000319",
		"Totals", "ACTIVE_AGENT_TIME", "TOTAL_TOKENS", "UNKNOWN_USAGE", "UNKNOWN_COST",
		"implementation", "1", "2m0s", "190", "123", "45", "67", "8", "$0.000773", "0", "0",
		"review-agent", "1", "1m0s", "50", "20", "5", "30", "7", "$0.000319", "0", "0",
		"combined", "2", "3m0s", "240", "143", "50", "97", "15", "$0.001092", "0", "0",
	} {
		is.Contains(stdout, want)
	}
}

func TestTaskStatsKeepsTokenUsageWhenCostPricingIsUnknown(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[{"id":"op-1","title":"Stats","status":"in_progress","priority":1,"issue_type":"task"}]`},
	})

	stateStore := taskstate.NewStoreWithClock(
		paths,
		clockSequence(
			time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
			time.Date(2026, 7, 7, 10, 1, 0, 0, time.UTC),
			time.Date(2026, 7, 7, 10, 2, 0, 0, time.UTC),
		),
	)
	run, err := stateStore.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:    "codex-profile",
		Profile:  "codex-profile",
		Harness:  "codex",
		Model:    "vendor-model",
		Command:  "codex",
		Args:     []string{"exec", "--model", "vendor-model"},
		Branch:   "main",
		Worktree: repoDir,
	})
	must.NoError(err)
	_, err = stateStore.RecordRunUsage("alpha", "op-1", run.Attempt, taskstate.RecordRunUsageOptions{
		Usage: &taskstate.AgentUsage{
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
		},
		UsageCapture: taskstate.AgentUsageCapture{
			Status: taskstate.UsageCaptureCaptured,
			Reason: "matched_codex_session",
		},
	})
	must.NoError(err)
	_, err = stateStore.FinishRun("alpha", "op-1", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)

	stdout, stderr := executeCommand(t, []string{"task", "stats", "op-1"})

	is.Empty(stderr)
	for _, want := range []string{
		"total=150 input=100 cached_input=0 output=50 reasoning_output=0",
		"unknown: no public pricing metadata for model vendor-model",
		"implementation", "1", "2m0s", "150", "100", "0", "50", "0", "$0.000000", "0", "1",
		"combined", "1", "2m0s", "150", "100", "0", "50", "0", "$0.000000", "0", "1",
	} {
		is.Contains(stdout, want)
	}
}

//nolint:funlen // The aggregate fixture setup documents the period metrics under test.
func TestTaskStatsAggregateGroupsResolvedTasksByDay(t *testing.T) {
	is := assert.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[
			{"id":"op-1","title":"First","status":"closed","priority":1,"issue_type":"task","created_at":"2026-07-01T00:00:00Z","closed_at":"2026-07-02T12:00:00Z"},
			{"id":"op-2","title":"Second","status":"closed","priority":1,"issue_type":"task","created_at":"2026-07-02T08:00:00Z","closed_at":"2026-07-02T18:00:00Z"},
			{"id":"op-3","title":"Unknown usage","status":"closed","priority":1,"issue_type":"task","closed_at":"2026-07-03T11:00:00Z"},
			{"id":"op-open","title":"Still open","status":"open","priority":1,"issue_type":"task","created_at":"2026-07-03T12:00:00Z"}
		]`},
	})

	aggregateStatsNow := time.Time{}
	stateStore := taskstate.NewStoreWithClock(paths, func() time.Time { return aggregateStatsNow })
	recordTaskStatsAggregateRun(t, stateStore, &aggregateStatsNow, repoDir, taskStatsAggregateRunFixture{
		taskID:     "op-1",
		model:      "gpt-5",
		startedAt:  time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC),
		finishedAt: time.Date(2026, 7, 2, 10, 30, 0, 0, time.UTC),
		usage: &taskstate.AgentUsage{
			InputTokens:           123,
			CachedInputTokens:     45,
			OutputTokens:          67,
			ReasoningOutputTokens: 8,
			TotalTokens:           1900,
		},
	})
	recordTaskStatsAggregateRun(t, stateStore, &aggregateStatsNow, repoDir, taskStatsAggregateRunFixture{
		taskID:     "op-2",
		model:      "vendor-model",
		startedAt:  time.Date(2026, 7, 2, 16, 0, 0, 0, time.UTC),
		finishedAt: time.Date(2026, 7, 2, 16, 20, 0, 0, time.UTC),
		usage: &taskstate.AgentUsage{
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  1150,
		},
	})
	recordTaskStatsAggregateRun(t, stateStore, &aggregateStatsNow, repoDir, taskStatsAggregateRunFixture{
		taskID:     "op-3",
		model:      "gpt-5",
		startedAt:  time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC),
		finishedAt: time.Date(2026, 7, 3, 10, 15, 0, 0, time.UTC),
	})

	stdout, stderr := executeCommand(t, []string{"task", "stats", "--group", "day"})

	is.Empty(stderr)
	for _, want := range []string{
		"Aggregate stats grouped by day",
		"Estimated API-equivalent cost",
		"Tasks without resolved timestamp: 1",
		"Resolved Tasks",
		"Lifecycle Time",
		"Agent Work",
		"Token Usage",
		"Estimated API-Equivalent Cost",
		"PERIOD", "RESOLVED_TASKS", "TREND",
		"FULL_AVG", "IMPLEMENTATION_AVG",
		"ACTIVE_AVG", "ACTIVE_TOTAL",
		"TOTAL_TOKENS", "UNKNOWN_USAGE", "UNKNOWN_COST",
	} {
		is.Contains(stdout, want)
	}
	is.Regexp(`(?m)^2026-07-02\s+2\s+#{20}$`, stdout)
	is.Regexp(`(?m)^2026-07-03\s+1\s+#{10}$`, stdout)
	is.Regexp(`(?m)^2026-07-02\s+23h0m0s\s+0\s+2h0m0s\s+0$`, stdout)
	is.Regexp(`(?m)^2026-07-03\s+-\s+1\s+1h0m0s\s+0$`, stdout)
	is.Regexp(`(?m)^2026-07-02\s+2\s+25m0s\s+50m0s$`, stdout)
	is.Regexp(`(?m)^2026-07-03\s+1\s+15m0s\s+15m0s$`, stdout)
	is.Regexp(`(?m)^2026-07-02\s+3K\s+1\.5K\s+0$`, stdout)
	is.Regexp(`(?m)^2026-07-03\s+0\s+-\s+1$`, stdout)
	is.Regexp(`(?m)^2026-07-02\s+\$0\.000773\s+\$0\.000773\s+1$`, stdout)
	is.Regexp(`(?m)^2026-07-03\s+\$0\.000000\s+-\s+1$`, stdout)
	assertTaskStatsAggregateTableLinesWithinWidth(t, stdout, 80)
}

func TestTaskStatsAggregateGroupsResolvedTasksByMonth(t *testing.T) {
	is := assert.New(t)
	newTestState(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[
			{"id":"op-1","title":"July","status":"closed","priority":1,"issue_type":"task","created_at":"2026-07-01T00:00:00Z","closed_at":"2026-07-02T12:00:00Z"},
			{"id":"op-2","title":"August","status":"closed","priority":1,"issue_type":"task","created_at":"2026-08-01T00:00:00Z","closed_at":"2026-08-03T00:00:00Z"}
		]`},
	})

	stdout, stderr := executeCommand(t, []string{"task", "stats", "--group", "month"})

	is.Empty(stderr)
	is.Contains(stdout, "Aggregate stats grouped by month")
	is.Contains(stdout, "Tasks without resolved timestamp: 0")
	is.Regexp(`(?m)^2026-07\s+1\s+#{20}$`, stdout)
	is.Regexp(`(?m)^2026-08\s+1\s+#{20}$`, stdout)
	is.Regexp(`(?m)^2026-07\s+36h0m0s\s+0\s+-\s+1$`, stdout)
	is.Regexp(`(?m)^2026-08\s+48h0m0s\s+0\s+-\s+1$`, stdout)
	assertTaskStatsAggregateTableLinesWithinWidth(t, stdout, 80)
}

func assertTaskStatsAggregateTableLinesWithinWidth(t *testing.T, output string, width int) {
	t.Helper()

	for _, line := range strings.Split(output, "\n") {
		if line == "" ||
			strings.HasPrefix(line, "Aggregate stats grouped by ") ||
			strings.HasPrefix(line, "Estimated API-equivalent cost is calculated") ||
			strings.HasPrefix(line, "Tasks without resolved timestamp: ") {
			continue
		}
		lineWidth := len([]rune(line))
		if lineWidth > width {
			t.Fatalf("aggregate table line width = %d, want <= %d:\n%s", lineWidth, width, output)
		}
	}
}

type taskStatsAggregateRunFixture struct {
	taskID     string
	model      string
	startedAt  time.Time
	finishedAt time.Time
	usage      *taskstate.AgentUsage
}

func recordTaskStatsAggregateRun(
	t *testing.T,
	stateStore taskstate.Store,
	now *time.Time,
	repoDir string,
	fixture taskStatsAggregateRunFixture,
) {
	t.Helper()
	must := require.New(t)

	*now = fixture.startedAt
	run, err := stateStore.StartRun("alpha", fixture.taskID, taskstate.StartRunOptions{
		Agent:    "codex",
		Profile:  "codex-profile",
		Harness:  "codex",
		Model:    fixture.model,
		Command:  "codex",
		Args:     []string{"exec", "--model", fixture.model},
		Branch:   "main",
		Worktree: repoDir,
	})
	must.NoError(err)
	if fixture.usage != nil {
		_, err = stateStore.RecordRunUsage("alpha", fixture.taskID, run.Attempt, taskstate.RecordRunUsageOptions{
			Usage: fixture.usage,
		})
		must.NoError(err)
	}

	*now = fixture.finishedAt
	_, err = stateStore.FinishRun("alpha", fixture.taskID, run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)
}

func TestTaskShowRendersClosedItemsAndHistory(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(t.TempDir(), "alpha")
	must.NoError(os.MkdirAll(repoDir, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:          "alpha",
		Name:        "Alpha",
		Path:        repoDir,
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "op",
	}}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[{"id":"op-closed","title":"done","status":"closed","priority":2,"issue_type":"task"}]`},
	})

	stateStore := taskstate.NewStoreWithClock(paths, func() time.Time {
		return time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	})
	_, err := stateStore.RecordTaskClosed("alpha", "op-closed", taskstate.TaskClosedOptions{
		Reason:          taskstate.CloseReasonPRMerged,
		PRURL:           "https://github.test/org/alpha/pull/42",
		ObservedPRState: "merged",
	})
	must.NoError(err)

	stdout, stderr := executeCommand(t, []string{"task", "show", "op-closed"})

	is.Empty(stderr)
	is.Contains(stdout, "ID: op-closed")
	is.Contains(stdout, "Status: closed")
	is.Contains(stdout, "History:\n  2026-01-02T03:04:05Z Task closed\n")
}

//nolint:funlen // The history sequence is the behavior under test.
func TestTaskShowRendersChronologicalHistoryForClosedEpic(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(t.TempDir(), "alpha")
	must.NoError(os.MkdirAll(repoDir, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:          "alpha",
		Name:        "Alpha",
		Path:        repoDir,
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "op",
	}}}))

	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	stateStore := taskstate.NewStoreWithClock(paths, func() time.Time { return now })
	_, err := stateStore.RecordSetupEvent("alpha", "op-epic", taskstate.EventWorktreeCreated, taskstate.SetupEventOptions{})
	must.NoError(err)
	now = now.Add(time.Minute)
	taskState, err := stateStore.Load("alpha", "op-epic")
	must.NoError(err)
	taskState.Events = append(taskState.Events, taskstate.Event{
		Type: taskstate.EventWorktreeReused,
		At:   now,
	})
	must.NoError(paths.WriteDataYAML(filepath.Join("repos", "alpha", "tasks", "op-epic.yaml"), taskState))
	now = now.Add(time.Minute)
	run, err := stateStore.StartRun("alpha", "op-epic", taskstate.StartRunOptions{Agent: "codex"})
	must.NoError(err)
	now = now.Add(time.Minute)
	_, err = stateStore.CompleteRun("alpha", "op-epic", run.Attempt, taskstate.CompleteRunOptions{
		Summary:             "Record task history",
		Description:         "Recorded completion history.",
		DetailedDescription: "Detailed history.",
	})
	must.NoError(err)
	now = now.Add(time.Minute)
	_, err = stateStore.FinishRun("alpha", "op-epic", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)
	now = now.Add(time.Minute)
	_, err = stateStore.RecordFinalizationCommit("alpha", "op-epic", "abc123")
	must.NoError(err)
	now = now.Add(time.Minute)
	_, err = stateStore.RecordFinalizationPush("alpha", "op-epic", taskstate.FinalizationPushOptions{
		Branch:     "main",
		PushTarget: taskstate.PushTargetMain,
	})
	must.NoError(err)
	now = now.Add(time.Minute)
	_, err = stateStore.RecordFinalizationClose("alpha", "op-epic", taskstate.FinalizationCloseOptions{
		Reason: taskstate.CloseReasonDefaultBranchPublished,
	})
	must.NoError(err)

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[{"id":"op-epic","title":"Closed epic","status":"closed","priority":1,"issue_type":"epic"}]`},
	})

	stdout, stderr := executeCommand(t, []string{"task", "show", "op-epic"})

	is.Empty(stderr)
	is.Contains(stdout, "Type: epic")
	is.Contains(stdout, "Status: closed")
	first := strings.Index(stdout, "2026-01-02T03:04:05Z Worktree created")
	second := strings.Index(stdout, "2026-01-02T03:06:05Z Run started")
	third := strings.Index(stdout, "2026-01-02T03:07:05Z Completion recorded")
	fourth := strings.Index(stdout, "2026-01-02T03:08:05Z Run finished")
	fifth := strings.Index(stdout, "2026-01-02T03:10:05Z Pushed main")
	sixth := strings.Index(stdout, "2026-01-02T03:11:05Z Task closed")
	is.Greater(first, -1)
	is.Greater(second, first)
	is.Greater(third, second)
	is.Greater(fourth, third)
	is.Greater(fifth, fourth)
	is.Greater(sixth, fifth)
	is.NotContains(stdout, "Worktree reused")
	is.NotContains(stdout, "codex")
	is.NotContains(stdout, "succeeded")
}

func TestTaskShowProjectsReviewAttemptMilestonesIntoHistory(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")

	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	stateStore := taskstate.NewStoreWithClock(paths, func() time.Time { return now })
	_, err := stateStore.RecordSetupEvent(
		"alpha",
		"op-review",
		taskstate.EventWorktreeCreated,
		taskstate.SetupEventOptions{},
	)
	must.NoError(err)
	now = now.Add(time.Minute)
	recordTaskShowReviewAttempt(t, stateStore, &now, taskstate.ReviewStatusPassed)
	_, err = stateStore.StartRun("alpha", "op-review", taskstate.StartRunOptions{Agent: "codex"})
	must.NoError(err)
	now = now.Add(time.Minute)
	recordTaskShowReviewAttempt(t, stateStore, &now, taskstate.ReviewStatusBlocked)
	recordTaskShowReviewAttempt(t, stateStore, &now, taskstate.ReviewStatusFailed)
	recordTaskShowReviewAttempt(t, stateStore, &now, taskstate.ReviewStatusAborted)

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[{"id":"op-review","title":"Review history","status":"open","priority":1,"issue_type":"task"}]`},
	})

	stdout, stderr := executeCommand(t, []string{"task", "show", "op-review"})

	is.Empty(stderr)
	expected := []string{
		"2026-01-02T03:04:05Z Worktree created",
		"2026-01-02T03:05:05Z Review attempt 1 started",
		"2026-01-02T03:06:05Z Review attempt 1 passed",
		"2026-01-02T03:07:05Z Run started",
		"2026-01-02T03:08:05Z Review attempt 2 started",
		"2026-01-02T03:09:05Z Review attempt 2 blocked",
		"2026-01-02T03:10:05Z Review attempt 3 started",
		"2026-01-02T03:11:05Z Review attempt 3 failed",
		"2026-01-02T03:12:05Z Review attempt 4 started",
		"2026-01-02T03:13:05Z Review attempt 4 aborted",
	}
	previous := strings.Index(stdout, "History:")
	is.Greater(previous, -1)
	for _, want := range expected {
		index := strings.Index(stdout, want)
		is.Greater(index, previous, want)
		previous = index
	}
}

func TestTaskShowProjectsReviewFollowUpCreationIntoHistory(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")

	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	stateStore := taskstate.NewStoreWithClock(paths, func() time.Time { return now })
	review, err := stateStore.StartReviewWithOptions("alpha", "op-review", taskstate.StartReviewOptions{
		Pipeline: "local",
		Step:     "manual",
	})
	must.NoError(err)
	_, err = stateStore.RecordReviewFinding("alpha", "op-review", review.Attempt, taskstate.ReviewFinding{
		Type:        taskstate.FindingTypeSeparateTask,
		Title:       "Extract helper",
		Description: "Track separately.",
		TaskProposal: taskstate.ReviewTaskProposal{
			Title:              "Extract helper",
			Description:        "Extract helper separately.",
			AcceptanceCriteria: "Helper extraction has tests.",
		},
	})
	must.NoError(err)
	now = now.Add(time.Minute)
	_, err = stateStore.RecordReviewFindingCreatedTask("alpha", "op-review", review.Attempt, 0, "op-42")
	must.NoError(err)

	taskState, err := stateStore.Load("alpha", "op-review")
	must.NoError(err)
	taskState.Reviews[0].Findings = append(taskState.Reviews[0].Findings, taskstate.ReviewFinding{
		Type:          taskstate.FindingTypeSeparateTask,
		Title:         "Legacy follow-up",
		Description:   "Created before timestamps were recorded.",
		CreatedTaskID: "op-legacy",
		TaskProposal: taskstate.ReviewTaskProposal{
			Title:              "Legacy follow-up",
			Description:        "Created before timestamps were recorded.",
			AcceptanceCriteria: "Legacy task exists.",
		},
	})
	must.NoError(paths.WriteDataYAML(filepath.Join("repos", "alpha", "tasks", "op-review.yaml"), taskState))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[{"id":"op-review","title":"Review history","status":"open","priority":1,"issue_type":"task"}]`},
	})

	stdout, stderr := executeCommand(t, []string{"task", "show", "op-review"})

	is.Empty(stderr)
	is.Contains(stdout, "2026-01-02T03:04:05Z Review attempt 1 started")
	is.Contains(stdout, "2026-01-02T03:05:05Z Review attempt 1 finding 1 created follow-up task op-42")
	is.NotContains(stdout, "op-legacy")
}

func recordTaskShowReviewAttempt(
	t *testing.T,
	store taskstate.Store,
	now *time.Time,
	status taskstate.ReviewStatus,
) {
	t.Helper()

	must := require.New(t)
	reviewAttempt, err := store.StartReviewWithOptions("alpha", "op-review", taskstate.StartReviewOptions{
		Pipeline: "local",
		Step:     "manual",
	})
	must.NoError(err)
	*now = now.Add(time.Minute)
	_, err = store.FinishReview(
		"alpha",
		"op-review",
		reviewAttempt.Attempt,
		status,
	)
	must.NoError(err)
	*now = now.Add(time.Minute)
}

func TestTaskShowFailsWhenLocalTaskStateCannotBeLoaded(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(t.TempDir(), "alpha")
	must.NoError(os.MkdirAll(repoDir, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:          "alpha",
		Name:        "Alpha",
		Path:        repoDir,
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "op",
	}}}))

	statePath, err := taskstate.NewStore(paths).Path("alpha", "op-corrupt")
	must.NoError(err)
	must.NoError(os.MkdirAll(filepath.Dir(statePath), 0o755))
	must.NoError(os.WriteFile(statePath, []byte("version: 1\nrepo_id: wrong\ntask_id: op-corrupt\n"), 0o600))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[{"id":"op-corrupt","title":"Corrupt state","status":"open","priority":1,"issue_type":"task"}]`},
	})

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "show", "op-corrupt"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "task show op-corrupt: load local task-state for repo alpha")
	is.ErrorContains(err, `repo_id is "wrong", expected "alpha"`)
}

func TestTaskShowRendersActiveNonTaskItems(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(t.TempDir(), "alpha")
	must.NoError(os.MkdirAll(repoDir, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:          "alpha",
		Name:        "Alpha",
		Path:        repoDir,
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "op",
	}}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[{"id":"op-bug","title":"bug","status":"open","priority":2,"issue_type":"bug"}]`},
	})

	stdout, stderr := executeCommand(t, []string{"task", "show", "op-bug"})

	is.Empty(stderr)
	for _, want := range []string{"Repository:", "ID: alpha", "Task:", "ID: op-bug", "Title: bug", "Status: open", "Priority: 2", "Type: bug"} {
		is.Contains(stdout, want)
	}
}

func TestTaskDirPrintsWorktreeDirectory(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(t.TempDir(), "alpha")
	otherRepoDir := filepath.Join(t.TempDir(), "beta")
	worktreeDir := filepath.Join(t.TempDir(), "op-1-worktree")
	must.NoError(os.MkdirAll(repoDir, 0o755))
	must.NoError(os.MkdirAll(otherRepoDir, 0o755))
	must.NoError(os.MkdirAll(worktreeDir, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{
		{
			ID:            "alpha",
			Name:          "Alpha",
			Path:          repoDir,
			DefaultBranch: "main",
			BeadsMode:     registry.BeadsModeLocal,
			BeadsPrefix:   "op",
		},
		{
			ID:            "beta",
			Name:          "Beta",
			Path:          otherRepoDir,
			DefaultBranch: "main",
			BeadsMode:     registry.BeadsModeLocal,
			BeadsPrefix:   "bt",
		},
	}}))

	logPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[
			{
				"id":"op-1",
				"title":"Worktree task",
				"status":"in_progress",
				"priority":2,
				"issue_type":"task",
				"metadata":{"orpheus.branch":"orpheus/op-1","orpheus.worktree":"` + worktreeDir + `"}
			}
		]`},
		otherRepoDir: {stderr: "other repo should not be queried", exitCode: 70},
	})

	stdout, stderr := executeCommand(t, []string{"task", "dir", "op-1"})

	is.Empty(stderr)
	is.Equal(worktreeDir+"\n", stdout)

	logData, err := os.ReadFile(logPath)
	must.NoError(err)
	log := string(logData)
	is.Contains(log, repoDir)
	is.NotContains(log, otherRepoDir)
	is.Contains(log, "--json --readonly --sandbox show --id op-1")
	is.NotContains(log, "--json --readonly --sandbox list")
}

func TestTaskDirPrintsRepoRootForMainTask(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(t.TempDir(), "alpha")
	metadataRepoDir := filepath.Join(repoDir, ".")
	must.NoError(os.MkdirAll(repoDir, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha",
		Path:          repoDir,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[
			{
				"id":"op-main",
				"title":"Main task",
				"status":"in_progress",
				"priority":1,
				"issue_type":"task",
				"metadata":{"orpheus.branch":"main","orpheus.worktree":"` + metadataRepoDir + `"}
			}
		]`},
	})

	stdout, stderr := executeCommand(t, []string{"task", "dir", "op-main"})

	is.Empty(stderr)
	is.Equal(filepath.Clean(repoDir)+"\n", stdout)
}

func TestTaskDirReportsMalformedAndUnknownPrefixes(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "dir", "notprefixed"})
	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "malformed task id")
	is.ErrorContains(err, "expected <prefix>-<number>")

	stdout, stderr, err = executeCommandWithError(t, []string{"task", "dir", "zz-1"})
	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "unknown task id prefix")
	is.ErrorContains(err, "orpheus repo list")
	is.ErrorContains(err, "register the repo")
}

func TestTaskDirReportsMissingAndInconsistentMetadata(t *testing.T) {
	for _, tc := range taskDirMetadataErrorCases() {
		t.Run(tc.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			newTestState(t)
			paths := currentTestPaths(t)
			store := registry.NewStore(paths)

			repoDir := filepath.Join(t.TempDir(), "alpha")
			must.NoError(os.MkdirAll(repoDir, 0o755))
			must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
				ID:            "alpha",
				Name:          "Alpha",
				Path:          repoDir,
				DefaultBranch: "main",
				BeadsMode:     registry.BeadsModeLocal,
				BeadsPrefix:   "op",
			}}}))

			withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
				repoDir: {stdout: `[
					{
						"id":"` + tc.taskID + `",
						"title":"Task dir metadata case",
						"status":"in_progress",
						"priority":1,
						"issue_type":"task",
						"metadata":` + tc.metadata + `
					}
				]`},
			})

			stdout, stderr, err := executeCommandWithError(t, []string{"task", "dir", tc.taskID})

			must.Error(err)
			is.Empty(stdout)
			is.Empty(stderr)
			is.ErrorContains(err, "task dir "+tc.taskID)
			is.ErrorContains(err, tc.wantMessage)
		})
	}
}

type taskDirMetadataErrorCase struct {
	name        string
	taskID      string
	metadata    string
	wantMessage string
}

func taskDirMetadataErrorCases() []taskDirMetadataErrorCase {
	return []taskDirMetadataErrorCase{
		{
			name:        "missing worktree",
			taskID:      "op-missing",
			metadata:    `{}`,
			wantMessage: "task has no Orpheus working directory metadata",
		},
		{
			name:        "missing branch",
			taskID:      "op-incomplete",
			metadata:    `{"orpheus.worktree":"/tmp/op-incomplete"}`,
			wantMessage: "orpheus.branch is missing",
		},
		{
			name:        "inconsistent target",
			taskID:      "op-inconsistent",
			metadata:    `{"orpheus.branch":"main","orpheus.worktree":"/tmp/op-inconsistent"}`,
			wantMessage: "task Orpheus target metadata is inconsistent",
		},
	}
}

//nolint:funlen // Workflow test is clearer when setup, command, and state assertions stay together.
func TestTaskRunExecutesImplementerDefaultAttachedFromDeterministicWorktree(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
		TitleTemplate: "[{{external_ref}}] {{summary}}",
	}}}))

	bdLogPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[
			{
				"id":"op-1",
				"title":"Implement attached run",
				"external_ref":"TREX-1234",
				"description":"Resolve the task and launch the configured agent.",
				"acceptance_criteria":"The agent gets the rendered prompt and ORPHEUS environment.",
				"status":"open",
				"priority":2,
				"issue_type":"task"
			}
		]`},
	})
	agentLogPath := withFakeAgent(t, "fake-agent", 0)
	writeTaskRunAgentConfig(
		t,
		paths,
		"recorder",
		"fake-agent",
		[]string{"--name", "{{session_name}}", "--prompt", "{{prompt}}", "--literal", "unchanged"},
	)
	worktreePath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-1"))
	must.NoError(err)

	stdout, stderr := executeCommand(t, []string{"task", "run", "op-1"})

	is.Contains(stdout, "fake agent stdout")
	is.Contains(stderr, "fake agent stderr")
	is.NotContains(stderr, "Orpheus M3 WIP")
	is.NotContains(stderr, "running attached agent")

	bdLog, err := os.ReadFile(bdLogPath)
	must.NoError(err)
	is.Contains(string(bdLog), repoPath)
	is.Contains(string(bdLog), "--json --readonly --sandbox show --id op-1")
	is.Contains(string(bdLog), "--json --sandbox update op-1 --status in_progress --set-metadata orpheus.branch=orpheus/op-1 --set-metadata orpheus.worktree="+worktreePath)
	is.NotContains(string(bdLog), "--json --readonly --sandbox list")

	log := readTestFileString(t, agentLogPath)
	for _, want := range []string{
		"PWD=" + worktreePath,
		"ARG_COUNT=6",
		"ARG_1<<END\n--name\nEND",
		"ARG_2<<END\nImplementing op-1 Implement attached run\nEND",
		"ARG_3<<END\n--prompt\nEND",
		"ARG_5<<END\n--literal\nEND",
		"ARG_6<<END\nunchanged\nEND",
		"ORPHEUS_REPO_ID=alpha",
		"ORPHEUS_TASK_ID=op-1",
		"ORPHEUS_WORKTREE=" + worktreePath,
		"ORPHEUS_BRANCH=orpheus/op-1",
		"ORPHEUS_AGENT_PROMPT<<END",
		"Run `orpheus agent context` now",
		"task instructions and execution contract",
	} {
		is.Contains(log, want)
	}
	promptArg := agentLogBlock(t, log, "ARG_4")
	is.Contains(promptArg, "You are an agent dispatched by Orpheus.")
	is.NotContains(promptArg, "Implement attached run")
	is.NotContains(log, "Resolve the task and launch the configured agent.")
	is.NotContains(log, "The agent gets the rendered prompt and ORPHEUS environment.")
	is.NotContains(log, "- Deterministic worktree: "+worktreePath)
	is.NotContains(log, "Summary:")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-1.yaml"), &state))
	must.Len(state.Runs, 1)
	is.Equal(1, state.Runs[0].Attempt)
	is.Equal(taskstate.RunStatusSucceeded, state.Runs[0].Status)
	is.Equal("recorder", state.Runs[0].Execution.Agent)
	is.Equal("recorder", state.Runs[0].Execution.Profile)
	is.Equal("fake-agent", state.Runs[0].Execution.Command)
	is.Equal("Implementing op-1 Implement attached run", state.Runs[0].Execution.SessionName)
	must.Len(state.Runs[0].Execution.Args, 6)
	is.Equal("--name", state.Runs[0].Execution.Args[0])
	is.Equal("Implementing op-1 Implement attached run", state.Runs[0].Execution.Args[1])
	is.Equal("--prompt", state.Runs[0].Execution.Args[2])
	is.Contains(state.Runs[0].Execution.Args[3], "You are an agent dispatched by Orpheus.")
	is.Contains(state.Runs[0].Execution.Args[3], "Run `orpheus agent context` now")
	is.NotContains(state.Runs[0].Execution.Args[3], "Implement attached run")
	is.Equal("--literal", state.Runs[0].Execution.Args[4])
	is.Equal("unchanged", state.Runs[0].Execution.Args[5])
	is.Equal("orpheus/op-1", state.Target.Branch)
	is.Equal(worktreePath, state.Target.Worktree)
	must.NotNil(state.Runs[0].Execution.FinishedAt)
	must.Len(state.Events, 3)
	is.Equal(taskstate.EventWorktreeCreated, state.Events[0].Type)
	is.Equal(taskstate.EventRunStarted, state.Events[1].Type)
	is.Equal(taskstate.EventRunFinished, state.Events[2].Type)

	secondStdout, secondStderr := executeCommand(t, []string{"task", "run", "op-1"})

	is.Contains(secondStdout, "fake agent stdout")
	is.Contains(secondStderr, "fake agent stderr")
	var retriedState taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-1.yaml"), &retriedState))
	must.Len(retriedState.Runs, 2)
	is.Equal(1, retriedState.Runs[0].Attempt)
	is.Equal(2, retriedState.Runs[1].Attempt)
	is.Equal(taskstate.RunStatusSucceeded, retriedState.Runs[0].Status)
	is.Equal(taskstate.RunStatusSucceeded, retriedState.Runs[1].Status)
	must.Len(retriedState.Events, 6)
	is.Equal(taskstate.EventWorktreeReused, retriedState.Events[3].Type)
	is.Equal(taskstate.EventRunStarted, retriedState.Events[4].Type)
	is.Equal(taskstate.EventRunFinished, retriedState.Events[5].Type)
}

//nolint:funlen // The generated Codex launch contract is best asserted end to end.
func TestTaskRunStructuredCodexProfileBuildsAttachedCommand(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[
			{
				"id":"op-codex",
				"title":"Structured Codex",
				"description":"Launch Codex through the structured profile.",
				"status":"open",
				"priority":2,
				"issue_type":"task"
			}
		]`},
	})
	agentLogPath := withFakeAgent(t, "codex", 0)
	writeStructuredCodexTaskRunAgentConfig(t, paths, "codex-medium", "gpt-5.4", "high", true)
	worktreePath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-codex"))
	must.NoError(err)

	stdout, stderr := executeCommand(t, []string{"task", "run", "op-codex"})

	is.Contains(stdout, "fake agent stdout")
	is.Contains(stderr, "fake agent stderr")

	log := readTestFileString(t, agentLogPath)
	for _, want := range []string{
		"PWD=" + worktreePath,
		"ARG_COUNT=6",
		"ARG_1<<END\n--model\nEND",
		"ARG_2<<END\ngpt-5.4\nEND",
		"ARG_3<<END\n--dangerously-bypass-approvals-and-sandbox\nEND",
		"ARG_4<<END\n-c\nEND",
		"ARG_5<<END\nmodel_reasoning_effort=high\nEND",
	} {
		is.Contains(log, want)
	}
	promptArg := agentLogBlock(t, log, "ARG_6")
	is.Contains(promptArg, "Implementing op-codex Structured Codex - ")
	is.Contains(promptArg, "You are an agent dispatched by Orpheus.")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-codex.yaml"), &state))
	must.Len(state.Runs, 1)
	execution := state.Runs[0].Execution
	is.Equal("codex-medium", execution.Agent)
	is.Equal("codex", execution.Harness)
	is.Equal("gpt-5.4", execution.Model)
	is.Equal("codex", execution.Command)
	is.Equal([]string{
		"--model",
		"gpt-5.4",
		"--dangerously-bypass-approvals-and-sandbox",
		"-c",
		"model_reasoning_effort=high",
		"Implementing op-codex Structured Codex - " + agent.RenderBootstrapPrompt(),
	}, execution.Args)
}

func TestTaskRunRejectsMissingRequiredExternalReferenceBeforeSetup(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "gated"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "gated",
		Name:          "Gated Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "gt",
		TitleTemplate: "[{{external_ref}}] {{summary}}",
	}}}))

	bdLogPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[
			{"id":"gt-1","title":"Missing external reference","status":"open","priority":1,"issue_type":"task"}
		]`},
	})

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "run", "gt-1"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "publication title template requires a task external reference")
	is.ErrorContains(err, "bd update gt-1 --external-ref <reference>")

	bdLog := readFileString(t, bdLogPath)
	is.Contains(bdLog, "--json --readonly --sandbox show --id gt-1")
	is.NotContains(bdLog, "--json --sandbox update")

	worktreePath, pathErr := paths.DataPath(filepath.Join("repos", "gated", "worktrees", "gt-1"))
	must.NoError(pathErr)
	_, statErr := os.Stat(worktreePath)
	is.ErrorIs(statErr, os.ErrNotExist)

	statePath, pathErr := paths.DataPath(filepath.Join("repos", "gated", "tasks", "gt-1.yaml"))
	must.NoError(pathErr)
	_, statErr = os.Stat(statePath)
	is.ErrorIs(statErr, os.ErrNotExist)
}

func TestTaskRunRejectsChildWhenImmediateParentEpicIsNotInProgress(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	bdLogPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[
			{"id":"op-child","title":"Child task","status":"open","priority":1,"issue_type":"task","parent":"op-epic"},
			{"id":"op-epic","title":"Paused epic","status":"open","priority":1,"issue_type":"epic"}
		]`},
	})

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "run", "op-child"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "immediate parent epic op-epic is open")
	is.ErrorContains(err, "immediate parent epic must be in_progress")

	bdLog := readFileString(t, bdLogPath)
	is.Contains(bdLog, "--json --readonly --sandbox show --id op-child")
	is.Contains(bdLog, "--json --readonly --sandbox list --all --limit 0")
	is.NotContains(bdLog, "--json --sandbox update")

	worktreePath, pathErr := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-child"))
	must.NoError(pathErr)
	_, statErr := os.Stat(worktreePath)
	is.ErrorIs(statErr, os.ErrNotExist)
}

//nolint:funlen // Workflow test is clearer when setup, command, and state assertions stay together.
func TestTaskRunMainExecutesAgentFromRegisteredRepoRoot(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	runGit(t, repoPath, "checkout", "-b", "manual-review")
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	bdLogPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[
			{
				"id":"op-main",
				"title":"Run in repo root",
				"description":"Use the registered default branch checkout.",
				"acceptance_criteria":"The agent runs from the repo root.",
				"status":"open",
				"priority":2,
				"issue_type":"task"
			}
		]`},
	})
	agentLogPath := withFakeAgent(t, "main-agent", 0)
	writeTaskRunAgentConfig(t, paths, "main", "main-agent", nil)
	worktreePath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-main"))
	must.NoError(err)

	stdout, stderr := executeCommand(t, []string{"task", "run", "--main", "op-main"})

	is.Contains(stdout, "fake agent stdout")
	is.Contains(stderr, "fake agent stderr")
	is.Equal("main", strings.TrimSpace(runGit(t, repoPath, "symbolic-ref", "--quiet", "--short", "HEAD")))
	_, statErr := os.Stat(worktreePath)
	must.ErrorIs(statErr, os.ErrNotExist)

	bdLog, err := os.ReadFile(bdLogPath)
	must.NoError(err)
	is.Contains(string(bdLog), "--json --readonly --sandbox show --id op-main")
	is.Contains(string(bdLog), "--json --readonly --sandbox list --all --limit 0")
	is.Contains(string(bdLog), "--json --sandbox update op-main --status in_progress --set-metadata orpheus.branch=main --set-metadata orpheus.worktree="+repoPath)
	is.NotContains(string(bdLog), "orpheus/op-main")

	agentLog, err := os.ReadFile(agentLogPath)
	must.NoError(err)
	log := string(agentLog)
	for _, want := range []string{
		"PWD=" + repoPath,
		"ORPHEUS_REPO_ID=alpha",
		"ORPHEUS_TASK_ID=op-main",
		"ORPHEUS_WORKTREE=" + repoPath,
		"ORPHEUS_BRANCH=main",
		"Run `orpheus agent context` now",
		"task instructions and execution contract",
	} {
		is.Contains(log, want)
	}
	is.NotContains(log, "- Registered repo root: "+repoPath)
	is.NotContains(log, "- Registered default branch: main")
	is.NotContains(log, "- Deterministic worktree")
	is.NotContains(log, "- Deterministic branch")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	must.Len(state.Runs, 1)
	is.Equal(taskstate.RunStatusSucceeded, state.Runs[0].Status)
	is.Equal("main", state.Target.Branch)
	is.Equal(repoPath, state.Target.Worktree)
	must.Len(state.Events, 3)
	is.Equal(taskstate.EventWorktreeReused, state.Events[0].Type)
	is.Equal(taskstate.EventRunStarted, state.Events[1].Type)
	is.Equal(taskstate.EventRunFinished, state.Events[2].Type)
}

//nolint:funlen // Workflow test is clearer when setup, command, and state assertions stay together.
func TestTaskRunRepoRootExecutesAgentFromRegisteredRepoRootOnTaskBranch(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	bdLogPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[
			{
				"id":"op-root",
				"title":"Run repo root task branch",
				"description":"Use the registered repo root on the task branch.",
				"acceptance_criteria":"The agent runs from the repo root.",
				"status":"open",
				"priority":2,
				"issue_type":"task"
			}
		]`},
	})
	agentLogPath := withFakeAgent(t, "repo-root-agent", 0)
	writeTaskRunAgentConfig(t, paths, "repo-root", "repo-root-agent", nil)
	worktreePath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-root"))
	must.NoError(err)

	stdout, stderr := executeCommand(t, []string{"task", "run", "--repo-root", "op-root"})

	is.Contains(stdout, "fake agent stdout")
	is.Contains(stderr, "fake agent stderr")
	is.Equal("orpheus/op-root", strings.TrimSpace(runGit(t, repoPath, "symbolic-ref", "--quiet", "--short", "HEAD")))
	_, statErr := os.Stat(worktreePath)
	must.ErrorIs(statErr, os.ErrNotExist)

	bdLog, err := os.ReadFile(bdLogPath)
	must.NoError(err)
	is.Contains(string(bdLog), "--json --readonly --sandbox show --id op-root")
	is.Contains(string(bdLog), "--json --readonly --sandbox list --all --limit 0")
	is.Contains(string(bdLog), "--json --sandbox update op-root --status in_progress --set-metadata orpheus.branch=orpheus/op-root --set-metadata orpheus.worktree="+repoPath)

	agentLog, err := os.ReadFile(agentLogPath)
	must.NoError(err)
	log := string(agentLog)
	for _, want := range []string{
		"PWD=" + repoPath,
		"ORPHEUS_REPO_ID=alpha",
		"ORPHEUS_TASK_ID=op-root",
		"ORPHEUS_WORKTREE=" + repoPath,
		"ORPHEUS_BRANCH=orpheus/op-root",
		"Run `orpheus agent context` now",
		"task instructions and execution contract",
	} {
		is.Contains(log, want)
	}

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-root.yaml"), &state))
	must.Len(state.Runs, 1)
	is.Equal(taskstate.RunStatusSucceeded, state.Runs[0].Status)
	is.Equal("orpheus/op-root", state.Target.Branch)
	is.Equal(repoPath, state.Target.Worktree)
	must.Len(state.Events, 3)
	is.Equal(taskstate.EventTaskBranchCreated, state.Events[0].Type)
	is.Equal(taskstate.EventRunStarted, state.Events[1].Type)
	is.Equal(taskstate.EventRunFinished, state.Events[2].Type)

	_, _, retryErr := executeCommandWithError(t, []string{"task", "run", "--repo-root", "op-root"})

	must.Error(retryErr)
	is.Contains(retryErr.Error(), "already has target branch")
	is.Contains(retryErr.Error(), "retry without --repo-root")
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-root.yaml"), &state))
	must.Len(state.Events, 3)
	is.Equal(taskstate.EventTaskBranchCreated, state.Events[0].Type)
	is.Equal(taskstate.EventRunStarted, state.Events[1].Type)
	is.Equal(taskstate.EventRunFinished, state.Events[2].Type)
}

func TestTaskRunPlainRetryRequiresMainForRepoRootMetadata(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	worktreePath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-main"))
	must.NoError(err)

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[
			{
				"id":"op-main",
				"title":"Retry main task",
				"status":"in_progress",
				"priority":1,
				"issue_type":"task",
				"metadata":{"orpheus.branch":"main","orpheus.worktree":"` + repoPath + `"}
			}
		]`},
	})

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "run", "op-main"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "repo-root/default-branch metadata")
	is.ErrorContains(err, "retry with `orpheus task run --main op-main`")
	_, statErr := os.Stat(worktreePath)
	is.ErrorIs(statErr, os.ErrNotExist)
}

func TestTaskRunRepoRootFailsDirtyRepoRootBeforeLaunch(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(os.WriteFile(filepath.Join(repoPath, "dirty.txt"), []byte("dirty"), 0o644))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[{"id":"op-root-dirty","title":"Dirty root","status":"open","priority":1,"issue_type":"task"}]`},
	})
	agentLogPath := withFakeAgent(t, "dirty-root-agent", 0)
	writeTaskRunAgentConfig(t, paths, "dirty-root", "dirty-root-agent", nil)

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "run", "--repo-root", "op-root-dirty"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "uncommitted changes")
	is.Equal("main", strings.TrimSpace(runGit(t, repoPath, "symbolic-ref", "--quiet", "--short", "HEAD")))
	_, agentLogErr := os.Stat(agentLogPath)
	is.ErrorIs(agentLogErr, os.ErrNotExist)
	statePath, err := paths.DataPath(filepath.Join("repos", "alpha", "tasks", "op-root-dirty.yaml"))
	must.NoError(err)
	_, stateErr := os.Stat(statePath)
	is.ErrorIs(stateErr, os.ErrNotExist)
}

func TestTaskRunMainAllowsOwnedInProgressRepoRootTask(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	bdLogPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[
			{
				"id":"op-main",
				"title":"Retry owned repo-root task",
				"status":"in_progress",
				"priority":1,
				"issue_type":"task",
				"metadata":{"orpheus.branch":"main","orpheus.worktree":"` + repoPath + `"}
			}
		]`},
	})
	withFakeAgent(t, "main-retry-agent", 0)
	writeTaskRunAgentConfig(t, paths, "main-retry", "main-retry-agent", nil)

	stdout, stderr := executeCommand(t, []string{"task", "run", "--main", "op-main"})

	is.Contains(stdout, "fake agent stdout")
	is.Contains(stderr, "fake agent stderr")
	bdLog, err := os.ReadFile(bdLogPath)
	must.NoError(err)
	is.Contains(string(bdLog), "--json --readonly --sandbox show --id op-main")
	is.Contains(string(bdLog), "--json --readonly --sandbox list --all --limit 0")
	is.NotContains(string(bdLog), "--json --sandbox update")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	must.Len(state.Runs, 1)
	is.Equal(taskstate.RunStatusSucceeded, state.Runs[0].Status)
	is.Equal("main", state.Target.Branch)
	is.Equal(repoPath, state.Target.Worktree)
}

func TestTaskRunMainBlocksOtherRepoRootOwnerButWorktreeRunStillWorks(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	bdLogPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[
			{
				"id":"op-owner",
				"title":"Existing repo-root owner",
				"status":"in_progress",
				"priority":1,
				"issue_type":"task",
				"metadata":{"orpheus.branch":"main","orpheus.worktree":"` + repoPath + `"}
			},
			{"id":"op-next","title":"Next task","status":"open","priority":2,"issue_type":"task"}
		]`},
	})
	agentLogPath := withFakeAgent(t, "next-agent", 0)
	writeTaskRunAgentConfig(t, paths, "next", "next-agent", nil)

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "run", "--main", "op-next"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "already has non-closed task op-owner owning repo-root metadata")
	_, agentLogErr := os.Stat(agentLogPath)
	is.ErrorIs(agentLogErr, os.ErrNotExist)

	worktreeStdout, worktreeStderr := executeCommand(t, []string{"task", "run", "op-next"})

	is.Contains(worktreeStdout, "fake agent stdout")
	is.Contains(worktreeStderr, "fake agent stderr")
	bdLog, err := os.ReadFile(bdLogPath)
	must.NoError(err)
	is.Equal(1, strings.Count(string(bdLog), "--json --readonly --sandbox list --all --limit 0"))
}

func TestTaskRunMainFailsDirtyRepoRootBeforeLaunch(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(os.WriteFile(filepath.Join(repoPath, "dirty.txt"), []byte("dirty"), 0o644))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[{"id":"op-dirty","title":"Dirty root","status":"open","priority":1,"issue_type":"task"}]`},
	})
	agentLogPath := withFakeAgent(t, "dirty-agent", 0)
	writeTaskRunAgentConfig(t, paths, "dirty", "dirty-agent", nil)

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "run", "--main", "op-dirty"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "uncommitted changes")
	_, agentLogErr := os.Stat(agentLogPath)
	is.ErrorIs(agentLogErr, os.ErrNotExist)
	statePath, err := paths.DataPath(filepath.Join("repos", "alpha", "tasks", "op-dirty.yaml"))
	must.NoError(err)
	_, stateErr := os.Stat(statePath)
	is.ErrorIs(stateErr, os.ErrNotExist)
}

//nolint:funlen // The dirty follow-up dispatch path is clearer as one linear CLI workflow.
func TestTaskRunReviewFollowUpAllowsDirtyMainTarget(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	registryStore := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	must.NoError(registryStore.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	runStore := taskstate.NewStore(paths)
	initialRun, err := runStore.StartRun("alpha", "op-followup", taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "main",
		Worktree: repoPath,
	})
	must.NoError(err)
	_, err = runStore.FinishRun("alpha", "op-followup", initialRun.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)
	review, err := runStore.StartReview("alpha", "op-followup")
	must.NoError(err)
	_, err = runStore.RecordReviewFinding("alpha", "op-followup", review.Attempt, taskstate.ReviewFinding{
		Type:            taskstate.FindingTypeBlocking,
		Title:           "Fix bug",
		Description:     "The reviewed change still has a blocker.",
		SuggestedAction: "Patch the dirty candidate changes.",
	})
	must.NoError(err)
	_, err = runStore.FinishReview("alpha", "op-followup", review.Attempt, taskstate.ReviewStatusBlocked)
	must.NoError(err)

	taskJSON := `[
		{
			"id":"op-followup",
			"title":"Follow up dirty main",
			"status":"in_progress",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.branch":"main","orpheus.worktree":"` + repoPath + `"}
		}
	]`
	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: taskJSON},
	})
	withFakeAgent(t, "followup-agent", 0)
	writeTaskRunAgentConfig(t, paths, "followup", "followup-agent", nil)

	stdout, stderr := executeCommand(t, []string{"task", "run", "op-followup"})

	is.Contains(stdout, "fake agent stdout")
	is.Contains(stderr, "fake agent stderr")
	is.Contains(runGit(t, repoPath, "status", "--short"), "reviewed.txt")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-followup.yaml"), &state))
	latestReview, ok := taskstate.LatestReview(state)
	must.True(ok)
	must.Len(latestReview.Findings, 1)
	is.Equal(2, latestReview.Findings[0].TargetedByRunAttempt)
	must.Len(state.Runs, 2)
	is.Equal("main", state.Target.Branch)
	is.Equal(repoPath, state.Target.Worktree)
	is.Equal("Resolving issues in op-followup Follow up dirty main", state.Runs[1].Execution.SessionName)
	must.NotNil(state.Runs[1].ReviewFollowUp)
	is.Equal([]int{0}, state.Runs[1].ReviewFollowUp.FindingIndexes)
}

func TestTaskReviewShowDisplaysLatestFindingsAndCreatedFollowUps(t *testing.T) {
	is := assert.New(t)
	paths, repoPath := setupTaskReviewShowRepo(t, "op-main")
	seedTaskReviewShowState(t, paths, repoPath)

	stdout, stderr := executeCommand(t, []string{"task", "review", "show", "op-main"})

	is.Empty(stderr)
	for _, want := range []string{
		"Review state for op-main (repo alpha)",
		"Latest authoritative review attempt:",
		"Attempt: 2",
		"Status: blocked",
		"Pipeline: quality",
		"Current step: ai-review",
		"- unit-tests (check), exit code 1",
		"- ai-review (agent_review)",
		"Step: unit-tests",
		"Finding 1:",
		"Type: blocking",
		"Title: Tests fail",
		"Description: make test fails.",
		"Resolution: open",
		"Suggested action: Fix failing tests.",
		"Step: ai-review",
		"Title: Race condition",
		"Resolution: targeted by follow-up run attempt 1",
		"Title: Known limitation",
		"Resolution: waived: Accepted risk for now.",
		"Title: Extract helper",
		"Resolution: converted/created task op-42",
		"Created follow-up Beads:",
		"op-41 (review attempt 1, finding 1, step manual): Older cleanup",
		"op-42 (review attempt 2, finding 4, step ai-review): Extract helper",
		"Next step: run `orpheus task run op-main` to address open blocking findings",
	} {
		is.Contains(stdout, want)
	}
}

func TestTaskReviewShowGuidesWhenTaskHasNoReviewAttempts(t *testing.T) {
	is := assert.New(t)
	setupTaskReviewShowRepo(t, "op-empty")

	stdout, stderr := executeCommand(t, []string{"task", "review", "show", "op-empty"})

	is.Empty(stderr)
	is.Contains(stdout, "Review state for op-empty (repo alpha)")
	is.Contains(stdout, "No review attempts recorded for op-empty.")
	is.Contains(stdout, "Next step: run `orpheus task review op-empty` after task work is ready.")
}

func TestTaskReviewShowDisplaysClosedTaskReviewState(t *testing.T) {
	is := assert.New(t)
	paths, repoPath := setupTaskReviewShowRepoWithStatus(t, "op-main", "closed")
	seedTaskReviewShowState(t, paths, repoPath)

	stdout, stderr := executeCommand(t, []string{"task", "review", "show", "op-main"})

	is.Empty(stderr)
	is.Contains(stdout, "Review state for op-main (repo alpha)")
	is.Contains(stdout, "Latest authoritative review attempt:")
	is.Contains(stdout, "Status: blocked")
	is.Contains(stdout, "Title: Tests fail")
	is.Contains(stdout, "Created follow-up Beads:")
	is.Contains(stdout, "op-42 (review attempt 2, finding 4, step ai-review): Extract helper")
}

func setupTaskReviewShowRepo(t *testing.T, taskID string) (state.Paths, string) {
	t.Helper()

	return setupTaskReviewShowRepoWithStatus(t, taskID, "in_progress")
}

func setupTaskReviewShowRepoWithStatus(t *testing.T, taskID string, status string) (state.Paths, string) {
	t.Helper()

	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	registryStore := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(registryStore.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: taskReviewShowTaskJSON(taskID, repoPath, status)},
	})
	return paths, repoPath
}

func taskReviewShowTaskJSON(taskID string, repoPath string, status string) string {
	return `[
		{
			"id":"` + taskID + `",
			"title":"Ready for review show",
			"status":"` + status + `",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.branch":"main","orpheus.worktree":"` + repoPath + `"}
		}
	]`
}

func seedTaskReviewShowState(t *testing.T, paths state.Paths, repoPath string) {
	t.Helper()

	must := require.New(t)
	runStore := taskstate.NewStore(paths)
	oldReview := recordCreatedReviewFollowUp(t, runStore)
	_, err := runStore.FinishReview("alpha", "op-main", oldReview.Attempt, taskstate.ReviewStatusPassed)
	must.NoError(err)

	latestReview := recordMixedReviewFindings(t, runStore)
	_, err = runStore.FinishReview("alpha", "op-main", latestReview.Attempt, taskstate.ReviewStatusBlocked)
	must.NoError(err)
	followUpRun, err := runStore.StartRun("alpha", "op-main", taskstate.StartRunOptions{
		Agent:    "codex",
		Branch:   "main",
		Worktree: repoPath,
	})
	must.NoError(err)
	_, err = runStore.TargetReviewFindings("alpha", "op-main", latestReview.Attempt, []int{1}, followUpRun.Attempt)
	must.NoError(err)
}

func recordCreatedReviewFollowUp(t *testing.T, runStore taskstate.Store) taskstate.ReviewAttempt {
	t.Helper()

	must := require.New(t)
	oldReview, err := runStore.StartReviewWithOptions("alpha", "op-main", taskstate.StartReviewOptions{
		Pipeline: "manual",
		Step:     "manual",
	})
	must.NoError(err)
	_, err = runStore.RecordReviewStep("alpha", "op-main", oldReview.Attempt, taskstate.RecordReviewStepOptions{
		Kind: "manual",
		Name: "manual",
	})
	must.NoError(err)
	_, err = runStore.RecordReviewFinding("alpha", "op-main", oldReview.Attempt, taskstate.ReviewFinding{
		Type:        taskstate.FindingTypeSeparateTask,
		Title:       "Older cleanup",
		Description: "Track old cleanup separately.",
		Step:        "manual",
		TaskProposal: taskstate.ReviewTaskProposal{
			Title:              "Older cleanup",
			Description:        "Clean up old code.",
			AcceptanceCriteria: "Cleanup is tested.",
		},
	})
	must.NoError(err)
	_, err = runStore.RecordReviewFindingCreatedTask("alpha", "op-main", oldReview.Attempt, 0, "op-41")
	must.NoError(err)
	return oldReview
}

func recordMixedReviewFindings(t *testing.T, runStore taskstate.Store) taskstate.ReviewAttempt {
	t.Helper()

	must := require.New(t)
	latestReview, err := runStore.StartReviewWithOptions("alpha", "op-main", taskstate.StartReviewOptions{
		Pipeline: "quality",
		Step:     "unit-tests",
	})
	must.NoError(err)
	exitCode := 1
	_, err = runStore.RecordReviewStep("alpha", "op-main", latestReview.Attempt, taskstate.RecordReviewStepOptions{
		Kind:     "check",
		Name:     "unit-tests",
		ExitCode: &exitCode,
	})
	must.NoError(err)
	_, err = runStore.RecordReviewStep("alpha", "op-main", latestReview.Attempt, taskstate.RecordReviewStepOptions{
		Kind: "agent_review",
		Name: "ai-review",
	})
	must.NoError(err)
	for _, finding := range mixedReviewFindings() {
		_, err = runStore.RecordReviewFinding("alpha", "op-main", latestReview.Attempt, finding)
		must.NoError(err)
	}
	_, err = runStore.RecordReviewFindingCreatedTask("alpha", "op-main", latestReview.Attempt, 3, "op-42")
	must.NoError(err)
	return latestReview
}

func mixedReviewFindings() []taskstate.ReviewFinding {
	return []taskstate.ReviewFinding{
		{
			Type:            taskstate.FindingTypeBlocking,
			Title:           "Tests fail",
			Description:     "make test fails.",
			Step:            "unit-tests",
			SuggestedAction: "Fix failing tests.",
		},
		{
			Type:            taskstate.FindingTypeBlocking,
			Title:           "Race condition",
			Description:     "The update path can race.",
			Step:            "ai-review",
			SuggestedAction: "Guard the shared state.",
		},
		{
			Type:            taskstate.FindingTypeBlocking,
			Title:           "Known limitation",
			Description:     "This is accepted for the MVP.",
			Step:            "ai-review",
			SuggestedAction: "Document the limitation.",
			Waiver:          "Accepted risk for now.",
		},
		{
			Type:        taskstate.FindingTypeSeparateTask,
			Title:       "Extract helper",
			Description: "A helper would reduce duplication.",
			Step:        "ai-review",
			TaskProposal: taskstate.ReviewTaskProposal{
				Title:              "Extract helper",
				Description:        "Extract the repeated helper.",
				AcceptanceCriteria: "Helper has focused tests.",
			},
		},
	}
}

func TestTaskRunWorktreeModeDoesNotCareAboutDirtyRepoRoot(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(os.WriteFile(filepath.Join(repoPath, "dirty.txt"), []byte("dirty"), 0o644))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[{"id":"op-worktree","title":"Dirty root does not block worktree","status":"open","priority":1,"issue_type":"task"}]`},
	})
	withFakeAgent(t, "worktree-agent", 0)
	writeTaskRunAgentConfig(t, paths, "worktree", "worktree-agent", nil)

	stdout, stderr := executeCommand(t, []string{"task", "run", "op-worktree"})

	is.Contains(stdout, "fake agent stdout")
	is.Contains(stderr, "fake agent stderr")
	worktreePath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-worktree"))
	must.NoError(err)
	_, statErr := os.Stat(worktreePath)
	is.NoError(statErr)
}

func TestTaskRunAllowsOwnedInProgressTaskWithMatchingMetadata(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	worktreePath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-owned"))
	must.NoError(err)

	bdLogPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[
			{
				"id":"op-owned",
				"title":"Retry owned task",
				"status":"in_progress",
				"priority":1,
				"issue_type":"task",
				"metadata":{"orpheus.branch":"orpheus/op-owned","orpheus.worktree":"` + worktreePath + `"}
			}
		]`},
	})
	withFakeAgent(t, "owned-agent", 0)
	writeTaskRunAgentConfig(t, paths, "owned", "owned-agent", nil)

	stdout, stderr := executeCommand(t, []string{"task", "run", "op-owned"})

	is.Contains(stdout, "fake agent stdout")
	is.Contains(stderr, "fake agent stderr")
	bdLog, err := os.ReadFile(bdLogPath)
	must.NoError(err)
	is.Contains(string(bdLog), "--json --readonly --sandbox show --id op-owned")
	is.NotContains(string(bdLog), "--json --sandbox update")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-owned.yaml"), &state))
	must.Len(state.Runs, 1)
	is.Equal(taskstate.RunStatusSucceeded, state.Runs[0].Status)
	is.Equal("orpheus/op-owned", state.Target.Branch)
	is.Equal(worktreePath, state.Target.Worktree)
}

//nolint:funlen // Failure workflow needs the fake command script and assertions in one scenario.
func TestTaskRunDoesNotLaunchOrRecordAttemptWhenMarkInProgressFails(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	binDir := t.TempDir()
	bdLogPath := filepath.Join(binDir, "bd.log")
	bdCountPath := filepath.Join(binDir, "bd-show-count")
	bdPath := filepath.Join(binDir, "bd")
	script := fmt.Sprintf(`#!/bin/sh
{
  pwd
  printf '%%s\n' "$*"
} >> "$FAKE_BD_LOG"
if [ "$PWD" != %s ]; then
  echo "unexpected PWD: $PWD" >&2
  exit 65
fi
case "$*" in
  "--json --readonly --sandbox show --id op-race")
    count=0
    if [ -f %s ]; then
      count=$(cat %s)
    fi
    count=$((count + 1))
    printf '%%s' "$count" > %s
    if [ "$count" -eq 1 ]; then
      cat <<'JSON'
[{"id":"op-race","title":"Race task","status":"open","priority":1,"issue_type":"task"}]
JSON
      exit 0
    fi
    cat <<'JSON'
[{"id":"op-race","title":"Race task","status":"in_progress","priority":1,"issue_type":"task"}]
JSON
    exit 0
    ;;
  "--json --sandbox update "*)
    echo "update should not be reached" >&2
    exit 67
    ;;
  *)
    echo "unexpected args: $*" >&2
    exit 64
    ;;
esac
`, shellQuote(repoPath), shellQuote(bdCountPath), shellQuote(bdCountPath), shellQuote(bdCountPath))
	must.NoError(os.WriteFile(bdPath, []byte(script), 0o755))
	t.Setenv("FAKE_BD_LOG", bdLogPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	agentLogPath := withFakeAgent(t, "race-agent", 0)
	writeTaskRunAgentConfig(t, paths, "race", "race-agent", nil)

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "run", "op-race"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	must.ErrorContains(err, "mark task in progress")
	must.ErrorContains(err, "task mutation conflict")
	is.ErrorContains(err, "orpheus.branch is missing")
	_, agentLogErr := os.Stat(agentLogPath)
	is.ErrorIs(agentLogErr, os.ErrNotExist)

	statePath, err := paths.DataPath(filepath.Join("repos", "alpha", "tasks", "op-race.yaml"))
	must.NoError(err)
	_, stateErr := os.Stat(statePath)
	is.ErrorIs(stateErr, os.ErrNotExist)
	bdLog, err := os.ReadFile(bdLogPath)
	must.NoError(err)
	is.Equal(2, strings.Count(string(bdLog), "--json --readonly --sandbox show --id op-race"))
	is.NotContains(string(bdLog), "--json --sandbox update")
}

func TestTaskRunFailsFastWhenGlobalMutationLockHeldBeforeSetup(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[{"id":"op-locked","title":"Locked setup","status":"open","priority":1,"issue_type":"task"}]`},
	})
	lockPath, err := paths.GlobalMutationLockPath()
	must.NoError(err)
	must.NoError(os.MkdirAll(filepath.Dir(lockPath), 0o755))
	must.NoError(os.WriteFile(lockPath, []byte("held by test"), 0o644))
	worktreePath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-locked"))
	must.NoError(err)

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "run", "op-locked"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	must.ErrorContains(err, "failed to acquire lock for task run setup: "+lockPath)
	_, statErr := os.Stat(worktreePath)
	is.ErrorIs(statErr, os.ErrNotExist)
}

func TestTaskRunReleasesGlobalMutationLockWhileAgentRunsAndReacquiresForFinish(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[{"id":"op-finalize","title":"Finalize lock","status":"open","priority":1,"issue_type":"task"}]`},
	})
	lockPath, err := paths.GlobalMutationLockPath()
	must.NoError(err)
	binDir := t.TempDir()
	agentLogPath := filepath.Join(binDir, "lock-agent.log")
	agentPath := filepath.Join(binDir, "lock-agent")
	script := fmt.Sprintf(`#!/bin/sh
if [ -e %s ]; then
  echo "mutation lock held while agent ran" >&2
  exit 66
fi
printf 'lock absent during agent\n' >> %s
: > %s
printf 'agent stdout\n'
`, shellQuote(lockPath), shellQuote(agentLogPath), shellQuote(lockPath))
	must.NoError(os.WriteFile(agentPath, []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeTaskRunAgentConfig(t, paths, "lock-check", "lock-agent", nil)

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "run", "op-finalize"})

	must.Error(err)
	is.Contains(stdout, "agent stdout")
	is.Empty(stderr)
	is.ErrorContains(err, "record run finish")
	is.ErrorContains(err, "failed to acquire lock for task run finalization: "+lockPath)
	agentLog, err := os.ReadFile(agentLogPath)
	must.NoError(err)
	is.Contains(string(agentLog), "lock absent during agent")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-finalize.yaml"), &state))
	must.Len(state.Runs, 1)
	is.Equal(taskstate.RunStatusRunning, state.Runs[0].Status)
	must.Len(state.Events, 2)
	is.Equal(taskstate.EventWorktreeCreated, state.Events[0].Type)
	is.Equal(taskstate.EventRunStarted, state.Events[1].Type)
}

func TestTaskRunAgentFlagSelectsNamedProfile(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[{"id":"op-2","title":"Use selected agent","status":"open","priority":1,"issue_type":"task"}]`},
	})
	agentLogPath := withFakeAgent(t, "selected-agent", 0)
	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{"implementer": "default"},
			"profiles": map[string]any{
				"default": map[string]any{"command": "missing-agent"},
				"custom":  map[string]any{"command": "selected-agent", "args": []string{"selected", "{{prompt}}"}},
			},
		},
	}))

	stdout, stderr := executeCommand(t, []string{"task", "run", "--agent", "custom", "op-2"})

	is.Contains(stdout, "fake agent stdout")
	is.Contains(stderr, "fake agent stderr")
	is.NotContains(stderr, "running attached agent")
	agentLog, err := os.ReadFile(agentLogPath)
	must.NoError(err)
	log := string(agentLog)
	is.Contains(log, "ARG_1<<END\nselected\nEND")
	is.Contains(log, "ARG_2<<END\nYou are an agent dispatched by Orpheus.")
	is.Contains(log, "Run `orpheus agent context` now")
	is.NotContains(log, "- ID: op-2")
	is.NotContains(log, "- Title: Use selected agent")
}

func TestTaskRunRecordsFailedAttemptWhenAgentExitsNonZero(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[{"id":"op-3","title":"Failing agent","status":"open","priority":1,"issue_type":"task"}]`},
	})
	withFakeAgent(t, "failing-agent", 7)
	writeTaskRunAgentConfig(t, paths, "failing", "failing-agent", nil)

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "run", "op-3"})

	must.Error(err)
	is.Contains(stdout, "fake agent stdout")
	is.Contains(stderr, "fake agent stderr")
	is.ErrorContains(err, "run agent \"failing\"")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-3.yaml"), &state))
	must.Len(state.Runs, 1)
	is.Equal(taskstate.RunStatusFailed, state.Runs[0].Status)
	must.NotNil(state.Runs[0].Execution.FinishedAt)
	must.Len(state.Events, 3)
	is.Equal(taskstate.EventWorktreeCreated, state.Events[0].Type)
	is.Equal(taskstate.EventRunStarted, state.Events[1].Type)
	is.Equal(taskstate.EventRunFinished, state.Events[2].Type)
	is.Equal(taskstate.RunStatusFailed, state.Events[2].Status)
}

func TestTaskRunRecordsStartFailureWhenAgentProcessDoesNotStart(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[{"id":"op-4","title":"Missing executable","status":"open","priority":1,"issue_type":"task"}]`},
	})
	writeTaskRunAgentConfig(t, paths, "missing", "definitely-missing-orpheus-agent", nil)

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "run", "op-4"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "start process")
	is.ErrorContains(err, "definitely-missing-orpheus-agent")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-4.yaml"), &state))
	must.Len(state.Runs, 1)
	is.Equal(taskstate.RunStatusFailed, state.Runs[0].Status)
	must.NotNil(state.Runs[0].Execution.FinishedAt)
	must.Len(state.Events, 3)
	is.Equal(taskstate.EventWorktreeCreated, state.Events[0].Type)
	is.Equal(taskstate.EventRunStarted, state.Events[1].Type)
	is.Equal(taskstate.EventRunStartFailed, state.Events[2].Type)
	is.Equal(taskstate.RunStatusFailed, state.Events[2].Status)
	is.Contains(state.Events[2].Error, "definitely-missing-orpheus-agent")
}

func TestTaskRunRefusesLatestRunningAttempt(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[{"id":"op-5","title":"Already running","status":"open","priority":1,"issue_type":"task"}]`},
	})
	_, err := taskstate.NewStore(paths).StartRun("alpha", "op-5", taskstate.StartRunOptions{Agent: "recorder"})
	must.NoError(err)
	worktreePath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-5"))
	must.NoError(err)

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "run", "op-5"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "latest run attempt 1 is still running")
	is.ErrorContains(err, "M3 cannot reconcile stale attached runs automatically")
	_, statErr := os.Stat(worktreePath)
	is.ErrorIs(statErr, os.ErrNotExist)
}

func TestTaskRunReportsUnknownAgentProfileBeforeLaunching(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[{"id":"op-3","title":"Missing agent","status":"open","priority":1,"issue_type":"task"}]`},
	})
	agentLogPath := withFakeAgent(t, "known-agent", 0)
	writeTaskRunAgentConfig(t, paths, "known", "known-agent", nil)

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "run", "--agent", "missing", "op-3"})

	must.Error(err)
	must.ErrorContains(err, "resolve agent profile")
	must.ErrorContains(err, "agent profile \"missing\" is not configured")
	is.Empty(stdout)
	is.Empty(stderr)
	_, logErr := os.Stat(agentLogPath)
	is.ErrorIs(logErr, os.ErrNotExist)
}

func TestTaskDoneCommitsPushesClosesAndRecordsFinalization(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Implement task done", "Commit reviewed repo-root changes.")
	recordPassedReview(t, paths, "alpha", "op-main")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	bdLogPath := withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	stdout, stderr := executeCommand(t, []string{"task", "done", "op-main"})

	is.Empty(stderr)
	is.Contains(stdout, "Finalized op-main")
	commit := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))
	is.Contains(stdout, commit)
	message := strings.TrimSpace(runGit(t, repoPath, "log", "-1", "--format=%B"))
	is.Equal("Implement task done\n\nCommit reviewed repo-root changes.", message)
	is.NotContains(message, "op-main")
	is.NotContains(message, "Orpheus")
	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	originHead := strings.TrimSpace(runGit(t, originPath, "rev-parse", "refs/heads/main"))
	is.Equal(commit, originHead)

	bdLog, err := os.ReadFile(bdLogPath)
	must.NoError(err)
	is.Contains(string(bdLog), "--json --readonly --sandbox show --id op-main")
	is.Contains(string(bdLog), "--json --sandbox close op-main")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	facts := taskstate.FinalizationFacts(state)
	is.Equal(commit, facts.Commit)
	must.NotNil(facts.CommittedAt)
	must.NotNil(facts.PushedAt)
	must.NotNil(facts.ClosedAt)
}

func TestTaskDoneRequiresPassedReview(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Implement task done", "Commit reviewed repo-root changes.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	headBefore := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "done", "op-main"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "has no local review attempt")
	is.ErrorContains(err, "orpheus task review op-main")
	is.Equal(headBefore, strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD")))
	is.Contains(runGit(t, repoPath, "status", "--short"), "reviewed.txt")
}

func TestTaskReviewApproveFinalizesAndRecordsPassedAttempt(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review approval", "Finalize after approval.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "op-main"}, "x\na\n")

	is.Contains(stderr, "Task: op-main - Ready for task done")
	is.Contains(stderr, "Latest completion: Review approval")
	is.Contains(stderr, "Completion description: Finalize after approval.")
	is.NotContains(stderr, "Original completion:")
	is.NotContains(stderr, "Latest fix completion:")
	is.Contains(stderr, "git status --short:")
	is.Contains(stderr, "reviewed.txt")
	is.NotContains(stderr, "git diff --stat:")
	is.Contains(stderr, "== Review step: local-review (manual) ==")
	is.Contains(stderr, "Review action [a=approve, b=block, v=advisory, t=task, q=abort]")
	is.NotContains(stderr, "p=promote advisory")
	is.Contains(stderr, "Choose approve, block, advisory, task, or abort.")
	is.Contains(stdout, "Finalized op-main")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	is.Empty(latest.Findings)
	is.NotEmpty(taskstate.FinalizationFacts(state).Commit)
}

func TestTaskReviewManualContextShowsOriginalAndLatestFollowUpCompletion(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Original implementation", "Implemented the main task.")
	recordReviewFollowUpCompletion(t, paths, "alpha", "op-main", repoPath, 1, "First fix", "Addressed the first review blocker.")
	recordReviewFollowUpCompletion(t, paths, "alpha", "op-main", repoPath, 2, "Latest fix", "Addressed the most recent review blocker.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "op-main"}, "a\n")

	is.Contains(stderr, "Original completion: Original implementation")
	is.Contains(stderr, "Original completion description: Implemented the main task.")
	is.Contains(stderr, "Latest fix completion: Latest fix")
	is.Contains(stderr, "Latest fix completion description: Addressed the most recent review blocker.")
	is.NotContains(stderr, "Latest completion: Latest fix")
	is.NotContains(stderr, "Latest fix completion: First fix")
	is.Contains(stdout, "Finalized op-main")

	message := strings.TrimSpace(runGit(t, repoPath, "log", "-1", "--format=%B"))
	is.Equal("Original implementation\n\nImplemented the main task.", message)
}

func TestTaskReviewRejectsStaleMetadataMirror(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review approval", "Finalize after approval.")

	staleWorktree := filepath.Join(root, "stale-worktree")
	taskJSON := mainReadyTaskJSON("op-main", staleWorktree)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, stderr, err := executeCommandWithInputAndError(t, []string{"task", "review", "op-main"}, []byte("a\n"))

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "task review op-main: task op-main metadata target is invalid")
	is.ErrorContains(err, taskmodel.MetadataWorktree+"="+strconv.Quote(staleWorktree))

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	_, ok := taskstate.LatestReview(state)
	is.False(ok)
}

func TestTaskReviewRejectsStagedCandidateChanges(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review staged", "Refuse staged changes.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	runGit(t, repoPath, "add", "reviewed.txt")

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, stderr, err := executeCommandWithInputAndError(t, []string{"task", "review", "op-main"}, []byte("a\n"))

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "review requires a clean Git index")
	is.Contains(runGit(t, repoPath, "status", "--short"), "A  reviewed.txt")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	_, ok := taskstate.LatestReview(state)
	is.False(ok)
}

func TestTaskReviewRejectsMissingCandidateChangesWithoutFinalizationCommit(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review empty", "There are no changes.")

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, stderr, err := executeCommandWithInputAndError(t, []string{"task", "review", "op-main"}, []byte("a\n"))

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "has no candidate changes to review")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	_, ok := taskstate.LatestReview(state)
	is.False(ok)
}

//nolint:funlen // The setup and restoration assertions are clearer in one workflow test.
func TestTaskReviewRestoresCandidateChangesMutatedDuringManualStep(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(os.WriteFile(filepath.Join(repoPath, "tracked.txt"), []byte("base\n"), 0o644))
	runGit(t, repoPath, "add", "tracked.txt")
	runGit(t, repoPath, "commit", "-m", "add tracked file")
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review restore", "Restore mutated candidates.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "tracked.txt"), []byte("candidate\n"), 0o644))
	must.NoError(os.WriteFile(filepath.Join(repoPath, "untracked.txt"), []byte("candidate untracked\n"), 0o644))

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})
	input := &mutatingReviewInput{
		input:    bytes.NewBufferString("b\nMutating finding\nThe step changed files\nRestore it\n"),
		repoPath: repoPath,
		mutate: func(repoPath string) error {
			if err := os.WriteFile(filepath.Join(repoPath, "tracked.txt"), []byte("mutated\n"), 0o644); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(repoPath, "untracked.txt")); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(repoPath, "created-by-review.txt"), []byte("new\n"), 0o644)
		},
	}

	stdout, stderr, err := executeCommandWithReaderAndError(t, []string{"task", "review", "op-main"}, input)

	must.Error(err)
	is.Empty(stdout)
	is.Contains(stderr, "Review action")
	is.ErrorContains(err, "review step mutated candidate changes")
	is.ErrorContains(err, "restored the pre-step snapshot")
	is.Equal("candidate\n", readFileString(t, filepath.Join(repoPath, "tracked.txt")))
	is.Equal("candidate untracked\n", readFileString(t, filepath.Join(repoPath, "untracked.txt")))
	_, statErr := os.Stat(filepath.Join(repoPath, "created-by-review.txt"))
	is.ErrorIs(statErr, os.ErrNotExist)
	status := runGit(t, repoPath, "status", "--short")
	is.Contains(status, " M tracked.txt")
	is.Contains(status, "?? untracked.txt")
	is.NotContains(status, "created-by-review.txt")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusFailed, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

type mutatingReviewInput struct {
	input    *bytes.Buffer
	repoPath string
	mutate   func(string) error
	done     bool
	err      error
}

func (r *mutatingReviewInput) Read(p []byte) (int, error) {
	if !r.done {
		r.done = true
		r.err = r.mutate(r.repoPath)
	}
	if r.err != nil {
		return 0, r.err
	}
	return r.input.Read(p)
}

func TestTaskReviewBlockingFindingBlocksWithoutFinalizing(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review blocking", "Do not finalize.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	headBefore := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "op-main"}, "b\nBug\nMust fix\nPatch it\n")

	is.Empty(stdout)
	is.Contains(stderr, "Review blocked for op-main.")
	is.Equal(headBefore, strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD")))
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

func TestTaskReviewAdvisoryAndSeparateTaskFindingsDoNotBlockApproval(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review advisory", "Finalize with notes.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	input := strings.Join([]string{
		"v",
		"Nit",
		"Small cleanup remains",
		"Consider later",
		"t",
		"Follow-up",
		"Extract helper later",
		"Track separately",
		"Create helper extraction task",
		"Extract the helper in a focused follow-up.",
		"Helper extraction has acceptance tests.",
		"a",
		"n",
		"",
	}, "\n")
	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "op-main"}, input)

	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "Finding title:")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Findings, 2)
	is.Equal(taskstate.FindingTypeAdvisory, latest.Findings[0].Type)
	is.Equal(taskstate.FindingTypeSeparateTask, latest.Findings[1].Type)
	is.Empty(latest.Findings[1].CreatedTaskID)
	is.Nil(latest.Findings[1].CreatedTaskAt)
}

func TestTaskReviewCreatesSelectedSeparateTaskFollowUp(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review follow-up", "Create follow-up task.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	bdLogPath := withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{
			dir:    repoPath,
			args:   "--json --sandbox create Extract helper --description Extract the helper later.\n\nProvenance:\nDiscovered during review of op-main in repository alpha (review attempt 1, finding 1). Review step: local-review. --acceptance Helper extraction has tests. --type task",
			stdout: `{"id":"op-41","title":"Extract helper","status":"open","issue_type":"task"}`,
		},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	input := strings.Join([]string{
		"t",
		"Follow-up",
		"Extract helper later",
		"",
		"Extract helper",
		"Extract the helper later.",
		"Helper extraction has tests.",
		"a",
		"1",
		"",
	}, "\n")
	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "op-main"}, input)

	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "Created follow-up Bead op-41 for review finding 1.")
	is.Contains(readFileString(t, bdLogPath), "--json --sandbox create Extract helper")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal("op-41", latest.Findings[0].CreatedTaskID)
	is.NotNil(latest.Findings[0].CreatedTaskAt)
}

func TestTaskReviewCanAbortWhenSeparateTaskCreationFails(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review follow-up", "Creation can fail.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	headBefore := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{
			dir:      repoPath,
			args:     "--json --sandbox create Extract helper --description Extract the helper later.\n\nProvenance:\nDiscovered during review of op-main in repository alpha (review attempt 1, finding 1). Review step: local-review. --acceptance Helper extraction has tests. --type task",
			stderr:   "database locked",
			exitCode: 1,
		},
	})

	input := strings.Join([]string{
		"t",
		"Follow-up",
		"Extract helper later",
		"",
		"Extract helper",
		"Extract the helper later.",
		"Helper extraction has tests.",
		"a",
		"1",
		"n",
	}, "\n")
	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "op-main"}, input)

	is.Empty(stdout)
	is.Contains(stderr, "Failed to create follow-up Bead for review finding 1")
	is.Equal(headBefore, strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD")))
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusAborted, latest.Status)
	must.Len(latest.Findings, 1)
	is.Empty(latest.Findings[0].CreatedTaskID)
	is.Nil(latest.Findings[0].CreatedTaskAt)
}

func TestTaskReviewAbortDoesNotFinalize(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review abort", "Do not finalize.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	headBefore := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "op-main"}, "q\n")

	is.Empty(stdout)
	is.Contains(stderr, "Review aborted for op-main.")
	is.Equal(headBefore, strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD")))
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusAborted, latest.Status)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

func TestTaskReviewPassingCheckContinuesToManualStep(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review checks", "Run checks before approval.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))

	check := writeReviewScript(t, `#!/bin/sh
printf 'check stdout %s %s\n' "$ORPHEUS_REVIEW_ATTEMPT" "$ORPHEUS_REVIEW_STEP"
printf 'check stderr %s\n' "$ORPHEUS_AGENT_PURPOSE" >&2
exit 0
`)
	writeReviewPipelineConfig(t, paths, "standard", map[string][]map[string]any{
		"standard": []map[string]any{
			{"kind": "check", "name": "unit", "command": check, "args": []string{"--direct"}},
			{"kind": "manual", "name": "approval"},
		},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "--pipeline", "standard", "op-main"}, "a\n")

	is.Contains(stdout, "check stdout 1 unit")
	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "== Review step: unit (check) ==")
	is.Contains(stderr, "== Review step: approval (manual) ==")
	is.Contains(stderr, "check stderr review")
	is.Contains(stderr, "Review action")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	is.Equal("standard", latest.Pipeline)
	must.Len(latest.Steps, 2)
	is.Equal("unit", latest.Steps[0].Name)
	must.NotNil(latest.Steps[0].ExitCode)
	is.Equal(0, *latest.Steps[0].ExitCode)
	is.Nil(latest.Steps[0].Execution)
	is.Equal("approval", latest.Steps[1].Name)
	is.Empty(latest.Findings)
}

func TestTaskReviewConfirmedManualCommandRunsAndRecordsStep(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review manual command", "Confirm before command.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))

	manual := writeReviewScript(t, `#!/bin/sh
printf 'manual command ran %s\n' "$ORPHEUS_REVIEW_STEP"
`)
	writeReviewPipelineConfig(t, paths, "standard", map[string][]map[string]any{
		"standard": []map[string]any{
			{"kind": "manual", "name": "inspect", "command": manual, "args": []string{"--hint"}},
		},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "--pipeline", "standard", "op-main"}, "\na\n")

	is.Contains(stdout, "manual command ran inspect")
	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "== Review step: inspect (manual) ==")
	is.Contains(stderr, "Task: op-main - Ready for task done")
	is.Contains(stderr, "Completion description: Confirm before command.")
	is.Contains(stderr, "git status --short:")
	is.NotContains(stderr, "git diff --stat:")
	is.Contains(stderr, "Run manual command for step \"inspect\"")
	is.Contains(stderr, "[Y/n]")
	is.Contains(stderr, "Review action")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Steps, 1)
	is.Equal("manual", latest.Steps[0].Kind)
	is.Equal("inspect", latest.Steps[0].Name)
	is.Nil(latest.Steps[0].Execution)
	must.NotNil(latest.Steps[0].ExitCode)
	is.Equal(0, *latest.Steps[0].ExitCode)
}

func TestTaskReviewImportsHunkBlockingNoteAndBlocksApproval(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review Hunk blocker", "Import blocking notes.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	installFakeHunkNotes(t, `{"comments":[{"noteId":"user:1","source":"user","filePath":"README.md","newRange":[12,12],"body":"This must be fixed before publication.","author":"user","createdAt":"2026-07-09T00:00:00.000Z","editable":true}]}`)

	manual := writeReviewScript(t, "#!/bin/sh\nprintf 'hunk command ran\\n'\n")
	writeReviewPipelineConfig(t, paths, "standard", map[string][]map[string]any{
		"standard": []map[string]any{
			{"kind": "manual", "name": "inspect", "command": manual, "hunk_notes": true},
		},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "--pipeline", "standard", "op-main"}, "\nb\nf\n")

	is.Contains(stdout, "hunk command ran")
	is.Contains(stderr, "Captured 1 Hunk note(s)")
	is.Contains(stderr, "Imported Hunk note user:1 as blocking finding.")
	is.Contains(stderr, "Review action [f=finish/block, b=block, v=advisory, t=task, q=abort]")
	is.Contains(stderr, "Review blocked for op-main.")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	must.Len(latest.Findings, 1)
	finding := latest.Findings[0]
	is.Equal(taskstate.FindingTypeBlocking, finding.Type)
	is.Equal("inspect", finding.Step)
	is.Contains(finding.Description, "Note ID: user:1")
	is.Contains(finding.Description, "File: README.md")
	is.Contains(finding.Description, "Location: new line 12")
	is.Contains(finding.Description, "Note body:\nThis must be fixed before publication.")
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

func TestTaskReviewImportsHunkAdvisoryNoteAndAllowsApproval(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review Hunk advisory", "Import advisory notes.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	installFakeHunkNotes(t, `{"comments":[{"noteId":"user:2","source":"user","filePath":"docs.md","oldRange":[4,5],"body":"Consider tightening this wording later.","editable":true}]}`)

	manual := writeReviewScript(t, "#!/bin/sh\n")
	writeReviewPipelineConfig(t, paths, "standard", map[string][]map[string]any{
		"standard": []map[string]any{
			{"kind": "manual", "name": "inspect", "command": manual, "hunk_notes": true},
		},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "--pipeline", "standard", "op-main"}, "\nv\na\n")

	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "Imported Hunk note user:2 as advisory finding.")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeAdvisory, latest.Findings[0].Type)
	is.Contains(latest.Findings[0].Description, "Location: old lines 4-5")
}

//nolint:funlen // The workflow spans Hunk import, approval, and Beads follow-up creation.
func TestTaskReviewImportsHunkSeparateTaskNoteAndCreatesFollowUp(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review Hunk follow-up", "Import separate-task notes.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	installFakeHunkNotes(t, `{"comments":[{"noteId":"user:3","source":"user","filePath":"internal/app.go","newRange":[30,31],"body":"This helper extraction can be separate.","editable":true}]}`)

	manual := writeReviewScript(t, "#!/bin/sh\n")
	writeReviewPipelineConfig(t, paths, "standard", map[string][]map[string]any{
		"standard": []map[string]any{
			{"kind": "manual", "name": "inspect", "command": manual, "hunk_notes": true},
		},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{
			dir:    repoPath,
			args:   "--json --sandbox create Extract helper --description Extract the helper later.\n\nProvenance:\nDiscovered during review of op-main in repository alpha (review attempt 1, finding 1). Review step: inspect. --acceptance Helper extraction has tests. --type task",
			stdout: `{"id":"op-41","title":"Extract helper","status":"open","issue_type":"task"}`,
		},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	input := strings.Join([]string{
		"",
		"t",
		"Extract helper",
		"Extract the helper later.",
		"Helper extraction has tests.",
		"a",
		"a",
		"",
	}, "\n")
	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "--pipeline", "standard", "op-main"}, input)

	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "Imported Hunk note user:3 as separate-task finding.")
	is.Contains(stderr, "Created follow-up Bead op-41 for review finding 1.")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeSeparateTask, latest.Findings[0].Type)
	is.Equal("op-41", latest.Findings[0].CreatedTaskID)
	is.NotNil(latest.Findings[0].CreatedTaskAt)
}

func TestTaskReviewHunkManualCommandWithNoCapturedNotesContinuesPrompt(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review Hunk empty", "No notes.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	installFakeHunkNotes(t, `{"comments":[]}`)

	manual := writeReviewScript(t, "#!/bin/sh\n")
	writeReviewPipelineConfig(t, paths, "standard", map[string][]map[string]any{
		"standard": []map[string]any{
			{"kind": "manual", "name": "inspect", "command": manual, "hunk_notes": true},
		},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "--pipeline", "standard", "op-main"}, "\na\n")

	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "Review action")
	is.NotContains(stderr, "Captured 1 Hunk note")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	is.Empty(latest.Findings)
}

func TestTaskReviewDeclinedManualCommandAbortsWithoutRunningCommand(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review decline", "Decline command.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	headBefore := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))

	markerPath := filepath.Join(repoPath, "manual-ran.txt")
	manual := writeReviewScript(t, fmt.Sprintf(`#!/bin/sh
printf 'unexpected manual command\n'
printf 'ran\n' > %s
`, shellQuote(markerPath)))
	writeReviewPipelineConfig(t, paths, "standard", map[string][]map[string]any{
		"standard": []map[string]any{
			{"kind": "manual", "name": "inspect", "command": manual},
		},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "--pipeline", "standard", "op-main"}, "n\n")

	is.Empty(stdout)
	is.Contains(stderr, "== Review step: inspect (manual) ==")
	is.Contains(stderr, "Task: op-main - Ready for task done")
	is.Contains(stderr, "Completion description: Decline command.")
	is.Contains(stderr, "Run manual command for step \"inspect\"")
	is.Contains(stderr, "Review aborted for op-main.")
	is.NotContains(stderr, "Review action")
	is.Equal(headBefore, strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD")))
	_, statErr := os.Stat(markerPath)
	is.ErrorIs(statErr, os.ErrNotExist)

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusAborted, latest.Status)
	is.Empty(latest.Steps)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

//nolint:funlen // The EOF confirmation fixture is clearer inline with its assertions.
func TestTaskReviewManualCommandEOFConfirmationAbortsWithoutRunningCommand(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{
			name:  "empty EOF",
			input: nil,
		},
		{
			name:  "decline without newline",
			input: []byte("n"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			root := newTestState(t)
			paths := currentTestPaths(t)
			store := registry.NewStore(paths)

			repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
			configureTestGitUser(t, repoPath)
			must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
				ID:            "alpha",
				Name:          "Alpha Repo",
				Path:          repoPath,
				DefaultBranch: "main",
				BeadsMode:     registry.BeadsModeLocal,
				BeadsPrefix:   "op",
			}}}))
			recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review EOF", "Abort on EOF.")
			must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))

			markerPath := filepath.Join(repoPath, "manual-ran.txt")
			manual := writeReviewScript(t, fmt.Sprintf(`#!/bin/sh
printf 'unexpected manual command\n'
printf 'ran\n' > %s
`, shellQuote(markerPath)))
			writeReviewPipelineConfig(t, paths, "standard", map[string][]map[string]any{
				"standard": []map[string]any{
					{"kind": "manual", "name": "inspect", "command": manual},
				},
			})

			taskJSON := mainReadyTaskJSON("op-main", repoPath)
			withFakeBDCommandResponses(t, []fakeBDCommandResponse{
				{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
			})

			stdout, stderr, err := executeCommandWithInputAndError(
				t,
				[]string{"task", "review", "--pipeline", "standard", "op-main"},
				test.input,
			)

			must.NoError(err)
			is.Empty(stdout)
			is.Contains(stderr, "Run manual command for step \"inspect\"")
			is.Contains(stderr, "Review aborted for op-main.")
			is.NotContains(stderr, "Review action")
			_, statErr := os.Stat(markerPath)
			is.ErrorIs(statErr, os.ErrNotExist)

			var state taskstate.TaskState
			must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
			latest, ok := taskstate.LatestReview(state)
			must.True(ok)
			is.Equal(taskstate.ReviewStatusAborted, latest.Status)
			is.Empty(latest.Steps)
		})
	}
}

func TestTaskReviewNonZeroCheckRecordsBlockingFindingAndStops(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:             "alpha",
		Name:           "Alpha Repo",
		Path:           repoPath,
		DefaultBranch:  "main",
		BeadsMode:      registry.BeadsModeLocal,
		BeadsPrefix:    "op",
		ReviewPipeline: "standard",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review failed check", "Block on check failure.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	headBefore := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))

	check := writeReviewScript(t, `#!/bin/sh
printf 'failing check output\n'
exit 7
`)
	writeReviewPipelineConfig(t, paths, "standard", map[string][]map[string]any{
		"standard": []map[string]any{
			{"kind": "check", "name": "unit", "command": check},
			{"kind": "manual", "name": "approval"},
		},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "op-main"}, "")

	is.Contains(stdout, "failing check output")
	is.Contains(stderr, "== Review step: unit (check) ==")
	is.Contains(stderr, "Review blocked for op-main by check \"unit\".")
	is.NotContains(stderr, "Review action")
	is.Equal(headBefore, strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD")))

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	must.Len(latest.Steps, 1)
	must.NotNil(latest.Steps[0].ExitCode)
	is.Equal(7, *latest.Steps[0].ExitCode)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Equal("unit", latest.Findings[0].Step)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

//nolint:funlen // The autonomous loop spans dispatch, review, follow-up, and publication.
func TestTaskRunAutonomousReviewFollowUpRepairsCheckAndPublishes(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	must.NoError(err)
	orpheusBin := buildOrpheusTestBinary(t, sourceRoot)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	agentPath := writeAutonomousReviewFixAgent(t, "auto-fix-agent", orpheusBin, true)
	check := writeReviewScript(t, `#!/bin/sh
if [ "$(cat status.txt 2>/dev/null)" = "pass" ]; then
  exit 0
fi
printf 'status is not pass\n'
exit 7
`)
	writeAutonomousReviewLoopConfig(t, paths, "auto-fix", agentPath, 4, []map[string]any{
		{"kind": "check", "name": "status", "command": check},
	})

	taskJSON := mainReadyTaskJSON("op-auto", repoPath)
	bdLogPath := withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-auto", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox list --all --limit 0", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-auto", stdout: "{}"},
	})

	stdout, stderr := executeCommand(t, []string{"task", "run", "--main", "op-auto"})

	is.Contains(stdout, "Finalized op-auto")
	is.Contains(stderr, "Review blocked for op-auto by check \"status\".")
	is.Contains(stderr, "Autonomous review follow-up for op-auto targets review attempt 1 finding(s) 1.")
	is.NotContains(stderr, "Autonomous review attempt budget exhausted")
	is.Equal("pass\n", readFileString(t, filepath.Join(repoPath, "status.txt")))
	is.NotEmpty(strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD")))

	bdLog, err := os.ReadFile(bdLogPath)
	must.NoError(err)
	is.Contains(string(bdLog), "--json --sandbox close op-auto")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-auto.yaml"), &state))
	must.Len(state.Runs, 2)
	must.Len(state.Reviews, 2)
	must.NotNil(state.Runs[1].ReviewFollowUp)
	is.Equal([]int{0}, state.Runs[1].ReviewFollowUp.FindingIndexes)
	is.Equal(2, state.Reviews[0].Findings[0].TargetedByRunAttempt)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	is.False(latest.AutonomousBudgetExhausted)
	is.NotEmpty(taskstate.FinalizationFacts(state).Commit)
}

//nolint:funlen // The exhaustion workflow needs two complete review/fix attempts.
func TestTaskRunAutonomousReviewLoopExhaustsPersistentCheckBlockers(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	must.NoError(err)
	orpheusBin := buildOrpheusTestBinary(t, sourceRoot)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	agentPath := writeAutonomousReviewFixAgent(t, "persistent-agent", orpheusBin, false)
	check := writeReviewScript(t, `#!/bin/sh
printf 'persistent failure\n'
exit 7
`)
	writeAutonomousReviewLoopConfig(t, paths, "persistent", agentPath, 2, []map[string]any{
		{"kind": "check", "name": "unit", "command": check},
	})

	taskJSON := mainReadyTaskJSON("op-stubborn", repoPath)
	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: taskJSON},
	})

	stdout, stderr := executeCommand(t, []string{"task", "run", "--main", "op-stubborn"})

	is.Contains(stdout, "Recorded completion for op-stubborn")
	is.NotContains(stdout, "Finalized op-stubborn")
	is.Equal(2, strings.Count(stderr, "Review blocked for op-stubborn by check \"unit\"."))
	is.Contains(stderr, "Autonomous review follow-up for op-stubborn targets review attempt 1 finding(s) 1.")
	is.Contains(stderr, "Autonomous review attempt budget exhausted for op-stubborn after 2 review attempt(s).")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-stubborn.yaml"), &state))
	must.Len(state.Runs, 2)
	must.Len(state.Reviews, 2)
	is.Equal(2, state.Reviews[0].Findings[0].TargetedByRunAttempt)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	is.True(latest.AutonomousBudgetExhausted)
	must.Len(latest.Findings, 1)
	is.Zero(latest.Findings[0].TargetedByRunAttempt)
	is.Empty(taskstate.FinalizationFacts(state).Commit)

	showStdout, showStderr := executeCommand(t, []string{"task", "review", "show", "op-stubborn"})
	is.Empty(showStderr)
	is.Contains(showStdout, "Autonomous review: attempt budget exhausted")
	is.Contains(showStdout, "Next step: autonomous review attempts are exhausted")

	taskStdout, taskStderr := executeCommand(t, []string{"task", "show", "op-stubborn"})
	is.Empty(taskStderr)
	is.Contains(taskStdout, "Review attempt 2 blocked (autonomous review budget exhausted)")
}

//nolint:funlen // The resumed review regression spans two commands and a nested follow-up dispatch.
func TestTaskReviewResumedAutonomousFollowUpPreservesSelectedImplementer(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	must.NoError(err)
	orpheusBin := buildOrpheusTestBinary(t, sourceRoot)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	selectedAgentPath := writeAutonomousReviewFixAgent(t, "selected-agent", orpheusBin, true)
	otherAgentPath := writeAutonomousReviewFixAgent(t, "other-agent", orpheusBin, false)
	check := writeReviewScript(t, `#!/bin/sh
if [ "$(cat status.txt 2>/dev/null)" = "pass" ]; then
  exit 0
fi
printf 'status is not pass\n'
exit 7
`)
	steps := []map[string]any{
		{"kind": "manual", "name": "approval"},
		{"kind": "check", "name": "status", "command": check},
	}
	writeAutonomousReviewLoopConfigWithImplementers(t, paths, "selected", selectedAgentPath, "other", otherAgentPath, "selected", 4, steps)

	taskJSON := mainReadyTaskJSON("op-resume", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-resume", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox list --all --limit 0", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-resume", stdout: "{}"},
	})

	runStdout, runStderr := executeCommand(t, []string{"task", "run", "--main", "--agent", "selected", "op-resume"})
	is.Contains(runStdout, "Recorded completion for op-resume")
	is.Contains(runStderr, "Review for op-resume is waiting for manual step \"approval\"")
	is.Equal("fail\n", readFileString(t, filepath.Join(repoPath, "status.txt")))

	writeAutonomousReviewLoopConfigWithImplementers(t, paths, "selected", selectedAgentPath, "other", otherAgentPath, "other", 4, steps)

	reviewStdout, reviewStderr := executeCommandWithInput(t, []string{"task", "review", "op-resume"}, "a\na\n")
	is.Contains(reviewStdout, "Finalized op-resume")
	is.Contains(reviewStderr, "Resuming review attempt 1 at manual step \"approval\".")
	is.Contains(reviewStderr, "Review blocked for op-resume by check \"status\".")
	is.Contains(reviewStderr, "Autonomous review follow-up for op-resume targets review attempt 1 finding(s) 1.")
	is.NotContains(reviewStderr, "Autonomous review attempt budget exhausted")
	is.Equal("pass\n", readFileString(t, filepath.Join(repoPath, "status.txt")))

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-resume.yaml"), &state))
	must.Len(state.Runs, 2)
	is.Equal("selected", state.Runs[0].Execution.Agent)
	is.Equal("selected", state.Runs[1].Execution.Agent)
	must.NotNil(state.Runs[1].ReviewFollowUp)
	is.Equal([]int{0}, state.Runs[1].ReviewFollowUp.FindingIndexes)
	must.Len(state.Reviews, 2)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	is.NotEmpty(taskstate.FinalizationFacts(state).Commit)
}

func TestTaskReviewCheckBlockerDowngradeContinuesPipeline(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review check downgrade", "Downgrade blocker.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))

	check := writeReviewScript(t, "#!/bin/sh\nexit 7\n")
	writeReviewPipelineConfig(t, paths, "standard", map[string][]map[string]any{
		"standard": []map[string]any{
			{"kind": "check", "name": "unit", "command": check},
			{"kind": "manual", "name": "approval"},
		},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	stdout, stderr := executeCommandWithInput(
		t,
		[]string{"task", "review", "--pipeline", "standard", "op-main"},
		"d\nFalse positive for this task.\na\n",
	)

	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "Automated blocking findings from step \"unit\"")
	is.Contains(stderr, "Decision for finding 1")
	is.Contains(stderr, "== Review step: approval (manual) ==")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeAdvisory, latest.Findings[0].Type)
	is.Equal("False positive for this task.", latest.Findings[0].DowngradeReason)
	is.NotEmpty(taskstate.FinalizationFacts(state).Commit)
}

func TestTaskReviewCheckBlockerWaiverContinuesPipeline(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review check waiver", "Waive blocker.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))

	check := writeReviewScript(t, "#!/bin/sh\nexit 7\n")
	writeReviewPipelineConfig(t, paths, "standard", map[string][]map[string]any{
		"standard": []map[string]any{
			{"kind": "check", "name": "unit", "command": check},
			{"kind": "manual", "name": "approval"},
		},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	stdout, stderr := executeCommandWithInput(
		t,
		[]string{"task", "review", "--pipeline", "standard", "op-main"},
		"c\nKnown flaky check.\na\n",
	)

	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "Automated blocking findings from step \"unit\"")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Equal("Known flaky check.", latest.Findings[0].Waiver)
	is.NotEmpty(taskstate.FinalizationFacts(state).Commit)
}

func TestTaskReviewCheckStartFailureMarksOperationalFailure(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review missing check", "Fail operationally.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	writeReviewPipelineConfig(t, paths, "standard", map[string][]map[string]any{
		"standard": []map[string]any{
			{"kind": "check", "name": "missing", "command": "definitely-missing-orpheus-check"},
		},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, stderr, err := executeCommandWithInputAndError(
		t,
		[]string{"task", "review", "--pipeline", "standard", "op-main"},
		nil,
	)

	must.Error(err)
	is.Empty(stdout)
	is.Contains(stderr, "== Review step: missing (check) ==")
	is.ErrorContains(err, "start check step \"missing\"")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusFailed, latest.Status)
	must.Len(latest.Steps, 1)
	is.Nil(latest.Steps[0].ExitCode)
	is.Empty(latest.Findings)
}

//nolint:funlen // The review-agent CLI fixture is clearer as one end-to-end scenario.
func TestTaskReviewAgentReviewStepLaunchesReviewerAndPassesWithoutFindings(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review agent", "Run attached reviewer.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	agentLogPath := withFakeAgent(t, "review-agent", 0)
	writeReviewAgentPipelineConfig(t, paths, "reviewer", "review-agent", []string{"{{session_name}} - {{prompt}}"}, "standard", map[string][]map[string]any{
		"standard": []map[string]any{{"kind": "agent_review", "name": "ai-review"}},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "op-main"}, "")

	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "fake agent stderr")

	agentLog, err := os.ReadFile(agentLogPath)
	must.NoError(err)
	log := string(agentLog)
	is.Contains(log, "PWD="+repoPath)
	is.Contains(log, "ORPHEUS_AGENT_PURPOSE=review")
	is.Contains(log, "ORPHEUS_REVIEW_ATTEMPT=1")
	is.Contains(log, "ORPHEUS_REVIEW_STEP=ai-review")
	is.Contains(log, "ORPHEUS_AGENT_PROMPT<<END")
	is.Contains(log, "You are an agent dispatched by Orpheus.")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Steps, 1)
	is.Equal("agent_review", latest.Steps[0].Kind)
	is.Equal("ai-review", latest.Steps[0].Name)
	must.NotNil(latest.Steps[0].Execution)
	is.Equal(taskstate.RunStatusSucceeded, latest.Steps[0].Execution.Status)
	is.Equal("review-agent", latest.Steps[0].Execution.Command)
	must.NotNil(latest.Steps[0].Execution.FinishedAt)
	is.Positive(latest.Steps[0].Execution.DurationMillis)
	is.Equal(taskstate.UsageCaptureUnknown, latest.Steps[0].Execution.UsageCapture.Status)
	is.Equal("usage capture is not supported for harness -", latest.Steps[0].Execution.UsageCapture.Reason)
	must.Len(latest.Steps[0].Execution.Args, 1)
	is.Contains(latest.Steps[0].Execution.Args[0], "Reviewing op-main Ready for task done - ")
	is.Contains(latest.Steps[0].Execution.Args[0], "You are an agent dispatched by Orpheus.")
	is.Empty(latest.Findings)
	is.NotEmpty(taskstate.FinalizationFacts(state).Commit)
}

//nolint:funlen // The Codex review-agent usage fixture is clearer as one end-to-end scenario.
func TestTaskReviewAgentReviewStepCapturesCodexUsage(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review agent", "Run attached reviewer.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))

	withFakeAgent(t, "codex", 0)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	sessionDir := filepath.Join(codexHome, "sessions", "2026", "07", "07")
	must.NoError(os.MkdirAll(sessionDir, 0o755))
	sessionPath := filepath.Join(sessionDir, "review-session.jsonl")
	writeCodexSessionLogForCLI(t, sessionPath, repoPath, "review-session", time.Now().UTC())
	writeStructuredCodexReviewAgentPipelineConfig(t, paths, "codex", "gpt-5", false, "standard", map[string][]map[string]any{
		"standard": []map[string]any{{"kind": "agent_review", "name": "ai-review"}},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "op-main"}, "")

	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "fake agent stderr")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	must.Len(latest.Steps, 1)
	must.NotNil(latest.Steps[0].Execution)
	execution := latest.Steps[0].Execution
	is.Equal(taskstate.RunStatusSucceeded, execution.Status)
	is.Equal("codex", execution.Harness)
	is.Equal("gpt-5", execution.Model)
	must.NotNil(execution.Session)
	is.Equal("review-session", execution.Session.ID)
	is.Equal(sessionPath, execution.Session.LogPath)
	must.NotNil(execution.Usage)
	is.Equal(190, execution.Usage.TotalTokens)
	is.Equal(taskstate.UsageCaptureCaptured, execution.UsageCapture.Status)
	is.Equal("matched_codex_session", execution.UsageCapture.Reason)
	is.Equal(1, execution.UsageCapture.CandidateCount)
}

func TestTaskReviewAgentReviewBlockingFindingStopsPipeline(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	must.NoError(err)
	orpheusBin := buildOrpheusTestBinary(t, sourceRoot)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review agent blocker", "Record a blocker.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	reviewer := writeReviewScript(t, fmt.Sprintf(`#!/bin/sh
%s agent review add \
  --type blocking \
  --title "Generated blocker" \
  --description "The review agent found a blocker." \
  --suggested-action "Fix the blocker."
`, shellQuote(orpheusBin)))
	writeReviewAgentPipelineConfig(t, paths, "reviewer", reviewer, nil, "standard", map[string][]map[string]any{
		"standard": {
			{"kind": "agent_review", "name": "ai-review"},
			{"kind": "manual", "name": "approval"},
		},
	})
	headBefore := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "op-main"}, "")

	is.Contains(stdout, "Recorded blocking review finding 1 for op-main.")
	is.Contains(stderr, "== Review step: ai-review (agent_review) ==")
	is.Contains(stderr, "Review blocked for op-main by agent_review \"ai-review\".")
	is.NotContains(stderr, "Review action")
	is.Equal(headBefore, strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD")))

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Equal("ai-review", latest.Findings[0].Step)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

//nolint:funlen // The mixed automated-blocker workflow spans review, show, and follow-up targeting.
func TestTaskReviewAgentReviewMixedAutomatedBlockerDecisions(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	must.NoError(err)
	orpheusBin := buildOrpheusTestBinary(t, sourceRoot)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review agent blockers", "Record blockers.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	reviewer := writeReviewScript(t, fmt.Sprintf(`#!/bin/sh
%s agent review add --type blocking --title "Keep blocker" --description "Still blocks." --suggested-action "Fix kept."
%s agent review add --type blocking --title "Downgrade blocker" --description "Can be advisory." --suggested-action "Document it."
%s agent review add --type blocking --title "Cancel blocker" --description "False positive." --suggested-action "Ignore it."
`, shellQuote(orpheusBin), shellQuote(orpheusBin), shellQuote(orpheusBin)))
	require.NoError(t, paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"implementer": "implementer",
				"reviewer":    "reviewer",
			},
			"profiles": map[string]any{
				"implementer": map[string]any{"command": "unused-implementer"},
				"reviewer": map[string]any{
					"command":     reviewer,
					"interactive": false,
				},
			},
		},
		"reviews": map[string]any{
			"default_pipeline": "standard",
			"pipelines": map[string]any{
				"standard": map[string]any{
					"steps": []map[string]any{
						{"kind": "agent_review", "name": "ai-review"},
						{"kind": "manual", "name": "approval"},
					},
				},
			},
		},
	}))

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, stderr := executeCommandWithInput(
		t,
		[]string{"task", "review", "--pipeline", "standard", "op-main"},
		"k\nd\nNot required for this task.\nw\nFalse positive from reviewer.\n",
	)

	is.Contains(stdout, "Recorded blocking review finding 1 for op-main.")
	is.Contains(stdout, "Recorded blocking review finding 2 for op-main.")
	is.Contains(stdout, "Recorded blocking review finding 3 for op-main.")
	is.Contains(stderr, "Automated blocking findings from step \"ai-review\"")
	is.Contains(stderr, "Review blocked for op-main by agent_review \"ai-review\".")
	is.NotContains(stderr, "== Review step: approval (manual) ==")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	must.Len(latest.Findings, 3)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Empty(latest.Findings[0].Waiver)
	is.Equal(taskstate.FindingTypeAdvisory, latest.Findings[1].Type)
	is.Equal("Not required for this task.", latest.Findings[1].DowngradeReason)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[2].Type)
	is.Equal("False positive from reviewer.", latest.Findings[2].Waiver)

	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})
	showStdout, showStderr := executeCommand(t, []string{"task", "review", "show", "op-main"})
	is.Empty(showStderr)
	is.Contains(showStdout, "Title: Keep blocker")
	is.Contains(showStdout, "Resolution: open")
	is.Contains(showStdout, "Title: Downgrade blocker")
	is.Contains(showStdout, "Resolution: downgraded to advisory: Not required for this task.")
	is.Contains(showStdout, "Title: Cancel blocker")
	is.Contains(showStdout, "Resolution: waived: False positive from reviewer.")

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: taskJSON},
	})
	withFakeAgent(t, "followup-agent", 0)
	writeTaskRunAgentConfig(t, paths, "followup", "followup-agent", nil)

	runStdout, runStderr := executeCommand(t, []string{"task", "run", "op-main"})
	is.Contains(runStdout, "fake agent stdout")
	is.Contains(runStderr, "fake agent stderr")

	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	must.Len(state.Runs, 2)
	must.NotNil(state.Runs[1].ReviewFollowUp)
	is.Equal([]int{0}, state.Runs[1].ReviewFollowUp.FindingIndexes)
	latest, ok = taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(2, latest.Findings[0].TargetedByRunAttempt)
	is.Zero(latest.Findings[1].TargetedByRunAttempt)
	is.Zero(latest.Findings[2].TargetedByRunAttempt)
}

//nolint:funlen // The promotion workflow spans review, inspection, and follow-up dispatch.
func TestTaskReviewPromotesAgentReviewAdvisoryAndTargetsFollowUp(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	must.NoError(err)
	orpheusBin := buildOrpheusTestBinary(t, sourceRoot)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review agent advisory", "Promote advisory if needed.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	reviewer := writeReviewScript(t, fmt.Sprintf(`#!/bin/sh
%s agent review add \
  --type advisory \
  --title "Generated advisory" \
  --description "The review agent found a risky edge case." \
  --suggested-action "Handle the edge case before publishing."
`, shellQuote(orpheusBin)))
	writeReviewAgentPipelineConfig(t, paths, "reviewer", reviewer, nil, "standard", map[string][]map[string]any{
		"standard": []map[string]any{
			{"kind": "agent_review", "name": "ai-review"},
			{"kind": "manual", "name": "approval"},
		},
	})
	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: taskJSON},
	})
	headBefore := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))

	input := strings.Join([]string{
		"p",
		"1",
		"a",
		"v",
		"Manual advisory",
		"The human reviewer added a non-blocking note.",
		"Keep this in mind later.",
		"f",
		"",
	}, "\n")
	inputPath := filepath.Join(t.TempDir(), "review-input.txt")
	must.NoError(os.WriteFile(inputPath, []byte(input), 0o644))
	inputFile, err := os.Open(inputPath)
	must.NoError(err)
	t.Cleanup(func() { _ = inputFile.Close() })
	stdout, stderr, err := executeCommandWithReaderAndError(t, []string{"task", "review", "op-main"}, inputFile)
	must.NoError(err, "execute task review\nstderr: %s", stderr)

	is.Contains(stdout, "Recorded advisory review finding 1 for op-main.")
	is.Contains(stderr, "Prior unresolved advisories:")
	is.Contains(stderr, "Finding 1 (ai-review): Generated advisory")
	is.Contains(stderr, "Review action [a=approve, b=block, p=promote advisory")
	is.Contains(stderr, "Promoted advisory finding 1 to blocking.")
	is.Contains(stderr, "Review action [f=finish/block, b=block, v=advisory, t=task, q=abort]")
	is.Contains(stderr, "Choose finish/block, block, advisory, task, or abort.")
	is.NotContains(stderr, "Finding 2 (approval): Manual advisory")
	is.Contains(stderr, "Review blocked for op-main.")
	is.Equal(headBefore, strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD")))

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	must.Len(latest.Findings, 2)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Equal("Generated advisory", latest.Findings[0].Title)
	is.Equal("ai-review", latest.Findings[0].Step)
	is.Equal(taskstate.FindingTypeAdvisory, latest.Findings[1].Type)
	is.Equal("approval", latest.Findings[1].Step)
	is.Empty(taskstate.FinalizationFacts(state).Commit)

	showStdout, showStderr := executeCommand(t, []string{"task", "review", "show", "op-main"})
	is.Empty(showStderr)
	is.Contains(showStdout, "Status: blocked")
	is.Contains(showStdout, "Type: blocking")
	is.Contains(showStdout, "Title: Generated advisory")
	is.Contains(showStdout, "Resolution: open")
	is.Contains(showStdout, "Next step: run `orpheus task run op-main` to address open blocking findings")

	withFakeAgent(t, "followup-agent", 0)
	writeTaskRunAgentConfig(t, paths, "followup", "followup-agent", nil)
	runStdout, runStderr := executeCommand(t, []string{"task", "run", "op-main"})

	is.Contains(runStdout, "fake agent stdout")
	is.Contains(runStderr, "fake agent stderr")
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok = taskstate.LatestReview(state)
	must.True(ok)
	must.Len(latest.Findings, 2)
	is.Equal(2, latest.Findings[0].TargetedByRunAttempt)
	is.Zero(latest.Findings[1].TargetedByRunAttempt)
	must.Len(state.Runs, 2)
	must.NotNil(state.Runs[1].ReviewFollowUp)
	is.Equal(latest.Attempt, state.Runs[1].ReviewFollowUp.ReviewAttempt)
	is.Equal([]int{0}, state.Runs[1].ReviewFollowUp.FindingIndexes)
}

func TestTaskReviewAgentReviewNonZeroExitMarksOperationalFailure(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review agent failure", "Fail operationally.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	withFakeAgent(t, "failing-review-agent", 7)
	writeReviewAgentPipelineConfig(t, paths, "reviewer", "failing-review-agent", nil, "standard", map[string][]map[string]any{
		"standard": []map[string]any{{"kind": "agent_review", "name": "ai-review"}},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, stderr, err := executeCommandWithInputAndError(
		t,
		[]string{"task", "review", "op-main"},
		nil,
	)

	must.Error(err)
	is.Contains(stdout, "fake agent stdout")
	is.Contains(stderr, "fake agent stderr")
	is.ErrorContains(err, "run agent_review step \"ai-review\"")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusFailed, latest.Status)
	must.Len(latest.Steps, 1)
	is.Equal("agent_review", latest.Steps[0].Kind)
	is.Empty(latest.Findings)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

func TestTaskReviewPipelineOverridePrecedence(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:             "alpha",
		Name:           "Alpha Repo",
		Path:           repoPath,
		DefaultBranch:  "main",
		BeadsMode:      registry.BeadsModeLocal,
		BeadsPrefix:    "op",
		ReviewPipeline: "repo",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review override", "Use CLI-selected pipeline.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))

	fail := writeReviewScript(t, "#!/bin/sh\nprintf 'wrong pipeline\n'\nexit 9\n")
	pass := writeReviewScript(t, "#!/bin/sh\nprintf 'cli pipeline\n'\nexit 0\n")
	writeReviewPipelineConfig(t, paths, "global", map[string][]map[string]any{
		"global": []map[string]any{{"kind": "check", "name": "global", "command": fail}},
		"repo":   []map[string]any{{"kind": "check", "name": "repo", "command": fail}},
		"cli":    []map[string]any{{"kind": "check", "name": "cli", "command": pass}},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "--pipeline", "cli", "op-main"}, "")

	is.Contains(stdout, "cli pipeline")
	is.NotContains(stdout, "wrong pipeline")
	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "== Review step: cli (check) ==")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal("cli", latest.Pipeline)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
}

func TestTaskReviewPipelineAliasResolvesToGlobalPipeline(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:                    "alpha",
		Name:                  "Alpha Repo",
		Path:                  repoPath,
		DefaultBranch:         "main",
		BeadsMode:             registry.BeadsModeLocal,
		BeadsPrefix:           "op",
		ReviewPipeline:        "repo",
		ReviewPipelineAliases: map[string]string{"quick": "cli"},
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review alias", "Use alias-selected pipeline.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))

	fail := writeReviewScript(t, "#!/bin/sh\nprintf 'wrong pipeline\n'\nexit 9\n")
	pass := writeReviewScript(t, "#!/bin/sh\nprintf 'alias pipeline\n'\nexit 0\n")
	writeReviewPipelineConfig(t, paths, "global", map[string][]map[string]any{
		"global": {{"kind": "check", "name": "global", "command": fail}},
		"repo":   {{"kind": "check", "name": "repo", "command": fail}},
		"cli":    {{"kind": "check", "name": "cli", "command": pass}},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "--pipeline", "quick", "op-main"}, "")

	is.Contains(stdout, "alias pipeline")
	is.NotContains(stdout, "wrong pipeline")
	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "== Review step: cli (check) ==")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal("cli", latest.Pipeline)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
}

func TestTaskReviewResumesManualWaitingAttempt(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:             "alpha",
		Name:           "Alpha Repo",
		Path:           repoPath,
		DefaultBranch:  "main",
		BeadsMode:      registry.BeadsModeLocal,
		BeadsPrefix:    "op",
		ReviewPipeline: "standard",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review resume", "Resume manual gate.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))

	fail := writeReviewScript(t, "#!/bin/sh\nprintf 'check reran\n'\nexit 9\n")
	writeReviewPipelineConfig(t, paths, "standard", map[string][]map[string]any{
		"standard": {
			{"kind": "check", "name": "lint", "command": fail},
			{"kind": "manual", "name": "inspect"},
		},
	})
	runStore := taskstate.NewStore(paths)
	reviewAttempt, err := runStore.StartReviewWithOptions("alpha", "op-main", taskstate.StartReviewOptions{
		Pipeline: "standard",
		Step:     "lint",
	})
	must.NoError(err)
	_, err = runStore.RecordReviewStep("alpha", "op-main", reviewAttempt.Attempt, taskstate.RecordReviewStepOptions{
		Kind: "check",
		Name: "lint",
	})
	must.NoError(err)
	_, err = runStore.PauseReviewForManual("alpha", "op-main", reviewAttempt.Attempt, "inspect")
	must.NoError(err)

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "review", "op-main"}, "a\n")

	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "Resuming review attempt 1 at manual step \"inspect\".")
	is.Contains(stderr, "== Review step: inspect (manual) ==")
	is.NotContains(stdout, "check reran")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(reviewAttempt.Attempt, latest.Attempt)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
}

func TestTaskReviewRejectsConflictingPipelineForManualWaitingAttempt(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review conflict", "Reject replacement.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	writeReviewPipelineConfig(t, paths, "standard", map[string][]map[string]any{
		"standard": {{"kind": "manual", "name": "inspect"}},
		"other":    {{"kind": "manual", "name": "other-inspect"}},
	})
	runStore := taskstate.NewStore(paths)
	reviewAttempt, err := runStore.StartReviewWithOptions("alpha", "op-main", taskstate.StartReviewOptions{
		Pipeline: "standard",
		Step:     "inspect",
	})
	must.NoError(err)
	_, err = runStore.PauseReviewForManual("alpha", "op-main", reviewAttempt.Attempt, "inspect")
	must.NoError(err)

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, _, err := executeCommandWithError(t, []string{"task", "review", "--pipeline", "other", "op-main"})

	must.Error(err)
	is.Empty(stdout)
	is.ErrorContains(err, "cannot replace a paused review")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusWaitingForManual, latest.Status)
	is.Equal("standard", latest.Pipeline)
	is.Equal("inspect", latest.Step)
}

func TestTaskReviewUnknownPipelineIncludesRepoAliases(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:                    "alpha",
		Name:                  "Alpha Repo",
		Path:                  repoPath,
		DefaultBranch:         "main",
		BeadsMode:             registry.BeadsModeLocal,
		BeadsPrefix:           "op",
		ReviewPipelineAliases: map[string]string{"quick": "standard"},
	}}}))
	writeReviewPipelineConfig(t, paths, "standard", map[string][]map[string]any{
		"standard": {{"kind": "manual", "name": "standard-review"}},
	})
	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, _, err := executeCommandWithError(t, []string{"task", "review", "--pipeline", "unknown", "op-main"})

	must.Error(err)
	is.Empty(stdout)
	is.ErrorContains(err, `CLI --pipeline "unknown" does not match a configured review pipeline`)
	is.ErrorContains(err, "configured pipelines: standard")
	is.ErrorContains(err, "configured repo aliases: quick=standard")
}

func TestTaskDoneRefusesRunningCompletionWithoutInteractiveConfirmation(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordRunningMainCompletion(t, paths, "alpha", "op-main", repoPath, "Implement task done", "Commit reviewed repo-root changes.")
	recordPassedReview(t, paths, "alpha", "op-main")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	headBefore := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	bdLogPath := withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "done", "op-main"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "explicit interactive confirmation is required")
	is.Equal(headBefore, strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD")))
	is.Contains(runGit(t, repoPath, "status", "--short"), "reviewed.txt")

	bdLog, readErr := os.ReadFile(bdLogPath)
	must.NoError(readErr)
	is.Contains(string(bdLog), "--json --readonly --sandbox show --id op-main")
	is.NotContains(string(bdLog), "--json --sandbox close op-main")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestRun(state)
	must.True(ok)
	is.Equal(taskstate.RunStatusRunning, latest.Status)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

//nolint:funlen // PR publication scenario is clearer as one linear workflow.
func TestTaskDonePublishesPRReadyTaskBranch(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	targets := taskSyncExpectedTargets(t, paths, repoPath, "op-sync")
	taskWorktree := targets.WorktreeTeam.Worktree
	runGit(t, repoPath, "branch", targets.WorktreeTeam.Branch, "main")
	runGit(t, repoPath, "worktree", "add", taskWorktree, targets.WorktreeTeam.Branch)
	must.NoError(os.WriteFile(filepath.Join(taskWorktree, "sync.txt"), []byte("sync\n"), 0o644))
	recordWorktreeCompletion(t, paths, "alpha", "op-sync", targets.WorktreeTeam.Branch, taskWorktree, "")
	recordPassedReview(t, paths, "alpha", "op-sync")

	bdLogPath := withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{
			dir:    repoPath,
			args:   "--json --readonly --sandbox show --id op-sync",
			stdout: syncReadyTaskJSON("op-sync", targets.WorktreeTeam.Branch, taskWorktree),
		},
		{
			dir:  repoPath,
			args: "--json --sandbox update op-sync --set-metadata orpheus.pr_url=https://github.test/org/alpha/pull/42",
		},
	})
	ghLogPath := withFakeGHPRResponses(t, fakeGHPRResponses{
		listStdout:   "[]",
		createStdout: "https://github.test/org/alpha/pull/42\n",
	})

	stdout, stderr := executeCommand(t, []string{"task", "done", "op-sync"})

	is.Empty(stderr)
	is.Contains(stdout, "Published op-sync")
	is.Contains(stdout, "pushed orpheus/op-sync")
	is.Contains(stdout, "created PR https://github.test/org/alpha/pull/42")
	is.Contains(stdout, "Backend task remains open for PR review")
	bdLog, readErr := os.ReadFile(bdLogPath)
	must.NoError(readErr)
	is.Contains(string(bdLog), "--json --sandbox update op-sync --set-metadata orpheus.pr_url=https://github.test/org/alpha/pull/42")
	ghLog, readErr := os.ReadFile(ghLogPath)
	must.NoError(readErr)
	is.Contains(string(ghLog), "ARG_1<<END\npr\nEND")
	is.Contains(string(ghLog), "ARG_2<<END\nlist\nEND")
	is.Contains(string(ghLog), "ARG_2<<END\ncreate\nEND")
	is.NotContains(string(ghLog), "ARG_2<<END\nview\nEND")
	is.Contains(string(ghLog), "ARG_8<<END\nReady for PR\nEND")
	is.Contains(string(ghLog), "Detailed PR body.")
	is.NotContains(string(ghLog), "Created by Orpheus.")
	is.NotContains(string(ghLog), "Ready for sync")
	is.NotContains(string(ghLog), "op-sync:")
	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	originCommit := strings.TrimSpace(runGit(t, originPath, "rev-parse", "refs/heads/orpheus/op-sync"))
	commit := strings.TrimSpace(runGit(t, taskWorktree, "rev-parse", "HEAD"))
	is.Equal(commit, originCommit)
}

func TestTaskDoneRecoversExistingBranchPR(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	targets := taskSyncExpectedTargets(t, paths, repoPath, "op-sync")
	taskWorktree := targets.WorktreeTeam.Worktree
	runGit(t, repoPath, "branch", targets.WorktreeTeam.Branch, "main")
	runGit(t, repoPath, "worktree", "add", taskWorktree, targets.WorktreeTeam.Branch)
	must.NoError(os.WriteFile(filepath.Join(taskWorktree, "sync.txt"), []byte("sync\n"), 0o644))
	recordWorktreeCompletion(t, paths, "alpha", "op-sync", targets.WorktreeTeam.Branch, taskWorktree, "")
	recordPassedReview(t, paths, "alpha", "op-sync")

	bdLogPath := withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{
			dir:    repoPath,
			args:   "--json --readonly --sandbox show --id op-sync",
			stdout: syncReadyTaskJSON("op-sync", targets.WorktreeTeam.Branch, taskWorktree),
		},
		{
			dir:  repoPath,
			args: "--json --sandbox update op-sync --set-metadata orpheus.pr_url=https://github.test/org/alpha/pull/7",
		},
	})
	ghLogPath := withFakeGHPRResponses(t, fakeGHPRResponses{
		listStdout:   `[{"url":"https://github.test/org/alpha/pull/7"}]`,
		createStdout: "unexpected create\n",
		createExit:   66,
	})

	stdout, stderr := executeCommand(t, []string{"task", "done", "op-sync"})

	is.Empty(stderr)
	is.Contains(stdout, "recovered existing PR https://github.test/org/alpha/pull/7")
	is.Contains(stdout, "Backend task remains open for PR review")
	bdLog, readErr := os.ReadFile(bdLogPath)
	must.NoError(readErr)
	is.Contains(string(bdLog), "--json --sandbox update op-sync --set-metadata orpheus.pr_url=https://github.test/org/alpha/pull/7")
	ghLog, readErr := os.ReadFile(ghLogPath)
	must.NoError(readErr)
	is.Contains(string(ghLog), "ARG_2<<END\nlist\nEND")
	is.NotContains(string(ghLog), "ARG_2<<END\ncreate\nEND")
	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	originCommit := strings.TrimSpace(runGit(t, originPath, "rev-parse", "refs/heads/orpheus/op-sync"))
	commit := strings.TrimSpace(runGit(t, taskWorktree, "rev-parse", "HEAD"))
	is.Equal(commit, originCommit)
}

func TestTaskSyncPollsExistingPRURLWithoutPushOrMutation(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	bdLogPath := withFakeBDCommandResponses(t, []fakeBDCommandResponse{{
		dir:  repoPath,
		args: "--json --readonly --sandbox show --id op-sync",
		stdout: `[
			{
				"id":"op-sync",
				"title":"Already in review",
				"status":"in_progress",
				"priority":1,
				"issue_type":"task",
				"metadata":{
					"orpheus.branch":"orpheus/op-sync",
					"orpheus.worktree":"` + filepath.Join(root, "unused-worktree") + `",
					"orpheus.pr_url":"https://github.test/org/alpha/pull/42"
				}
			}
		]`,
	}})
	ghLogPath := withFakeGHPRResponses(t, fakeGHPRResponses{
		listStdout:   "unexpected list\n",
		listExit:     66,
		createStdout: "unexpected create\n",
		createExit:   66,
		statusStdout: `{"url":"https://github.test/org/alpha/pull/42","state":"OPEN","merged":false}`,
		statusExit:   0,
	})

	stdout, stderr := executeCommand(t, []string{"task", "sync", "op-sync"})

	is.Empty(stderr)
	is.Contains(stdout, "Synced op-sync")
	is.Contains(stdout, "PR https://github.test/org/alpha/pull/42 is still open for review")
	bdLog, readErr := os.ReadFile(bdLogPath)
	must.NoError(readErr)
	is.Contains(string(bdLog), "--json --readonly --sandbox show --id op-sync")
	is.NotContains(string(bdLog), "--json --sandbox update")
	ghLog, readErr := os.ReadFile(ghLogPath)
	must.NoError(readErr)
	is.Contains(string(ghLog), "ARG_2<<END\nview\nEND")
	is.NotContains(string(ghLog), "ARG_2<<END\nlist\nEND")
	is.NotContains(string(ghLog), "ARG_2<<END\ncreate\nEND")
}

//nolint:funlen // Sync scenario is clearer when provider, backend, and audit checks stay together.
func TestTaskSyncClosesBackendAndRecordsLocalAuditForMergedPR(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	taskJSON := `[
		{
			"id":"op-sync",
			"title":"Already merged",
			"status":"in_progress",
			"priority":1,
			"issue_type":"task",
			"metadata":{
				"orpheus.branch":"orpheus/op-sync",
				"orpheus.worktree":"` + filepath.Join(root, "unused-worktree") + `",
				"orpheus.pr_url":"https://github.test/org/alpha/pull/42"
			}
		}
	]`
	bdLogPath := withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-sync", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-sync", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-sync", stdout: "{}"},
	})
	ghLogPath := withFakeGHPRResponses(t, fakeGHPRResponses{
		listStdout:   "unexpected list\n",
		listExit:     66,
		createStdout: "unexpected create\n",
		createExit:   66,
		statusStdout: `{"url":"https://github.test/org/alpha/pull/42","state":"MERGED","merged":true}`,
		statusExit:   0,
	})

	stdout, stderr := executeCommand(t, []string{"task", "sync", "op-sync"})

	is.Empty(stderr)
	is.Contains(stdout, "Synced op-sync")
	is.Contains(stdout, "PR https://github.test/org/alpha/pull/42 is merged")
	is.Contains(stdout, "Backend task was closed")

	bdLog, readErr := os.ReadFile(bdLogPath)
	must.NoError(readErr)
	is.Equal(2, strings.Count(string(bdLog), "--json --readonly --sandbox show --id op-sync"))
	is.Contains(string(bdLog), "--json --sandbox close op-sync")
	is.NotContains(string(bdLog), "--json --sandbox update")

	ghLog, readErr := os.ReadFile(ghLogPath)
	must.NoError(readErr)
	is.Contains(string(ghLog), "ARG_2<<END\nview\nEND")
	is.NotContains(string(ghLog), "ARG_2<<END\nlist\nEND")
	is.NotContains(string(ghLog), "ARG_2<<END\ncreate\nEND")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-sync.yaml"), &state))
	must.Len(state.Events, 1)
	event := state.Events[0]
	is.Equal(taskstate.EventTaskClosed, event.Type)
	is.Equal(taskstate.CloseReasonPRMerged, event.CloseReason)
	is.Equal("https://github.test/org/alpha/pull/42", event.PRURL)
	is.Equal("merged", event.ObservedPRState)
}

//nolint:funlen // Table-driven sync error scenarios share setup that is best kept adjacent.
func TestTaskSyncExistingPRErrorsDoNotMutateBackendOrAudit(t *testing.T) {
	tests := []struct {
		name         string
		taskPRURL    string
		statusStdout string
		statusExit   int
		wantError    string
		wantGHView   bool
	}{
		{
			name:      "stored PR URL rejected by provider",
			taskPRURL: "not-a-url",
			statusStdout: `could not parse pull request URL
`,
			statusExit: 65,
			wantError:  "pull request URL \"not-a-url\" is invalid",
		},
		{
			name:         "provider omits PR URL",
			taskPRURL:    "https://github.test/org/alpha/pull/42",
			statusStdout: `{"state":"OPEN","mergedAt":null}`,
			wantError:    "valid PR URL",
			wantGHView:   true,
		},
		{
			name:         "provider returns malformed PR URL",
			taskPRURL:    "https://github.test/org/alpha/pull/42",
			statusStdout: `{"url":"not-a-url","state":"OPEN","mergedAt":null}`,
			wantError:    "valid PR URL",
			wantGHView:   true,
		},
		{
			name:      "provider cannot access repository",
			taskPRURL: "https://github.test/org/alpha/pull/42",
			statusStdout: `Could not resolve to a Repository with the name 'org/alpha'
`,
			statusExit: 1,
			wantError:  "could not be resolved by gh",
			wantGHView: true,
		},
		{
			name:      "provider authentication failure",
			taskPRURL: "https://github.test/org/alpha/pull/42",
			statusStdout: `gh auth login required
`,
			statusExit: 1,
			wantError:  "gh authentication failed or is missing",
			wantGHView: true,
		},
		{
			name:         "closed without merge",
			taskPRURL:    "https://github.test/org/alpha/pull/42",
			statusStdout: `{"url":"https://github.test/org/alpha/pull/42","state":"CLOSED","mergedAt":null}`,
			wantError:    "closed without merge",
			wantGHView:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			root := newTestState(t)
			paths := currentTestPaths(t)
			store := registry.NewStore(paths)

			repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
			must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
				ID:            "alpha",
				Name:          "Alpha Repo",
				Path:          repoPath,
				DefaultBranch: "main",
				BeadsMode:     registry.BeadsModeLocal,
				BeadsPrefix:   "op",
			}}}))

			taskJSON := `[
				{
					"id":"op-sync",
					"title":"Existing PR error",
					"status":"in_progress",
					"priority":1,
					"issue_type":"task",
					"metadata":{"orpheus.pr_url":"` + tt.taskPRURL + `"}
				}
			]`
			bdLogPath := withFakeBDCommandResponses(t, []fakeBDCommandResponse{{
				dir:    repoPath,
				args:   "--json --readonly --sandbox show --id op-sync",
				stdout: taskJSON,
			}})
			ghLogPath := withFakeGHPRResponses(t, fakeGHPRResponses{
				listStdout:   "unexpected list\n",
				listExit:     66,
				createStdout: "unexpected create\n",
				createExit:   66,
				statusStdout: tt.statusStdout,
				statusExit:   tt.statusExit,
			})

			stdout, stderr, err := executeCommandWithError(t, []string{"task", "sync", "op-sync"})

			must.Error(err)
			is.Empty(stdout)
			is.Empty(stderr)
			is.ErrorContains(err, tt.wantError)

			bdLog, readErr := os.ReadFile(bdLogPath)
			must.NoError(readErr)
			is.Contains(string(bdLog), "--json --readonly --sandbox show --id op-sync")
			is.NotContains(string(bdLog), "--json --sandbox close op-sync")
			is.NotContains(string(bdLog), "--json --sandbox update")

			ghLog, readErr := os.ReadFile(ghLogPath)
			switch {
			case tt.wantGHView:
				must.NoError(readErr)
				is.Contains(string(ghLog), "ARG_2<<END\nview\nEND")
				is.NotContains(string(ghLog), "ARG_2<<END\nlist\nEND")
				is.NotContains(string(ghLog), "ARG_2<<END\ncreate\nEND")
			case readErr == nil:
				is.Empty(string(ghLog))
			default:
				is.True(os.IsNotExist(readErr), "read gh log: %v", readErr)
			}

			_, statErr := os.Stat(filepath.Join(paths.DataRoot, "repos", "alpha", "tasks", "op-sync.yaml"))
			is.True(os.IsNotExist(statErr), "task-state audit file should not be created: %v", statErr)
		})
	}
}

func TestTaskSyncSkipsClosedTaskWithoutPRPolling(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	closedTaskJSON := `[
		{
			"id":"op-sync",
			"title":"Already closed",
			"status":"closed",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.pr_url":"https://github.test/org/alpha/pull/42"}
		}
	]`
	bdLogPath := withFakeBDCommandResponses(t, []fakeBDCommandResponse{{
		dir:    repoPath,
		args:   "--json --readonly --sandbox show --id op-sync",
		stdout: closedTaskJSON,
	}})
	ghLogPath := withFakeGHPRResponses(t, fakeGHPRResponses{
		listStdout:   "unexpected list\n",
		listExit:     66,
		createStdout: "unexpected create\n",
		createExit:   66,
		statusStdout: "unexpected view\n",
		statusExit:   66,
	})

	stdout, stderr := executeCommand(t, []string{"task", "sync", "op-sync"})

	is.Empty(stderr)
	is.Contains(stdout, "Skipped op-sync")
	is.Contains(stdout, "task is closed")
	is.Contains(stdout, "No backend changes were made")

	bdLog, readErr := os.ReadFile(bdLogPath)
	must.NoError(readErr)
	is.Contains(string(bdLog), "--json --readonly --sandbox show --id op-sync")
	is.NotContains(string(bdLog), "--json --sandbox update")
	is.NotContains(string(bdLog), "--json --sandbox close")

	ghLog, readErr := os.ReadFile(ghLogPath)
	if readErr == nil {
		is.Empty(string(ghLog))
	} else {
		is.True(os.IsNotExist(readErr), "read gh log: %v", readErr)
	}
}

func TestTaskSyncSkipsTaskWithoutPRURLAtRepoRoot(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	runGit(t, repoPath, "checkout", "-b", "orpheus/op-sync")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "sync.txt"), []byte("sync\n"), 0o644))
	runGit(t, repoPath, "add", "sync.txt")
	runGit(t, repoPath, "commit", "-m", "sync task branch")
	commit := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))
	recordWorktreeCompletion(t, paths, "alpha", "op-sync", "orpheus/op-sync", repoPath, commit)

	withFakeBDCommandResponses(t, []fakeBDCommandResponse{{
		dir:    repoPath,
		args:   "--json --readonly --sandbox show --id op-sync",
		stdout: syncReadyTaskJSON("op-sync", "orpheus/op-sync", repoPath),
	}})

	stdout, stderr := executeCommand(t, []string{"task", "sync", "op-sync"})

	is.Empty(stderr)
	is.Contains(stdout, "Skipped op-sync")
	is.Contains(stdout, "orpheus.pr_url is not set")
	is.Contains(stdout, "No backend changes were made")
}

func TestTaskSyncSkipsMainSoloLocalReadyTaskWithoutPRURL(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Local ready", "Needs human review.")

	withFakeBDCommandResponses(t, []fakeBDCommandResponse{{
		dir:    repoPath,
		args:   "--json --readonly --sandbox show --id op-main",
		stdout: mainReadyTaskJSON("op-main", repoPath),
	}})

	stdout, stderr := executeCommand(t, []string{"task", "sync", "op-main"})

	is.Empty(stderr)
	is.Contains(stdout, "Skipped op-main")
	is.Contains(stdout, "orpheus.pr_url is not set")
	is.Contains(stdout, "No backend changes were made")
}

func TestTaskDoneFeatureBranchPushFailureIsNonZero(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	targets := taskSyncExpectedTargets(t, paths, repoPath, "op-sync")
	taskWorktree := targets.WorktreeTeam.Worktree
	runGit(t, repoPath, "branch", targets.WorktreeTeam.Branch, "main")
	runGit(t, repoPath, "worktree", "add", taskWorktree, targets.WorktreeTeam.Branch)
	must.NoError(os.WriteFile(filepath.Join(taskWorktree, "sync.txt"), []byte("sync\n"), 0o644))
	recordWorktreeCompletion(t, paths, "alpha", "op-sync", targets.WorktreeTeam.Branch, taskWorktree, "")
	recordPassedReview(t, paths, "alpha", "op-sync")
	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	must.NoError(os.RemoveAll(originPath))

	withFakeBDCommandResponses(t, []fakeBDCommandResponse{{
		dir:    repoPath,
		args:   "--json --readonly --sandbox show --id op-sync",
		stdout: syncReadyTaskJSON("op-sync", targets.WorktreeTeam.Branch, taskWorktree),
	}})

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "done", "op-sync"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "push task branch")
	is.ErrorContains(err, "origin")
}

//nolint:funlen // Sync-all boundary scenario is clearer as a single workflow.
func TestTaskSyncAllPollsPRBoundaryTasks(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	targets := taskSyncExpectedTargets(t, paths, repoPath, "op-create")
	taskWorktree := targets.WorktreeTeam.Worktree
	runGit(t, repoPath, "branch", targets.WorktreeTeam.Branch, "main")
	runGit(t, repoPath, "worktree", "add", taskWorktree, targets.WorktreeTeam.Branch)
	must.NoError(os.WriteFile(filepath.Join(taskWorktree, "sync-all.txt"), []byte("sync all\n"), 0o644))
	runGit(t, taskWorktree, "add", "sync-all.txt")
	runGit(t, taskWorktree, "commit", "-m", "sync all task branch")
	commit := strings.TrimSpace(runGit(t, taskWorktree, "rev-parse", "HEAD"))
	recordWorktreeCompletion(t, paths, "alpha", "op-create", targets.WorktreeTeam.Branch, taskWorktree, commit)

	listJSON := `[
		{
			"id":"op-create",
			"title":"Ready for sync all",
			"status":"in_progress",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.branch":"` + targets.WorktreeTeam.Branch + `","orpheus.worktree":"` + taskWorktree + `"}
		},
		{
			"id":"op-open",
			"title":"Already in review",
			"status":"in_progress",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.pr_url":"https://github.test/org/alpha/pull/77"}
		},
		{
			"id":"op-epic",
			"title":"Planning item",
			"status":"in_progress",
			"priority":1,
			"issue_type":"epic",
			"metadata":{"orpheus.pr_url":"https://github.test/org/alpha/pull/88"}
		}
	]`
	openTaskJSON := `[
		{
			"id":"op-open",
			"title":"Already in review",
			"status":"in_progress",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.pr_url":"https://github.test/org/alpha/pull/77"}
		}
	]`
	bdLogPath := withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox list --all --limit 0", stdout: listJSON},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-open", stdout: openTaskJSON},
	})
	ghLogPath := withFakeGHPRResponses(t, fakeGHPRResponses{
		statusStdout: `{"url":"https://github.test/org/alpha/pull/77","state":"OPEN","merged":false}`,
	})

	stdout, stderr := executeCommand(t, []string{"task", "sync", "--all"})

	is.Empty(stderr)
	is.Contains(stdout, "Open/in-review PRs (1):")
	is.Contains(stdout, "op-open (alpha): PR https://github.test/org/alpha/pull/77 is still open for review")
	is.NotContains(stdout, "op-create")
	is.NotContains(stdout, "op-epic")

	bdLog, readErr := os.ReadFile(bdLogPath)
	must.NoError(readErr)
	is.Contains(string(bdLog), "--json --readonly --sandbox list --all --limit 0")
	is.Contains(string(bdLog), "--json --readonly --sandbox show --id op-open")
	is.NotContains(string(bdLog), "--id op-create")
	is.NotContains(string(bdLog), "--id op-epic")
	ghLog, readErr := os.ReadFile(ghLogPath)
	must.NoError(readErr)
	is.NotContains(string(ghLog), "ARG_2<<END\nlist\nEND")
	is.NotContains(string(ghLog), "ARG_2<<END\ncreate\nEND")
	is.Contains(string(ghLog), "ARG_2<<END\nview\nEND")
}

func TestTaskSyncAllReturnsNonZeroAfterCandidateError(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	taskJSON := `[
		{
			"id":"op-closed-pr",
			"title":"Closed without merge",
			"status":"in_progress",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.pr_url":"https://github.test/org/alpha/pull/90"}
		}
	]`
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox list --all --limit 0", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-closed-pr", stdout: taskJSON},
	})
	withFakeGHPRResponses(t, fakeGHPRResponses{
		statusStdout: `{"url":"https://github.test/org/alpha/pull/90","state":"CLOSED","merged":false}`,
	})

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "sync", "--all"})

	must.Error(err)
	is.Empty(stderr)
	is.Contains(stdout, "Errors (1):")
	is.Contains(stdout, "op-closed-pr (alpha): sync:")
	is.Contains(stdout, "closed without merge")
	is.ErrorContains(err, "task sync --all failed for 1 item")
}

//nolint:funlen // Cross-repo sync-all scenario is clearer as one integrated workflow.
func TestTaskSyncAllGroupsCrossRepoResultsAndReturnsNonZeroAfterFailures(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	alphaPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	betaPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "beta"))
	gammaPath := filepath.Join(root, "repos", "gamma")
	must.NoError(os.MkdirAll(gammaPath, 0o755))
	configureTestGitUser(t, alphaPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{
		{
			ID:            "alpha",
			Name:          "Alpha Repo",
			Path:          alphaPath,
			DefaultBranch: "main",
			BeadsMode:     registry.BeadsModeLocal,
			BeadsPrefix:   "a",
		},
		{
			ID:            "beta",
			Name:          "Beta Repo",
			Path:          betaPath,
			DefaultBranch: "main",
			BeadsMode:     registry.BeadsModeLocal,
			BeadsPrefix:   "b",
		},
		{
			ID:            "gamma",
			Name:          "Gamma Repo",
			Path:          gammaPath,
			DefaultBranch: "main",
			BeadsMode:     registry.BeadsModeLocal,
			BeadsPrefix:   "g",
		},
	}}))

	alphaTargets, err := workflow.ExpectedTargetsForTask(taskmodel.Repository{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          alphaPath,
		DefaultBranch: "main",
	}, "a-create", paths)
	must.NoError(err)
	alphaWorktree := alphaTargets.WorktreeTeam.Worktree
	runGit(t, alphaPath, "branch", alphaTargets.WorktreeTeam.Branch, "main")
	runGit(t, alphaPath, "worktree", "add", alphaWorktree, alphaTargets.WorktreeTeam.Branch)
	must.NoError(os.WriteFile(filepath.Join(alphaWorktree, "sync-all.txt"), []byte("sync all\n"), 0o644))
	runGit(t, alphaWorktree, "add", "sync-all.txt")
	runGit(t, alphaWorktree, "commit", "-m", "sync all task branch")
	commit := strings.TrimSpace(runGit(t, alphaWorktree, "rev-parse", "HEAD"))
	recordWorktreeCompletion(
		t,
		paths,
		"alpha",
		"a-create",
		alphaTargets.WorktreeTeam.Branch,
		alphaWorktree,
		commit,
	)

	alphaListJSON := `[
		{
			"id":"a-create",
			"title":"Create PR",
			"status":"in_progress",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.branch":"` + alphaTargets.WorktreeTeam.Branch + `","orpheus.worktree":"` + alphaWorktree + `"}
		},
		{
			"id":"a-open",
			"title":"Already open",
			"status":"in_progress",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.pr_url":"https://github.test/org/alpha/pull/77"}
		}
	]`
	openTaskJSON := `[
		{
			"id":"a-open",
			"title":"Already open",
			"status":"in_progress",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.pr_url":"https://github.test/org/alpha/pull/77"}
		}
	]`
	betaListJSON := `[
		{
			"id":"b-merged",
			"title":"Merged PR",
			"status":"in_progress",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.pr_url":"https://github.test/org/beta/pull/88"}
		},
		{
			"id":"b-closed",
			"title":"Closed PR",
			"status":"in_progress",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.pr_url":"https://github.test/org/beta/pull/99"}
		}
	]`
	mergedTaskJSON := `[
		{
			"id":"b-merged",
			"title":"Merged PR",
			"status":"in_progress",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.pr_url":"https://github.test/org/beta/pull/88"}
		}
	]`
	closedTaskJSON := `[
		{
			"id":"b-closed",
			"title":"Closed PR",
			"status":"in_progress",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.pr_url":"https://github.test/org/beta/pull/99"}
		}
	]`
	bdLogPath := withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: alphaPath, args: "--json --readonly --sandbox list --all --limit 0", stdout: alphaListJSON},
		{dir: betaPath, args: "--json --readonly --sandbox list --all --limit 0", stdout: betaListJSON},
		{
			dir:      gammaPath,
			args:     "--json --readonly --sandbox list --all --limit 0",
			stderr:   "bd unavailable\n",
			exitCode: 1,
		},
		{dir: alphaPath, args: "--json --readonly --sandbox show --id a-open", stdout: openTaskJSON},
		{dir: betaPath, args: "--json --readonly --sandbox show --id b-merged", stdout: mergedTaskJSON},
		{dir: betaPath, args: "--json --readonly --sandbox show --id b-merged", stdout: mergedTaskJSON},
		{dir: betaPath, args: "--json --sandbox close b-merged", stdout: "{}"},
		{dir: betaPath, args: "--json --readonly --sandbox show --id b-closed", stdout: closedTaskJSON},
	})
	ghLogPath := withFakeGHPRResponses(t, fakeGHPRResponses{
		statusByURL: []fakeGHPRStatusResponse{
			{
				url:    "https://github.test/org/alpha/pull/77",
				stdout: `{"url":"https://github.test/org/alpha/pull/77","state":"OPEN","mergedAt":null}`,
			},
			{
				url:    "https://github.test/org/beta/pull/88",
				stdout: `{"url":"https://github.test/org/beta/pull/88","state":"MERGED","mergedAt":"2026-06-14T10:00:00Z"}`,
			},
			{
				url:    "https://github.test/org/beta/pull/99",
				stdout: `{"url":"https://github.test/org/beta/pull/99","state":"CLOSED","mergedAt":null}`,
			},
		},
	})

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "sync", "--all"})

	must.Error(err)
	is.Empty(stderr)
	is.Contains(stdout, "Open/in-review PRs (1):")
	is.Contains(stdout, "a-open (alpha): PR https://github.test/org/alpha/pull/77 is still open for review")
	is.Contains(stdout, "Merged/closed tasks (1):")
	is.Contains(stdout, "b-merged (beta): PR https://github.test/org/beta/pull/88 is merged; backend task was closed")
	is.Contains(stdout, "Errors (2):")
	is.Contains(stdout, "repo gamma: scan_tasks:")
	is.Contains(stdout, "bd unavailable")
	is.Contains(stdout, "b-closed (beta): sync:")
	is.Contains(stdout, "closed without merge")
	is.ErrorContains(err, "task sync --all failed for 2 item")

	bdLog := readFileString(t, bdLogPath)
	is.Contains(bdLog, alphaPath+"\n--json --readonly --sandbox list --all --limit 0")
	is.Contains(bdLog, betaPath+"\n--json --readonly --sandbox list --all --limit 0")
	is.Contains(bdLog, gammaPath+"\n--json --readonly --sandbox list --all --limit 0")
	is.NotContains(bdLog, "--id a-create")
	is.NotContains(bdLog, "--set-metadata orpheus.pr_url=https://github.test/org/alpha/pull/42")
	is.Contains(bdLog, "--json --sandbox close b-merged")
	is.NotContains(bdLog, "--json --sandbox close b-closed")

	ghLog := readFileString(t, ghLogPath)
	is.NotContains(ghLog, "ARG_2<<END\nlist\nEND")
	is.NotContains(ghLog, "ARG_2<<END\ncreate\nEND")
	is.Equal(3, strings.Count(ghLog, "ARG_2<<END\nview\nEND"))

	var betaState taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "beta", "tasks", "b-merged.yaml"), &betaState))
	must.Len(betaState.Events, 1)
	event := betaState.Events[0]
	is.Equal(taskstate.EventTaskClosed, event.Type)
	is.Equal(taskstate.CloseReasonPRMerged, event.CloseReason)
	is.Equal("https://github.test/org/beta/pull/88", event.PRURL)
	is.Equal("merged", event.ObservedPRState)
}

func TestTaskDoneInfersSingleMainReadyTaskFromRepoRootAndUsesOverrides(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-infer", repoPath, "Stored summary", "Stored details.")
	recordPassedReview(t, paths, "alpha", "op-infer")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "human-review.txt"), []byte("human\n"), 0o644))
	t.Chdir(repoPath)

	taskJSON := mainReadyTaskJSON("op-infer", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox list --all --limit 0", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-infer", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-infer", stdout: "{}"},
	})

	stdout, stderr := executeCommand(t, []string{
		"task",
		"done",
		"--summary",
		"Human reviewed summary",
		"--description",
		"Human adjusted details.",
	})

	is.Empty(stderr)
	is.Contains(stdout, "Finalized op-infer")
	message := strings.TrimSpace(runGit(t, repoPath, "log", "-1", "--format=%B"))
	is.Equal("Human reviewed summary\n\nHuman adjusted details.", message)
}

func TestTaskDoneInfersRepoRootFeatureBranchTask(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	const taskID = "op-root-infer"
	const branch = "orpheus/op-root-infer"
	runGit(t, repoPath, "checkout", "-b", branch)
	recordWorktreeCompletion(t, paths, "alpha", taskID, branch, repoPath, "")
	recordPassedReview(t, paths, "alpha", taskID)
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	t.Chdir(repoPath)

	taskJSON := syncReadyTaskJSON(taskID, branch, repoPath)
	bdLogPath := withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox list --all --limit 0", stdout: taskJSON},
		{
			dir:  repoPath,
			args: "--json --sandbox update " + taskID + " --set-metadata orpheus.pr_url=https://github.test/org/alpha/pull/42",
		},
	})
	withFakeGHPRResponses(t, fakeGHPRResponses{
		listStdout:   "[]",
		createStdout: "https://github.test/org/alpha/pull/42\n",
	})

	stdout, stderr := executeCommand(t, []string{"task", "done"})

	is.Empty(stderr)
	is.Contains(stdout, "Published "+taskID)
	is.Contains(stdout, "pushed "+branch)
	is.Contains(stdout, "created PR https://github.test/org/alpha/pull/42")
	bdLog := readFileString(t, bdLogPath)
	is.Contains(bdLog, "--json --sandbox update "+taskID+" --set-metadata orpheus.pr_url=https://github.test/org/alpha/pull/42")
	is.NotContains(bdLog, "--json --sandbox close "+taskID)
	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	is.Equal(
		strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD")),
		strings.TrimSpace(runGit(t, originPath, "rev-parse", "refs/heads/"+branch)),
	)
}

func TestTaskDoneInfersWorktreeTask(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	const taskID = "op-worktree-infer"
	targets := taskSyncExpectedTargets(t, paths, repoPath, taskID)
	worktree := targets.WorktreeTeam.Worktree
	branch := targets.WorktreeTeam.Branch
	runGit(t, repoPath, "branch", branch, "main")
	runGit(t, repoPath, "worktree", "add", worktree, branch)
	recordWorktreeCompletion(t, paths, "alpha", taskID, branch, worktree, "")
	recordPassedReview(t, paths, "alpha", taskID)
	must.NoError(os.WriteFile(filepath.Join(worktree, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	t.Chdir(worktree)

	taskJSON := syncReadyTaskJSON(taskID, branch, worktree)
	bdLogPath := withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox list --all --limit 0", stdout: taskJSON},
		{
			dir:  repoPath,
			args: "--json --sandbox update " + taskID + " --set-metadata orpheus.pr_url=https://github.test/org/alpha/pull/42",
		},
	})
	withFakeGHPRResponses(t, fakeGHPRResponses{
		listStdout:   "[]",
		createStdout: "https://github.test/org/alpha/pull/42\n",
	})

	stdout, stderr := executeCommand(t, []string{"task", "done"})

	is.Empty(stderr)
	is.Contains(stdout, "Published "+taskID)
	is.Contains(stdout, "pushed "+branch)
	is.Contains(stdout, "created PR https://github.test/org/alpha/pull/42")
	bdLog := readFileString(t, bdLogPath)
	is.Contains(bdLog, "--json --sandbox update "+taskID+" --set-metadata orpheus.pr_url=https://github.test/org/alpha/pull/42")
	is.NotContains(bdLog, "--json --sandbox close "+taskID)
	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	is.Equal(
		strings.TrimSpace(runGit(t, worktree, "rev-parse", "HEAD")),
		strings.TrimSpace(runGit(t, originPath, "rev-parse", "refs/heads/"+branch)),
	)
}

func TestTaskDoneRejectsRemovedDetailsOverride(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	stdout, stderr, err := executeCommandWithError(t, []string{
		"task",
		"done",
		"op-main",
		"--details",
		"Old details.",
	})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "unknown flag: --details")
}

func TestTaskDoneWithoutTaskIDRequiresExactRegisteredRepoRoot(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	nested := filepath.Join(repoPath, "nested")
	must.NoError(os.MkdirAll(nested, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	t.Chdir(nested)

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "done"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	must.ErrorContains(err, "cwd must be exactly a registered repo root")
	is.ErrorContains(err, "pass <task-id>")
}

func TestTaskDoneRefusesNoChangesWithoutRecordedFinalizationCommit(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-clean", repoPath, "Clean summary", "Clean details.")
	recordPassedReview(t, paths, "alpha", "op-clean")
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{{
		dir:    repoPath,
		args:   "--json --readonly --sandbox show --id op-clean",
		stdout: mainReadyTaskJSON("op-clean", repoPath),
	}})

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "done", "op-clean"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "has no changes to commit")
	is.ErrorContains(err, "has no recorded finalization commit")
}

func TestTaskDoneRetriesPushAndCloseFromRecordedFinalizationCommit(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	configureTestGitUser(t, repoPath)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	recordMainCompletion(t, paths, "alpha", "op-retry", repoPath, "Retry summary", "Retry details.")
	recordPassedReview(t, paths, "alpha", "op-retry")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "already-committed.txt"), []byte("committed\n"), 0o644))
	runGit(t, repoPath, "add", "already-committed.txt")
	runGit(t, repoPath, "commit", "-m", "Retry summary", "-m", "Retry details.")
	commit := strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD"))
	_, err := taskstate.NewStore(paths).RecordFinalizationCommit("alpha", "op-retry", commit)
	must.NoError(err)

	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-retry", stdout: mainReadyTaskJSON("op-retry", repoPath)},
		{dir: repoPath, args: "--json --sandbox close op-retry", stdout: "{}"},
	})

	stdout, stderr := executeCommand(t, []string{"task", "done", "op-retry"})

	is.Empty(stderr)
	is.Contains(stdout, "Finalized op-retry")
	is.Equal(commit, strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD")))
	originPath := strings.TrimSpace(runGit(t, repoPath, "remote", "get-url", "origin"))
	is.Equal(commit, strings.TrimSpace(runGit(t, originPath, "rev-parse", "refs/heads/main")))

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-retry.yaml"), &state))
	facts := taskstate.FinalizationFacts(state)
	is.Equal(commit, facts.Commit)
	must.NotNil(facts.PushedAt)
	must.NotNil(facts.ClosedAt)
}

func withFakeBDTaskResponses(t *testing.T, responses map[string]fakeBDTaskResponse) string {
	t.Helper()

	binDir := t.TempDir()
	fixtureDir := filepath.Join(binDir, "fixtures")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatalf("create fake bd fixtures: %v", err)
	}

	logPath := filepath.Join(binDir, "bd.log")
	var script strings.Builder
	script.WriteString(`#!/bin/sh
{
  pwd
  printf '%s\n' "$*"
} >> "$FAKE_BD_LOG"
is_update=0
case "$*" in
  "--json --readonly --sandbox list --all --limit 0"|"--json --readonly --sandbox show --id "*)
    ;;
  "--json --sandbox update "*)
    is_update=1
    ;;
  *)
    echo "unexpected args: $*" >&2
    exit 64
    ;;
esac
case "$PWD" in
`)

	index := 0
	for dir, response := range responses {
		writeFakeBDTaskResponseCase(t, &script, fixtureDir, index, dir, response)
		index++
	}
	script.WriteString(`esac
echo "no fake bd response for $PWD" >&2
exit 65
`)

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(script.String()), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("FAKE_BD_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func writeFakeBDTaskResponseCase(
	t *testing.T,
	script *strings.Builder,
	fixtureDir string,
	index int,
	dir string,
	response fakeBDTaskResponse,
) {
	t.Helper()

	stdoutPath := filepath.Join(fixtureDir, fmt.Sprintf("stdout-%d.json", index))
	stderrPath := filepath.Join(fixtureDir, fmt.Sprintf("stderr-%d.txt", index))
	writeTestFile(t, stdoutPath, response.stdout, "fake bd stdout")
	writeTestFile(t, stderrPath, response.stderr, "fake bd stderr")
	exitCode := response.exitCode
	if exitCode == 0 && response.stderr != "" && response.stdout == "" {
		exitCode = 1
	}
	fmt.Fprintf(script, "  %s)\n", shellQuote(dir))
	fmt.Fprintln(script, "    if [ \"$is_update\" = 1 ]; then")
	fmt.Fprintln(script, "      printf '{}\\n'")
	fmt.Fprintln(script, "      exit 0")
	fmt.Fprintln(script, "    fi")
	fmt.Fprintf(script, "    cat %s\n", shellQuote(stdoutPath))
	fmt.Fprintf(script, "    cat %s >&2\n", shellQuote(stderrPath))
	fmt.Fprintf(script, "    exit %d\n", exitCode)
	fmt.Fprintln(script, "    ;;")
}

type fakeGHPRResponses struct {
	listStdout   string
	listExit     int
	createStdout string
	createExit   int
	statusStdout string
	statusExit   int
	statusByURL  []fakeGHPRStatusResponse
}

type fakeGHPRStatusResponse struct {
	url    string
	stdout string
	exit   int
}

const fakeGHPRScriptHeader = `#!/bin/sh
{
  pwd
  printf 'ARGC=%%s\n' "$#"
  index=0
  for arg in "$@"; do
    index=$((index + 1))
    printf 'ARG_%%s<<END\n%%s\nEND\n' "$index" "$arg"
  done
  if [ "$1 $2" = "pr create" ]; then
    printf 'STDIN<<END\n'
    cat
    printf '\nEND\n'
  fi
} >> "$FAKE_GH_LOG"
case "$1 $2" in
  "pr list")
    cat %s
    exit %d
    ;;
  "pr create")
    cat %s
    exit %d
    ;;
  "pr view")
`

const fakeGHPRScriptFooter = `    cat %s
    exit %d
    ;;
esac
echo "unexpected gh args: $*" >&2
exit 65
`

func withFakeGHPRResponses(t *testing.T, responses fakeGHPRResponses) string {
	t.Helper()

	binDir := t.TempDir()
	fixtureDir := filepath.Join(binDir, "fixtures")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatalf("create fake gh fixtures: %v", err)
	}

	listStdoutPath, createStdoutPath, statusStdoutPath := writeFakeGHPRFixtures(t, fixtureDir, responses)
	logPath := filepath.Join(binDir, "gh.log")
	script := fmt.Sprintf(
		fakeGHPRScriptHeader,
		shellQuote(listStdoutPath),
		responses.listExit,
		shellQuote(createStdoutPath),
		responses.createExit,
	)

	if len(responses.statusByURL) > 0 {
		script += `    case "$3" in
`
		for i, response := range responses.statusByURL {
			statusPath := filepath.Join(fixtureDir, fmt.Sprintf("status-stdout-url-%d.txt", i))
			if err := os.WriteFile(statusPath, []byte(response.stdout), 0o644); err != nil {
				t.Fatalf("write fake gh URL status stdout: %v", err)
			}
			script += fmt.Sprintf("      %s)\n", shellQuote(response.url))
			script += fmt.Sprintf("        cat %s\n", shellQuote(statusPath))
			script += fmt.Sprintf("        exit %d\n", response.exit)
			script += "        ;;\n"
		}
		script += `    esac
`
	}

	script += fmt.Sprintf(fakeGHPRScriptFooter, shellQuote(statusStdoutPath), responses.statusExit)

	ghPath := filepath.Join(binDir, "gh")
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("FAKE_GH_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func writeFakeGHPRFixtures(t *testing.T, fixtureDir string, responses fakeGHPRResponses) (string, string, string) {
	t.Helper()

	listStdoutPath := filepath.Join(fixtureDir, "list-stdout.txt")
	createStdoutPath := filepath.Join(fixtureDir, "create-stdout.txt")
	statusStdoutPath := filepath.Join(fixtureDir, "status-stdout.txt")
	writeTestFile(t, listStdoutPath, responses.listStdout, "fake gh list stdout")
	writeTestFile(t, createStdoutPath, responses.createStdout, "fake gh create stdout")
	writeTestFile(t, statusStdoutPath, responses.statusStdout, "fake gh status stdout")
	return listStdoutPath, createStdoutPath, statusStdoutPath
}

func writeTestFile(t *testing.T, path string, content string, label string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", label, err)
	}
}

func configureTestGitUser(t *testing.T, repoPath string) {
	t.Helper()
	runGit(t, repoPath, "config", "user.name", "Orpheus Test")
	runGit(t, repoPath, "config", "user.email", "orpheus@example.com")
}

func clockSequence(times ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if len(times) == 0 {
			return time.Now().UTC()
		}
		if index >= len(times) {
			return times[len(times)-1]
		}
		value := times[index]
		index++
		return value
	}
}

func taskSyncExpectedTargets(
	t *testing.T,
	paths state.Paths,
	repoPath string,
	taskID string,
) workflow.ExpectedTargets {
	t.Helper()
	targets, err := workflow.ExpectedTargetsForTask(taskmodel.Repository{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
	}, taskID, paths)
	if err != nil {
		t.Fatalf("expected sync targets: %v", err)
	}
	return targets
}

func recordMainCompletion(t *testing.T, paths state.Paths, repoID string, taskID string, repoPath string, summary string, description string) {
	t.Helper()
	store := taskstate.NewStore(paths)
	attempt, err := store.StartRun(repoID, taskID, taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "main",
		Worktree: repoPath,
	})
	if err != nil {
		t.Fatalf("start main run: %v", err)
	}
	if _, err := store.CompleteRun(repoID, taskID, attempt.Attempt, taskstate.CompleteRunOptions{
		Summary:             summary,
		Description:         description,
		DetailedDescription: "Detailed PR body.",
	}); err != nil {
		t.Fatalf("complete main run: %v", err)
	}
	if _, err := store.FinishRun(repoID, taskID, attempt.Attempt, taskstate.RunStatusSucceeded); err != nil {
		t.Fatalf("finish main run: %v", err)
	}
}

func recordReviewFollowUpCompletion(
	t *testing.T,
	paths state.Paths,
	repoID string,
	taskID string,
	repoPath string,
	reviewAttempt int,
	summary string,
	description string,
) {
	t.Helper()
	store := taskstate.NewStore(paths)
	attempt, err := store.StartRun(repoID, taskID, taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "main",
		Worktree: repoPath,
		ReviewFollowUp: &taskstate.ReviewFollowUp{
			ReviewAttempt:  reviewAttempt,
			FindingIndexes: []int{0},
		},
	})
	if err != nil {
		t.Fatalf("start review follow-up run: %v", err)
	}
	if _, err := store.CompleteRun(repoID, taskID, attempt.Attempt, taskstate.CompleteRunOptions{
		Summary:             summary,
		Description:         description,
		DetailedDescription: "Detailed follow-up body.",
	}); err != nil {
		t.Fatalf("complete review follow-up run: %v", err)
	}
	if _, err := store.FinishRun(repoID, taskID, attempt.Attempt, taskstate.RunStatusSucceeded); err != nil {
		t.Fatalf("finish review follow-up run: %v", err)
	}
}

func recordRunningMainCompletion(t *testing.T, paths state.Paths, repoID string, taskID string, repoPath string, summary string, description string) {
	t.Helper()
	store := taskstate.NewStore(paths)
	attempt, err := store.StartRun(repoID, taskID, taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "main",
		Worktree: repoPath,
	})
	if err != nil {
		t.Fatalf("start running main run: %v", err)
	}
	if _, err := store.CompleteRun(repoID, taskID, attempt.Attempt, taskstate.CompleteRunOptions{
		Summary:             summary,
		Description:         description,
		DetailedDescription: "Detailed PR body.",
	}); err != nil {
		t.Fatalf("complete running main run: %v", err)
	}
}

func recordPassedReview(t *testing.T, paths state.Paths, repoID string, taskID string) {
	t.Helper()
	store := taskstate.NewStore(paths)
	review, err := store.StartReview(repoID, taskID)
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	if _, err := store.FinishReview(repoID, taskID, review.Attempt, taskstate.ReviewStatusPassed); err != nil {
		t.Fatalf("finish review: %v", err)
	}
}

func recordWorktreeCompletion(
	t *testing.T,
	paths state.Paths,
	repoID string,
	taskID string,
	branch string,
	worktree string,
	commit string,
) {
	t.Helper()
	store := taskstate.NewStore(paths)
	attempt, err := store.StartRun(repoID, taskID, taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   branch,
		Worktree: worktree,
	})
	if err != nil {
		t.Fatalf("start worktree run: %v", err)
	}
	if _, err := store.CompleteRun(repoID, taskID, attempt.Attempt, taskstate.CompleteRunOptions{
		Summary:             "Ready for PR",
		Description:         "Implemented task branch changes.",
		DetailedDescription: "Detailed PR body.",
		Commit:              commit,
	}); err != nil {
		t.Fatalf("complete worktree run: %v", err)
	}
	if _, err := store.FinishRun(repoID, taskID, attempt.Attempt, taskstate.RunStatusSucceeded); err != nil {
		t.Fatalf("finish worktree run: %v", err)
	}
}

func mainReadyTaskJSON(taskID string, repoPath string) string {
	return `[
		{
			"id":"` + taskID + `",
			"title":"Ready for task done",
			"status":"in_progress",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.branch":"main","orpheus.worktree":"` + repoPath + `"}
		}
	]`
}

func syncReadyTaskJSON(taskID string, branch string, worktree string) string {
	return `[
		{
			"id":"` + taskID + `",
			"title":"Ready for sync",
			"description":"Ready for reviewed publication.",
			"acceptance_criteria":"Sync skips tasks without recorded pull requests.",
			"status":"in_progress",
			"priority":1,
			"issue_type":"task",
			"metadata":{"orpheus.branch":"` + branch + `","orpheus.worktree":"` + worktree + `"}
		}
	]`
}

func withFakeAgent(t *testing.T, name string, exitCode int) string {
	t.Helper()

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, name+".log")
	script := fmt.Sprintf(`#!/bin/sh
{
  printf 'PWD=%%s\n' "$PWD"
  printf 'ARG_COUNT=%%s\n' "$#"
  index=0
  for arg in "$@"; do
    index=$((index + 1))
    printf 'ARG_%%s<<END\n%%s\nEND\n' "$index" "$arg"
  done
  printf 'ORPHEUS_REPO_ID=%%s\n' "$ORPHEUS_REPO_ID"
  printf 'ORPHEUS_TASK_ID=%%s\n' "$ORPHEUS_TASK_ID"
  printf 'ORPHEUS_WORKTREE=%%s\n' "$ORPHEUS_WORKTREE"
  printf 'ORPHEUS_BRANCH=%%s\n' "$ORPHEUS_BRANCH"
  printf 'ORPHEUS_AGENT_PURPOSE=%%s\n' "$ORPHEUS_AGENT_PURPOSE"
  printf 'ORPHEUS_REVIEW_ATTEMPT=%%s\n' "$ORPHEUS_REVIEW_ATTEMPT"
  printf 'ORPHEUS_REVIEW_STEP=%%s\n' "$ORPHEUS_REVIEW_STEP"
  printf 'ORPHEUS_AGENT_PROMPT<<END\n%%s\nEND\n' "$ORPHEUS_AGENT_PROMPT"
} >> "$FAKE_AGENT_LOG"
printf 'fake agent stdout\n'
printf 'fake agent stderr\n' >&2
exit %d
`, exitCode)

	agentPath := filepath.Join(binDir, name)
	if err := os.WriteFile(agentPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}
	t.Setenv("FAKE_AGENT_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func writeCodexSessionLogForCLI(t *testing.T, path string, cwd string, sessionID string, startedAt time.Time) {
	t.Helper()

	timestamp := startedAt.UTC().Format(time.RFC3339Nano)
	content := `{"timestamp":"` + timestamp + `","type":"session_meta","payload":{"session_id":"` + sessionID + `","id":"` + sessionID + `","timestamp":"` + timestamp + `","cwd":"` + cwd + `","model":"gpt-5"}}
{"timestamp":"` + timestamp + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":123,"cached_input_tokens":45,"output_tokens":67,"reasoning_output_tokens":8,"total_tokens":190}}}}
`
	writeTestFile(t, path, content, "codex session log")
}

func writeTaskRunAgentConfig(t *testing.T, paths state.Paths, name string, command string, args []string) {
	t.Helper()

	profile := map[string]any{"command": command}
	if args != nil {
		profile["args"] = args
	}
	require.NoError(t, paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{"implementer": name},
			"profiles": map[string]any{
				name: profile,
			},
		},
	}))
}

func writeStructuredCodexTaskRunAgentConfig(
	t *testing.T,
	paths state.Paths,
	name string,
	model string,
	thinking string,
	interactive bool,
) {
	t.Helper()

	require.NoError(t, paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{"implementer": name},
			"profiles": map[string]any{
				name: map[string]any{
					"harness":     "codex",
					"model":       model,
					"thinking":    thinking,
					"interactive": interactive,
				},
			},
		},
	}))
}

func writeReviewPipelineConfig(
	t *testing.T,
	paths state.Paths,
	defaultPipeline string,
	pipelines map[string][]map[string]any,
) {
	t.Helper()

	configPipelines := map[string]any{}
	for name, steps := range pipelines {
		configPipelines[name] = map[string]any{"steps": steps}
	}
	require.NoError(t, paths.WriteConfigYAML(reviewconfig.ConfigFile, map[string]any{
		"reviews": map[string]any{
			"default_pipeline": defaultPipeline,
			"pipelines":        configPipelines,
		},
	}))
}

func writeReviewAgentPipelineConfig(
	t *testing.T,
	paths state.Paths,
	reviewerName string,
	reviewerCommand string,
	reviewerArgs []string,
	defaultPipeline string,
	pipelines map[string][]map[string]any,
) {
	t.Helper()

	configPipelines := map[string]any{}
	for name, steps := range pipelines {
		configPipelines[name] = map[string]any{"steps": steps}
	}
	reviewerProfile := map[string]any{"command": reviewerCommand}
	if reviewerArgs != nil {
		reviewerProfile["args"] = reviewerArgs
	}
	require.NoError(t, paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"implementer": "implementer",
				"reviewer":    reviewerName,
			},
			"profiles": map[string]any{
				"implementer": map[string]any{"command": "unused-implementer"},
				reviewerName:  reviewerProfile,
			},
		},
		"reviews": map[string]any{
			"default_pipeline": defaultPipeline,
			"pipelines":        configPipelines,
		},
	}))
}

func writeStructuredCodexReviewAgentPipelineConfig(
	t *testing.T,
	paths state.Paths,
	reviewerName string,
	model string,
	interactive bool,
	defaultPipeline string,
	pipelines map[string][]map[string]any,
) {
	t.Helper()

	configPipelines := map[string]any{}
	for name, steps := range pipelines {
		configPipelines[name] = map[string]any{"steps": steps}
	}
	require.NoError(t, paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"implementer": "implementer",
				"reviewer":    reviewerName,
			},
			"profiles": map[string]any{
				"implementer": map[string]any{"command": "unused-implementer"},
				reviewerName: map[string]any{
					"harness":     "codex",
					"model":       model,
					"interactive": interactive,
				},
			},
		},
		"reviews": map[string]any{
			"default_pipeline": defaultPipeline,
			"pipelines":        configPipelines,
		},
	}))
}

func writeAutonomousReviewLoopConfig(
	t *testing.T,
	paths state.Paths,
	implementerName string,
	implementerCommand string,
	maxAttempts int,
	steps []map[string]any,
) {
	t.Helper()

	require.NoError(t, paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"implementer": implementerName,
			},
			"profiles": map[string]any{
				implementerName: map[string]any{"command": implementerCommand},
			},
		},
		"reviews": map[string]any{
			"default_pipeline":               "standard",
			"max_autonomous_review_attempts": maxAttempts,
			"pipelines": map[string]any{
				"standard": map[string]any{"steps": steps},
			},
		},
	}))
}

func writeAutonomousReviewLoopConfigWithImplementers(
	t *testing.T,
	paths state.Paths,
	firstName string,
	firstCommand string,
	secondName string,
	secondCommand string,
	defaultName string,
	maxAttempts int,
	steps []map[string]any,
) {
	t.Helper()

	require.NoError(t, paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"implementer": defaultName,
			},
			"profiles": map[string]any{
				firstName:  map[string]any{"command": firstCommand},
				secondName: map[string]any{"command": secondCommand},
			},
		},
		"reviews": map[string]any{
			"default_pipeline":               "standard",
			"max_autonomous_review_attempts": maxAttempts,
			"pipelines": map[string]any{
				"standard": map[string]any{"steps": steps},
			},
		},
	}))
}

func writeAutonomousReviewFixAgent(t *testing.T, name string, orpheusBin string, repair bool) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	passWrite := "printf 'still failing %s\\n' \"$count\" > status.txt"
	if repair {
		passWrite = `if [ "$count" -gt 1 ]; then
  printf 'pass\n' > status.txt
else
  printf 'fail\n' > status.txt
fi`
	}
	script := fmt.Sprintf(`#!/bin/sh
set -eu
count=1
if [ -f run-count.txt ]; then
  count=$(( $(cat run-count.txt) + 1 ))
fi
printf '%%s\n' "$count" > run-count.txt
%s
%s agent done \
  --summary "Autonomous run $count" \
  --description "Autonomous run $count completed." \
  --detailed-description "Detailed autonomous run $count."
`, passWrite, shellQuote(orpheusBin))
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write autonomous review fix agent: %v", err)
	}
	return path
}

func writeReviewScript(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "review-step")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write review script: %v", err)
	}
	return path
}

func buildOrpheusTestBinary(t *testing.T, sourceRoot string) string {
	t.Helper()

	binPath := filepath.Join(t.TempDir(), "orpheus")
	command := exec.Command("go", "build", "-o", binPath, "./cmd/orpheus")
	command.Dir = sourceRoot
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build orpheus test binary: %v\n%s", err, output)
	}
	return binPath
}

func readTestFileString(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func installFakeHunkNotes(t *testing.T, response string) {
	t.Helper()

	binDir := t.TempDir()
	hunkPath := filepath.Join(binDir, "hunk")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "session" ] && [ "$2" = "comment" ] && [ "$3" = "list" ]; then
  printf '%%s\n' %s
  exit 0
fi
printf 'unexpected fake hunk call: %%s\n' "$*" >&2
exit 65
`, shellQuote(response))
	require.NoError(t, os.WriteFile(hunkPath, []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
