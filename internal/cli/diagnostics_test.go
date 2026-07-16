package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/stretchr/testify/require"
)

func TestVerboseRepoListDiagnosticsDistinguishMissingRegistry(t *testing.T) {
	newTestState(t)

	stdout, stderr := executeCommand(t, []string{"--verbose", "repo", "list"})

	require.Contains(t, stdout, "ID")
	require.Contains(t, stderr, `msg="xdg path resolution started"`)
	require.Contains(t, stderr, `msg="xdg path resolution finished"`)
	require.Contains(t, stderr, `component=registry`)
	require.Contains(t, stderr, `operation=load`)
	require.Contains(t, stderr, `status=expected_absence`)
	require.Contains(t, stderr, `duration_ms=`)
	require.NotContains(t, stdout, "level=DEBUG")
}

func TestVerboseRepoAddDiagnosticsCoverDiscoveryLockAndPersistence(t *testing.T) {
	withFakeBDInit(t)
	repoPath := newTestRepoPath(t)

	stdout, stderr := executeCommand(t, []string{"--verbose", "repo", "add", repoPath})

	require.Contains(t, stdout, "Added repo alpha")
	for _, want := range []string{
		`component=git operation=rev_parse_root`,
		`component=git operation=list_remotes`,
		`component=beads operation=inspect_local`,
		`status=expected_absence`,
		`component=state operation=mutation_lock`,
		`component=registry operation=load`,
		`component=registry operation=save`,
		`component=beads operation=init`,
		`repo_id=alpha`,
	} {
		require.Contains(t, stderr, want)
	}
	initFinishLine := diagnosticLineContaining(t, stderr,
		`msg="beads command finished"`,
		`component=beads`,
		`operation=init`,
	)
	for _, want := range []string{`repo_id=alpha`, `exit_code=0`} {
		require.Contains(t, initFinishLine, want)
	}
	for _, secret := range []string{"--prefix", "--skip-agents", "BD_NON_INTERACTIVE", "BEADS_DIR"} {
		require.NotContains(t, stderr, secret)
	}
}

func TestVerboseRepoAddLocalBeadsSubprocessDiagnosticsIncludeRepo(t *testing.T) {
	repoPath := newTestRepoPath(t)
	beadsDir := filepath.Join(repoPath, ".beads")
	require.NoError(t, os.MkdirAll(beadsDir, 0o755))
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{
			dir:    repoPath,
			args:   "--json --readonly context",
			stdout: fmt.Sprintf(`{"beads_dir":%q}`, beadsDir),
		},
		{
			dir:    repoPath,
			args:   "--json --readonly config get issue_prefix",
			stdout: `{"key":"issue_prefix","value":"op"}`,
		},
	})

	stdout, stderr := executeCommand(t, []string{"--verbose", "repo", "add", repoPath})

	require.Contains(t, stdout, "Added repo alpha")
	for _, operation := range []string{"context", "config"} {
		finishLine := diagnosticLineContaining(t, stderr,
			`msg="beads command finished"`,
			`component=beads`,
			`operation=`+operation,
		)
		require.Contains(t, finishLine, `repo_id=alpha`)
		require.Contains(t, finishLine, `exit_code=0`)
	}
}

func TestVerboseRepoAddClassifiesBDNoWorkspaceAsExpectedAbsence(t *testing.T) {
	repoPath := newTestRepoPath(t)
	require.NoError(t, os.MkdirAll(filepath.Join(repoPath, ".beads"), 0o755))
	managedDir, err := registry.ManagedBeadsDir(currentTestPaths(t), "alpha")
	require.NoError(t, err)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{
			dir:      repoPath,
			args:     "--json --readonly context",
			stdout:   `{"error":"no_beads_directory","message":"No active beads workspace found."}`,
			exitCode: 1,
		},
		{
			dir:  managedDir,
			args: "init --non-interactive --prefix alpha --skip-agents --skip-hooks --quiet",
		},
	})

	stdout, stderr := executeCommand(t, []string{"--verbose", "repo", "add", repoPath})

	require.Contains(t, stdout, "Added repo alpha")
	contextFinishLine := diagnosticLineContaining(t, stderr,
		`msg="beads command finished"`,
		`component=beads`,
		`operation=context`,
	)
	require.Contains(t, contextFinishLine, `status=expected_absence`)
	require.Contains(t, contextFinishLine, `exit_code=1`)
	require.NotContains(t, contextFinishLine, `status=failure`)
}

func TestVerboseBeadsSubprocessFailureLogsExitCodeWithoutOutput(t *testing.T) {
	withFailingBDInit(t, 7, "SECRET_PROCESS_OUTPUT")
	repoPath := newTestRepoPath(t)

	stdout, stderr, err := executeCommandWithError(t, []string{"--verbose", "repo", "add", repoPath})

	require.Error(t, err)
	require.Empty(t, stdout)
	require.Contains(t, stderr, `component=beads operation=init`)
	require.Contains(t, stderr, `status=failure`)
	require.Contains(t, stderr, `exit_code=7`)
	require.NotContains(t, stderr, "SECRET_PROCESS_OUTPUT")
}

func TestVerboseTaskShowDiagnosticsIncludeTaskStateAndBackendBoundaries(t *testing.T) {
	_, backendDir := setupVerboseTaskShowDiagnostics(t)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{{
		dir:  backendDir,
		args: "--json --readonly --sandbox show --id op-1",
		stdout: `[{"id":"op-1","title":"Inspect diagnostics","status":"open",` +
			`"priority":1,"issue_type":"task","labels":[],"metadata":{}}]`,
	}})

	stdout, stderr := executeCommand(t, []string{"--verbose", "task", "show", "op-1"})

	require.Contains(t, stdout, "Inspect diagnostics")
	require.Contains(t, stderr, `component=beads operation=show`)
	require.Contains(t, stderr, `component=taskstate operation=load`)
	require.Contains(t, stderr, `status=expected_absence`)
	require.Contains(t, stderr, `repo_id=alpha`)
	require.Contains(t, stderr, `task_id=op-1`)
	require.NotContains(t, stderr, "Inspect diagnostics")
}

func TestVerboseTaskShowBeadsFailureDiagnosticsIncludeRepoAndTask(t *testing.T) {
	_, backendDir := setupVerboseTaskShowDiagnostics(t)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{{
		dir:      backendDir,
		args:     "--json --readonly --sandbox show --id op-1",
		stderr:   "SECRET_PROCESS_OUTPUT --id op-1",
		exitCode: 13,
	}})

	stdout, stderr, err := executeCommandWithError(t, []string{"--verbose", "task", "show", "op-1"})

	require.Error(t, err)
	require.Empty(t, stdout)
	finishLine := diagnosticLine(t, stderr, `msg="beads command finished"`)
	for _, want := range []string{
		`component=beads`,
		`operation=show`,
		`status=failure`,
		`duration_ms=`,
		`exit_code=13`,
		`repo_id=alpha`,
		`task_id=op-1`,
	} {
		require.Contains(t, finishLine, want)
	}
	for _, secret := range []string{"SECRET_PROCESS_OUTPUT", "--id op-1"} {
		require.NotContains(t, stderr, secret)
	}
}

func setupVerboseTaskShowDiagnostics(t *testing.T) (string, string) {
	t.Helper()

	root := newTestState(t)
	paths := currentTestPaths(t)
	backendDir, err := registry.ManagedBeadsDir(paths, "alpha")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(backendDir, 0o755))
	require.NoError(t, registry.NewStore(paths).Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha",
		Path:          filepath.Join(root, "repo"),
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeManaged,
		BeadsPrefix:   "op",
	}}}))
	return root, backendDir
}

func diagnosticLine(t *testing.T, diagnostics string, contains string) string {
	t.Helper()

	return diagnosticLineContaining(t, diagnostics, contains)
}

func diagnosticLineContaining(t *testing.T, diagnostics string, contains ...string) string {
	t.Helper()

	for _, line := range strings.Split(diagnostics, "\n") {
		matched := true
		for _, value := range contains {
			if !strings.Contains(line, value) {
				matched = false
				break
			}
		}
		if matched {
			return line
		}
	}
	t.Fatalf("diagnostic line containing %q not found in:\n%s", contains, diagnostics)
	return ""
}

func withFailingBDInit(t *testing.T, exitCode int, stderrText string) {
	t.Helper()

	binDir := t.TempDir()
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' %s >&2
exit %d
`, diagShellQuote(stderrText), exitCode)
	bdPath := filepath.Join(binDir, "bd")
	require.NoError(t, os.WriteFile(bdPath, []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func diagShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
