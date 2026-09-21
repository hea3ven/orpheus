//go:build integration

package cli_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowStatusGroupsLocalTaskSnapshots(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	paths, repoDir, reads := setupWorkflowStatusGroupsLocalTaskSnapshots(t, fixture)
	_ = paths

	stdout, stderr := fixture.mustExecute("status")

	is.Empty(stderr)
	for _, want := range []string{
		"TASK_ID", "STATUS", "Ready", "Alpha Repo", "ar-ready", "Ready task", "ar-dep", "Open dependency",
		"Needs attention", "ar-failed", "Failed attached agent", "run attempt 1 failed",
		"Working", "ar-running", "Running attached agent", "run attempt 1 is running",
		"Idle", "ar-idle", "Idle without run", "no attached run recorded",
		"ar-succeeded", "Succeeded attached agent", "agent exited without completion",
		"Reviewing", "ar-review", "Review task", "https://example.test/pr/3",
		"ar-missing", "Needs inspection", "missing dependency ar-bug",
	} {
		is.Contains(stdout, want)
	}
	for _, hidden := range []string{"Blocked", "ar-blocked", "Done / closed", "ar-closed"} {
		is.NotContains(stdout, hidden)
	}
	assertStatusGroupOrder(t, stdout, []string{"Needs attention", "Reviewing", "Working", "Idle", "Ready"})

	fullStdout, fullStderr := fixture.mustExecute("status", "--full")
	is.Empty(fullStderr)
	assertFullStatusGroupOutput(t, fullStdout)

	jsonStdout, jsonStderr := fixture.mustExecute("status", "--json")
	is.Empty(jsonStderr)
	var jsonEntries []taskViewJSONTaskEntry
	must.NoError(json.Unmarshal([]byte(jsonStdout), &jsonEntries))
	jsonEntriesByID := make(map[string]taskViewJSONTaskEntry, len(jsonEntries))
	for _, entry := range jsonEntries {
		jsonEntriesByID[entry.ID] = entry
	}
	for _, taskID := range []string{"ar-ready", "ar-dep", "ar-idle", "ar-running", "ar-failed", "ar-succeeded", "ar-review", "ar-missing"} {
		_, ok := jsonEntriesByID[taskID]
		is.True(ok, "JSON output must contain visible task %s", taskID)
	}
	for _, hiddenID := range []string{"ar-blocked", "ar-closed"} {
		_, ok := jsonEntriesByID[hiddenID]
		is.False(ok, "JSON output must hide default status task %s", hiddenID)
	}
	is.Equal("ready", jsonEntriesByID["ar-ready"].Status)
	is.Equal("task", jsonEntriesByID["ar-ready"].Kind)
	is.Empty(jsonEntriesByID["ar-ready"].EpicProgress)

	fullJSONStdout, fullJSONStderr := fixture.mustExecute("status", "--full", "--json")
	is.Empty(fullJSONStderr)
	var fullJSONEntries []taskViewJSONTaskEntry
	must.NoError(json.Unmarshal([]byte(fullJSONStdout), &fullJSONEntries))
	fullJSONEntriesByID := make(map[string]taskViewJSONTaskEntry, len(fullJSONEntries))
	for _, entry := range fullJSONEntries {
		fullJSONEntriesByID[entry.ID] = entry
	}
	_, hasBlocked := fullJSONEntriesByID["ar-blocked"]
	_, hasClosed := fullJSONEntriesByID["ar-closed"]
	is.True(hasBlocked)
	is.True(hasClosed)
	is.GreaterOrEqual(reads.count("list"), 4)
	is.Contains(reads.directories(), repoDir)
}

func setupWorkflowStatusGroupsLocalTaskSnapshots(t *testing.T, fixture *workflowFixture) (taskstate.Store, string, *taskSourceReads) {
	t.Helper()

	repos := saveTaskWorkflowRepos(t, fixture, taskWorkflowRepoSpec("alpha", "Alpha Repo", "ar"))
	repoDir := repos["alpha"]
	stateStore := taskstate.NewStore(fixture.paths)
	_, err := stateStore.StartRun("alpha", "ar-running", taskstate.StartRunOptions{Agent: "recorder"})
	require.NoError(t, err)
	failedRun, err := stateStore.StartRun("alpha", "ar-failed", taskstate.StartRunOptions{Agent: "recorder"})
	require.NoError(t, err)
	_, err = stateStore.FinishRun("alpha", "ar-failed", failedRun.Attempt, taskstate.RunStatusFailed)
	require.NoError(t, err)
	succeededRun, err := stateStore.StartRun("alpha", "ar-succeeded", taskstate.StartRunOptions{Agent: "recorder"})
	require.NoError(t, err)
	_, err = stateStore.FinishRun("alpha", "ar-succeeded", succeededRun.Attempt, taskstate.RunStatusSucceeded)
	require.NoError(t, err)

	reads := withWorkflowSources(t, fixture, map[string]taskSourceResult{repoDir: {tasks: workflowStatusTasks(repoDir)}})
	return stateStore, repoDir, reads
}

func workflowStatusTasks(_ string) []taskmodel.Task {
	return []taskmodel.Task{
		taskWFTask("ar-ready", "Ready task", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(1)),
		taskWFTask("ar-dep", "Open dependency", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(1)),
		taskWFTask("ar-idle", "Idle without run", taskmodel.StatusInProgress, taskmodel.IssueTypeTask, taskWFPriority(4)),
		taskWFTask("ar-running", "Running attached agent", taskmodel.StatusInProgress, taskmodel.IssueTypeTask, taskWFPriority(2)),
		taskWFTask("ar-failed", "Failed attached agent", taskmodel.StatusInProgress, taskmodel.IssueTypeTask, taskWFPriority(2)),
		taskWFTask("ar-succeeded", "Succeeded attached agent", taskmodel.StatusInProgress, taskmodel.IssueTypeTask, taskWFPriority(3)),
		taskWFTask("ar-blocked", "Blocked task", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(2), taskWFBlocks("ar-dep")),
		taskWFTask("ar-review", "Review task", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(3), taskWFMetadata(map[string]string{taskmodel.MetadataPRURL: "https://example.test/pr/3"})),
		taskWFTask("ar-missing", "Needs inspection", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(4), taskWFBlocks("ar-bug")),
		taskWFTask("ar-closed", "Closed task", taskmodel.StatusClosed, taskmodel.IssueTypeTask, taskWFPriority(1)),
	}
}

func assertFullStatusGroupOutput(t *testing.T, fullStdout string) {
	t.Helper()
	is := assert.New(t)
	for _, want := range []string{"Blocked", "ar-blocked", "Blocked task", "blocked by ar-dep", "Done / closed", "ar-closed", "Closed task"} {
		is.Contains(fullStdout, want)
	}
	is.Contains(fullStdout, "STATUS")
	assertStatusGroupOrder(t, fullStdout, []string{"Needs attention", "Reviewing", "Working", "Idle", "Ready", "Blocked", "Done / closed"})
	header := strings.SplitN(fullStdout, "\n", 2)[0]
	is.Less(strings.Index(header, "TITLE"), strings.Index(header, "DETAIL"))
}

func TestIntegrationWorkflowStatusFullIgnoresCorruptClosedAndPullRequestStates(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)

	repos := saveTaskWorkflowRepos(t, fixture, taskWorkflowRepoSpec("alpha", "Alpha Repo", "ar"))
	writeMismatchedTaskState(t, fixture, "alpha", "ar-closed")
	writeMismatchedTaskState(t, fixture, "alpha", "ar-pr")
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repos["alpha"]: {tasks: []taskmodel.Task{
		taskWFTask("ar-closed", "Closed task", taskmodel.StatusClosed, taskmodel.IssueTypeTask),
		taskWFTask("ar-pr", "PR task", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFMetadata(map[string]string{taskmodel.MetadataPRURL: "https://example.test/pr/1"})),
	}}})

	stdout, stderr := fixture.mustExecute("status", "--full")

	is.Empty(stderr)
	is.Contains(stdout, "ar-closed")
	is.Contains(stdout, "ar-pr")
}

func TestIntegrationWorkflowStatusShowsSuccessfulMainRunAsLocalRepoRootReview(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	repo := taskWorkflowRepoSpec("alpha", "Alpha Repo", "ar")
	repos := saveTaskWorkflowRepos(t, fixture, repo)
	repoDir := repos["alpha"]
	runStore := taskstate.NewStore(fixture.paths)
	attempt, err := runStore.StartRun("alpha", "ar-main", taskstate.StartRunOptions{Agent: "recorder", Branch: "main", Worktree: repoDir})
	must.NoError(err)
	_, err = runStore.CompleteRun("alpha", "ar-main", attempt.Attempt, taskstate.CompleteRunOptions{Summary: "Ready", Description: "Ready for local review.", DetailedDescription: "Detailed PR body.", TechnicalExplanation: "Technical explanation."})
	must.NoError(err)
	_, err = runStore.FinishRun("alpha", "ar-main", attempt.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repoDir: {tasks: []taskmodel.Task{
		taskWFTask("ar-main", "Local main review", taskmodel.StatusInProgress, taskmodel.IssueTypeTask, taskWFPriority(2), taskWFMetadata(map[string]string{taskmodel.MetadataBranch: "main", taskmodel.MetadataWorktree: repoDir})),
	}}})

	stdout, stderr := fixture.mustExecute("status")

	is.Empty(stderr)
	is.Contains(stdout, "Reviewing")
	is.Contains(stdout, "ar-main")
	is.Contains(stdout, "Local main review")
	is.Contains(stdout, "local review; run task run")
}

func TestIntegrationWorkflowStatusAndTaskListUseLocalRunHistoryOnOpenTaskAsNeedsAttention(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	repos := saveTaskWorkflowRepos(t, fixture, taskWorkflowRepoSpec("alpha", "Alpha Repo", "ar"))
	_, err := taskstate.NewStore(fixture.paths).StartRun("alpha", "ar-running", taskstate.StartRunOptions{Agent: "recorder"})
	must.NoError(err)
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repos["alpha"]: {tasks: []taskmodel.Task{
		taskWFTask("ar-running", "Already running", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(2)),
		taskWFTask("ar-ready", "Ready task", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(1)),
	}}})

	stdout, stderr := fixture.mustExecute("status")

	is.Empty(stderr)
	is.Contains(stdout, "Needs attention")
	is.Contains(stdout, "ar-running")
	is.Contains(stdout, "backend status is open but local run attempt 1 is running")
	is.Contains(stdout, "Ready")
	is.Contains(stdout, "ar-ready")
	listStdout, listStderr := fixture.mustExecute("task", "list")
	is.Empty(listStderr)
	is.Contains(listStdout, "ar-ready")
	is.Contains(listStdout, "ar-running")
}

func TestIntegrationWorkflowStatusAndTaskListRenderEquivalentRowsIdentically(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	setupWorkflowStatusGroupsLocalTaskSnapshots(t, fixture)

	statusOutput, statusStderr := fixture.mustExecute("status")
	listOutput, listStderr := fixture.mustExecute("task", "list")

	require.Empty(t, statusStderr)
	require.Empty(t, listStderr)
	for _, taskID := range []string{"ar-ready", "ar-review", "ar-running"} {
		assert.Equal(t, tableRowForTask(statusOutput, taskID), tableRowForTask(listOutput, taskID))
	}
	assert.NotContains(t, statusOutput, "ar-blocked")
	assert.Contains(t, listOutput, "ar-blocked")
	assert.NotContains(t, listOutput, "ar-closed")
}

func tableRowForTask(output string, taskID string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, taskID) {
			return line
		}
	}
	return ""
}

func TestIntegrationWorkflowStatusRendersEpicChildrenAsIntegratedTreeRows(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)

	repos := saveTaskWorkflowRepos(t, fixture, taskWorkflowRepoSpec("alpha", "Alpha Repo", "ar"))
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repos["alpha"]: {tasks: []taskmodel.Task{
		taskWFTask("ar-epic", "Active epic", taskmodel.StatusInProgress, taskmodel.IssueTypeEpic, taskWFPriority(1), taskWFChildCount(4)),
		taskWFTask("ar-ready", "Ready child", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(2), taskWFParent("ar-epic")),
		taskWFTask("ar-nested", "Nested epic", taskmodel.StatusInProgress, taskmodel.IssueTypeEpic, taskWFPriority(2), taskWFParent("ar-epic")),
		taskWFTask("ar-nested-child", "Nested child", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(3), taskWFParent("ar-nested")),
		taskWFTask("ar-blocked", "Hidden blocked child", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(2), taskWFParent("ar-epic"), taskWFBlocks("ar-ready")),
		taskWFTask("ar-done", "Hidden done child", taskmodel.StatusClosed, taskmodel.IssueTypeTask, taskWFPriority(2), taskWFParent("ar-epic")),
	}}})

	stdout, stderr := fixture.mustExecute("status")

	is.Empty(stderr)
	is.Contains(stdout, "STATUS")
	is.Contains(stdout, "Working")
	is.Contains(stdout, "ar-epic")
	is.Contains(stdout, "1/4 done")
	is.Contains(stdout, "└─ ar-ready")
	is.NotContains(stdout, "ar-blocked")
	is.NotContains(stdout, "ar-done")
	assertStatusGroupOrder(t, stdout, []string{"ar-epic", "├─ ar-nested", "│ └─ ar-nested-child", "└─ ar-ready"})

	fullStdout, fullStderr := fixture.mustExecute("status", "--full")
	is.Empty(fullStderr)
	is.Contains(fullStdout, "├─ ar-blocked")
	is.Contains(fullStdout, "└─ ar-done")
	assertStatusGroupOrder(t, fullStdout, []string{"ar-epic", "├─ ar-nested", "│ └─ ar-nested-child", "├─ ar-ready", "├─ ar-blocked", "└─ ar-done"})
	assertTaskListRendersActiveEpicTree(t, fixture)
}

func assertTaskListRendersActiveEpicTree(t *testing.T, fixture *workflowFixture) {
	t.Helper()
	listStdout, listStderr := fixture.mustExecute("task", "list")
	require.Empty(t, listStderr)
	assert.Contains(t, listStdout, "1/4 done")
	assert.Contains(t, listStdout, "├─ ar-blocked")
	assert.Contains(t, listStdout, "├─ ar-nested")
	assert.Contains(t, listStdout, "└─ ar-ready")
	assert.NotContains(t, listStdout, "ar-done")
}

func TestIntegrationWorkflowStatusReportsRepoFailuresInUnknownGroupAndReturnsError(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)

	repos := saveTaskWorkflowRepos(t, fixture, taskWorkflowRepoSpec("broken", "Broken Repo", "br"), taskWorkflowRepoSpec("ok", "OK Repo", "ok"))
	withWorkflowSources(t, fixture, map[string]taskSourceResult{
		repos["broken"]: {err: errors.New("bd exploded")},
		repos["ok"]:     {tasks: []taskmodel.Task{taskWFTask("ok-1", "Ready despite another repo failure", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(1))}},
	})

	stdout, stderr, err := fixture.execute("status")

	must.Error(err)
	is.ErrorContains(err, "status completed with 1 repo failure")
	is.Contains(stdout, "Ready")
	is.Contains(stdout, "OK Repo")
	is.Contains(stdout, "ok-1")
	is.Contains(stdout, "Ready despite another repo failure")
	is.Contains(stdout, "Needs attention")
	is.Contains(stdout, "Broken Repo")
	is.Contains(stdout, "task_backend/snapshot")
	is.Contains(stdout, "bd exploded")
	is.Contains(stderr, "status: repo broken")
	is.Contains(stderr, "source=task_backend")
	is.Contains(stderr, "operation=snapshot")
	is.Contains(stderr, "Broken Repo")
	is.Contains(stderr, "prefix br")
	is.Contains(stderr, "bd exploded")

	jsonStdout, jsonStderr, jsonErr := fixture.execute("status", "--json")
	must.Error(jsonErr)
	is.Contains(jsonStderr, "status: repo broken")
	var jsonEntries []json.RawMessage
	must.NoError(json.Unmarshal([]byte(jsonStdout), &jsonEntries))
	is.Len(jsonEntries, 2)
	var failure taskViewJSONRepoFailureEntry
	must.NoError(json.Unmarshal(jsonEntries[0], &failure))
	is.Equal("repo_failure", failure.Kind)
	is.Equal("needs_attention", failure.Status)
	is.Contains(failure.Detail.Message, "bd exploded")
	var taskEntry taskViewJSONTaskEntry
	must.NoError(json.Unmarshal(jsonEntries[1], &taskEntry))
	is.Equal("task", taskEntry.Kind)
	is.Equal("ok-1", taskEntry.ID)
}

func TestIntegrationWorkflowTaskViewsApplySharedSortModesAcrossRepositories(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()

	repos := saveTaskWorkflowRepos(t, fixture, taskWorkflowRepoSpec("beta", "Beta", "b"), taskWorkflowRepoSpec("alpha", "Alpha", "a"))
	withWorkflowSources(t, fixture, map[string]taskSourceResult{
		repos["beta"]: {tasks: []taskmodel.Task{
			taskWFTask("b-review", "Review", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(4), taskWFCreated("2026-01-04T00:00:00Z"), taskWFUpdated("2026-01-01T00:00:00Z"), taskWFMetadata(map[string]string{taskmodel.MetadataPRURL: "https://example.test/pr/1"})),
			taskWFTask("b-ready", "Ready", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(1), taskWFCreated("2026-01-03T00:00:00Z"), taskWFUpdated("2026-01-05T00:00:00Z")),
		}},
		repos["alpha"]: {tasks: []taskmodel.Task{
			taskWFTask("a-ready-p2", "Lowest priority", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(2), taskWFCreated("2026-01-01T00:00:00Z"), taskWFUpdated("2026-01-03T00:00:00Z")),
			taskWFTask("a-ready-p0", "Highest priority", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFPriority(0), taskWFCreated("2026-01-02T00:00:00Z"), taskWFUpdated("2026-01-02T00:00:00Z")),
		}},
	})

	statusOrder := []string{"b-review", "a-ready-p0", "b-ready", "a-ready-p2"}
	createdOrder := []string{"b-review", "b-ready", "a-ready-p0", "a-ready-p2"}
	updatedOrder := []string{"b-ready", "a-ready-p2", "a-ready-p0", "b-review"}
	for _, test := range []struct {
		name string
		args []string
		want []string
	}{
		{name: "status default", args: []string{"status"}, want: statusOrder},
		{name: "status created", args: []string{"status", "--sort", "created"}, want: createdOrder},
		{name: "status updated", args: []string{"status", "--sort", "updated"}, want: updatedOrder},
		{name: "task list default", args: []string{"task", "list"}, want: createdOrder},
		{name: "task list status", args: []string{"task", "list", "--sort", "status"}, want: statusOrder},
		{name: "task list updated", args: []string{"task", "list", "--sort", "updated"}, want: updatedOrder},
	} {
		stdout, stderr := fixture.mustExecute(test.args...)
		if stderr != "" {
			t.Errorf("%s stderr = %q, want empty", test.name, stderr)
			continue
		}
		assertTaskViewOutputOrder(t, stdout, test.want)
		jsonArgs := append(append([]string{}, test.args...), "--json")
		jsonStdout, jsonStderr := fixture.mustExecute(jsonArgs...)
		if jsonStderr != "" {
			t.Errorf("%s JSON stderr = %q, want empty", test.name, jsonStderr)
			continue
		}
		var jsonEntries []taskViewJSONTaskEntry
		if err := json.Unmarshal([]byte(jsonStdout), &jsonEntries); err != nil {
			t.Errorf("%s JSON output does not parse: %v\n%s", test.name, err, jsonStdout)
			continue
		}
		jsonIDs := make([]string, 0, len(jsonEntries))
		for _, entry := range jsonEntries {
			jsonIDs = append(jsonIDs, entry.ID)
		}
		if !assert.Equal(t, test.want, jsonIDs, "%s JSON entry order", test.name) {
			continue
		}
	}
}

func assertTaskViewOutputOrder(t *testing.T, output string, taskIDs []string) {
	t.Helper()
	previous := -1
	for _, taskID := range taskIDs {
		index := strings.Index(output, taskID)
		if index < 0 {
			t.Fatalf("output missing task %q:\n%s", taskID, output)
		}
		if index <= previous {
			t.Fatalf("task %q appeared out of order:\n%s", taskID, output)
		}
		previous = index
	}
}

func assertStatusGroupOrder(t *testing.T, output string, groups []string) {
	t.Helper()
	previous := -1
	for _, group := range groups {
		index := strings.Index(output, group)
		if index < 0 {
			t.Fatalf("output missing section %q:\n%s", group, output)
		}
		if index <= previous {
			t.Fatalf("section %q appeared out of order in output:\n%s", group, output)
		}
		previous = index
	}
}
