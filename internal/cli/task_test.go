package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/registry"
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

func TestTaskListListsActiveTasksAcrossRegisteredReposWithDefaultAndDetailedTables(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
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
		{
			ID:          "local-alpha",
			Name:        "Local Alpha",
			Path:        localDir,
			BeadsMode:   registry.BeadsModeLocal,
			BeadsPrefix: "la",
		},
		{
			ID:          "managed-beta",
			Name:        "Managed Beta",
			Path:        managedRepoPath,
			BeadsMode:   registry.BeadsModeManaged,
			BeadsPrefix: "mb",
		},
	}}))

	logPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		localDir: {stdout: `[
			{"id":"la-1","title":"Local active","status":"open","priority":2,"issue_type":"task","metadata":{"orpheus.branch":"task/la-1","orpheus.worktree":"/tmp/la-1"}},
			{"id":"la-closed","title":"Closed local task","status":"closed","priority":1,"issue_type":"task"},
			{"id":"la-bug","title":"Local bug","status":"open","priority":1,"issue_type":"bug"}
		]`},
		managedDir: {stdout: `[
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
	} {
		is.NotContains(stdout, hidden)
	}
	is.NotContains(stdout, "la-closed")
	is.NotContains(stdout, "orpheus.branch")

	detailedStdout, detailedStderr := executeCommand(t, []string{"task", "list", "--details"})

	is.Empty(detailedStderr)
	for _, want := range []string{
		"REPO_ID", "REPO", "TASK_PREFIX", "TASK_ID", "STATUS", "P", "BRANCH", "WORKTREE", "PR", "TITLE",
		"local-alpha", "Local Alpha", "la", "la-1", "open", "2", "task/la-1", "/tmp/la-1", "Local active",
		"local-alpha", "Local Alpha", "la", "la-bug", "open", "1", "Local bug",
		"managed-beta", "Managed Beta", "mb", "mb-1", "in_progress", "3", "https://example.test/pr/1", "Managed active",
	} {
		is.Contains(detailedStdout, want)
	}
	localDetail := regexp.MustCompile(`(?m)^local-alpha\s+Local Alpha\s+la\s+la-1\s+open\s+2\s+task/la-1\s+/tmp/la-1\s+-\s+Local active$`)
	managedDetail := regexp.MustCompile(`(?m)^managed-beta\s+Managed Beta\s+mb\s+mb-1\s+in_progress\s+3\s+-\s+-\s+https://example\.test/pr/1\s+Managed active$`)
	is.True(localDetail.MatchString(detailedStdout), "local detail row should show absent PR metadata as -")
	is.True(managedDetail.MatchString(detailedStdout), "managed detail row should show absent branch/worktree metadata as -")
	is.NotContains(detailedStdout, "branch=task/la-1")
	is.NotContains(detailedStdout, "worktree=/tmp/la-1")
	is.NotContains(detailedStdout, "pr=https://example.test/pr/1")
	is.NotContains(detailedStdout, "la-closed")
	is.NotContains(detailedStdout, "orpheus.branch")

	logData, err := os.ReadFile(logPath)
	must.NoError(err)
	log := string(logData)
	is.Contains(log, localDir)
	is.Contains(log, managedDir)
	is.Equal(4, strings.Count(log, "--json --readonly --sandbox list --all --limit 0"))
}

func TestTaskReadyListsReadyTasksAcrossRegisteredRepos(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
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
		{
			ID:          "local-alpha",
			Name:        "Local Alpha",
			Path:        localDir,
			BeadsMode:   registry.BeadsModeLocal,
			BeadsPrefix: "la",
		},
		{
			ID:          "managed-beta",
			Name:        "Managed Beta",
			Path:        managedRepoPath,
			BeadsMode:   registry.BeadsModeManaged,
			BeadsPrefix: "mb",
		},
	}}))

	logPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		localDir: {stdout: `[
			{"id":"la-1","title":"Local ready","status":"open","priority":2,"issue_type":"task"},
			{"id":"la-bug","title":"Local bug ready","status":"open","priority":1,"issue_type":"bug"},
			{"id":"la-chore","title":"Local chore ready","status":"open","priority":3,"issue_type":"chore"},
			{"id":"la-epic","title":"Local epic ready","status":"open","priority":1,"issue_type":"epic"},
			{"id":"la-closed","title":"Closed local task","status":"closed","priority":1,"issue_type":"task"}
		]`},
		managedDir: {stdout: `[
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
	is.Contains(log, localDir)
	is.Contains(log, managedDir)
	is.Equal(2, strings.Count(log, "--json --readonly --sandbox list --all --limit 0"))
	is.NotContains(log, "--json --readonly --sandbox ready")
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
	must := require.New(t)
	newTestState(t)
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
		{
			ID:          "local-alpha",
			Name:        "Local Alpha",
			Path:        localDir,
			BeadsMode:   registry.BeadsModeLocal,
			BeadsPrefix: "la",
		},
		{
			ID:          "managed-beta",
			Name:        "Managed Beta",
			Path:        managedRepoPath,
			BeadsMode:   registry.BeadsModeManaged,
			BeadsPrefix: "mb",
		},
	}}))

	logPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		localDir: {stdout: `[
			{
				"id":"la-42",
				"title":"Implement local task show",
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
		managedDir: {stderr: "managed repo should not be queried", exitCode: 70},
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
	} {
		is.Contains(stdout, want)
	}
	is.NotContains(stdout, "orpheus.branch")
	is.NotContains(stdout, "managed-beta")

	logData, err := os.ReadFile(logPath)
	must.NoError(err)
	log := string(logData)
	is.Contains(log, localDir)
	is.NotContains(log, managedDir)
	is.Contains(log, "--json --readonly --sandbox show --id la-42")
	is.NotContains(log, "--json --readonly --sandbox list")
	is.NotContains(log, "--json --readonly --sandbox ready")
}

func TestTaskShowReportsMalformedAndUnknownPrefixes(t *testing.T) {
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

func TestTaskShowReportsClosedItemsOutOfScope(t *testing.T) {
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

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "show", "op-closed"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "out of scope for M2 task views")
	is.ErrorContains(err, "expected an active item")
	is.ErrorContains(err, "issue_type=task")
	is.ErrorContains(err, "status=closed")
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
	testCases := []struct {
		name        string
		taskID      string
		metadata    string
		wantMessage string
	}{
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

	for _, tc := range testCases {
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

func TestTaskRunExecutesDefaultAgentAttachedFromDeterministicWorktree(t *testing.T) {
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
				"id":"op-1",
				"title":"Implement attached run",
				"description":"Resolve the task and launch the configured agent.",
				"acceptance_criteria":"The agent gets the rendered prompt and ORPHEUS environment.",
				"status":"open",
				"priority":2,
				"issue_type":"task"
			}
		]`},
	})
	agentLogPath := withFakeAgent(t, "fake-agent", 0)
	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"default_agent": "recorder",
		"agents": map[string]any{
			"recorder": map[string]any{
				"command": "fake-agent",
				"args":    []string{"--prompt", "{{prompt}}", "--literal", "unchanged"},
			},
		},
	}))
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

	agentLog, err := os.ReadFile(agentLogPath)
	must.NoError(err)
	log := string(agentLog)
	for _, want := range []string{
		"PWD=" + worktreePath,
		"ARG_COUNT=4",
		"ARG_1<<END\n--prompt\nEND",
		"ARG_3<<END\n--literal\nEND",
		"ARG_4<<END\nunchanged\nEND",
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
	is.Contains(log, "ARG_2<<END\nYou are an attached implementation agent dispatched by Orpheus.")
	is.NotContains(log, "Resolve the task and launch the configured agent.")
	is.NotContains(log, "The agent gets the rendered prompt and ORPHEUS environment.")
	is.NotContains(log, "- Deterministic worktree: "+worktreePath)
	is.NotContains(log, "Summary:")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-1.yaml"), &state))
	must.Len(state.Runs, 1)
	is.Equal(1, state.Runs[0].Attempt)
	is.Equal(taskstate.RunStatusSucceeded, state.Runs[0].Status)
	is.Equal("recorder", state.Runs[0].Agent)
	is.Equal("fake-agent", state.Runs[0].Command)
	must.Len(state.Runs[0].Args, 4)
	is.Equal("--prompt", state.Runs[0].Args[0])
	is.Contains(state.Runs[0].Args[1], "You are an attached implementation agent dispatched by Orpheus.")
	is.Contains(state.Runs[0].Args[1], "Run `orpheus agent context` now")
	is.NotContains(state.Runs[0].Args[1], "Implement attached run")
	is.Equal("--literal", state.Runs[0].Args[2])
	is.Equal("unchanged", state.Runs[0].Args[3])
	is.Equal("orpheus/op-1", state.Runs[0].Branch)
	is.Equal(worktreePath, state.Runs[0].Worktree)
	must.NotNil(state.Runs[0].FinishedAt)
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
	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"default_agent": "main",
		"agents":        map[string]any{"main": map[string]any{"command": "main-agent"}},
	}))
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
	is.Equal("main", state.Runs[0].Branch)
	is.Equal(repoPath, state.Runs[0].Worktree)
	must.Len(state.Events, 3)
	is.Equal(taskstate.EventWorktreeReused, state.Events[0].Type)
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
	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"default_agent": "main-retry",
		"agents":        map[string]any{"main-retry": map[string]any{"command": "main-retry-agent"}},
	}))

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
	is.Equal("main", state.Runs[0].Branch)
	is.Equal(repoPath, state.Runs[0].Worktree)
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
	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"default_agent": "next",
		"agents":        map[string]any{"next": map[string]any{"command": "next-agent"}},
	}))

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "run", "--main", "op-next"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "already has non-closed task op-owner owning repo-root/default-branch metadata")
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
	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"default_agent": "dirty",
		"agents":        map[string]any{"dirty": map[string]any{"command": "dirty-agent"}},
	}))

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
	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"default_agent": "worktree",
		"agents":        map[string]any{"worktree": map[string]any{"command": "worktree-agent"}},
	}))

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
	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"default_agent": "owned",
		"agents":        map[string]any{"owned": map[string]any{"command": "owned-agent"}},
	}))

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
	is.Equal("orpheus/op-owned", state.Runs[0].Branch)
	is.Equal(worktreePath, state.Runs[0].Worktree)
}

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
	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"default_agent": "race",
		"agents":        map[string]any{"race": map[string]any{"command": "race-agent"}},
	}))

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
	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"default_agent": "lock-check",
		"agents":        map[string]any{"lock-check": map[string]any{"command": "lock-agent"}},
	}))

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
		"default_agent": "default",
		"agents": map[string]any{
			"default": map[string]any{"command": "missing-agent"},
			"custom":  map[string]any{"command": "selected-agent", "args": []string{"selected", "{{prompt}}"}},
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
	is.Contains(log, "ARG_2<<END\nYou are an attached implementation agent dispatched by Orpheus.")
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
	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"default_agent": "failing",
		"agents":        map[string]any{"failing": map[string]any{"command": "failing-agent"}},
	}))

	stdout, stderr, err := executeCommandWithError(t, []string{"task", "run", "op-3"})

	must.Error(err)
	is.Contains(stdout, "fake agent stdout")
	is.Contains(stderr, "fake agent stderr")
	is.ErrorContains(err, "run agent \"failing\"")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-3.yaml"), &state))
	must.Len(state.Runs, 1)
	is.Equal(taskstate.RunStatusFailed, state.Runs[0].Status)
	must.NotNil(state.Runs[0].FinishedAt)
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
	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"default_agent": "missing",
		"agents":        map[string]any{"missing": map[string]any{"command": "definitely-missing-orpheus-agent"}},
	}))

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
	must.NotNil(state.Runs[0].FinishedAt)
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
	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"default_agent": "known",
		"agents":        map[string]any{"known": map[string]any{"command": "known-agent"}},
	}))

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
	is.Equal(taskstate.EventTaskClosedPRMerged, event.Type)
	is.Equal("https://github.test/org/alpha/pull/42", event.PRURL)
	is.Equal("merged", event.ObservedPRState)
}

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
	is.Equal(taskstate.EventTaskClosedPRMerged, event.Type)
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
		stdoutPath := filepath.Join(fixtureDir, fmt.Sprintf("stdout-%d.json", index))
		stderrPath := filepath.Join(fixtureDir, fmt.Sprintf("stderr-%d.txt", index))
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
		fmt.Fprintf(&script, "  %s)\n", shellQuote(dir))
		fmt.Fprintln(&script, "    if [ \"$is_update\" = 1 ]; then")
		fmt.Fprintln(&script, "      printf '{}\\n'")
		fmt.Fprintln(&script, "      exit 0")
		fmt.Fprintln(&script, "    fi")
		fmt.Fprintf(&script, "    cat %s\n", shellQuote(stdoutPath))
		fmt.Fprintf(&script, "    cat %s >&2\n", shellQuote(stderrPath))
		fmt.Fprintf(&script, "    exit %d\n", exitCode)
		fmt.Fprintln(&script, "    ;;")
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

func withFakeGHPRResponses(t *testing.T, responses fakeGHPRResponses) string {
	t.Helper()

	binDir := t.TempDir()
	fixtureDir := filepath.Join(binDir, "fixtures")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatalf("create fake gh fixtures: %v", err)
	}

	listStdoutPath := filepath.Join(fixtureDir, "list-stdout.txt")
	createStdoutPath := filepath.Join(fixtureDir, "create-stdout.txt")
	statusStdoutPath := filepath.Join(fixtureDir, "status-stdout.txt")
	if err := os.WriteFile(listStdoutPath, []byte(responses.listStdout), 0o644); err != nil {
		t.Fatalf("write fake gh list stdout: %v", err)
	}
	if err := os.WriteFile(createStdoutPath, []byte(responses.createStdout), 0o644); err != nil {
		t.Fatalf("write fake gh create stdout: %v", err)
	}
	if err := os.WriteFile(statusStdoutPath, []byte(responses.statusStdout), 0o644); err != nil {
		t.Fatalf("write fake gh status stdout: %v", err)
	}

	logPath := filepath.Join(binDir, "gh.log")
	script := fmt.Sprintf(`#!/bin/sh
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
`, shellQuote(listStdoutPath), responses.listExit, shellQuote(createStdoutPath), responses.createExit)

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

	script += fmt.Sprintf(`    cat %s
    exit %d
    ;;
esac
echo "unexpected gh args: $*" >&2
exit 65
`, shellQuote(statusStdoutPath), responses.statusExit)

	ghPath := filepath.Join(binDir, "gh")
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	t.Setenv("FAKE_GH_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func configureTestGitUser(t *testing.T, repoPath string) {
	t.Helper()
	runGit(t, repoPath, "config", "user.name", "Orpheus Test")
	runGit(t, repoPath, "config", "user.email", "orpheus@example.com")
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

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
