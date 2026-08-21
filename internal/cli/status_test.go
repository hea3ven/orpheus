//go:build integration

//nolint:testpackage // Invocation-scoped fixture requires internal composition wiring.
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeBDCommandResponse struct {
	dir      string
	args     string
	stdout   string
	stderr   string
	exitCode int
}

func TestIntegrationStatusGroupsLocalTaskSnapshots(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	logPath := setupStatusGroupsLocalTaskSnapshots(t)

	stdout, stderr := executeCommand(t, []string{"status"})

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
	for _, hidden := range []string{"Blocked", "ar-blocked", "Done / closed", "ar-closed", "Bug item"} {
		is.NotContains(stdout, hidden)
	}

	assertStatusGroupOrder(t, stdout, []string{"Needs attention", "Reviewing", "Working", "Idle", "Ready"})

	fullStdout, fullStderr := executeCommand(t, []string{"status", "--full"})
	is.Empty(fullStderr)
	assertFullStatusGroupOutput(t, fullStdout)

	jsonStdout, jsonStderr := executeCommand(t, []string{"status", "--json"})
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

	fullJSONStdout, fullJSONStderr := executeCommand(t, []string{"status", "--full", "--json"})
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

	logData, err := os.ReadFile(logPath)
	must.NoError(err)
	log := string(logData)
	is.Contains(log, "--json --readonly --sandbox list --all --limit 0")
	is.NotContains(log, "--json --readonly --sandbox ready")
	is.NotContains(log, "show --id")
	is.NotContains(log, "gh ")
}

const statusGroupsLocalTasksJSON = `[
	{"id":"ar-ready","title":"Ready task","status":"open","priority":1,"issue_type":"task"},
	{"id":"ar-dep","title":"Open dependency","status":"open","priority":1,"issue_type":"task"},
	{"id":"ar-idle","title":"Idle without run","status":"in_progress","priority":4,"issue_type":"task"},
	{"id":"ar-running","title":"Running attached agent","status":"in_progress","priority":2,"issue_type":"task"},
	{"id":"ar-failed","title":"Failed attached agent","status":"in_progress","priority":2,"issue_type":"task"},
	{"id":"ar-succeeded","title":"Succeeded attached agent","status":"in_progress","priority":3,"issue_type":"task"},
	{"id":"ar-blocked","title":"Blocked task","status":"open","priority":2,"issue_type":"task","dependencies":[{"id":"ar-dep","dependency_type":"blocks"}]},
	{"id":"ar-review","title":"Review task","status":"open","priority":3,"issue_type":"task","metadata":{"orpheus.pr_url":"https://example.test/pr/3"}},
	{"id":"ar-missing","title":"Needs inspection","status":"open","priority":4,"issue_type":"task","dependencies":[{"id":"ar-bug","dependency_type":"blocks"}]},
	{"id":"ar-closed","title":"Closed task","status":"closed","priority":1,"issue_type":"task"},
	{"id":"ar-bug","title":"Bug item","status":"open","priority":1,"issue_type":"bug"}
]`

func setupStatusGroupsLocalTaskSnapshots(t *testing.T) string {
	t.Helper()

	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(testutil.CanonicalTempDir(t), "alpha")
	must.NoError(os.MkdirAll(repoDir, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:          "alpha",
		Name:        "Alpha Repo",
		Path:        repoDir,
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "ar",
	}}}))

	stateStore := taskstate.NewStore(paths)
	_, err := stateStore.StartRun("alpha", "ar-running", taskstate.StartRunOptions{Agent: "recorder"})
	must.NoError(err)
	failedRun, err := stateStore.StartRun("alpha", "ar-failed", taskstate.StartRunOptions{Agent: "recorder"})
	must.NoError(err)
	_, err = stateStore.FinishRun("alpha", "ar-failed", failedRun.Attempt, taskstate.RunStatusFailed)
	must.NoError(err)
	succeededRun, err := stateStore.StartRun("alpha", "ar-succeeded", taskstate.StartRunOptions{Agent: "recorder"})
	must.NoError(err)
	_, err = stateStore.FinishRun("alpha", "ar-succeeded", succeededRun.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)

	return withFakeBDCommandResponses(t, []fakeBDCommandResponse{{
		dir:    repoDir,
		args:   "--json --readonly --sandbox list --all --limit 0",
		stdout: statusGroupsLocalTasksJSON,
	}})
}

func assertFullStatusGroupOutput(t *testing.T, fullStdout string) {
	t.Helper()

	is := assert.New(t)
	for _, want := range []string{
		"Blocked", "ar-blocked", "Blocked task", "blocked by ar-dep",
		"Done / closed", "ar-closed", "Closed task",
	} {
		is.Contains(fullStdout, want)
	}
	is.Contains(fullStdout, "STATUS")
	assertStatusGroupOrder(t, fullStdout, []string{
		"Needs attention", "Reviewing", "Working", "Idle", "Ready", "Blocked", "Done / closed",
	})
	header := strings.SplitN(fullStdout, "\n", 2)[0]
	is.Less(strings.Index(header, "TITLE"), strings.Index(header, "DETAIL"))
}

func TestIntegrationStatusFullIgnoresCorruptClosedAndPullRequestStates(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(testutil.CanonicalTempDir(t), "alpha")
	must.NoError(os.MkdirAll(repoDir, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:          "alpha",
		Name:        "Alpha Repo",
		Path:        repoDir,
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "ar",
	}}}))

	stateStore := taskstate.NewStore(paths)
	for _, taskID := range []string{"ar-closed", "ar-pr"} {
		statePath, err := stateStore.Path("alpha", taskID)
		must.NoError(err)
		must.NoError(os.MkdirAll(filepath.Dir(statePath), 0o755))
		must.NoError(os.WriteFile(statePath, []byte("not: [valid"), 0o644))
	}
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{{
		dir:  repoDir,
		args: "--json --readonly --sandbox list --all --limit 0",
		stdout: `[
			{"id":"ar-closed","title":"Closed task","status":"closed","issue_type":"task"},
			{"id":"ar-pr","title":"PR task","status":"open","issue_type":"task","metadata":{"orpheus.pr_url":"https://example.test/pr/1"}}
		]`,
	}})

	stdout, stderr := executeCommand(t, []string{"status", "--full"})

	is.Empty(stderr)
	is.Contains(stdout, "ar-closed")
	is.Contains(stdout, "ar-pr")
}

func TestIntegrationStatusShowsSuccessfulMainRunAsLocalRepoRootReview(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(testutil.CanonicalTempDir(t), "alpha")
	must.NoError(os.MkdirAll(repoDir, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoDir,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "ar",
	}}}))
	runStore := taskstate.NewStore(paths)
	attempt, err := runStore.StartRun("alpha", "ar-main", taskstate.StartRunOptions{Agent: "recorder", Branch: "main", Worktree: repoDir})
	must.NoError(err)
	_, err = runStore.CompleteRun("alpha", "ar-main", attempt.Attempt, taskstate.CompleteRunOptions{
		Summary:              "Ready",
		Description:          "Ready for local review.",
		DetailedDescription:  "Detailed PR body.",
		TechnicalExplanation: "Technical explanation.",
	})
	must.NoError(err)
	_, err = runStore.FinishRun("alpha", "ar-main", attempt.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)

	withFakeBDCommandResponses(t, []fakeBDCommandResponse{{
		dir:  repoDir,
		args: "--json --readonly --sandbox list --all --limit 0",
		stdout: `[
			{
				"id":"ar-main",
				"title":"Local main review",
				"status":"in_progress",
				"priority":2,
				"issue_type":"task",
				"metadata":{"orpheus.branch":"main","orpheus.worktree":"` + repoDir + `"}
			}
		]`,
	}})

	stdout, stderr := executeCommand(t, []string{"status"})

	is.Empty(stderr)
	is.Contains(stdout, "Reviewing")
	is.Contains(stdout, "ar-main")
	is.Contains(stdout, "Local main review")
	is.Contains(stdout, "local review; run task run")
}

func TestIntegrationStatusAndTaskListUseLocalRunHistoryOnOpenTaskAsNeedsAttention(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(testutil.CanonicalTempDir(t), "alpha")
	must.NoError(os.MkdirAll(repoDir, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:          "alpha",
		Name:        "Alpha Repo",
		Path:        repoDir,
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "ar",
	}}}))
	_, err := taskstate.NewStore(paths).StartRun("alpha", "ar-running", taskstate.StartRunOptions{Agent: "recorder"})
	must.NoError(err)

	withFakeBDCommandResponses(t, []fakeBDCommandResponse{{
		dir:  repoDir,
		args: "--json --readonly --sandbox list --all --limit 0",
		stdout: `[
			{"id":"ar-running","title":"Already running","status":"open","priority":2,"issue_type":"task"},
			{"id":"ar-ready","title":"Ready task","status":"open","priority":1,"issue_type":"task"}
		]`,
	}})

	stdout, stderr := executeCommand(t, []string{"status"})

	is.Empty(stderr)
	is.Contains(stdout, "Needs attention")
	is.Contains(stdout, "ar-running")
	is.Contains(stdout, "backend status is open but local run attempt 1 is running")
	is.Contains(stdout, "Ready")
	is.Contains(stdout, "ar-ready")

	listStdout, listStderr := executeCommand(t, []string{"task", "list"})

	is.Empty(listStderr)
	is.Contains(listStdout, "ar-ready")
	is.Contains(listStdout, "ar-running")
}

func TestIntegrationStatusAndTaskListRenderEquivalentRowsIdentically(t *testing.T) {
	t.Parallel()

	setupStatusGroupsLocalTaskSnapshots(t)

	statusOutput, statusStderr := executeCommand(t, []string{"status"})
	listOutput, listStderr := executeCommand(t, []string{"task", "list"})

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

func TestIntegrationStatusRendersEpicChildrenAsIntegratedTreeRows(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(testutil.CanonicalTempDir(t), "alpha")
	must.NoError(os.MkdirAll(repoDir, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:          "alpha",
		Name:        "Alpha Repo",
		Path:        repoDir,
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "ar",
	}}}))

	withFakeBDCommandResponses(t, []fakeBDCommandResponse{{
		dir:  repoDir,
		args: "--json --readonly --sandbox list --all --limit 0",
		stdout: `[
			{"id":"ar-epic","title":"Active epic","status":"in_progress","priority":1,"issue_type":"epic","child_count":4},
			{"id":"ar-ready","title":"Ready child","status":"open","priority":2,"issue_type":"task","parent":"ar-epic"},
			{"id":"ar-nested","title":"Nested epic","status":"in_progress","priority":2,"issue_type":"epic","parent":"ar-epic"},
			{"id":"ar-nested-child","title":"Nested child","status":"open","priority":3,"issue_type":"task","parent":"ar-nested"},
			{"id":"ar-blocked","title":"Hidden blocked child","status":"open","priority":2,"issue_type":"task","parent":"ar-epic","dependencies":[{"id":"ar-ready","dependency_type":"blocks"}]},
			{"id":"ar-done","title":"Hidden done child","status":"closed","priority":2,"issue_type":"task","parent":"ar-epic"}
		]`,
	}})

	stdout, stderr := executeCommand(t, []string{"status"})

	is.Empty(stderr)
	is.Contains(stdout, "STATUS")
	is.Contains(stdout, "Working")
	is.Contains(stdout, "ar-epic")
	is.Contains(stdout, "1/4 done")
	is.Contains(stdout, "└─ ar-ready")
	is.NotContains(stdout, "ar-blocked")
	is.NotContains(stdout, "ar-done")
	assertStatusGroupOrder(t, stdout, []string{"ar-epic", "├─ ar-nested", "│ └─ ar-nested-child", "└─ ar-ready"})

	fullStdout, fullStderr := executeCommand(t, []string{"status", "--full"})

	is.Empty(fullStderr)
	is.Contains(fullStdout, "├─ ar-blocked")
	is.Contains(fullStdout, "└─ ar-done")
	assertStatusGroupOrder(t, fullStdout, []string{
		"ar-epic",
		"├─ ar-nested",
		"│ └─ ar-nested-child",
		"├─ ar-ready",
		"├─ ar-blocked",
		"└─ ar-done",
	})

	assertTaskListRendersActiveEpicTree(t)
}

func assertTaskListRendersActiveEpicTree(t *testing.T) {
	t.Helper()

	listStdout, listStderr := executeCommand(t, []string{"task", "list"})
	require.Empty(t, listStderr)
	assert.Contains(t, listStdout, "1/4 done")
	assert.Contains(t, listStdout, "├─ ar-blocked")
	assert.Contains(t, listStdout, "├─ ar-nested")
	assert.Contains(t, listStdout, "└─ ar-ready")
	assert.NotContains(t, listStdout, "ar-done")
}

func TestIntegrationStatusReportsRepoFailuresInUnknownGroupAndReturnsError(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	brokenDir := filepath.Join(testutil.CanonicalTempDir(t), "broken")
	okDir := filepath.Join(testutil.CanonicalTempDir(t), "ok")
	must.NoError(os.MkdirAll(brokenDir, 0o755))
	must.NoError(os.MkdirAll(okDir, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{
		{ID: "broken", Name: "Broken Repo", Path: brokenDir, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "br"},
		{ID: "ok", Name: "OK Repo", Path: okDir, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "ok"},
	}}))

	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{
			dir:      brokenDir,
			args:     "--json --readonly --sandbox list --all --limit 0",
			stderr:   "bd exploded",
			exitCode: 7,
		},
		{
			dir:  okDir,
			args: "--json --readonly --sandbox list --all --limit 0",
			stdout: `[
				{
					"id":"ok-1",
					"title":"Ready despite another repo failure",
					"status":"open",
					"priority":1,
					"issue_type":"task"
				}
			]`,
		},
	})

	stdout, stderr, err := executeCommandWithError(t, []string{"status"})

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

	jsonStdout, jsonStderr, jsonErr := executeCommandWithError(t, []string{"status", "--json"})
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

func TestIntegrationTaskViewsApplySharedSortModesAcrossRepositories(t *testing.T) {
	t.Parallel()

	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)
	betaDir := filepath.Join(testutil.CanonicalTempDir(t), "beta")
	alphaDir := filepath.Join(testutil.CanonicalTempDir(t), "alpha")
	must.NoError(os.MkdirAll(betaDir, 0o755))
	must.NoError(os.MkdirAll(alphaDir, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{
		{ID: "beta", Name: "Beta", Path: betaDir, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "b"},
		{ID: "alpha", Name: "Alpha", Path: alphaDir, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "a"},
	}}))

	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{
			dir:  betaDir,
			args: "--json --readonly --sandbox list --all --limit 0",
			stdout: `[
				{"id":"b-review","title":"Review","status":"open","priority":4,"issue_type":"task","created_at":"2026-01-04T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","metadata":{"orpheus.pr_url":"https://example.test/pr/1"}},
				{"id":"b-ready","title":"Ready","status":"open","priority":1,"issue_type":"task","created_at":"2026-01-03T00:00:00Z","updated_at":"2026-01-05T00:00:00Z"}
			]`,
		},
		{
			dir:  alphaDir,
			args: "--json --readonly --sandbox list --all --limit 0",
			stdout: `[
				{"id":"a-ready-p2","title":"Lowest priority","status":"open","priority":2,"issue_type":"task","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-03T00:00:00Z"},
				{"id":"a-ready-p0","title":"Highest priority","status":"open","priority":0,"issue_type":"task","created_at":"2026-01-02T00:00:00Z","updated_at":"2026-01-02T00:00:00Z"}
			]`,
		},
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
		stdout, stderr := executeCommand(t, test.args)
		if stderr != "" {
			t.Errorf("%s stderr = %q, want empty", test.name, stderr)
			continue
		}
		assertTaskViewOutputOrder(t, stdout, test.want)

		jsonArgs := append(append([]string{}, test.args...), "--json")
		jsonStdout, jsonStderr := executeCommand(t, jsonArgs)
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

func withFakeBDCommandResponses(t *testing.T, responses []fakeBDCommandResponse) string {
	t.Helper()

	binDir := testutil.CanonicalTempDir(t)
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
case "$PWD|$*" in
`)

	for i, response := range responses {
		stdoutPath := filepath.Join(fixtureDir, fmt.Sprintf("stdout-%d.json", i))
		stderrPath := filepath.Join(fixtureDir, fmt.Sprintf("stderr-%d.txt", i))
		if err := os.WriteFile(stdoutPath, []byte(response.stdout), 0o644); err != nil {
			t.Fatalf("write fake bd stdout: %v", err)
		}
		if err := os.WriteFile(stderrPath, []byte(response.stderr), 0o644); err != nil {
			t.Fatalf("write fake bd stderr: %v", err)
		}
		exitCode := response.exitCode
		if exitCode == 0 && response.stderr != "" && response.stdout == "" {
			exitCode = 1
		}
		args := response.args
		if args == "--json --readonly --sandbox list --all --limit 0" {
			args += " --type task"
		}
		fmt.Fprintf(&script, "  %s)\n", shellQuote(canonicalFixturePath(t, response.dir)+"|"+args))
		fmt.Fprintf(&script, "    cat %s\n", shellQuote(stdoutPath))
		fmt.Fprintf(&script, "    cat %s >&2\n", shellQuote(stderrPath))
		fmt.Fprintf(&script, "    exit %d\n", exitCode)
		fmt.Fprintln(&script, "    ;;")
	}
	script.WriteString(`esac
case "$*" in
  "--json --readonly --sandbox list --all --limit 0 --type epic") printf '[]\n'; exit 0 ;;
  --json\ --sandbox\ update\ *--set-metadata\ orpheus.branch=*) exit 0 ;;
  --json\ --sandbox\ update\ *--set-metadata\ orpheus.pr_url=*) exit 0 ;;
esac
echo "unexpected fake bd call: $PWD|$*" >&2
exit 65
`)

	bdPath := filepath.Join(binDir, "bd")
	if err := writeTestExecutable(bdPath, []byte(script.String())); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	setTestEnvironment(t, "FAKE_BD_LOG", logPath)
	prependTestPath(t, binDir)
	return logPath
}
