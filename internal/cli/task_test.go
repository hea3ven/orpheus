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
	"github.com/hea3ven/orpheus/internal/taskstate"
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
		"- ID: op-1",
		"- Title: Implement attached run",
		"Resolve the task and launch the configured agent.",
		"The agent gets the rendered prompt and ORPHEUS environment.",
		"- Name: Alpha Repo",
		"- Current execution directory: " + worktreePath,
		"- Deterministic worktree: " + worktreePath,
		"- Deterministic branch: orpheus/op-1",
		"Do not commit manually",
		"Summary:",
		"Details:",
		"Checks:",
		"Follow-ups:",
	} {
		is.Contains(log, want)
	}
	is.Contains(log, "ARG_2<<END\nYou are an attached implementation agent dispatched by Orpheus.")

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
	is.ErrorIs(statErr, os.ErrNotExist)

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
		"- Registered repo root: " + repoPath,
		"- Registered default branch: main",
		"registered repo root on the registered default branch",
	} {
		is.Contains(log, want)
	}
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
	is.ErrorContains(err, "mark task in progress")
	is.ErrorContains(err, "task mutation conflict")
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
	is.ErrorContains(err, "failed to acquire lock for task run setup: "+lockPath)
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
	is.Contains(log, "- ID: op-2")
	is.Contains(log, "- Title: Use selected agent")
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
	is.ErrorContains(err, "resolve agent profile")
	is.ErrorContains(err, "agent profile \"missing\" is not configured")
	is.Empty(stdout)
	is.Empty(stderr)
	_, logErr := os.Stat(agentLogPath)
	is.ErrorIs(logErr, os.ErrNotExist)
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
