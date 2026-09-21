//go:build integration

package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskListListsAllActiveItemsWithStatusProjectionPresentation(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)

	repos := saveTaskWorkflowRepos(t, fixture, taskWorkflowRepoSpec("local-alpha", "Local Alpha", "la"), taskWorkflowRepoSpec("managed-beta", "Managed Beta", "mb"))
	reads := withWorkflowSources(t, fixture, map[string]taskSourceResult{
		repos["local-alpha"]: {tasks: []taskmodel.Task{
			taskWFTask("la-1", "Local active", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(2), taskWFMetadata(map[string]string{taskmodel.MetadataBranch: "task/la-1", taskmodel.MetadataWorktree: "/fixture/la-1"})),
			taskWFTask("la-closed", "Closed local task", taskmodel.StatusClosed, taskmodel.IssueTypeTask, taskWFPriority(1)),
		}},
		repos["managed-beta"]: {tasks: []taskmodel.Task{taskWFTask("mb-1", "Managed active", taskmodel.StatusInProgress, taskmodel.IssueTypeTask, taskWFPriority(3), taskWFMetadata(map[string]string{taskmodel.MetadataPRURL: "https://example.test/pr/1"}))}},
	})

	stdout, stderr := fixture.mustExecute("task", "list")

	is.Empty(stderr)
	for _, want := range []string{"TASK_ID", "STATUS", "P", "TITLE", "REPO", "DETAIL", "Local Alpha", "la-1", "Ready", "2", "Local active", "Managed Beta", "mb-1", "Reviewing", "3", "Managed active", "https://example.test/pr/1"} {
		is.Contains(stdout, want)
	}
	for _, hidden := range []string{"REPO_ID", "TASK_PREFIX", "ORPHEUS", "local-alpha", "managed-beta", "branch=task/la-1", "worktree=/fixture/la-1", "pr=https://example.test/pr/1", "la-closed", "orpheus.branch"} {
		is.NotContains(stdout, hidden)
	}
	jsonStdout, jsonStderr := fixture.mustExecute("task", "list", "--json")
	is.Empty(jsonStderr)
	var jsonEntries []taskViewJSONTaskEntry
	require.NoError(t, json.Unmarshal([]byte(jsonStdout), &jsonEntries))
	jsonIDs := make([]string, 0, len(jsonEntries))
	entriesByID := map[string]taskViewJSONTaskEntry{}
	for _, entry := range jsonEntries {
		jsonIDs = append(jsonIDs, entry.ID)
		entriesByID[entry.ID] = entry
	}
	is.Equal([]string{"la-1", "mb-1"}, jsonIDs)
	is.Equal("ready", entriesByID["la-1"].Status)
	is.Equal("reviewing", entriesByID["mb-1"].Status)
	is.Equal("task", entriesByID["la-1"].Kind)
	is.Equal(4, reads.count("list"))
}

func TestIntegrationWorkflowTaskListScopesOneRegisteredRepository(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	repos := saveTaskWorkflowRepos(t, fixture, taskWorkflowRepoSpec("alpha", "Alpha Repo", "a"), taskWorkflowRepoSpec("beta", "Beta Repo", "b"))
	reads := withWorkflowSources(t, fixture, map[string]taskSourceResult{
		repos["alpha"]: {tasks: taskListScopeTasks()},
		repos["beta"]:  {err: errors.New("excluded backend should not be queried")},
	})

	stdout, stderr, err := fixture.execute("task", "list", "--repo", "alpha")
	must.NoError(err)
	is.Empty(stderr)
	for _, want := range []string{"a-open", "a-epic", "Alpha Repo"} {
		is.Contains(stdout, want)
	}
	for _, hidden := range []string{"a-closed", "a-late", "a-other", "Beta Repo", "excluded backend should not be queried"} {
		is.NotContains(stdout, hidden)
	}

	jsonStdout, jsonStderr, jsonErr := fixture.execute("task", "list", "--repo", "a", "--json")
	must.NoError(jsonErr)
	is.Empty(jsonStderr)
	var jsonEntries []taskViewJSONTaskEntry
	must.NoError(json.Unmarshal([]byte(jsonStdout), &jsonEntries))
	jsonIDs := make([]string, 0, len(jsonEntries))
	for _, entry := range jsonEntries {
		jsonIDs = append(jsonIDs, entry.ID)
		is.Equal("alpha", entry.Repository.ID)
	}
	is.ElementsMatch([]string{"a-epic", "a-open"}, jsonIDs)

	filteredArgs := []string{"task", "list", "--repo", "Alpha Repo", "--query", "match", "--type", "task", "--created-after", "2026-06-01", "--created-before", "2026-06-05", "--updated-after", "2026-06-02", "--updated-before", "2026-06-05", "--status", "closed"}
	for _, sortMode := range []string{"created", "status"} {
		filteredStdout, filteredStderr, filteredErr := fixture.execute(append(append([]string{}, filteredArgs...), "--sort", sortMode)...)
		must.NoError(filteredErr)
		is.Empty(filteredStderr)
		is.Contains(filteredStdout, "a-closed")
		for _, hidden := range []string{"a-open", "a-late", "a-epic", "a-other", "Beta Repo"} {
			is.NotContains(filteredStdout, hidden)
		}
	}
	filteredJSON, filteredJSONStderr, filteredJSONErr := fixture.execute(append(append([]string{}, filteredArgs...), "--sort", "updated", "--json")...)
	must.NoError(filteredJSONErr)
	is.Empty(filteredJSONStderr)
	var filteredEntries []taskViewJSONTaskEntry
	must.NoError(json.Unmarshal([]byte(filteredJSON), &filteredEntries))
	must.Len(filteredEntries, 1)
	is.Equal("a-closed", filteredEntries[0].ID)
	_, _, unknownErr := fixture.execute("task", "list", "--repo", "missing")
	must.Error(unknownErr)
	is.ErrorContains(unknownErr, `repo "missing" is not registered`)
	is.ErrorContains(unknownErr, "orpheus repo list")
	is.NotContains(reads.directories(), repos["beta"])
}

func taskListScopeTasks() []taskmodel.Task {
	return []taskmodel.Task{
		taskWFTask("a-closed", "MATCH closed task", taskmodel.StatusClosed, taskmodel.IssueTypeTask, taskWFPriority(1), taskWFCreated("2026-06-03T00:00:00Z"), taskWFUpdated("2026-06-03T00:00:00Z")),
		taskWFTask("a-open", "match open task", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(1), taskWFCreated("2026-06-03T00:00:00Z"), taskWFUpdated("2026-06-03T00:00:00Z")),
		taskWFTask("a-late", "match updated too late", taskmodel.StatusClosed, taskmodel.IssueTypeTask, taskWFPriority(1), taskWFCreated("2026-06-03T00:00:00Z"), taskWFUpdated("2026-06-06T00:00:00Z")),
		taskWFTask("a-epic", "Selected epic", taskmodel.StatusOpen, taskmodel.IssueTypeEpic, taskWFPriority(1)),
		taskWFTask("a-other", "unrelated", taskmodel.StatusClosed, taskmodel.IssueTypeTask, taskWFPriority(1), taskWFCreated("2026-06-03T00:00:00Z"), taskWFUpdated("2026-06-03T00:00:00Z")),
	}
}

func TestIntegrationWorkflowTaskListReportsSelectedRepositoryFailure(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	repos := saveTaskWorkflowRepos(t, fixture, taskWorkflowRepoSpec("alpha", "Broken Repo", "a"), taskWorkflowRepoSpec("beta", "Healthy Repo", "b"))
	reads := withWorkflowSources(t, fixture, map[string]taskSourceResult{repos["alpha"]: {err: errors.New("selected backend failed")}, repos["beta"]: {tasks: []taskmodel.Task{taskWFTask("b-1", "Excluded healthy task", taskmodel.StatusOpen, taskmodel.IssueTypeTask)}}})

	stdout, stderr, err := fixture.execute("task", "list", "--repo", "a")

	must.Error(err)
	is.ErrorContains(err, "task list completed with 1 repo failure")
	is.Contains(stdout, "Broken Repo")
	is.Contains(stderr, "task list: repo alpha")
	is.Contains(stderr, "selected backend failed")
	is.NotContains(stdout, "Healthy Repo")
	is.NotContains(stderr, "Healthy Repo")
	is.NotContains(reads.directories(), repos["beta"])
}

func TestIntegrationWorkflowTaskListComposesSourceAndProjectedStatusFilters(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "a")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: taskListScopeTasks()}})

	args := []string{"task", "list", "--query", "match", "--type", "task", "--created-after", "2026-06-01", "--created-before", "2026-06-05", "--updated-after", "2026-06-02", "--updated-before", "2026-06-05", "--status", "closed"}
	stdout, stderr := fixture.mustExecute(args...)

	is.Empty(stderr)
	is.Contains(stdout, "a-closed")
	for _, hidden := range []string{"a-open", "a-late", "a-other"} {
		is.NotContains(stdout, hidden)
	}
	jsonStdout, jsonStderr := fixture.mustExecute(append([]string{"task", "list", "--json"}, args[2:]...)...)
	is.Empty(jsonStderr)
	var jsonEntries []taskViewJSONTaskEntry
	require.NoError(t, json.Unmarshal([]byte(jsonStdout), &jsonEntries))
	require.Len(t, jsonEntries, 1)
	is.Equal("a-closed", jsonEntries[0].ID)
	is.Equal("closed", jsonEntries[0].Status)
}

func TestIntegrationWorkflowTaskListReportsPartialRepoFailures(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	repos := saveTaskWorkflowRepos(t, fixture, taskWorkflowRepoSpec("broken", "Broken Repo", "br"), taskWorkflowRepoSpec("ok", "OK Repo", "ok"))
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repos["broken"]: {err: errors.New("bd exploded")}, repos["ok"]: {tasks: []taskmodel.Task{taskWFTask("ok-1", "Listed despite another repo failure", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(1))}}})

	stdout, stderr, err := fixture.execute("task", "list")

	must.Error(err)
	is.ErrorContains(err, "task list completed with 1 repo failure")
	for _, want := range []string{"TASK_ID", "OK Repo", "ok-1", "Listed despite another repo failure", "Broken Repo"} {
		is.Contains(stdout, want)
	}
	for _, want := range []string{"task list: repo broken", "needs attention", "Broken Repo", "prefix br", "bd exploded"} {
		is.Contains(stderr, want)
	}
	jsonStdout, jsonStderr, jsonErr := fixture.execute("task", "list", "--json")
	must.Error(jsonErr)
	is.Contains(jsonStderr, "task list: repo broken")
	var jsonEntries []taskViewJSONTaskEntry
	must.NoError(json.Unmarshal([]byte(jsonStdout), &jsonEntries))
	is.Len(jsonEntries, 1)
	is.Equal("task", jsonEntries[0].Kind)
	is.Equal("ok-1", jsonEntries[0].ID)
}

func TestIntegrationWorkflowTaskShowResolvesPrefixQueriesOnlyResolvedRepoAndRendersDetails(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)

	repos := saveTaskWorkflowRepos(t, fixture, taskWorkflowRepoSpec("local-alpha", "Local Alpha", "la"), taskWorkflowRepoSpec("managed-beta", "Managed Beta", "mb"))
	reads := withWorkflowSources(t, fixture, map[string]taskSourceResult{
		repos["local-alpha"]:  {tasks: []taskmodel.Task{taskWFTask("la-42", "Implement local task show", taskmodel.StatusInProgress, taskmodel.IssueTypeTask, taskWFPriority(2), taskWFExternalRef("TREX-1234"), taskWFDetails("Render a backend-neutral detail view.\nKeep it read-only.", "Use prefix resolution and the task backend.", "Only the resolved repo is queried."), taskWFLabels("m2", "task-show"), taskWFMetadata(map[string]string{taskmodel.MetadataBranch: "task/la-42", taskmodel.MetadataWorktree: "/fixture/la-42", taskmodel.MetadataPRURL: "https://example.test/pr/42"}))}},
		repos["managed-beta"]: {err: errors.New("managed repo should not be queried")},
	})

	stdout, stderr := fixture.mustExecute("task", "show", "la-42")

	is.Empty(stderr)
	for _, want := range []string{"Repository:", "ID: local-alpha", "Name: Local Alpha", "Task prefix: la", "Task:", "ID: la-42", "Title: Implement local task show", "External reference: TREX-1234", "Status: in_progress", "Priority: 2", "Type: task", "Labels: m2, task-show", "Description:", "Render a backend-neutral detail view.", "Keep it read-only.", "Design: Use prefix resolution and the task backend.", "Acceptance criteria: Only the resolved repo is queried.", "Orpheus metadata:", "Branch: task/la-42", "Worktree: /fixture/la-42", "PR: https://example.test/pr/42", "History:", "  -"} {
		is.Contains(stdout, want)
	}
	for _, hidden := range []string{"orpheus.branch", "managed-beta", "Children:"} {
		is.NotContains(stdout, hidden)
	}
	is.NotContains(reads.directories(), repos["managed-beta"])
}

func TestIntegrationWorkflowTaskShowEpicRendersSortedDirectChildrenFromResolvedRepo(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)

	repos := saveTaskWorkflowRepos(t, fixture, taskWorkflowRepoSpec("local-alpha", "Local Alpha", "la"), taskWorkflowRepoSpec("managed-beta", "Managed Beta", "mb"))
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repos["local-alpha"]: {tasks: []taskmodel.Task{
		taskWFTask("la-epic", "Plan release", taskmodel.StatusInProgress, taskmodel.IssueTypeEpic),
		taskWFTask("la-child-z", "Closed task", taskmodel.StatusClosed, taskmodel.IssueTypeTask, taskWFParent("la-epic")),
		taskWFTask("la-child-b", "Nested epic", taskmodel.StatusInProgress, taskmodel.IssueTypeEpic, taskWFParent("la-epic")),
		taskWFTask("la-grandchild", "Must not appear", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFParent("la-child-a")),
	}}, repos["managed-beta"]: {err: errors.New("managed repo should not be queried")}})

	stdout, stderr := fixture.mustExecute("task", "show", "la-epic")

	is.Empty(stderr)
	for _, want := range []string{"Children:", "ID: la-child-b, Status: in_progress, Type: epic, Title: Nested epic", "ID: la-child-z, Status: closed, Type: task, Title: Closed task", "Orpheus metadata:", "History:"} {
		is.Contains(stdout, want)
	}
	is.NotContains(stdout, "la-child-a")
	is.NotContains(stdout, "la-grandchild")
	is.Less(strings.Index(stdout, "la-child-b"), strings.Index(stdout, "la-child-z"))
}

func TestIntegrationWorkflowTaskShowEpicRendersEmptyChildrenState(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask("op-epic", "Empty epic", taskmodel.StatusOpen, taskmodel.IssueTypeEpic)}}})

	stdout, stderr := fixture.mustExecute("task", "show", "op-epic")

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Children:\n  - No direct children.\n")
}

func TestIntegrationWorkflowTaskShowEpicReportsChildQueryFailureWithRepositoryAndParent(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()

	registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	fixture.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		return workflowChildFailureBackend{}, nil
	}

	stdout, stderr, err := fixture.execute("task", "show", "op-epic")

	require.Error(t, err)
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	assert.ErrorContains(t, err, "task show op-epic: query direct children for parent task op-epic in repo alpha")
	assert.ErrorContains(t, err, "backend unavailable")
}

type workflowChildFailureBackend struct{}

func (b workflowChildFailureBackend) Get(_ context.Context, id string) (taskmodel.Task, error) {
	if id == "op-epic" {
		return taskWFTask("op-epic", "Epic", taskmodel.StatusOpen, taskmodel.IssueTypeEpic, taskWFChildCount(1)), nil
	}
	return taskmodel.Task{}, taskmodel.ErrNotFound
}
func (b workflowChildFailureBackend) List(context.Context) ([]taskmodel.Task, error) {
	return nil, errors.New("backend unavailable")
}
func TestIntegrationWorkflowTaskStatsRendersImplementationExecutionUsage(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask("op-1", "Stats", taskmodel.StatusInProgress, taskmodel.IssueTypeTask, taskWFPriority(1))}}})
	stateStore := taskstate.NewStoreWithClock(fixture.paths, clockSequence(workflowTime("2026-07-07T10:00:00Z"), workflowTime("2026-07-07T10:01:00Z"), workflowTime("2026-07-07T10:02:00Z"), workflowTime("2026-07-07T10:03:00Z"), workflowTime("2026-07-07T10:04:00Z"), workflowTime("2026-07-07T10:05:00Z")))
	run, err := stateStore.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "codex-profile", Profile: "codex-profile", Harness: "codex", Model: "gpt-5", Command: "codex", Args: []string{"exec", "--model", "gpt-5"}, Branch: "main", Worktree: repo})
	must.NoError(err)
	_, err = stateStore.RecordRunUsage("alpha", "op-1", run.Attempt, taskstate.RecordRunUsageOptions{Session: &taskstate.AgentSession{ID: "session-123", LogPath: "/fixture/codex.jsonl"}, Usage: &taskstate.AgentUsage{InputTokens: 123, CachedInputTokens: 45, OutputTokens: 67, ReasoningOutputTokens: 8, TotalTokens: 190}, UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: "matched_codex_session", CandidateCount: 1}})
	must.NoError(err)
	_, err = stateStore.FinishRun("alpha", "op-1", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)
	reviewAttempt, err := stateStore.StartReviewWithOptions("alpha", "op-1", taskstate.StartReviewOptions{Pipeline: "standard", Step: "ai-review"})
	must.NoError(err)
	_, err = stateStore.RecordReviewStep("alpha", "op-1", reviewAttempt.Attempt, taskstate.RecordReviewStepOptions{Kind: "agent_review", Name: "ai-review", Execution: &taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, Agent: "reviewer", Profile: "reviewer", Harness: "codex", Model: "gpt-5", Command: "codex", Args: []string{"exec", "--model", "gpt-5", "review"}, StartedAt: workflowTime("2026-07-07T10:03:00Z")}})
	must.NoError(err)
	_, err = stateStore.FinishReviewStepExecution("alpha", "op-1", reviewAttempt.Attempt, "ai-review", taskstate.FinishReviewStepExecutionOptions{Status: taskstate.RunStatusSucceeded, Session: &taskstate.AgentSession{ID: "review-session-123", LogPath: "/fixture/codex-review.jsonl"}, Usage: &taskstate.AgentUsage{InputTokens: 20, CachedInputTokens: 5, OutputTokens: 30, ReasoningOutputTokens: 7, TotalTokens: 50}, UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: "matched_codex_session", CandidateCount: 1}})
	must.NoError(err)
	_, err = stateStore.FinishReview("alpha", "op-1", reviewAttempt.Attempt, taskstate.ReviewStatusPassed)
	must.NoError(err)

	stdout, stderr := fixture.mustExecute("task", "stats", "op-1")

	is.Empty(stderr)
	for _, want := range []string{"Executions", "Estimated cost uses harness-reported estimates", "implementation", "codex-profile", "codex", "gpt-5", `"codex" "exec" "--model" "gpt-5"`, "2026-07-07T10:00:00Z", "2026-07-07T10:02:00Z", "2m0s", "succeeded", "session-123", "total=190 input=123 cached_input=45 output=67 reasoning_output=8", "estimated API-equivalent cost=$0.000773", "review-agent", "ai-review", "review-session-123", "total=50 input=20 cached_input=5 output=30 reasoning_output=7", "combined", "$0.001092"} {
		is.Contains(stdout, want)
	}
}

func TestIntegrationWorkflowTaskStatsRendersSyncConflictResolutionExecutionUsage(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask("op-1", "Stats", taskmodel.StatusInProgress, taskmodel.IssueTypeTask)}}})
	startedAt := workflowTime("2026-07-07T10:00:00Z")
	finishedAt := workflowTime("2026-07-07T10:12:00Z")
	stateStore := taskstate.NewStoreWithClock(fixture.paths, clockSequence(startedAt, finishedAt))
	opts := taskstate.SyncConflictResolutionEventOptions{Execution: taskstate.AgentExecution{Agent: "codex", Profile: "sync-profile", Harness: "codex", Model: "gpt-5", Command: "codex", Args: []string{"exec", "--profile", "sync-profile"}, SessionName: "sync-conflict-op-1", StartedAt: startedAt}, Branch: "orpheus/op-1", DefaultBranch: "main", Worktree: repo, PRURL: "https://github.test/org/repo/pull/42", ConflictFiles: []string{"conflict.txt"}}
	_, err := stateStore.RecordSyncConflictResolutionStarted("alpha", "op-1", opts)
	must.NoError(err)
	finishedOpts := opts
	finishedOpts.Commit = "merge123"
	finishedOpts.Usage = taskstate.RecordRunUsageOptions{Session: &taskstate.AgentSession{ID: "sync-session-123", LogPath: "/fixture/codex-sync.jsonl"}, Usage: &taskstate.AgentUsage{InputTokens: 120, CachedInputTokens: 10, OutputTokens: 30, ReasoningOutputTokens: 5, TotalTokens: 165}, UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: "matched_codex_session", CandidateCount: 1}}
	_, err = stateStore.RecordSyncConflictResolutionFinished("alpha", "op-1", finishedOpts)
	must.NoError(err)

	stdout, stderr := fixture.mustExecute("task", "stats", "op-1")

	is.Empty(stderr)
	for _, want := range []string{"sync-conflict-resolution", "sync-profile", "codex", "gpt-5", `"codex" "exec" "--profile" "sync-profile"`, "2026-07-07T10:00:00Z", "2026-07-07T10:12:00Z", "12m0s", "succeeded", "sync-session-123", "total=165 input=120 cached_input=10 output=30 reasoning_output=5", "estimated API-equivalent cost=$", "Totals"} {
		is.Contains(stdout, want)
	}
	is.Regexp(`(?m)^sync-conflict-resolution\s+1\s+12m0s\s+165\s+120\s+10\s+30\s+5\s+\$[0-9.]+\s+0\s+0$`, stdout)
}

func TestIntegrationWorkflowTaskStatsKeepsTokenUsageWhenCostPricingIsUnknown(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask("op-1", "Stats", taskmodel.StatusInProgress, taskmodel.IssueTypeTask)}}})
	stateStore := taskstate.NewStoreWithClock(fixture.paths, clockSequence(workflowTime("2026-07-07T10:00:00Z"), workflowTime("2026-07-07T10:01:00Z"), workflowTime("2026-07-07T10:02:00Z")))
	run, err := stateStore.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "codex-profile", Profile: "codex-profile", Harness: "codex", Model: "vendor-model", Command: "codex", Args: []string{"exec", "--model", "vendor-model"}, Branch: "main", Worktree: repo})
	must.NoError(err)
	_, err = stateStore.RecordRunUsage("alpha", "op-1", run.Attempt, taskstate.RecordRunUsageOptions{Usage: &taskstate.AgentUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}, UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: "matched_codex_session"}})
	must.NoError(err)
	_, err = stateStore.FinishRun("alpha", "op-1", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)
	stdout, stderr := fixture.mustExecute("task", "stats", "op-1")
	is.Empty(stderr)
	for _, want := range []string{"total=150 input=100 cached_input=0 output=50 reasoning_output=0", "unknown: no public pricing metadata for model vendor-model", "implementation", "1", "2m0s", "150", "$0.000000"} {
		is.Contains(stdout, want)
	}
}

func TestIntegrationWorkflowTaskStatsUsesPiReportedEstimatedCost(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask("op-1", "Stats", taskmodel.StatusInProgress, taskmodel.IssueTypeTask)}}})
	stateStore := taskstate.NewStoreWithClock(fixture.paths, clockSequence(workflowTime("2026-07-07T10:00:00Z"), workflowTime("2026-07-07T10:01:00Z"), workflowTime("2026-07-07T10:02:00Z")))
	run, err := stateStore.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "pi-profile", Profile: "pi-profile", Harness: "pi", Model: "openai-codex/gpt-5.5", Command: "pi", Args: []string{"--model", "openai-codex/gpt-5.5"}, Branch: "main", Worktree: repo})
	must.NoError(err)
	_, err = stateStore.RecordRunUsage("alpha", "op-1", run.Attempt, taskstate.RecordRunUsageOptions{Session: &taskstate.AgentSession{ID: "pi-session", LogPath: "/fixture/pi.jsonl"}, Usage: &taskstate.AgentUsage{InputTokens: 100, CachedInputTokens: 20, OutputTokens: 30, ReasoningOutputTokens: 5, TotalTokens: 130}, UsageCost: &taskstate.AgentUsageCost{Kind: agent.UsageCostKindPiReportedEstimated, Currency: "USD", AmountMicroUSD: 1240, Source: "Pi usage.cost.total", Notes: "Pi-reported estimate only; not exact billed cost or invoice reconciliation."}, UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: "matched_pi_session"}})
	must.NoError(err)
	_, err = stateStore.FinishRun("alpha", "op-1", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)
	stdout, stderr := fixture.mustExecute("task", "stats", "op-1")
	is.Empty(stderr)
	for _, want := range []string{"Estimated cost uses harness-reported estimates", "pi-profile", "pi", "openai-codex/gpt-5.5", "pi-session", "total=130 input=100 cached_input=20 output=30 reasoning_output=5", "Pi-reported estimated cost=$0.001240", "kind=pi_reported_estimated", "source=Pi usage.cost.total"} {
		is.Contains(stdout, want)
	}
}

func TestIntegrationWorkflowTaskStatsCountsMissingPiUsageCostAsUnknown(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask("op-1", "Stats", taskmodel.StatusInProgress, taskmodel.IssueTypeTask)}}})
	stateStore := taskstate.NewStoreWithClock(fixture.paths, clockSequence(workflowTime("2026-07-07T10:00:00Z"), workflowTime("2026-07-07T10:01:00Z"), workflowTime("2026-07-07T10:02:00Z")))
	run, err := stateStore.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "pi-profile", Profile: "pi-profile", Harness: "pi", Model: "openai-codex/gpt-5.5", Command: "pi", Args: []string{"--model", "openai-codex/gpt-5.5"}, Branch: "main", Worktree: repo})
	must.NoError(err)
	_, err = stateStore.RecordRunUsage("alpha", "op-1", run.Attempt, taskstate.RecordRunUsageOptions{UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureUnknown, Reason: "matching_pi_session_has_no_assistant_usage", CandidateCount: 1}})
	must.NoError(err)
	_, err = stateStore.FinishRun("alpha", "op-1", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)
	stdout, stderr := fixture.mustExecute("task", "stats", "op-1")
	is.Empty(stderr)
	for _, want := range []string{"pi-profile", "pi", "openai-codex/gpt-5.5", "unknown: matching_pi_session_has_no_assistant_usage (candidates=1)", "$0.000000"} {
		is.Contains(stdout, want)
	}
}

func TestIntegrationWorkflowTaskStatsAggregateGroupsResolvedTasksByDay(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask("op-1", "First", taskmodel.StatusClosed, taskmodel.IssueTypeTask, taskWFCreated("2026-07-01T00:00:00Z"), taskWFClosed("2026-07-02T12:00:00Z")), taskWFTask("op-2", "Second", taskmodel.StatusClosed, taskmodel.IssueTypeTask, taskWFCreated("2026-07-02T08:00:00Z"), taskWFClosed("2026-07-02T18:00:00Z")), taskWFTask("op-3", "Unknown usage", taskmodel.StatusClosed, taskmodel.IssueTypeTask, taskWFClosed("2026-07-03T11:00:00Z")), taskWFTask("op-open", "Still open", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFCreated("2026-07-03T12:00:00Z"))}}})
	now := time.Time{}
	stateStore := taskstate.NewStoreWithClock(fixture.paths, func() time.Time { return now })
	recordWorkflowTaskStatsAggregateRun(t, stateStore, &now, repo, taskStatsWorkflowRunFixture{taskID: "op-1", model: "gpt-5", startedAt: workflowTime("2026-07-02T10:00:00Z"), finishedAt: workflowTime("2026-07-02T10:30:00Z"), usage: &taskstate.AgentUsage{InputTokens: 123, CachedInputTokens: 45, OutputTokens: 67, ReasoningOutputTokens: 8, TotalTokens: 1900}})
	recordWorkflowTaskStatsAggregateRun(t, stateStore, &now, repo, taskStatsWorkflowRunFixture{taskID: "op-2", model: "vendor-model", startedAt: workflowTime("2026-07-02T16:00:00Z"), finishedAt: workflowTime("2026-07-02T16:20:00Z"), usage: &taskstate.AgentUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 1150}})
	recordWorkflowTaskStatsAggregateRun(t, stateStore, &now, repo, taskStatsWorkflowRunFixture{taskID: "op-3", model: "gpt-5", startedAt: workflowTime("2026-07-03T10:00:00Z"), finishedAt: workflowTime("2026-07-03T10:15:00Z")})
	stdout, stderr := fixture.mustExecute("task", "stats", "--group", "day")
	is.Empty(stderr)
	is.Contains(stdout, "Task stats throughput view grouped by day")
	is.Contains(stdout, "Tasks without resolved timestamp: 1")
	is.Regexp(`(?m)^2026-07-02\s+2\s+2h0m0s\s+2h0m0s\s+2/2$`, stdout)
	is.Regexp(`(?m)^2026-07-03\s+1\s+1h0m0s\s+1h0m0s\s+1/1$`, stdout)
	stdout, stderr = fixture.mustExecute("task", "stats", "--group", "day", "--view", "consumption", "--from", "2026-07-02", "--to", "2026-07-02", "--repo", "alpha")
	is.Empty(stderr)
	is.Contains(stdout, "Task stats consumption view grouped by day")
	is.Contains(stdout, "Filters: from=2026-07-02 to=2026-07-02 repo=alpha")
	is.Regexp(`(?m)^2026-07-02\s+2\s+2\s+3K\s+1\.5K\s+2/2\s+\$0\.000773\s+\$0\.000773\s+1/2$`, stdout)
}

func TestIntegrationWorkflowTaskStatsAggregateGroupsResolvedTasksByMonth(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask("op-1", "July", taskmodel.StatusClosed, taskmodel.IssueTypeTask, taskWFClosed("2026-07-02T12:00:00Z")), taskWFTask("op-2", "August", taskmodel.StatusClosed, taskmodel.IssueTypeTask, taskWFClosed("2026-08-03T00:00:00Z"))}}})
	stdout, stderr := fixture.mustExecute("task", "stats", "--group", "month")
	is.Empty(stderr)
	is.Contains(stdout, "Task stats throughput view grouped by month")
	is.Contains(stdout, "Tasks without resolved timestamp: 0")
	is.Regexp(`(?m)^2026-07\s+1\s+-\s+-\s+0/1$`, stdout)
	is.Regexp(`(?m)^2026-08\s+1\s+-\s+-\s+0/1$`, stdout)
}

func TestIntegrationWorkflowTaskStatsAggregateRepoFilterSkipsUnselectedRepoFailures(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)

	repos := saveTaskWorkflowRepos(t, fixture, taskWorkflowRepoSpec("alpha", "Alpha", "op"), taskWorkflowRepoSpec("beta", "Beta", "bt"))
	reads := withWorkflowSources(t, fixture, map[string]taskSourceResult{repos["alpha"]: {tasks: []taskmodel.Task{taskWFTask("op-1", "Selected", taskmodel.StatusClosed, taskmodel.IssueTypeTask, taskWFClosed("2026-07-02T12:00:00Z"))}}, repos["beta"]: {err: errors.New("bd exploded")}})
	stdout, stderr := fixture.mustExecute("task", "stats", "--group", "day", "--repo", "alpha")
	is.Empty(stderr)
	is.Contains(stdout, "Task stats throughput view grouped by day")
	is.Contains(stdout, "Filters: repo=alpha")
	is.Regexp(`(?m)^2026-07-02\s+1\s+-\s+-\s+0/1$`, stdout)
	is.NotContains(reads.directories(), repos["beta"])
}

func TestIntegrationWorkflowTaskStatsAggregateReceivesOnlyTaskSourceItems(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask("op-task", "Task", taskmodel.StatusClosed, taskmodel.IssueTypeTask, taskWFClosed("2026-07-02T12:00:00Z")), taskWFTask("op-epic-closed", "Closed epic", taskmodel.StatusClosed, taskmodel.IssueTypeEpic, taskWFClosed("2026-07-02T12:00:00Z")), taskWFTask("op-epic-open", "Open epic", taskmodel.StatusOpen, taskmodel.IssueTypeEpic)}}})
	writeMismatchedTaskState(t, fixture, "alpha", "op-epic-closed")
	stdout, stderr := fixture.mustExecute("task", "stats", "--group", "day")
	is.Empty(stderr)
	is.Contains(stdout, "Task stats throughput view grouped by day")
	is.Regexp(`(?m)^2026-07-02\s+1\s+-\s+-\s+0/1$`, stdout)
}

func TestIntegrationWorkflowTaskStatsDirectEpicStatsRemainAvailable(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask("op-epic", "Closed epic", taskmodel.StatusClosed, taskmodel.IssueTypeEpic, taskWFClosed("2026-07-02T12:00:00Z"))}}})
	now := time.Time{}
	stateStore := taskstate.NewStoreWithClock(fixture.paths, func() time.Time { return now })
	recordWorkflowTaskStatsAggregateRun(t, stateStore, &now, repo, taskStatsWorkflowRunFixture{taskID: "op-epic", model: "gpt-5", startedAt: workflowTime("2026-07-02T10:00:00Z"), finishedAt: workflowTime("2026-07-02T10:05:00Z"), usage: &taskstate.AgentUsage{InputTokens: 100, OutputTokens: 50, TotalTokens: 150}})
	stdout, stderr := fixture.mustExecute("task", "stats", "op-epic")
	is.Empty(stderr)
	is.Contains(stdout, "Executions")
	is.Contains(stdout, "implementation")
	is.Contains(stdout, "gpt-5")
	is.Contains(stdout, "5m0s")
	is.Contains(stdout, "150")
}

func TestIntegrationWorkflowTaskShowRendersClosedItemsAndHistory(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask("op-closed", "done", taskmodel.StatusClosed, taskmodel.IssueTypeTask, taskWFPriority(2))}}})
	stateStore := taskstate.NewStoreWithClock(fixture.paths, func() time.Time { return workflowTime("2026-01-02T03:04:05Z") })
	_, err := stateStore.RecordTaskClosed("alpha", "op-closed", taskstate.TaskClosedOptions{Reason: taskstate.CloseReasonPRMerged, PRURL: "https://github.test/org/alpha/pull/42", ObservedPRState: "merged"})
	must.NoError(err)
	stdout, stderr := fixture.mustExecute("task", "show", "op-closed")
	is.Empty(stderr)
	is.Contains(stdout, "ID: op-closed")
	is.Contains(stdout, "Status: closed")
	is.Contains(stdout, "History:\n  2026-01-02T03:04:05Z Task closed\n")
}

func TestIntegrationWorkflowTaskShowRendersChronologicalHistoryForClosedEpic(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask("op-epic", "Closed epic", taskmodel.StatusClosed, taskmodel.IssueTypeEpic, taskWFPriority(1))}}})
	now := workflowTime("2026-01-02T03:04:05Z")
	stateStore := taskstate.NewStoreWithClock(fixture.paths, func() time.Time { return now })
	_, err := stateStore.RecordSetupEvent("alpha", "op-epic", taskstate.EventWorktreeCreated, taskstate.SetupEventOptions{})
	must.NoError(err)
	now = now.Add(time.Minute)
	taskState, err := stateStore.Load("alpha", "op-epic")
	must.NoError(err)
	taskState.Events = append(taskState.Events, taskstate.Event{Type: taskstate.EventWorktreeReused, At: now})
	must.NoError(fixture.paths.WriteDataYAML(filepath.Join("repos", "alpha", "tasks", "op-epic.yaml"), taskState))
	now = now.Add(time.Minute)
	run, err := stateStore.StartRun("alpha", "op-epic", taskstate.StartRunOptions{Agent: "codex"})
	must.NoError(err)
	now = now.Add(time.Minute)
	_, err = stateStore.CompleteRun("alpha", "op-epic", run.Attempt, taskstate.CompleteRunOptions{Summary: "Record task history", Description: "Recorded completion history.", DetailedDescription: "Detailed history.", TechnicalExplanation: "Technical explanation."})
	must.NoError(err)
	now = now.Add(time.Minute)
	_, err = stateStore.FinishRun("alpha", "op-epic", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)
	now = now.Add(time.Minute)
	_, err = stateStore.RecordFinalizationCommit("alpha", "op-epic", "abc123")
	must.NoError(err)
	now = now.Add(time.Minute)
	_, err = stateStore.RecordFinalizationPush("alpha", "op-epic", taskstate.FinalizationPushOptions{Branch: "main", PushTarget: taskstate.PushTargetMain})
	must.NoError(err)
	now = now.Add(time.Minute)
	_, err = stateStore.RecordFinalizationClose("alpha", "op-epic", taskstate.FinalizationCloseOptions{Reason: taskstate.CloseReasonDefaultBranchPublished})
	must.NoError(err)
	stdout, stderr := fixture.mustExecute("task", "show", "op-epic")
	is.Empty(stderr)
	expected := []string{"2026-01-02T03:04:05Z Worktree created", "2026-01-02T03:06:05Z Run started", "2026-01-02T03:07:05Z Completion recorded", "2026-01-02T03:08:05Z Run finished", "2026-01-02T03:10:05Z Pushed main", "2026-01-02T03:11:05Z Task closed"}
	prev := strings.Index(stdout, "History:")
	for _, want := range expected {
		idx := strings.Index(stdout, want)
		is.Greater(idx, prev, want)
		prev = idx
	}
	is.NotContains(stdout, "Worktree reused")
	is.NotContains(stdout, "codex")
	is.NotContains(stdout, "succeeded")
}

func TestIntegrationWorkflowTaskShowProjectsReviewAttemptMilestonesIntoHistory(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask("op-review", "Review history", taskmodel.StatusOpen, taskmodel.IssueTypeTask)}}})
	now := workflowTime("2026-01-02T03:04:05Z")
	stateStore := taskstate.NewStoreWithClock(fixture.paths, func() time.Time { return now })
	_, err := stateStore.RecordSetupEvent("alpha", "op-review", taskstate.EventWorktreeCreated, taskstate.SetupEventOptions{})
	must.NoError(err)
	now = now.Add(time.Minute)
	recordTaskShowReviewAttempt(t, stateStore, &now, taskstate.ReviewStatusPassed)
	_, err = stateStore.StartRun("alpha", "op-review", taskstate.StartRunOptions{Agent: "codex"})
	must.NoError(err)
	now = now.Add(time.Minute)
	recordTaskShowReviewAttempt(t, stateStore, &now, taskstate.ReviewStatusBlocked)
	recordTaskShowReviewAttempt(t, stateStore, &now, taskstate.ReviewStatusFailed)
	recordTaskShowReviewAttempt(t, stateStore, &now, taskstate.ReviewStatusAborted)
	stdout, stderr := fixture.mustExecute("task", "show", "op-review")
	is.Empty(stderr)
	previous := strings.Index(stdout, "History:")
	for _, want := range []string{"2026-01-02T03:04:05Z Worktree created", "2026-01-02T03:05:05Z Review attempt 1 started", "2026-01-02T03:06:05Z Review attempt 1 passed", "2026-01-02T03:07:05Z Run started", "2026-01-02T03:08:05Z Review attempt 2 started", "2026-01-02T03:09:05Z Review attempt 2 blocked", "2026-01-02T03:10:05Z Review attempt 3 started", "2026-01-02T03:11:05Z Review attempt 3 failed", "2026-01-02T03:12:05Z Review attempt 4 started", "2026-01-02T03:13:05Z Review attempt 4 aborted"} {
		idx := strings.Index(stdout, want)
		is.Greater(idx, previous, want)
		previous = idx
	}
}

func TestIntegrationWorkflowTaskShowProjectsReviewFollowUpCreationIntoHistory(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask("op-review", "Review history", taskmodel.StatusOpen, taskmodel.IssueTypeTask)}}})
	now := workflowTime("2026-01-02T03:04:05Z")
	stateStore := taskstate.NewStoreWithClock(fixture.paths, func() time.Time { return now })
	review, err := stateStore.StartReviewWithOptions("alpha", "op-review", taskstate.StartReviewOptions{Pipeline: "local", Step: "manual"})
	must.NoError(err)
	_, err = stateStore.RecordReviewFinding("alpha", "op-review", review.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeSeparateTask, Title: "Extract helper", Description: "Track separately.", TaskProposal: taskstate.ReviewTaskProposal{Title: "Extract helper", Description: "Extract helper separately.", AcceptanceCriteria: "Helper extraction has tests."}})
	must.NoError(err)
	now = now.Add(time.Minute)
	_, err = stateStore.RecordReviewFindingCreatedTask("alpha", "op-review", review.Attempt, 0, "op-42")
	must.NoError(err)
	taskState, err := stateStore.Load("alpha", "op-review")
	must.NoError(err)
	taskState.Reviews[0].Findings = append(taskState.Reviews[0].Findings, taskstate.ReviewFinding{Type: taskstate.FindingTypeSeparateTask, Title: "Legacy follow-up", Description: "Created before timestamps were recorded.", CreatedTaskID: "op-legacy", TaskProposal: taskstate.ReviewTaskProposal{Title: "Legacy follow-up", Description: "Created before timestamps were recorded.", AcceptanceCriteria: "Legacy task exists."}})
	must.NoError(fixture.paths.WriteDataYAML(filepath.Join("repos", "alpha", "tasks", "op-review.yaml"), taskState))
	stdout, stderr := fixture.mustExecute("task", "show", "op-review")
	is.Empty(stderr)
	is.Contains(stdout, "2026-01-02T03:04:05Z Review attempt 1 started")
	is.Contains(stdout, "2026-01-02T03:05:05Z Review attempt 1 finding 1 created follow-up task op-42")
	is.NotContains(stdout, "op-legacy")
}

func TestIntegrationWorkflowTaskShowFailsWhenLocalTaskStateCannotBeLoaded(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask("op-corrupt", "Corrupt state", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(1))}}})
	writeMismatchedTaskState(t, fixture, "alpha", "op-corrupt")
	stdout, stderr, err := fixture.execute("task", "show", "op-corrupt")
	require.Error(t, err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "task show op-corrupt: load local task-state for repo alpha")
	is.ErrorContains(err, `repo_id is "wrong", expected "alpha"`)
}

func TestIntegrationWorkflowTaskShowRejectsUnsupportedItemsAtTaskSourceBoundary(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {getErrors: map[string]error{"op-bug": unsupportedTaskSourceError(taskmodel.IssueTypeBug)}}})
	stdout, stderr, err := fixture.execute("task", "show", "op-bug")
	is.Empty(stdout)
	is.Empty(stderr)
	require.Error(t, err)
	is.ErrorContains(err, "unsupported task source item")
	is.ErrorContains(err, "issue type \"bug\" is not task or epic")
}

func TestIntegrationWorkflowTaskDirPrintsWorktreeDirectory(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)

	repos := saveTaskWorkflowRepos(t, fixture, taskWorkflowRepoSpec("alpha", "Alpha", "op"), taskWorkflowRepoSpec("beta", "Beta", "bt"))
	worktreeDir := "/fixture/worktrees/op-1"
	reads := withWorkflowSources(t, fixture, map[string]taskSourceResult{repos["alpha"]: {tasks: []taskmodel.Task{taskWFTask("op-1", "Worktree task", taskmodel.StatusInProgress, taskmodel.IssueTypeTask, taskWFMetadata(map[string]string{taskmodel.MetadataBranch: "orpheus/op-1", taskmodel.MetadataWorktree: worktreeDir}))}}, repos["beta"]: {err: errors.New("other repo should not be queried")}})
	stdout, stderr := fixture.mustExecute("task", "dir", "op-1")
	is.Empty(stderr)
	is.Equal(worktreeDir+"\n", stdout)
	is.NotContains(reads.directories(), repos["beta"])
}

func TestIntegrationWorkflowTaskDirPrintsRepoRootForMainTask(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)

	repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask("op-main", "Main task", taskmodel.StatusInProgress, taskmodel.IssueTypeTask, taskWFMetadata(map[string]string{taskmodel.MetadataBranch: "main", taskmodel.MetadataWorktree: filepath.Join(repo, ".")}))}}})
	stdout, stderr := fixture.mustExecute("task", "dir", "op-main")
	is.Empty(stderr)
	is.Equal(filepath.Clean(repo)+"\n", stdout)
}

func TestIntegrationWorkflowTaskDirReportsMissingAndInconsistentMetadata(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, taskID string
		metadata     map[string]string
		want         string
	}{{"missing worktree", "op-missing", map[string]string{}, "task has no Orpheus working directory metadata"}, {"missing branch", "op-incomplete", map[string]string{taskmodel.MetadataWorktree: "/fixture/op-incomplete"}, "orpheus.branch is missing"}, {"inconsistent target", "op-inconsistent", map[string]string{taskmodel.MetadataBranch: "main", taskmodel.MetadataWorktree: "/fixture/op-inconsistent"}, "task Orpheus target metadata is inconsistent"}} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCommandWorkflow(t)
			is := assert.New(t)

			repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
			withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{taskWFTask(tc.taskID, "Task dir metadata case", taskmodel.StatusInProgress, taskmodel.IssueTypeTask, taskWFMetadata(tc.metadata))}}})
			stdout, stderr, err := fixture.execute("task", "dir", tc.taskID)
			require.Error(t, err)
			is.Empty(stdout)
			is.Empty(stderr)
			is.ErrorContains(err, "task dir "+tc.taskID)
			is.ErrorContains(err, tc.want)
		})
	}
}

func TestIntegrationWorkflowTaskShowReviewDisplaysCrossAttemptFindingHistory(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	store, repoPath := setupTaskShowReviewWorkflowRepo(t, fixture, "op-main")
	seedTaskShowReviewState(t, fixture.paths, repoPath)
	_ = store
	stdout, stderr := fixture.mustExecute("task", "show", "review", "op-main")
	is.Empty(stderr)
	for _, want := range []string{"Review state for op-main (repo alpha)", "Authoritative review history:", "Attempt 1: passed (1 authoritative finding(s))", "1/1 · manual · separate_task · created task op-41 · Older cleanup", "Attempt 2: blocked (4 authoritative finding(s))", "2/1 · unit-tests · blocking · open · Tests fail", "2/2 · ai-review · blocking · follow-up run 1 running · Race condition", "2/3 · ai-review · blocking · waived · Known limitation", "2/4 · ai-review · separate_task · created task op-42 · Extract helper", "orpheus task show review <task-id> <review-attempt> <finding-number>", "Next step: run `orpheus task run op-main` to address open blocking findings"} {
		is.Contains(stdout, want)
	}
}

func TestIntegrationWorkflowTaskShowReviewGuidesRetryAfterFailedFollowUp(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	runStore, repoPath := setupTaskShowReviewWorkflowRepo(t, fixture, "op-retry")
	review, err := runStore.StartReview("alpha", "op-retry")
	must.NoError(err)
	_, err = runStore.RecordReviewFinding("alpha", "op-retry", review.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Title: "Retry me", Description: "The first fix failed."})
	must.NoError(err)
	_, err = runStore.FinishReview("alpha", "op-retry", review.Attempt, taskstate.ReviewStatusBlocked)
	must.NoError(err)
	failed, err := runStore.StartRun("alpha", "op-retry", taskstate.StartRunOptions{Agent: "implementer", Branch: "main", Worktree: repoPath, ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: review.Attempt, FindingIndexes: []int{0}}})
	must.NoError(err)
	_, err = runStore.TargetReviewFindings("alpha", "op-retry", review.Attempt, []int{0}, failed.Attempt)
	must.NoError(err)
	_, err = runStore.FinishRun("alpha", "op-retry", failed.Attempt, taskstate.RunStatusFailed)
	must.NoError(err)
	stdout, stderr := fixture.mustExecute("task", "show", "review", "op-retry")
	is.Empty(stderr)
	is.Contains(stdout, "follow-up run 1 failed · Retry me")
	is.Contains(stdout, "Next step: retry `orpheus task run op-retry`")
}

func TestIntegrationWorkflowTaskShowReviewGuidesWhenTaskHasNoReviewAttempts(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	setupTaskShowReviewWorkflowRepo(t, fixture, "op-empty")
	stdout, stderr := fixture.mustExecute("task", "show", "review", "op-empty")
	is.Empty(stderr)
	is.Contains(stdout, "Review state for op-empty (repo alpha)")
	is.Contains(stdout, "No review attempts recorded for op-empty.")
	is.Contains(stdout, "Next step: run `orpheus task run op-empty` after task work is ready.")
}

func TestIntegrationWorkflowTaskShowReviewRendersManuallyAddressedFinding(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	runStore, _ := setupTaskShowReviewWorkflowRepo(t, fixture, "op-addressed")
	reviewAttempt, err := runStore.StartReviewWithOptions("alpha", "op-addressed", taskstate.StartReviewOptions{Pipeline: "default", Step: "inspect"})
	must.NoError(err)
	_, err = runStore.RecordReviewFinding("alpha", "op-addressed", reviewAttempt.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Title: "Direct repair", Description: "Fixed outside the review loop.", Step: "inspect"})
	must.NoError(err)
	_, err = runStore.FinishReview("alpha", "op-addressed", reviewAttempt.Attempt, taskstate.ReviewStatusBlocked)
	must.NoError(err)
	_, err = runStore.AddressReviewBlockingFindingManually("alpha", "op-addressed", reviewAttempt.Attempt, 0, "Verified in the worktree.")
	must.NoError(err)
	stdout, stderr := fixture.mustExecute("task", "show", "review", "op-addressed", "1", "1")
	is.Empty(stderr)
	is.Contains(stdout, "Authoritative finding 1/1:")
	is.Contains(stdout, "Disposition: addressed manually: Verified in the worktree.")
}

func TestIntegrationWorkflowTaskShowReviewGuidesPausedAutomatedBlockerDecision(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	runStore, _ := setupTaskShowReviewWorkflowRepo(t, fixture, "op-paused")
	reviewAttempt, err := runStore.StartReviewWithOptions("alpha", "op-paused", taskstate.StartReviewOptions{Pipeline: "quality", Step: "unit"})
	must.NoError(err)
	_, err = runStore.RecordReviewStep("alpha", "op-paused", reviewAttempt.Attempt, taskstate.RecordReviewStepOptions{Kind: taskstate.ReviewStepKindCheck, Name: "unit"})
	must.NoError(err)
	_, err = runStore.RecordReviewFinding("alpha", "op-paused", reviewAttempt.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Title: "Check failed", Description: "make test failed.", Step: "unit"})
	must.NoError(err)
	_, err = runStore.PauseReviewForAutomatedBlockerDecision("alpha", "op-paused", reviewAttempt.Attempt, "unit")
	must.NoError(err)
	stdout, stderr := fixture.mustExecute("task", "show", "review", "op-paused", "1")
	is.Empty(stderr)
	is.Contains(stdout, "Automated blocker decisions: paused")
	is.Contains(stdout, "Next step: automated blocker decision is paused; run `orpheus task run op-paused` to resume step unit.")
}

func TestIntegrationWorkflowTaskShowReviewGuidesInterruptedAutomatedBlockerDecision(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	runStore, _ := setupTaskShowReviewWorkflowRepo(t, fixture, "op-interrupted")
	reviewAttempt, err := runStore.StartReviewWithOptions("alpha", "op-interrupted", taskstate.StartReviewOptions{Pipeline: "quality", Step: "unit"})
	must.NoError(err)
	_, err = runStore.RecordReviewFinding("alpha", "op-interrupted", reviewAttempt.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Title: "Check failed", Description: "make test failed.", Step: "unit"})
	must.NoError(err)
	_, err = runStore.MarkReviewAutomatedBlockerDecisionInterrupted("alpha", "op-interrupted", reviewAttempt.Attempt)
	must.NoError(err)
	_, err = runStore.FinishReview("alpha", "op-interrupted", reviewAttempt.Attempt, taskstate.ReviewStatusBlocked)
	must.NoError(err)
	stdout, stderr := fixture.mustExecute("task", "show", "review", "op-interrupted", "1")
	is.Empty(stderr)
	is.Contains(stdout, "Automated blocker decisions: interrupted")
	is.Contains(stdout, "Next step: automated blocker decisions were interrupted; run `orpheus task run op-interrupted` to start a fresh review.")
}

func TestIntegrationWorkflowTaskShowReviewDisplaysClosedTaskReviewState(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	_, repoPath := setupTaskShowReviewWorkflowRepoWithStatus(t, fixture, "op-main", taskmodel.StatusClosed)
	seedTaskShowReviewState(t, fixture.paths, repoPath)
	stdout, stderr := fixture.mustExecute("task", "show", "review", "op-main")
	is.Empty(stderr)
	is.Contains(stdout, "Review state for op-main (repo alpha)")
	is.Contains(stdout, "Authoritative review history:")
	is.Contains(stdout, "Attempt 2: blocked")
	is.Contains(stdout, "2/1 · unit-tests · blocking · open · Tests fail")
	is.Contains(stdout, "2/4 · ai-review · separate_task · created task op-42 · Extract helper")
}
