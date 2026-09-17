//go:build integration

//nolint:testpackage // Invocation-scoped fixture requires internal composition wiring.
package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	"github.com/hea3ven/orpheus/internal/testguard"
	"github.com/hea3ven/orpheus/internal/testutil"
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
	repoDir := filepath.Join(testutil.CanonicalTempDir(t), id)
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
	localDir := filepath.Join(testutil.CanonicalTempDir(t), "local-alpha")
	managedRepoPath := filepath.Join(testutil.CanonicalTempDir(t), "managed-beta")
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

func TestIntegrationTaskListListsAllActiveItemsWithStatusProjectionPresentation(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	newTestState(t)
	repos := registerLocalManagedTaskTestRepos(t)

	logPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repos.localDir: {stdout: `[
			{"id":"la-1","title":"Local active","status":"open","priority":2,"issue_type":"task","metadata":{"orpheus.branch":"task/la-1","orpheus.worktree":"/fixture/la-1"}},
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
		"TASK_ID", "STATUS", "P", "TITLE", "REPO", "DETAIL",
		"Local Alpha", "la-1", "Ready", "2", "Local active",
		"Managed Beta", "mb-1", "Reviewing", "3", "Managed active", "https://example.test/pr/1",
	} {
		is.Contains(stdout, want)
	}
	for _, hidden := range []string{
		"REPO_ID", "TASK_PREFIX", "ORPHEUS", "local-alpha", "managed-beta", "branch=task/la-1", "worktree=/fixture/la-1", "pr=https://example.test/pr/1",
		"la-closed", "la-bug", "Local bug", "orpheus.branch",
	} {
		is.NotContains(stdout, hidden)
	}

	jsonStdout, jsonStderr := executeCommand(t, []string{"task", "list", "--json"})
	is.Empty(jsonStderr)
	var jsonEntries []taskViewJSONTaskEntry
	require.NoError(t, json.Unmarshal([]byte(jsonStdout), &jsonEntries))
	jsonIDs := make([]string, 0, len(jsonEntries))
	for _, entry := range jsonEntries {
		jsonIDs = append(jsonIDs, entry.ID)
	}
	assert.Equal(t, []string{"la-1", "mb-1"}, jsonIDs)
	entriesByID := make(map[string]taskViewJSONTaskEntry, len(jsonEntries))
	for _, entry := range jsonEntries {
		entriesByID[entry.ID] = entry
	}
	assert.Equal(t, "ready", entriesByID["la-1"].Status)
	assert.Equal(t, "reviewing", entriesByID["mb-1"].Status)
	assert.Equal(t, "task", entriesByID["la-1"].Kind)

	assertTaskListBDLog(t, logPath, repos)
}

func TestIntegrationTaskListScopesOneRegisteredRepository(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)
	alphaDir := filepath.Join(testutil.CanonicalTempDir(t), "alpha")
	betaDir := filepath.Join(testutil.CanonicalTempDir(t), "beta")
	for _, dir := range []string{alphaDir, betaDir} {
		must.NoError(os.MkdirAll(dir, 0o755))
	}
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{
		{ID: "alpha", Name: "Alpha Repo", Path: alphaDir, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "a"},
		{ID: "beta", Name: "Beta Repo", Path: betaDir, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "b"},
	}}))

	logPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		alphaDir: {stdout: `[
			{"id":"a-closed","title":"MATCH closed task","status":"closed","priority":1,"issue_type":"task","created_at":"2026-06-03T00:00:00Z","updated_at":"2026-06-03T00:00:00Z"},
			{"id":"a-open","title":"match open task","status":"open","priority":1,"issue_type":"task","created_at":"2026-06-03T00:00:00Z","updated_at":"2026-06-03T00:00:00Z"},
			{"id":"a-late","title":"match updated too late","status":"closed","priority":1,"issue_type":"task","created_at":"2026-06-03T00:00:00Z","updated_at":"2026-06-06T00:00:00Z"},
			{"id":"a-epic","title":"Selected epic","status":"open","priority":1,"issue_type":"epic"},
			{"id":"a-other","title":"unrelated","status":"closed","priority":1,"issue_type":"task","created_at":"2026-06-03T00:00:00Z","updated_at":"2026-06-03T00:00:00Z"}
		]`},
		betaDir: {stderr: "excluded backend should not be queried", exitCode: 7},
	})

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "list", "--repo", "alpha"})
	must.NoError(err)
	is.Empty(stderr)
	for _, want := range []string{"a-open", "a-epic", "Alpha Repo"} {
		is.Contains(stdout, want)
	}
	for _, hidden := range []string{"a-closed", "a-late", "a-other", "Beta Repo", "excluded backend should not be queried"} {
		is.NotContains(stdout, hidden)
	}

	jsonStdout, jsonStderr, jsonErr := executeCommandWithError(t, []string{"task", "list", "--repo", "a", "--json"})
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

	filteredArgs := []string{
		"task", "list", "--repo", "Alpha Repo",
		"--query", "match",
		"--type", "task",
		"--created-after", "2026-06-01",
		"--created-before", "2026-06-05",
		"--updated-after", "2026-06-02",
		"--updated-before", "2026-06-05",
		"--status", "closed",
	}
	for _, sortMode := range []string{"created", "status"} {
		filteredStdout, filteredStderr, filteredErr := executeCommandWithError(t, append(append([]string{}, filteredArgs...), "--sort", sortMode))
		must.NoError(filteredErr)
		is.Empty(filteredStderr)
		is.Contains(filteredStdout, "a-closed")
		for _, hidden := range []string{"a-open", "a-late", "a-epic", "a-other", "Beta Repo"} {
			is.NotContains(filteredStdout, hidden)
		}
	}

	filteredJSON, filteredJSONStderr, filteredJSONErr := executeCommandWithError(t, append(append([]string{}, filteredArgs...), "--sort", "updated", "--json"))
	must.NoError(filteredJSONErr)
	is.Empty(filteredJSONStderr)
	var filteredEntries []taskViewJSONTaskEntry
	must.NoError(json.Unmarshal([]byte(filteredJSON), &filteredEntries))
	must.Len(filteredEntries, 1)
	is.Equal("a-closed", filteredEntries[0].ID)

	_, _, unknownErr := executeCommandWithError(t, []string{"task", "list", "--repo", "missing"})
	must.Error(unknownErr)
	is.ErrorContains(unknownErr, `repo "missing" is not registered`)
	is.ErrorContains(unknownErr, "orpheus repo list")

	log := readFileString(t, logPath)
	is.Contains(log, alphaDir)
	is.NotContains(log, betaDir)
	is.Equal(7, strings.Count(log, "--json --readonly --sandbox list --all --limit 0"))
}

func TestIntegrationTaskListReportsSelectedRepositoryFailure(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)
	alphaDir := filepath.Join(testutil.CanonicalTempDir(t), "alpha")
	betaDir := filepath.Join(testutil.CanonicalTempDir(t), "beta")
	for _, dir := range []string{alphaDir, betaDir} {
		must.NoError(os.MkdirAll(dir, 0o755))
	}
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{
		{ID: "alpha", Name: "Broken Repo", Path: alphaDir, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "a"},
		{ID: "beta", Name: "Healthy Repo", Path: betaDir, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "b"},
	}}))
	logPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		alphaDir: {stderr: "selected backend failed", exitCode: 7},
		betaDir:  {stdout: `[{"id":"b-1","title":"Excluded healthy task","status":"open","issue_type":"task"}]`},
	})

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "list", "--repo", "a"})

	must.Error(err)
	is.ErrorContains(err, "task list completed with 1 repo failure")
	is.Contains(stdout, "Broken Repo")
	is.Contains(stderr, "task list: repo alpha")
	is.Contains(stderr, "selected backend failed")
	is.NotContains(stdout, "Healthy Repo")
	is.NotContains(stderr, "Healthy Repo")

	log := readFileString(t, logPath)
	is.Contains(log, alphaDir)
	is.NotContains(log, betaDir)
}

func TestIntegrationTaskListComposesSourceAndProjectedStatusFilters(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	newTestState(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "a")
	logPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{repoDir: {stdout: `[
		{"id":"a-closed","title":"MATCH closed task","status":"closed","priority":1,"issue_type":"task","created_at":"2026-06-03T00:00:00Z","updated_at":"2026-06-03T00:00:00Z"},
		{"id":"a-open","title":"match open task","status":"open","priority":1,"issue_type":"task","created_at":"2026-06-03T00:00:00Z","updated_at":"2026-06-03T00:00:00Z"},
		{"id":"a-late","title":"match updated too late","status":"closed","priority":1,"issue_type":"task","created_at":"2026-06-03T00:00:00Z","updated_at":"2026-06-06T00:00:00Z"},
		{"id":"a-other","title":"unrelated","status":"closed","priority":1,"issue_type":"task","created_at":"2026-06-03T00:00:00Z","updated_at":"2026-06-03T00:00:00Z"}
	]`}})

	stdout, stderr := executeCommand(t, []string{
		"task", "list",
		"--query", "match",
		"--type", "task",
		"--created-after", "2026-06-01",
		"--created-before", "2026-06-05",
		"--updated-after", "2026-06-02",
		"--updated-before", "2026-06-05",
		"--status", "closed",
	})

	is.Empty(stderr)
	is.Contains(stdout, "a-closed")
	for _, hidden := range []string{"a-open", "a-late", "a-other"} {
		is.NotContains(stdout, hidden)
	}

	jsonStdout, jsonStderr := executeCommand(t, []string{
		"task", "list", "--json",
		"--query", "match",
		"--type", "task",
		"--created-after", "2026-06-01",
		"--created-before", "2026-06-05",
		"--updated-after", "2026-06-02",
		"--updated-before", "2026-06-05",
		"--status", "closed",
	})
	is.Empty(jsonStderr)
	var jsonEntries []taskViewJSONTaskEntry
	if !is.NoError(json.Unmarshal([]byte(jsonStdout), &jsonEntries)) {
		return
	}
	if !is.Len(jsonEntries, 1) {
		return
	}
	is.Equal("a-closed", jsonEntries[0].ID)
	is.Equal("closed", jsonEntries[0].Status)
	log := readFileString(t, logPath)
	is.Contains(log, "--json --readonly --sandbox list --all --limit 0 --type task --created-after 2026-06-01 --created-before 2026-06-05 --updated-after 2026-06-02 --updated-before 2026-06-05")
}

func assertTaskListBDLog(t *testing.T, logPath string, repos localManagedTaskRepos) {
	t.Helper()

	is := assert.New(t)
	log := readFileString(t, logPath)
	is.Contains(log, repos.localDir)
	is.Contains(log, repos.managedDir)
	is.Equal(8, strings.Count(log, "--json --readonly --sandbox list --all --limit 0"))
}

func TestIntegrationTaskReadyIsNotACommand(t *testing.T) {
	t.Parallel()

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "ready"})
	require.NoError(t, err)
	assert.Contains(t, stdout, "Available Commands:")
	assert.NotContains(t, stdout, "ready")
	assert.Empty(t, stderr)

	completion, completionStderr, err := executeCommandWithError(t, []string{"__complete", "task", "r"})
	require.NoError(t, err)
	assert.NotContains(t, completion, "ready")
	assert.NotContains(t, completion, "review")
	assert.Contains(t, completion, "run")
	assert.Contains(t, completionStderr, "Completion ended with directive")
}

func TestIntegrationTaskListRetiresDetailsAndLongFlags(t *testing.T) {
	t.Parallel()

	for _, flag := range []string{"--details", "--long"} {
		_, _, err := executeCommandWithError(t, []string{"task", "list", flag})
		require.ErrorContains(t, err, "unknown flag: "+flag)
	}
}

func TestIntegrationTaskListReportsPartialRepoFailures(t *testing.T) {
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

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		brokenDir: {stderr: "bd exploded", exitCode: 7},
		okDir: {stdout: `[
			{"id":"ok-1","title":"Listed despite another repo failure","status":"open","priority":1,"issue_type":"task"}
		]`},
	})

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "list"})

	must.Error(err)
	is.ErrorContains(err, "task list completed with 1 repo failure")
	is.Contains(stdout, "TASK_ID")
	is.Contains(stdout, "OK Repo")
	is.Contains(stdout, "ok-1")
	is.Contains(stdout, "Listed despite another repo failure")
	is.Contains(stdout, "Broken Repo")
	is.Contains(stderr, "task list: repo broken")
	is.Contains(stderr, "needs attention")
	is.Contains(stderr, "Broken Repo")
	is.Contains(stderr, "prefix br")
	is.Contains(stderr, "bd exploded")

	jsonStdout, jsonStderr, jsonErr := executeCommandWithError(t, []string{"task", "list", "--json"})
	must.Error(jsonErr)
	is.Contains(jsonStderr, "task list: repo broken")
	var jsonEntries []taskViewJSONTaskEntry
	must.NoError(json.Unmarshal([]byte(jsonStdout), &jsonEntries))
	is.Len(jsonEntries, 1)
	is.Equal("task", jsonEntries[0].Kind)
	is.Equal("ok-1", jsonEntries[0].ID)
}

func TestIntegrationTaskShowResolvesPrefixQueriesOnlyResolvedRepoAndRendersDetails(t *testing.T) {
	t.Parallel()

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
				"metadata":{"orpheus.branch":"task/la-42","orpheus.worktree":"/fixture/la-42","orpheus.pr_url":"https://example.test/pr/42"}
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
		"Worktree: /fixture/la-42",
		"PR: https://example.test/pr/42",
		"History:",
		"  -",
	} {
		is.Contains(stdout, want)
	}
	is.NotContains(stdout, "orpheus.branch")
	is.NotContains(stdout, "managed-beta")
	is.NotContains(stdout, "Children:")

	assertTaskShowBDLog(t, logPath, repos)
}

func TestIntegrationTaskShowEpicRendersSortedDirectChildrenFromResolvedRepo(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	newTestState(t)
	repos := registerLocalManagedTaskTestRepos(t)

	logPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repos.localDir: {stdout: `[
			{"id":"la-epic","title":"Plan release","status":"in_progress","issue_type":"epic"},
			{"id":"la-child-z","title":"Closed task","status":"closed","issue_type":"task","parent":"la-epic"},
			{"id":"la-child-a","title":"Open task","status":"open","issue_type":"bug","parent":"la-epic"},
			{"id":"la-child-b","title":"Nested epic","status":"in_progress","issue_type":"epic","parent":"la-epic"},
			{"id":"la-grandchild","title":"Must not appear","status":"open","issue_type":"task","parent":"la-child-a"}
		]`},
		repos.managedDir: {stderr: "managed repo should not be queried", exitCode: 70},
	})

	stdout, stderr := executeCommand(t, []string{"task", "show", "la-epic"})

	is.Empty(stderr)
	for _, want := range []string{
		"Children:",
		"ID: la-child-b, Status: in_progress, Type: epic, Title: Nested epic",
		"ID: la-child-z, Status: closed, Type: task, Title: Closed task",
		"Orpheus metadata:",
		"History:",
	} {
		is.Contains(stdout, want)
	}
	is.NotContains(stdout, "la-child-a")
	is.NotContains(stdout, "la-grandchild")
	is.Less(strings.Index(stdout, "la-child-b"), strings.Index(stdout, "la-child-z"))

	log := readFileString(t, logPath)
	is.Contains(log, repos.localDir)
	is.Contains(log, "--json --readonly --sandbox show --id la-epic")
	is.Contains(log, "--json --readonly --sandbox list --all --limit 0")
	is.NotContains(log, repos.managedDir)
}

func TestIntegrationTaskShowEpicRendersEmptyChildrenState(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	newTestState(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")
	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[{"id":"op-epic","title":"Empty epic","status":"open","issue_type":"epic"}]`},
	})

	stdout, stderr := executeCommand(t, []string{"task", "show", "op-epic"})

	is.Empty(stderr)
	is.Contains(stdout, "Children:\n  - No direct children.\n")
}

func TestIntegrationTaskShowEpicReportsChildQueryFailureWithRepositoryAndParent(t *testing.T) {
	t.Parallel()

	must := require.New(t)
	newTestState(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")
	withFailingTaskShowChildren(t, repoDir)

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "show", "op-epic"})

	must.Error(err)
	must.Empty(stdout)
	must.Empty(stderr)
	must.ErrorContains(err, "task show op-epic: query direct children for parent task op-epic in repo alpha")
	must.ErrorContains(err, "backend unavailable")
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

func TestIntegrationTaskShowReportsMalformedAndUnknownPrefixes(t *testing.T) {
	t.Parallel()

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

func TestIntegrationTaskStatsRendersImplementationExecutionUsage(t *testing.T) {
	t.Parallel()

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
		Session: &taskstate.AgentSession{ID: "session-123", LogPath: "/fixture/codex.jsonl"},
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
		Session: &taskstate.AgentSession{ID: "review-session-123", LogPath: "/fixture/codex-review.jsonl"},
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
		"Executions", "Estimated cost uses harness-reported estimates",
		"TYPE", "ATTEMPT", "STEP", "PROFILE", "HARNESS", "MODEL", "COMMAND", "STARTED", "FINISHED", "DURATION", "STATUS", "SESSION", "USAGE", "ESTIMATED_COST",
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

func TestIntegrationTaskStatsRendersSyncConflictResolutionExecutionUsage(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[{"id":"op-1","title":"Stats","status":"in_progress","priority":1,"issue_type":"task"}]`},
	})

	startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	finishedAt := time.Date(2026, 7, 7, 10, 12, 0, 0, time.UTC)
	stateStore := taskstate.NewStoreWithClock(paths, clockSequence(startedAt, finishedAt))
	opts := taskstate.SyncConflictResolutionEventOptions{
		Execution: taskstate.AgentExecution{
			Agent:       "codex",
			Profile:     "sync-profile",
			Harness:     "codex",
			Model:       "gpt-5",
			Command:     "codex",
			Args:        []string{"exec", "--profile", "sync-profile"},
			SessionName: "sync-conflict-op-1",
			StartedAt:   startedAt,
		},
		Branch:        "orpheus/op-1",
		DefaultBranch: "main",
		Worktree:      repoDir,
		PRURL:         "https://github.test/org/repo/pull/42",
		ConflictFiles: []string{"conflict.txt"},
	}
	_, err := stateStore.RecordSyncConflictResolutionStarted("alpha", "op-1", opts)
	must.NoError(err)

	finishedOpts := opts
	finishedOpts.Commit = "merge123"
	finishedOpts.Usage = taskstate.RecordRunUsageOptions{
		Session: &taskstate.AgentSession{ID: "sync-session-123", LogPath: "/fixture/codex-sync.jsonl"},
		Usage: &taskstate.AgentUsage{
			InputTokens:           120,
			CachedInputTokens:     10,
			OutputTokens:          30,
			ReasoningOutputTokens: 5,
			TotalTokens:           165,
		},
		UsageCapture: taskstate.AgentUsageCapture{
			Status:         taskstate.UsageCaptureCaptured,
			Reason:         "matched_codex_session",
			CandidateCount: 1,
		},
	}
	_, err = stateStore.RecordSyncConflictResolutionFinished("alpha", "op-1", finishedOpts)
	must.NoError(err)

	stdout, stderr := executeCommand(t, []string{"task", "stats", "op-1"})

	is.Empty(stderr)
	for _, want := range []string{
		"sync-conflict-resolution", "sync-profile", "codex", "gpt-5",
		`"codex" "exec" "--profile" "sync-profile"`,
		"2026-07-07T10:00:00Z", "2026-07-07T10:12:00Z", "12m0s", "succeeded",
		"sync-session-123", "total=165 input=120 cached_input=10 output=30 reasoning_output=5",
		"estimated API-equivalent cost=$", "kind=estimated_api_equivalent", "Totals",
	} {
		is.Contains(stdout, want)
	}
	is.Regexp(
		`(?m)^sync-conflict-resolution\s+1\s+12m0s\s+165\s+120\s+10\s+30\s+5\s+\$[0-9.]+\s+0\s+0$`,
		stdout,
	)
	is.Regexp(
		`(?m)^combined\s+1\s+12m0s\s+165\s+120\s+10\s+30\s+5\s+\$[0-9.]+\s+0\s+0$`,
		stdout,
	)
}

func TestIntegrationTaskStatsKeepsTokenUsageWhenCostPricingIsUnknown(t *testing.T) {
	t.Parallel()

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

func TestIntegrationTaskStatsUsesPiReportedEstimatedCost(t *testing.T) {
	t.Parallel()

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
		Agent:    "pi-profile",
		Profile:  "pi-profile",
		Harness:  "pi",
		Model:    "openai-codex/gpt-5.5",
		Command:  "pi",
		Args:     []string{"--model", "openai-codex/gpt-5.5"},
		Branch:   "main",
		Worktree: repoDir,
	})
	must.NoError(err)
	_, err = stateStore.RecordRunUsage("alpha", "op-1", run.Attempt, taskstate.RecordRunUsageOptions{
		Session: &taskstate.AgentSession{ID: "pi-session", LogPath: "/fixture/pi.jsonl"},
		Usage: &taskstate.AgentUsage{
			InputTokens:           100,
			CachedInputTokens:     20,
			OutputTokens:          30,
			ReasoningOutputTokens: 5,
			TotalTokens:           130,
		},
		UsageCost: &taskstate.AgentUsageCost{
			Kind:           agent.UsageCostKindPiReportedEstimated,
			Currency:       "USD",
			AmountMicroUSD: 1240,
			Source:         "Pi usage.cost.total",
			Notes:          "Pi-reported estimate only; not exact billed cost or invoice reconciliation.",
		},
		UsageCapture: taskstate.AgentUsageCapture{
			Status: taskstate.UsageCaptureCaptured,
			Reason: "matched_pi_session",
		},
	})
	must.NoError(err)
	_, err = stateStore.FinishRun("alpha", "op-1", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)

	stdout, stderr := executeCommand(t, []string{"task", "stats", "op-1"})

	is.Empty(stderr)
	for _, want := range []string{
		"Estimated cost uses harness-reported estimates",
		"ESTIMATED_COST",
		"pi-profile", "pi", "openai-codex/gpt-5.5", "pi-session",
		"total=130 input=100 cached_input=20 output=30 reasoning_output=5",
		"Pi-reported estimated cost=$0.001240",
		"kind=pi_reported_estimated",
		"source=Pi usage.cost.total",
		"implementation", "1", "2m0s", "130", "100", "20", "30", "5", "$0.001240", "0", "0",
		"combined", "1", "2m0s", "130", "100", "20", "30", "5", "$0.001240", "0", "0",
	} {
		is.Contains(stdout, want)
	}
}

func TestIntegrationTaskStatsCountsMissingPiUsageCostAsUnknown(t *testing.T) {
	t.Parallel()

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
		Agent:    "pi-profile",
		Profile:  "pi-profile",
		Harness:  "pi",
		Model:    "openai-codex/gpt-5.5",
		Command:  "pi",
		Args:     []string{"--model", "openai-codex/gpt-5.5"},
		Branch:   "main",
		Worktree: repoDir,
	})
	must.NoError(err)
	_, err = stateStore.RecordRunUsage("alpha", "op-1", run.Attempt, taskstate.RecordRunUsageOptions{
		UsageCapture: taskstate.AgentUsageCapture{
			Status:         taskstate.UsageCaptureUnknown,
			Reason:         "matching_pi_session_has_no_assistant_usage",
			CandidateCount: 1,
		},
	})
	must.NoError(err)
	_, err = stateStore.FinishRun("alpha", "op-1", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)

	stdout, stderr := executeCommand(t, []string{"task", "stats", "op-1"})

	is.Empty(stderr)
	for _, want := range []string{
		"pi-profile", "pi", "openai-codex/gpt-5.5",
		"unknown: matching_pi_session_has_no_assistant_usage (candidates=1)",
		"implementation", "1", "2m0s", "0", "0", "0", "0", "0", "$0.000000", "1", "1",
		"combined", "1", "2m0s", "0", "0", "0", "0", "0", "$0.000000", "1", "1",
	} {
		is.Contains(stdout, want)
	}
}

func TestIntegrationTaskStatsAggregateGroupsResolvedTasksByDay(t *testing.T) {
	t.Parallel()

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
		"Task stats throughput view grouped by day",
		"Date anchor: task resolution",
		"Estimated cost uses harness-reported estimates",
		"Tasks without resolved timestamp: 1",
		"PERIOD", "RESOLVED", "WORKFLOW_MEDIAN", "WORKFLOW_P75", "WORKFLOW_COVERAGE",
	} {
		is.Contains(stdout, want)
	}
	is.NotContains(stdout, "Resolved Tasks")
	is.NotContains(stdout, "TREND")
	is.Regexp(`(?m)^2026-07-02\s+2\s+2h0m0s\s+2h0m0s\s+2/2$`, stdout)
	is.Regexp(`(?m)^2026-07-03\s+1\s+1h0m0s\s+1h0m0s\s+1/1$`, stdout)
	assertTaskStatsAggregateTableLinesWithinWidth(t, stdout, 100)

	stdout, stderr = executeCommand(t, []string{
		"task", "stats", "--group", "day", "--view", "consumption",
		"--from", "2026-07-02", "--to", "2026-07-02", "--repo", "alpha",
	})

	is.Empty(stderr)
	is.Contains(stdout, "Task stats consumption view grouped by day")
	is.Contains(stdout, "Filters: from=2026-07-02 to=2026-07-02 repo=alpha")
	is.Contains(stdout, "Executions without launch timestamp: 0")
	is.Regexp(`(?m)^2026-07-02\s+2\s+2\s+3K\s+1\.5K\s+2/2\s+\$0\.000773\s+\$0\.000773\s+1/2$`, stdout)
}

func TestIntegrationTaskStatsAggregateGroupsResolvedTasksByMonth(t *testing.T) {
	t.Parallel()

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
	is.Contains(stdout, "Task stats throughput view grouped by month")
	is.Contains(stdout, "Tasks without resolved timestamp: 0")
	is.Regexp(`(?m)^2026-07\s+1\s+-\s+-\s+0/1$`, stdout)
	is.Regexp(`(?m)^2026-08\s+1\s+-\s+-\s+0/1$`, stdout)
	assertTaskStatsAggregateTableLinesWithinWidth(t, stdout, 100)
}

func TestIntegrationTaskStatsAggregateRepoFilterSkipsUnselectedRepoFailures(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)
	alphaDir := filepath.Join(testutil.CanonicalTempDir(t), "alpha")
	betaDir := filepath.Join(testutil.CanonicalTempDir(t), "beta")
	must.NoError(os.MkdirAll(alphaDir, 0o755))
	must.NoError(os.MkdirAll(betaDir, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{
		{ID: "alpha", Name: "Alpha", Path: alphaDir, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "op"},
		{ID: "beta", Name: "Beta", Path: betaDir, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "bt"},
	}}))

	bdLogPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		alphaDir: {stdout: `[
			{"id":"op-1","title":"Selected","status":"closed","priority":1,"issue_type":"task","closed_at":"2026-07-02T12:00:00Z"}
		]`},
		betaDir: {stderr: "bd exploded", exitCode: 7},
	})

	stdout, stderr := executeCommand(t, []string{"task", "stats", "--group", "day", "--repo", "alpha"})

	is.Empty(stderr)
	is.Contains(stdout, "Task stats throughput view grouped by day")
	is.Contains(stdout, "Filters: repo=alpha")
	is.Regexp(`(?m)^2026-07-02\s+1\s+-\s+-\s+0/1$`, stdout)
	bdLog, err := os.ReadFile(bdLogPath)
	must.NoError(err)
	is.Contains(string(bdLog), alphaDir)
	is.NotContains(string(bdLog), betaDir)
}

func TestIntegrationTaskStatsAggregateReceivesOnlyTaskSourceItems(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[
			{"id":"op-task","title":"Task","status":"closed","priority":1,"issue_type":"task","closed_at":"2026-07-02T12:00:00Z"},
			{"id":"op-bug","title":"Bug","status":"closed","priority":1,"issue_type":"bug","closed_at":"2026-07-02T12:00:00Z"},
			{"id":"op-chore","title":"Chore","status":"closed","priority":1,"issue_type":"chore","closed_at":"2026-07-02T12:00:00Z"},
			{"id":"op-custom","title":"Custom","status":"closed","priority":1,"issue_type":"custom","closed_at":"2026-07-02T12:00:00Z"},
			{"id":"op-epic-closed","title":"Closed epic","status":"closed","priority":1,"issue_type":"epic","closed_at":"2026-07-02T12:00:00Z"},
			{"id":"op-epic-open","title":"Open epic","status":"open","priority":1,"issue_type":"epic"}
		]`},
	})

	epicStatePath, err := paths.DataPath(filepath.Join("repos", "alpha", "tasks", "op-epic-closed.yaml"))
	must.NoError(err)
	must.NoError(os.MkdirAll(filepath.Dir(epicStatePath), 0o755))
	must.NoError(os.WriteFile(epicStatePath, []byte("runs: [\n"), 0o644))

	stdout, stderr := executeCommand(t, []string{"task", "stats", "--group", "day"})

	is.Empty(stderr)
	is.Contains(stdout, "Task stats throughput view grouped by day")
	is.Contains(stdout, "Tasks without resolved timestamp: 0")
	is.Regexp(`(?m)^2026-07-02\s+1\s+-\s+-\s+0/1$`, stdout)
}

func TestIntegrationTaskStatsDirectEpicStatsRemainAvailable(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[
			{"id":"op-epic","title":"Closed epic","status":"closed","priority":1,"issue_type":"epic","closed_at":"2026-07-02T12:00:00Z"}
		]`},
	})

	statsNow := time.Time{}
	stateStore := taskstate.NewStoreWithClock(paths, func() time.Time { return statsNow })
	recordTaskStatsAggregateRun(t, stateStore, &statsNow, repoDir, taskStatsAggregateRunFixture{
		taskID:     "op-epic",
		model:      "gpt-5",
		startedAt:  time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC),
		finishedAt: time.Date(2026, 7, 2, 10, 5, 0, 0, time.UTC),
		usage: &taskstate.AgentUsage{
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
		},
	})

	stdout, stderr := executeCommand(t, []string{"task", "stats", "op-epic"})

	is.Empty(stderr)
	is.Contains(stdout, "Executions")
	is.Contains(stdout, "implementation")
	is.Contains(stdout, "gpt-5")
	is.Contains(stdout, "5m0s")
	is.Contains(stdout, "150")
}

func assertTaskStatsAggregateTableLinesWithinWidth(t *testing.T, output string, width int) {
	t.Helper()

	for _, line := range strings.Split(output, "\n") {
		if line == "" ||
			strings.HasPrefix(line, "Aggregate stats grouped by ") ||
			strings.HasPrefix(line, "Task stats ") ||
			strings.HasPrefix(line, "Date anchor: ") ||
			strings.HasPrefix(line, "Filters: ") ||
			strings.HasPrefix(line, "Estimated cost uses harness-reported estimates") ||
			strings.HasPrefix(line, "Tasks without resolved timestamp: ") ||
			strings.HasPrefix(line, "Executions without launch timestamp: ") {
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

func TestIntegrationTaskShowRendersClosedItemsAndHistory(t *testing.T) {
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

func TestIntegrationTaskShowRendersChronologicalHistoryForClosedEpic(t *testing.T) {
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
		Summary:              "Record task history",
		Description:          "Recorded completion history.",
		DetailedDescription:  "Detailed history.",
		TechnicalExplanation: "Technical explanation.",
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

func TestIntegrationTaskShowProjectsReviewAttemptMilestonesIntoHistory(t *testing.T) {
	t.Parallel()

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

func TestIntegrationTaskShowProjectsReviewFollowUpCreationIntoHistory(t *testing.T) {
	t.Parallel()

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

func TestIntegrationTaskShowFailsWhenLocalTaskStateCannotBeLoaded(t *testing.T) {
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

func TestIntegrationTaskShowRejectsUnsupportedItemsAtTaskSourceBoundary(t *testing.T) {
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
		Name:        "Alpha",
		Path:        repoDir,
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "op",
	}}}))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[{"id":"op-bug","title":"bug","status":"open","priority":2,"issue_type":"bug"}]`},
	})

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "show", "op-bug"})

	is.Empty(stdout)
	is.Empty(stderr)
	must.Error(err)
	is.ErrorContains(err, "unsupported task source item")
	is.ErrorContains(err, "issue type \"bug\" is not task or epic")
}

func TestIntegrationTaskDirPrintsWorktreeDirectory(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(testutil.CanonicalTempDir(t), "alpha")
	otherRepoDir := filepath.Join(testutil.CanonicalTempDir(t), "beta")
	worktreeDir := filepath.Join(testutil.CanonicalTempDir(t), "op-1-worktree")
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

func TestIntegrationTaskDirPrintsRepoRootForMainTask(t *testing.T) {
	t.Parallel()

	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(testutil.CanonicalTempDir(t), "alpha")
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

func TestIntegrationTaskDirReportsMalformedAndUnknownPrefixes(t *testing.T) {
	t.Parallel()

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

func TestIntegrationTaskDirReportsMissingAndInconsistentMetadata(t *testing.T) {
	t.Parallel()

	for _, tc := range taskDirMetadataErrorCases() {
		t.Run(tc.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			newTestState(t)
			paths := currentTestPaths(t)
			store := registry.NewStore(paths)

			repoDir := filepath.Join(testutil.CanonicalTempDir(t), "alpha")
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
			metadata:    `{"orpheus.worktree":"/fixture/op-incomplete"}`,
			wantMessage: "orpheus.branch is missing",
		},
		{
			name:        "inconsistent target",
			taskID:      "op-inconsistent",
			metadata:    `{"orpheus.branch":"main","orpheus.worktree":"/fixture/op-inconsistent"}`,
			wantMessage: "task Orpheus target metadata is inconsistent",
		},
	}
}

func TestIntegrationTaskRunReviewFollowUpAllowsDirtyMainTarget(t *testing.T) {
	t.Parallel()
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
	is.Equal("main", state.GitFacts.Branch)
	is.Equal(repoPath, state.GitFacts.Worktree)
	is.Equal("Resolving issues in op-followup Follow up dirty main", state.Runs[1].Execution.SessionName)
	must.NotNil(state.Runs[1].ReviewFollowUp)
	is.Equal([]int{0}, state.Runs[1].ReviewFollowUp.FindingIndexes)
}

func TestIntegrationTaskShowReviewDisplaysCrossAttemptFindingHistory(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	paths, repoPath := setupTaskShowReviewRepo(t, "op-main")
	seedTaskShowReviewState(t, paths, repoPath)

	stdout, stderr := executeCommand(t, []string{"task", "show", "review", "op-main"})

	is.Empty(stderr)
	for _, want := range []string{
		"Review state for op-main (repo alpha)",
		"Authoritative review history:",
		"Attempt 1: passed (1 authoritative finding(s))",
		"1/1 · manual · separate_task · created task op-41 · Older cleanup",
		"Attempt 2: blocked (4 authoritative finding(s))",
		"2/1 · unit-tests · blocking · open · Tests fail",
		"2/2 · ai-review · blocking · follow-up run 1 running · Race condition",
		"2/3 · ai-review · blocking · waived · Known limitation",
		"2/4 · ai-review · separate_task · created task op-42 · Extract helper",
		"orpheus task show review <task-id> <review-attempt> <finding-number>",
		"Next step: run `orpheus task run op-main` to address open blocking findings",
	} {
		is.Contains(stdout, want)
	}
}

func TestIntegrationTaskShowReviewGuidesRetryAfterFailedFollowUp(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	paths, repoPath := setupTaskShowReviewRepo(t, "op-retry")
	runStore := taskstate.NewStore(paths)

	review, err := runStore.StartReview("alpha", "op-retry")
	must.NoError(err)
	_, err = runStore.RecordReviewFinding("alpha", "op-retry", review.Attempt, taskstate.ReviewFinding{
		Type:        taskstate.FindingTypeBlocking,
		Title:       "Retry me",
		Description: "The first fix failed.",
	})
	must.NoError(err)
	_, err = runStore.FinishReview("alpha", "op-retry", review.Attempt, taskstate.ReviewStatusBlocked)
	must.NoError(err)
	failed, err := runStore.StartRun("alpha", "op-retry", taskstate.StartRunOptions{
		Agent:          "implementer",
		Branch:         "main",
		Worktree:       repoPath,
		ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: review.Attempt, FindingIndexes: []int{0}},
	})
	must.NoError(err)
	_, err = runStore.TargetReviewFindings("alpha", "op-retry", review.Attempt, []int{0}, failed.Attempt)
	must.NoError(err)
	_, err = runStore.FinishRun("alpha", "op-retry", failed.Attempt, taskstate.RunStatusFailed)
	must.NoError(err)

	stdout, stderr := executeCommand(t, []string{"task", "show", "review", "op-retry"})

	is.Empty(stderr)
	is.Contains(stdout, "follow-up run 1 failed · Retry me")
	is.Contains(stdout, "Next step: retry `orpheus task run op-retry`")
}

func TestIntegrationTaskShowReviewGuidesWhenTaskHasNoReviewAttempts(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	setupTaskShowReviewRepo(t, "op-empty")

	stdout, stderr := executeCommand(t, []string{"task", "show", "review", "op-empty"})

	is.Empty(stderr)
	is.Contains(stdout, "Review state for op-empty (repo alpha)")
	is.Contains(stdout, "No review attempts recorded for op-empty.")
	is.Contains(stdout, "Next step: run `orpheus task run op-empty` after task work is ready.")
}

func TestIntegrationTaskShowReviewRendersManuallyAddressedFinding(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	paths, _ := setupTaskShowReviewRepo(t, "op-addressed")
	runStore := taskstate.NewStore(paths)
	reviewAttempt, err := runStore.StartReviewWithOptions("alpha", "op-addressed", taskstate.StartReviewOptions{
		Pipeline: "default",
		Step:     "inspect",
	})
	must.NoError(err)
	_, err = runStore.RecordReviewFinding("alpha", "op-addressed", reviewAttempt.Attempt, taskstate.ReviewFinding{
		Type:        taskstate.FindingTypeBlocking,
		Title:       "Direct repair",
		Description: "Fixed outside the review loop.",
		Step:        "inspect",
	})
	must.NoError(err)
	_, err = runStore.FinishReview("alpha", "op-addressed", reviewAttempt.Attempt, taskstate.ReviewStatusBlocked)
	must.NoError(err)
	_, err = runStore.AddressReviewBlockingFindingManually("alpha", "op-addressed", reviewAttempt.Attempt, 0, "Verified in the worktree.")
	must.NoError(err)

	stdout, stderr := executeCommand(t, []string{"task", "show", "review", "op-addressed", "1", "1"})
	is.Empty(stderr)
	is.Contains(stdout, "Authoritative finding 1/1:")
	is.Contains(stdout, "Disposition: addressed manually: Verified in the worktree.")
}

func TestIntegrationTaskShowReviewGuidesPausedAutomatedBlockerDecision(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	paths, _ := setupTaskShowReviewRepo(t, "op-paused")
	runStore := taskstate.NewStore(paths)
	reviewAttempt, err := runStore.StartReviewWithOptions("alpha", "op-paused", taskstate.StartReviewOptions{
		Pipeline: "quality",
		Step:     "unit",
	})
	must.NoError(err)
	_, err = runStore.RecordReviewStep("alpha", "op-paused", reviewAttempt.Attempt, taskstate.RecordReviewStepOptions{
		Kind: taskstate.ReviewStepKindCheck,
		Name: "unit",
	})
	must.NoError(err)
	_, err = runStore.RecordReviewFinding("alpha", "op-paused", reviewAttempt.Attempt, taskstate.ReviewFinding{
		Type:        taskstate.FindingTypeBlocking,
		Title:       "Check failed",
		Description: "make test failed.",
		Step:        "unit",
	})
	must.NoError(err)
	_, err = runStore.PauseReviewForAutomatedBlockerDecision("alpha", "op-paused", reviewAttempt.Attempt, "unit")
	must.NoError(err)

	stdout, stderr := executeCommand(t, []string{"task", "show", "review", "op-paused", "1"})

	is.Empty(stderr)
	is.Contains(stdout, "Automated blocker decisions: paused")
	is.Contains(stdout, "Next step: automated blocker decision is paused; run `orpheus task run op-paused` to resume step unit.")
}

func TestIntegrationTaskShowReviewGuidesInterruptedAutomatedBlockerDecision(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	paths, _ := setupTaskShowReviewRepo(t, "op-interrupted")
	runStore := taskstate.NewStore(paths)
	reviewAttempt, err := runStore.StartReviewWithOptions("alpha", "op-interrupted", taskstate.StartReviewOptions{
		Pipeline: "quality",
		Step:     "unit",
	})
	must.NoError(err)
	_, err = runStore.RecordReviewFinding("alpha", "op-interrupted", reviewAttempt.Attempt, taskstate.ReviewFinding{
		Type:        taskstate.FindingTypeBlocking,
		Title:       "Check failed",
		Description: "make test failed.",
		Step:        "unit",
	})
	must.NoError(err)
	_, err = runStore.MarkReviewAutomatedBlockerDecisionInterrupted("alpha", "op-interrupted", reviewAttempt.Attempt)
	must.NoError(err)
	_, err = runStore.FinishReview("alpha", "op-interrupted", reviewAttempt.Attempt, taskstate.ReviewStatusBlocked)
	must.NoError(err)

	stdout, stderr := executeCommand(t, []string{"task", "show", "review", "op-interrupted", "1"})

	is.Empty(stderr)
	is.Contains(stdout, "Automated blocker decisions: interrupted")
	is.Contains(stdout, "Next step: automated blocker decisions were interrupted; run `orpheus task run op-interrupted` to start a fresh review.")
}

func TestIntegrationTaskShowReviewDisplaysClosedTaskReviewState(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	paths, repoPath := setupTaskShowReviewRepoWithStatus(t, "op-main", "closed")
	seedTaskShowReviewState(t, paths, repoPath)

	stdout, stderr := executeCommand(t, []string{"task", "show", "review", "op-main"})

	is.Empty(stderr)
	is.Contains(stdout, "Review state for op-main (repo alpha)")
	is.Contains(stdout, "Authoritative review history:")
	is.Contains(stdout, "Attempt 2: blocked")
	is.Contains(stdout, "2/1 · unit-tests · blocking · open · Tests fail")
	is.Contains(stdout, "2/4 · ai-review · separate_task · created task op-42 · Extract helper")
}

func setupTaskShowReviewRepo(t *testing.T, taskID string) (state.Paths, string) {
	t.Helper()

	return setupTaskShowReviewRepoWithStatus(t, taskID, "in_progress")
}

func setupTaskShowReviewRepoWithStatus(t *testing.T, taskID string, status string) (state.Paths, string) {
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
		repoPath: {stdout: taskShowReviewTaskJSON(taskID, repoPath, status)},
	})
	return paths, repoPath
}

func taskShowReviewTaskJSON(taskID string, repoPath string, status string) string {
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

func seedTaskShowReviewState(t *testing.T, paths state.Paths, repoPath string) {
	t.Helper()

	must := require.New(t)
	runStore := taskstate.NewStore(paths)
	oldReview := recordCreatedReviewFollowUp(t, runStore)
	_, err := runStore.FinishReview("alpha", "op-main", oldReview.Attempt, taskstate.ReviewStatusPassed)
	must.NoError(err)

	latestReview := recordMixedReviewFindings(t, runStore)
	latestReview, err = runStore.MarkReviewAutomatedBlockerDecisionKept("alpha", "op-main", latestReview.Attempt)
	must.NoError(err)
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

func TestIntegrationTaskReviewRejectsStaleMetadataMirror(t *testing.T) {
	t.Parallel()
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
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review approval", "Finalize after approval.")

	staleWorktree := filepath.Join(root, "stale-worktree")
	taskJSON := mainReadyTaskJSON("op-main", staleWorktree)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, stderr, err := executeCommandWithInputAndError(t, []string{"task", "run", "op-main"}, []byte("a\n"))

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "task run op-main: task op-main metadata target is invalid")
	is.ErrorContains(err, taskmodel.MetadataWorktree+"="+strconv.Quote(staleWorktree))

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	_, ok := taskstate.LatestReview(state)
	is.False(ok)
}

func TestIntegrationTaskReviewRejectsStagedCandidateChanges(t *testing.T) {
	t.Parallel()
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

	stdout, stderr, err := executeCommandWithInputAndError(t, []string{"task", "run", "op-main"}, []byte("a\n"))

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "review requires a clean Git index")
	is.ErrorContains(err, "orpheus task run <task-id>")
	is.NotContains(err.Error(), "orpheus task review")
	is.Contains(runGit(t, repoPath, "status", "--short"), "A  reviewed.txt")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	_, ok := taskstate.LatestReview(state)
	is.False(ok)
}

func TestIntegrationTaskReviewRejectsMissingCandidateChangesWithoutFinalizationCommit(t *testing.T) {
	t.Parallel()
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

	stdout, stderr, err := executeCommandWithInputAndError(t, []string{"task", "run", "op-main"}, []byte("a\n"))

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "has no candidate changes to review")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	_, ok := taskstate.LatestReview(state)
	is.False(ok)
}

func TestIntegrationTaskReviewRestoresCandidateChangesMutatedDuringManualStep(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
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
		input:    bytes.NewBufferString("b\nMutating finding\nThe step changed files\nRestore it\nf\n"),
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

	stdout, stderr, err := executeCommandWithReaderAndError(t, []string{"task", "run", "op-main"}, input)

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

func TestIntegrationTaskRunUsesSeparateTaskProposalSelection(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	must.NoError(err)
	orpheusBin := buildOrpheusTestBinary(t, sourceRoot)
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

	implementer := writeReviewScript(t, fmt.Sprintf(`#!/bin/sh
printf 'reviewed\n' > reviewed.txt
%s agent done --summary "Implementation" --description "Implemented." --detailed-description "Implemented details." --technical-explanation "Implemented the review fixture."
`, shellQuote(orpheusBin)))
	reviewer := writeReviewScript(t, fmt.Sprintf(`#!/bin/sh
%s agent review add \
  --type separate-task \
  --title "Extract helper" \
  --description "The helper can be extracted later." \
  --task-title "Extract shared helper" \
  --task-description "Create a shared helper." \
  --task-acceptance-criteria "Helper is covered by tests."
`, shellQuote(orpheusBin)))
	must.NoError(testutil.WriteConfigYAML(paths, agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"implementer": "implementer",
				"reviewer":    "reviewer",
			},
			"profiles": map[string]any{
				"implementer": map[string]any{"command": implementer, "interactive": false},
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
					"steps": []map[string]any{{"kind": "agent_review", "name": "ai-review"}},
				},
			},
		},
	}))

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox list --all --limit 0", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	withFakeGHPRResponses(t, fakeGHPRResponses{listStdout: "[]", createStdout: "https://github.test/org/alpha/pull/1\n"})
	stdout, stderr := executeCommandWithScriptedInput(t, []string{"task", "run", "--repo-root", "op-main"}, "", "n\n")

	is.Contains(stdout, "Published op-main")
	is.Contains(stderr, "Separate-task review findings can be created as standalone Beads")
	is.Contains(stderr, "Create follow-up Beads [numbers, a=all, n=none]")
	is.NotContains(stderr, "Created follow-up Bead")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeSeparateTask, latest.Findings[0].Type)
	is.Empty(latest.Findings[0].CreatedTaskID)
}

func TestIntegrationTaskReviewPassingCheckContinuesToManualStep(t *testing.T) {
	t.Parallel()
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

	stdout, stderr := executeCommandWithInput(t, []string{"task", "run", "--pipeline", "standard", "op-main"}, "a\n")

	is.Contains(stdout, "check stdout 1 unit")
	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "== Review step: unit (check) ==")
	is.Contains(stderr, "◆ REVIEW STEP · approval (manual)")
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

func TestIntegrationTaskReviewConfirmedManualCommandRunsAndRecordsStep(t *testing.T) {
	t.Parallel()
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

	stdout, stderr := executeCommandWithInput(t, []string{"task", "run", "--pipeline", "standard", "op-main"}, "\na\n")

	is.Contains(stdout, "manual command ran inspect")
	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "◆ REVIEW STEP · inspect (manual)")
	is.Contains(stderr, "TASK  op-main — Ready for task done")
	is.Contains(stderr, "DESCRIPTION\nConfirm before command.")
	is.Contains(stderr, "TECHNICAL EXPLANATION\nTechnical explanation.")
	is.Contains(stderr, "≡ GIT STATUS --SHORT")
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

func TestIntegrationTaskReviewImportsHunkBlockingNoteAndBlocksApproval(t *testing.T) {
	// Hunk import drives a multi-step review and finalization lock lifecycle.
	// Keep it serial to prevent another CLI scenario from interleaving that state.
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
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review Hunk blocker", "Import blocking notes.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))
	installFakeHunkNotes(t, `{"comments":[{"noteId":"user:1","source":"user","filePath":"README.md","newRange":[12,12],"body":"This must be fixed before publication.","author":"user","createdAt":"2026-07-09T00:00:00.000Z","editable":true}]}`)

	manual := writeReviewScript(t, "#!/bin/sh\nprintf 'hunk command ran\\n'\n")
	writeReviewPipelineConfig(t, paths, "standard", map[string][]map[string]any{
		"standard": []map[string]any{
			{"kind": "manual", "name": "inspect", "command": manual, "hunk_notes": true},
		},
	})

	setReviewMaxAutonomousAttempts(t, paths, 1)
	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "run", "--pipeline", "standard", "op-main"}, "\nb\nf\n")

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

func TestIntegrationTaskReviewImportsHunkAdvisoryNoteAndAllowsApproval(t *testing.T) {
	t.Parallel()
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

	stdout, stderr := executeCommandWithInput(t, []string{"task", "run", "--pipeline", "standard", "op-main"}, "\nv\na\n")

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

func TestIntegrationTaskReviewImportsHunkSeparateTaskNoteAndCreatesFollowUp(t *testing.T) {
	t.Parallel()
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
	stdout, stderr := executeCommandWithInput(t, []string{"task", "run", "--pipeline", "standard", "op-main"}, input)

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

func TestIntegrationTaskReviewHunkManualCommandWithNoCapturedNotesContinuesPrompt(t *testing.T) {
	t.Parallel()
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

	stdout, stderr := executeCommandWithInput(t, []string{"task", "run", "--pipeline", "standard", "op-main"}, "\na\n")

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

func TestIntegrationTaskReviewDeclinedManualCommandAbortsWithoutRunningCommand(t *testing.T) {
	t.Parallel()
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

	stdout, stderr := executeCommandWithInput(t, []string{"task", "run", "--pipeline", "standard", "op-main"}, "n\n")

	is.Empty(stdout)
	is.Contains(stderr, "◆ REVIEW STEP · inspect (manual)")
	is.Contains(stderr, "TASK  op-main — Ready for task done")
	is.Contains(stderr, "DESCRIPTION\nDecline command.")
	is.Contains(stderr, "TECHNICAL EXPLANATION\nTechnical explanation.")
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

func TestIntegrationTaskReviewManualCommandEOFConfirmationHandlesUnavailableInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		input       []byte
		wantMessage string
		wantStatus  taskstate.ReviewStatus
	}{
		{
			name:        "empty EOF",
			input:       nil,
			wantMessage: "Review for op-main is waiting for manual step \"inspect\" because manual review input is unavailable.",
			wantStatus:  taskstate.ReviewStatusWaitingForManual,
		},
		{
			name:        "decline without newline",
			input:       []byte("n"),
			wantMessage: "Review aborted for op-main.",
			wantStatus:  taskstate.ReviewStatusAborted,
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
				[]string{"task", "run", "--pipeline", "standard", "op-main"},
				test.input,
			)

			must.NoError(err)
			is.Empty(stdout)
			is.Contains(stderr, "Run manual command for step \"inspect\"")
			is.Contains(stderr, test.wantMessage)
			is.NotContains(stderr, "Review action")
			_, statErr := os.Stat(markerPath)
			is.ErrorIs(statErr, os.ErrNotExist)

			var state taskstate.TaskState
			must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
			latest, ok := taskstate.LatestReview(state)
			must.True(ok)
			is.Equal(test.wantStatus, latest.Status)
			is.Empty(latest.Steps)
		})
	}
}

func TestIntegrationTaskReviewNonZeroCheckRecordsBlockingFindingAndStops(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
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

	stdout, stderr := executeCommandWithInput(t, []string{"task", "run", "op-main"}, "")

	is.Contains(stdout, "failing check output")
	is.Contains(stderr, "== Review step: unit (check) ==")
	is.Contains(stderr, "Automated blocking findings from step \"unit\"")
	is.Contains(stderr, "Automated blocker decisions for op-main were interrupted")
	is.Contains(stderr, "Review blocked for op-main by check \"unit\".")
	is.NotContains(stderr, "Review action")
	is.Equal(headBefore, strings.TrimSpace(runGit(t, repoPath, "rev-parse", "HEAD")))

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	is.True(latest.AutomatedBlockerDecisionInterrupted)
	must.Len(latest.Steps, 1)
	must.NotNil(latest.Steps[0].ExitCode)
	is.Equal(7, *latest.Steps[0].ExitCode)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Equal("unit", latest.Findings[0].Step)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationTaskReviewAgentReviewStepCapturesCodexUsage(t *testing.T) {
	t.Parallel()
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
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review agent", "Run attached reviewer.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))

	withFakeAgent(t, "codex", 0)
	codexHome := testutil.CanonicalTempDir(t)
	setTestEnvironment(t, "CODEX_HOME", codexHome)
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

	stdout, stderr := executeCommandWithInput(t, []string{"task", "run", "op-main"}, "")

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

func TestIntegrationTaskReviewAgentReviewStepCapturesPiUsage(t *testing.T) {
	t.Parallel()
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
	recordMainCompletion(t, paths, "alpha", "op-main", repoPath, "Review agent", "Run attached reviewer.")
	must.NoError(os.WriteFile(filepath.Join(repoPath, "reviewed.txt"), []byte("reviewed\n"), 0o644))

	withFakeAgent(t, "pi", 0)
	piSessionDir := testutil.CanonicalTempDir(t)
	setTestEnvironment(t, "PI_CODING_AGENT_SESSION_DIR", piSessionDir)
	sessionPath := filepath.Join(piSessionDir, "review-pi-session.jsonl")
	writePiSessionLogForCLI(t, sessionPath, repoPath, "review-pi-session", time.Now().UTC())
	writeStructuredPiReviewAgentPipelineConfig(t, paths, "pi", "openai-codex/gpt-5.5", false, "standard", map[string][]map[string]any{
		"standard": []map[string]any{{"kind": "agent_review", "name": "ai-review"}},
	})

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	stdout, stderr := executeCommandWithInput(t, []string{"task", "run", "op-main"}, "")

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
	is.Equal("pi", execution.Harness)
	is.Equal("openai-codex/gpt-5.5", execution.Model)
	must.NotNil(execution.Session)
	is.Equal("review-pi-session", execution.Session.ID)
	is.Equal(sessionPath, execution.Session.LogPath)
	must.NotNil(execution.Usage)
	is.Equal(180, execution.Usage.TotalTokens)
	must.NotNil(execution.UsageCost)
	is.Equal(int64(1240), execution.UsageCost.AmountMicroUSD)
	is.Equal(taskstate.UsageCaptureCaptured, execution.UsageCapture.Status)
	is.Equal("matched_pi_session", execution.UsageCapture.Reason)
	is.Equal(1, execution.UsageCapture.CandidateCount)
}

func TestIntegrationTaskStartActivatesEligibleEpicAndRejectsOrdinaryTask(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	newTestState(t)
	repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")
	logPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[
			{"id":"op-epic","title":"Release","status":"open","issue_type":"epic","parent":"op-parent","dependencies":[{"id":"op-dependency","dependency_type":"blocks"}]},
			{"id":"op-parent","title":"Portfolio","status":"in_progress","issue_type":"epic"},
			{"id":"op-dependency","title":"Prerequisite","status":"closed","issue_type":"task"}
		]`},
	})

	stdout, stderr := executeCommand(t, []string{"task", "start", "op-epic"})

	is.Empty(stderr)
	is.Equal("Epic op-epic started.\n", stdout)
	log := readFileString(t, logPath)
	is.Equal(4, strings.Count(log, "--json --readonly --sandbox show --id"))
	is.Contains(log, "--json --sandbox update op-epic --status in_progress")

	newTestState(t)
	repoDir = registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")
	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoDir: {stdout: `[{"id":"op-task","title":"Implementation","status":"open","issue_type":"task"}]`},
	})
	_, _, err := executeCommandWithError(t, []string{"task", "start", "op-task"})
	is.Error(err)
	is.ErrorContains(err, "item is not an epic")
	is.ErrorContains(err, "use `orpheus task run op-task` for the normal task workflow")
}

func TestIntegrationTaskStartHonorsParentDependencyAndIdempotency(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		output  string
		wantOut string
		wantErr string
	}{
		{
			name: "parent not active",
			output: `[
				{"id":"op-epic","status":"open","issue_type":"epic","parent":"op-parent"},
				{"id":"op-parent","status":"open","issue_type":"epic"}
			]`,
			wantErr: "parent epic op-parent must be in progress",
		},
		{
			name: "dependency remains active",
			output: `[
				{"id":"op-epic","status":"open","issue_type":"epic","dependencies":[{"id":"op-dependency","dependency_type":"blocks"}]},
				{"id":"op-dependency","status":"in_progress","issue_type":"task"}
			]`,
			wantErr: "blocking dependencies are not closed: op-dependency",
		},
		{
			name:    "already in progress",
			output:  `[{"id":"op-epic","status":"in_progress","issue_type":"epic"}]`,
			wantOut: "Epic op-epic is already in progress.\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			newTestState(t)
			repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")
			logPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{repoDir: {stdout: tt.output}})

			stdout, stderr, err := executeCommandWithError(t, []string{"task", "start", "op-epic"})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("task start error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("task start error = %v", err)
			}
			if stderr != "" || stdout != tt.wantOut {
				t.Fatalf("task start output = (%q, %q), want (%q, empty)", stdout, stderr, tt.wantOut)
			}
			if strings.Contains(readFileString(t, logPath), "--json --sandbox update") {
				t.Fatal("idempotent start unexpectedly updated the task source")
			}
		})
	}
}

func TestIntegrationTaskCloseRequiresVerifiedClosedChildrenAndIsIdempotent(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name    string
		output  string
		wantOut string
		wantErr string
	}{
		{
			name: "closes fully completed epic",
			output: `[
				{"id":"op-epic","status":"in_progress","issue_type":"epic","child_count":2},
				{"id":"op-child-a","status":"closed","issue_type":"task","parent":"op-epic"},
				{"id":"op-child-b","status":"closed","issue_type":"epic","parent":"op-epic"}
			]`,
			wantOut: "Epic op-epic closed.\n",
		},
		{
			name: "reports active child ids",
			output: `[
				{"id":"op-epic","status":"in_progress","issue_type":"epic","child_count":2},
				{"id":"op-child-z","status":"open","issue_type":"task","parent":"op-epic"},
				{"id":"op-child-a","status":"in_progress","issue_type":"task","parent":"op-epic"}
			]`,
			wantErr: "direct child items are still active: op-child-a, op-child-z",
		},
		{
			name: "refuses incomplete child listing",
			output: `[
				{"id":"op-epic","status":"in_progress","issue_type":"epic","child_count":2},
				{"id":"op-child","status":"closed","issue_type":"task","parent":"op-epic"}
			]`,
			wantErr: "source reports 2 child items but only 1 could be inspected",
		},
		{
			name:    "already closed",
			output:  `[{"id":"op-epic","status":"closed","issue_type":"epic"}]`,
			wantOut: "Epic op-epic is already closed.\n",
		},
		{
			name:    "ordinary task uses normal workflow guidance",
			output:  `[{"id":"op-epic","status":"in_progress","issue_type":"task"}]`,
			wantErr: "use `orpheus task run op-epic` for the normal task workflow",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			newTestState(t)
			repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")
			logPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{repoDir: {stdout: tt.output}})

			stdout, stderr, err := executeCommandWithError(t, []string{"task", "close", "op-epic"})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("task close error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("task close error = %v", err)
			}
			if stderr != "" || stdout != tt.wantOut {
				t.Fatalf("task close output = (%q, %q), want (%q, empty)", stdout, stderr, tt.wantOut)
			}
			log := readFileString(t, logPath)
			if tt.name == "closes fully completed epic" && !strings.Contains(log, "--json --sandbox close op-epic") {
				t.Fatalf("close command missing from bd log:\n%s", log)
			}
			if tt.name == "already closed" && strings.Contains(log, "--json --sandbox close") {
				t.Fatal("idempotent close unexpectedly updated the task source")
			}
		})
	}
}

func TestIntegrationTaskEpicLifecycleAdapterFailuresAreSourceNeutral(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name      string
		command   string
		status    string
		wantError string
	}{
		{name: "start", command: "start", status: "open", wantError: "cannot start epic op-epic"},
		{name: "close", command: "close", status: "in_progress", wantError: "cannot close epic op-epic"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			newTestState(t)
			repoDir := registerLocalTaskTestRepo(t, "alpha", "Alpha", "op")
			withFailingEpicLifecycleMutation(t, repoDir, tt.status)

			stdout, stderr, err := executeCommandWithError(t, []string{"task", tt.command, "op-epic"})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("task %s error = %v, want containing %q", tt.command, err, tt.wantError)
			}
			if stdout != "" || stderr != "" {
				t.Fatalf("task %s output = (%q, %q), want empty", tt.command, stdout, stderr)
			}
			for _, forbidden := range []string{"Beads", repoDir, "bd update", "bd close"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Errorf("task %s error = %q, must not expose %q", tt.command, err, forbidden)
				}
			}
		})
	}
}

func withFailingEpicLifecycleMutation(t *testing.T, repoDir string, status string) {
	t.Helper()

	binDir := testutil.CanonicalTempDir(t)
	bdPath := filepath.Join(binDir, "bd")
	taskJSON := fmt.Sprintf(`[{"id":"op-epic","status":%q,"issue_type":"epic"}]`, status)
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  "--json --readonly --sandbox show --id "*)
    printf '%%s\n' %s
    ;;
  "--json --readonly --sandbox list --all --limit 0 --type task")
    printf '%%s\n' %s
    ;;
  "--json --readonly --sandbox list --all --limit 0 --type epic")
    printf '[]\n'
    ;;
  "--json --sandbox update "*|"--json --sandbox close "*)
    printf 'Beads mutation failed in %s while running bd %%s\n' "$*" >&2
    exit 17
    ;;
  *)
    printf 'unexpected bd command: %%s\n' "$*" >&2
    exit 64
    ;;
esac
`, shellQuote(taskJSON), shellQuote(taskJSON), shellQuote(filepath.Join(repoDir, "backend-data")))
	if err := writeTestExecutable(bdPath, []byte(script)); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	prependTestPath(t, binDir)
}

func withFailingTaskShowChildren(t *testing.T, repoDir string) {
	t.Helper()

	binDir := testutil.CanonicalTempDir(t)
	bdPath := filepath.Join(binDir, "bd")
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
  "--json --readonly --sandbox show --id op-epic")
    printf '%%s\n' %s
    ;;
  "--json --readonly --sandbox list --all --limit 0 --type task")
    printf 'backend unavailable\n' >&2
    exit 17
    ;;
  *)
    printf 'unexpected bd command: %%s\n' "$*" >&2
    exit 64
    ;;
esac
`, shellQuote(`[{"id":"op-epic","title":"Epic","status":"open","issue_type":"epic"}]`))
	if err := writeTestExecutable(bdPath, []byte(script)); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	prependTestPath(t, binDir)
}

func withFakeBDTaskResponses(t *testing.T, responses map[string]fakeBDTaskResponse) string {
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
is_update=0
case "$*" in
  "--json --readonly --sandbox list --all --limit 0 --type task"*)
    ;;
  "--json --readonly --sandbox list --all --limit 0 --type epic"*)
    printf '[]\n'
    exit 0
    ;;
  "--json --readonly --sandbox show --id "*)
    ;;
  "--json --sandbox update "*|"--json --sandbox close "*)
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
	if err := writeTestExecutable(bdPath, []byte(script.String())); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	setTestEnvironment(t, "FAKE_BD_LOG", logPath)
	prependTestPath(t, binDir)
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
	fmt.Fprintf(script, "  %s)\n", shellQuote(canonicalFixturePath(t, dir)))
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

	binDir := testutil.CanonicalTempDir(t)
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

	script += fmt.Sprintf(fakeGHPRScriptFooter, shellQuote(statusStdoutPath), responses.statusExit)

	ghPath := filepath.Join(binDir, "gh")
	if err := writeTestExecutable(ghPath, []byte(script)); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	setTestEnvironment(t, "FAKE_GH_LOG", logPath)
	prependTestPath(t, binDir)
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
		Summary:              summary,
		Description:          description,
		DetailedDescription:  "Detailed PR body.",
		TechnicalExplanation: "Technical explanation.",
	}); err != nil {
		t.Fatalf("complete main run: %v", err)
	}
	if _, err := store.FinishRun(repoID, taskID, attempt.Attempt, taskstate.RunStatusSucceeded); err != nil {
		t.Fatalf("finish main run: %v", err)
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

func withFakeAgent(t *testing.T, name string, exitCode int) string {
	t.Helper()

	binDir := testutil.CanonicalTempDir(t)
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
	if err := writeTestExecutable(agentPath, []byte(script)); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}
	setTestEnvironment(t, "FAKE_AGENT_LOG", logPath)
	setTestEnvironment(t, testguard.FakeAgentEnvKey(name), agentPath)
	prependTestPath(t, binDir)
	return logPath
}

func writeCodexSessionLogForCLI(t *testing.T, path string, cwd string, sessionID string, startedAt time.Time) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create codex session log directory: %v", err)
	}
	timestamp := startedAt.UTC().Format(time.RFC3339Nano)
	content := `{"timestamp":"` + timestamp + `","type":"session_meta","payload":{"session_id":"` + sessionID + `","id":"` + sessionID + `","timestamp":"` + timestamp + `","cwd":"` + cwd + `","model":"gpt-5"}}
{"timestamp":"` + timestamp + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":123,"cached_input_tokens":45,"output_tokens":67,"reasoning_output_tokens":8,"total_tokens":190}}}}
`
	writeTestFile(t, path, content, "codex session log")
}

func writePiSessionLogForCLI(t *testing.T, path string, cwd string, sessionID string, startedAt time.Time) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create pi session log directory: %v", err)
	}
	timestamp := startedAt.UTC().Format(time.RFC3339Nano)
	content := strings.Join([]string{
		`{"type":"session","version":3,"id":"` + sessionID + `","timestamp":"` + timestamp + `","cwd":"` + cwd + `"}`,
		`{"type":"model_change","id":"model","timestamp":"` + timestamp + `","provider":"openai-codex","modelId":"gpt-5.5"}`,
		`{"type":"message","id":"assistant-1","timestamp":"` + timestamp + `","message":{"role":"assistant","usage":{"input":100,"output":20,"cacheRead":10,"cacheWrite":3,"reasoning":5,"totalTokens":120,"cost":{"total":0.001234}}}}`,
		`{"type":"message","id":"assistant-2","timestamp":"` + timestamp + `","message":{"role":"assistant","usage":{"input":50,"output":10,"cacheRead":7,"cacheWrite":0,"reasoning":0,"totalTokens":60,"cost":{"total":0.000006}}}}`,
		"",
	}, "\n")
	writeTestFile(t, path, content, "pi session log")
}

func writeTaskRunAgentConfig(t *testing.T, paths state.Paths, name string, command string, args []string) {
	t.Helper()

	profile := map[string]any{"command": command}
	if args != nil {
		profile["args"] = args
	}
	config := map[string]any{}
	if err := paths.ReadConfigYAML(agent.ConfigFile, &config); err != nil && !strings.Contains(err.Error(), "file does not exist") {
		require.NoError(t, err)
	}
	agentsConfig, _ := config["agents"].(map[string]any)
	if agentsConfig == nil {
		agentsConfig = map[string]any{}
	}
	defaults, _ := agentsConfig["defaults"].(map[string]any)
	if defaults == nil {
		defaults = map[string]any{}
	}
	defaults["implementer"] = name
	profiles, _ := agentsConfig["profiles"].(map[string]any)
	if profiles == nil {
		profiles = map[string]any{}
	}
	profiles[name] = profile
	agentsConfig["defaults"] = defaults
	agentsConfig["profiles"] = profiles
	config["agents"] = agentsConfig
	require.NoError(t, testutil.WriteConfigYAML(paths, agent.ConfigFile, config))
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
	require.NoError(t, testutil.WriteConfigYAML(paths, reviewconfig.ConfigFile, map[string]any{
		"reviews": map[string]any{
			"default_pipeline": defaultPipeline,
			"pipelines":        configPipelines,
		},
	}))
}

func setReviewMaxAutonomousAttempts(t *testing.T, paths state.Paths, maxAttempts int) {
	t.Helper()

	var config map[string]any
	err := paths.ReadConfigYAML(reviewconfig.ConfigFile, &config)
	if err != nil && strings.Contains(err.Error(), "file does not exist") {
		config = map[string]any{
			"reviews": map[string]any{
				"default_pipeline":               "default",
				"max_autonomous_review_attempts": maxAttempts,
				"pipelines": map[string]any{
					"default": map[string]any{"steps": []map[string]any{{"kind": "manual", "name": "local-review"}}},
				},
			},
		}
		require.NoError(t, testutil.WriteConfigYAML(paths, reviewconfig.ConfigFile, config))
		return
	}
	require.NoError(t, err)
	reviews, ok := config["reviews"].(map[string]any)
	require.True(t, ok, "reviews config is missing")
	reviews["max_autonomous_review_attempts"] = maxAttempts
	require.NoError(t, testutil.WriteConfigYAML(paths, reviewconfig.ConfigFile, config))
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
	require.NoError(t, testutil.WriteConfigYAML(paths, agent.ConfigFile, map[string]any{
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

func writeStructuredPiReviewAgentPipelineConfig(
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
	require.NoError(t, testutil.WriteConfigYAML(paths, agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"implementer": "implementer",
				"reviewer":    reviewerName,
			},
			"profiles": map[string]any{
				"implementer": map[string]any{"command": "unused-implementer"},
				reviewerName: map[string]any{
					"harness":     "pi",
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

func writeReviewScript(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(testutil.CanonicalTempDir(t), "review-step")
	if err := writeTestExecutable(path, []byte(content)); err != nil {
		t.Fatalf("write review script: %v", err)
	}
	return path
}

func buildOrpheusTestBinary(t *testing.T, sourceRoot string) string {
	t.Helper()

	binPath := filepath.Join(testutil.CanonicalTempDir(t), "orpheus")
	command := exec.Command("go", "build", "-o", binPath, "./cmd/orpheus")
	command.Dir = sourceRoot
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build orpheus test binary: %v\n%s", err, output)
	}
	return binPath
}

func installFakeHunkNotes(t *testing.T, response string) {
	t.Helper()

	binDir := testutil.CanonicalTempDir(t)
	hunkPath := filepath.Join(binDir, "hunk")
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "session" ] && [ "$2" = "comment" ] && [ "$3" = "list" ]; then
  printf '%%s\n' %s
  exit 0
fi
printf 'unexpected fake hunk call: %%s\n' "$*" >&2
exit 65
`, shellQuote(response))
	require.NoError(t, writeTestExecutable(hunkPath, []byte(script)))
	prependTestPath(t, binDir)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
