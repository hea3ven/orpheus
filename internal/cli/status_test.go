package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/taskstate"
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

func TestStatusGroupsLocalTaskSnapshots(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	logPath := setupStatusGroupsLocalTaskSnapshots(t)

	stdout, stderr := executeCommand(t, []string{"status"})

	is.Empty(stderr)
	for _, want := range []string{
		"TASK_ID", "STATUS", "Ready", "Alpha Repo", "ar-ready", "Ready task", "ar-dep", "Open dependency", "ar-bug", "Bug item",
		"Needs attention", "ar-failed", "Failed attached agent", "run attempt 1 failed",
		"Working", "ar-running", "Running attached agent", "run attempt 1 is running",
		"Idle", "ar-idle", "Idle without run", "no attached run recorded",
		"ar-succeeded", "Succeeded attached agent", "agent exited without completion",
		"Reviewing", "ar-review", "Review task", "https://example.test/pr/3",
		"ar-missing", "Needs inspection", "missing dependency ar-gone",
	} {
		is.Contains(stdout, want)
	}
	for _, hidden := range []string{"Blocked", "ar-blocked", "Done / closed", "ar-closed"} {
		is.NotContains(stdout, hidden)
	}

	assertStatusGroupOrder(t, stdout, []string{"Needs attention", "Reviewing", "Working", "Idle", "Ready"})

	fullStdout, fullStderr := executeCommand(t, []string{"status", "--full"})
	is.Empty(fullStderr)
	assertFullStatusGroupOutput(t, fullStdout)

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
	{"id":"ar-missing","title":"Needs inspection","status":"open","priority":4,"issue_type":"task","dependencies":[{"id":"ar-gone","dependency_type":"blocks"}]},
	{"id":"ar-closed","title":"Closed task","status":"closed","priority":1,"issue_type":"task"},
	{"id":"ar-bug","title":"Bug item","status":"open","priority":1,"issue_type":"bug"}
]`

func setupStatusGroupsLocalTaskSnapshots(t *testing.T) string {
	t.Helper()

	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(t.TempDir(), "alpha")
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

func TestStatusShowsSuccessfulMainRunAsLocalRepoRootReview(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(t.TempDir(), "alpha")
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
		Summary:             "Ready",
		Description:         "Ready for local review.",
		DetailedDescription: "Detailed PR body.",
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
	is.Contains(stdout, "local review; run task review")
}

func TestStatusAndTaskReadyUseLocalRunHistoryOnOpenTaskAsNeedsAttention(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(t.TempDir(), "alpha")
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

	readyStdout, readyStderr := executeCommand(t, []string{"task", "ready"})

	is.Empty(readyStderr)
	is.Contains(readyStdout, "ar-ready")
	is.NotContains(readyStdout, "ar-running")
}

func TestStatusRendersEpicChildrenAsIntegratedTreeRows(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoDir := filepath.Join(t.TempDir(), "alpha")
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
}

func TestStatusReportsRepoFailuresInUnknownGroupAndReturnsError(t *testing.T) {
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
		fmt.Fprintf(&script, "  %s)\n", shellQuote(response.dir+"|"+response.args))
		fmt.Fprintf(&script, "    cat %s\n", shellQuote(stdoutPath))
		fmt.Fprintf(&script, "    cat %s >&2\n", shellQuote(stderrPath))
		fmt.Fprintf(&script, "    exit %d\n", exitCode)
		fmt.Fprintln(&script, "    ;;")
	}
	script.WriteString(`esac
echo "unexpected fake bd call: $PWD|$*" >&2
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
