package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
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

	logPath := withFakeBDCommandResponses(t, []fakeBDCommandResponse{{
		dir:  repoDir,
		args: "--json --readonly --sandbox list --all --limit 0",
		stdout: `[
			{"id":"ar-ready","title":"Ready task","status":"open","priority":1,"issue_type":"task"},
			{"id":"ar-dep","title":"Open dependency","status":"open","priority":1,"issue_type":"task"},
			{"id":"ar-working","title":"Work in progress","status":"in_progress","priority":4,"issue_type":"task"},
			{"id":"ar-blocked","title":"Blocked task","status":"open","priority":2,"issue_type":"task","dependencies":[{"id":"ar-dep","dependency_type":"blocks"}]},
			{"id":"ar-review","title":"Review task","status":"in_progress","priority":3,"issue_type":"task","metadata":{"orpheus.pr_url":"https://example.test/pr/3"}},
			{"id":"ar-missing","title":"Needs inspection","status":"open","priority":4,"issue_type":"task","dependencies":[{"id":"ar-gone","dependency_type":"blocks"}]},
			{"id":"ar-closed","title":"Closed task","status":"closed","priority":1,"issue_type":"task"},
			{"id":"ar-bug","title":"Bug item","status":"open","priority":1,"issue_type":"bug"}
		]`,
	}})

	stdout, stderr := executeCommand(t, []string{"status"})

	is.Empty(stderr)
	for _, want := range []string{
		"Ready to run (3)", "Alpha Repo", "ar-ready", "Ready task", "ar-dep", "Open dependency", "ar-bug", "Bug item",
		"Working (1)", "ar-working", "Work in progress",
		"In review (1)", "ar-review", "Review task", "https://example.test/pr/3",
		"Unknown / needs attention (1)", "ar-missing", "Needs inspection", "missing dependency ar-gone",
	} {
		is.Contains(stdout, want)
	}
	for _, hidden := range []string{"Blocked (1)", "ar-blocked", "Done / closed (1)", "ar-closed", "STATUS"} {
		is.NotContains(stdout, hidden)
	}

	assertStatusGroupOrder(t, stdout, []string{
		"Unknown / needs attention",
		"In review",
		"Working",
		"Ready to run",
	})
	for _, section := range []string{
		statusSection(t, stdout, "Working", "Ready to run"),
		statusSection(t, stdout, "Ready to run", ""),
	} {
		is.NotContains(section, "DETAIL")
	}

	fullStdout, fullStderr := executeCommand(t, []string{"status", "--full"})
	is.Empty(fullStderr)
	for _, want := range []string{
		"Blocked (1)", "ar-blocked", "Blocked task", "blocked by ar-dep",
		"Done / closed (1)", "ar-closed", "Closed task",
	} {
		is.Contains(fullStdout, want)
	}
	is.NotContains(fullStdout, "STATUS")
	assertStatusGroupOrder(t, fullStdout, []string{
		"Unknown / needs attention",
		"In review",
		"Working",
		"Ready to run",
		"Blocked",
		"Done / closed",
	})
	for _, section := range []string{
		statusSection(t, fullStdout, "Working", "Ready to run"),
		statusSection(t, fullStdout, "Ready to run", "Blocked"),
		statusSection(t, fullStdout, "Done / closed", ""),
	} {
		is.NotContains(section, "DETAIL")
	}
	blockedHeader := statusSection(t, fullStdout, "Blocked", "Done / closed")
	is.Less(strings.Index(blockedHeader, "TITLE"), strings.Index(blockedHeader, "DETAIL"))

	logData, err := os.ReadFile(logPath)
	must.NoError(err)
	log := string(logData)
	is.Contains(log, "--json --readonly --sandbox list --all --limit 0")
	is.NotContains(log, "--json --readonly --sandbox ready")
	is.NotContains(log, "show --id")
	is.NotContains(log, "gh ")
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
			dir:    okDir,
			args:   "--json --readonly --sandbox list --all --limit 0",
			stdout: `[{"id":"ok-1","title":"Ready despite another repo failure","status":"open","priority":1,"issue_type":"task"}]`,
		},
	})

	stdout, stderr, err := executeCommandWithError(t, []string{"status"})

	must.Error(err)
	is.ErrorContains(err, "status completed with 1 repo failure")
	is.Contains(stdout, "Ready to run (1)")
	is.Contains(stdout, "OK Repo")
	is.Contains(stdout, "ok-1")
	is.Contains(stdout, "Ready despite another repo failure")
	is.Contains(stdout, "Unknown / needs attention (1)")
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

func statusSection(t *testing.T, output string, start string, end string) string {
	t.Helper()

	startIndex := strings.Index(output, start)
	if startIndex < 0 {
		t.Fatalf("output missing section %q:\n%s", start, output)
	}
	if end == "" {
		return output[startIndex:]
	}
	endIndex := strings.Index(output[startIndex:], "\n"+end)
	if endIndex < 0 {
		return output[startIndex:]
	}
	return output[startIndex : startIndex+endIndex]
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
