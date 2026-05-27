package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
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
		"Managed Beta", "mb-1", "in_progress", "3", "Managed active",
	} {
		is.Contains(stdout, want)
	}
	for _, hidden := range []string{
		"REPO_ID", "BEADS_PREFIX", "ORPHEUS", "local-alpha", "managed-beta", "branch=task/la-1", "worktree=/tmp/la-1", "pr=https://example.test/pr/1",
	} {
		is.NotContains(stdout, hidden)
	}
	is.NotContains(stdout, "la-closed")
	is.NotContains(stdout, "la-bug")
	is.NotContains(stdout, "orpheus.branch")

	detailedStdout, detailedStderr := executeCommand(t, []string{"task", "list", "--details"})

	is.Empty(detailedStderr)
	for _, want := range []string{
		"REPO_ID", "REPO", "BEADS_PREFIX", "TASK_ID", "STATUS", "P", "BRANCH", "WORKTREE", "PR", "TITLE",
		"local-alpha", "Local Alpha", "la", "la-1", "open", "2", "task/la-1", "/tmp/la-1", "Local active",
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
	is.NotContains(detailedStdout, "la-bug")
	is.NotContains(detailedStdout, "orpheus.branch")

	logData, err := os.ReadFile(logPath)
	must.NoError(err)
	log := string(logData)
	is.Contains(log, localDir)
	is.Contains(log, managedDir)
	is.Equal(4, strings.Count(log, "--json --readonly --sandbox list --type task --limit 0"))
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
			{"id":"la-closed","title":"Closed local task","status":"closed","priority":1,"issue_type":"task"},
			{"id":"la-bug","title":"Local bug","status":"open","priority":1,"issue_type":"bug"}
		]`},
		managedDir: {stdout: `[
			{"id":"mb-1","title":"Managed ready","status":"in_progress","priority":3,"issue_type":"task"}
		]`},
	})

	stdout, stderr := executeCommand(t, []string{"task", "ready"})

	is.Empty(stderr)
	for _, want := range []string{
		"REPO", "TASK_ID", "STATUS", "P", "TITLE",
		"Local Alpha", "la-1", "open", "2", "Local ready",
		"Managed Beta", "mb-1", "in_progress", "3", "Managed ready",
	} {
		is.Contains(stdout, want)
	}
	for _, hidden := range []string{"REPO_ID", "BEADS_PREFIX", "local-alpha", "managed-beta"} {
		is.NotContains(stdout, hidden)
	}
	is.NotContains(stdout, "la-closed")
	is.NotContains(stdout, "la-bug")

	logData, err := os.ReadFile(logPath)
	must.NoError(err)
	log := string(logData)
	is.Contains(log, localDir)
	is.Contains(log, managedDir)
	is.Equal(2, strings.Count(log, "--json --readonly --sandbox ready --type task --limit 0"))
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
case "$*" in
  "--json --readonly --sandbox ready --type task --limit 0"|"--json --readonly --sandbox list --type task --limit 0")
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

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
