package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
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

func TestVerboseTaskRunSetupFailureUsesInvocationDiagnostics(t *testing.T) {
	newTestState(t)

	stdout, stderr, err := executeCommandWithError(t, []string{"--verbose", "task", "run", "op-missing"})

	require.Error(t, err)
	require.Empty(t, stdout)
	for _, want := range []string{
		`msg="xdg path resolution started"`,
		`msg="xdg path resolution finished"`,
		`component=registry operation=load`,
		`status=expected_absence`,
		`duration_ms=`,
	} {
		require.Contains(t, stderr, want)
	}
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

func TestVerboseTaskSyncCLIDiagnosticsUsesSyncStatusKey(t *testing.T) {
	root := newTestState(t)
	paths := currentTestPaths(t)
	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	require.NoError(t, registry.NewStore(paths).Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{{
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
	withFakeGHPRResponses(t, fakeGHPRResponses{
		listStdout:   "unexpected list\n",
		listExit:     66,
		createStdout: "unexpected create\n",
		createExit:   66,
		statusStdout: `{"url":"https://github.test/org/alpha/pull/42","state":"OPEN","merged":false}`,
		statusExit:   0,
	})

	stdout, stderr := executeCommand(t, []string{"--verbose", "task", "sync", "op-sync"})

	require.Contains(t, stdout, "Synced op-sync")
	finishLine := diagnosticLineContaining(t, stderr, `msg="synced task"`, `task_id=op-sync`)
	require.Contains(t, finishLine, `sync_status=already_in_review`)
	require.NotContains(t, stderr, ` status=already_in_review`)
	require.NotContains(t, stderr, "https://github.test/org/alpha/pull/42")
}

func TestVerboseTaskRunDiagnosticsCoverDispatchProcessUsageAndPersistence(t *testing.T) {
	paths, repoPath := setupVerboseTaskRunDiagnostics(t, "op-diag", "Inspect dispatch diagnostics", "fake-agent", 0)
	writeTaskRunAgentConfig(t, paths, "recorder", "fake-agent", nil)

	stdout, stderr := executeCommand(t, []string{"--verbose", "task", "run", "op-diag"})

	require.Contains(t, stdout, "fake agent stdout")
	for _, want := range []string{
		`msg="dispatch setup started"`,
		`msg="git target preparation finished"`,
		`msg="backend task mutation finished"`,
		`msg="task run persistence finished"`,
		`component=state operation=mutation_lock`,
		`component=beads operation=update`,
		`component=taskstate operation=start_run`,
		`component=cli operation=task_run_agent_process`,
		`lifecycle_outcome=success`,
		`component=agent operation=usage_capture`,
		`reason=unsupported_harness:unknown`,
		`repo_id=alpha`,
		`task_id=op-diag`,
		`attempt=1`,
	} {
		require.Contains(t, stderr, want)
	}
	usageStartLine := diagnosticLineContaining(t, stderr,
		`msg="agent usage capture started"`,
		`repo_id=alpha`,
		`task_id=op-diag`,
		`attempt=1`,
	)
	require.Contains(t, usageStartLine, `status=started`)
	usageFinishLine := diagnosticLineContaining(t, stderr,
		`msg="agent usage capture finished"`,
		`repo_id=alpha`,
		`task_id=op-diag`,
		`attempt=1`,
	)
	for _, want := range []string{`status=failure`, `reason=unsupported_harness:unknown`} {
		require.Contains(t, usageFinishLine, want)
	}
	processLine := diagnosticLineContaining(t, stderr,
		`msg="attached agent process finished"`,
		`task_id=op-diag`,
	)
	for _, want := range []string{`lifecycle_outcome=success`, `exit_code=0`} {
		require.Contains(t, processLine, want)
	}
	for _, secret := range []string{"ORPHEUS_AGENT_PROMPT", "Run `orpheus agent context`", "fake agent stdout"} {
		require.NotContains(t, stderr, secret)
	}
	_ = repoPath
}

func TestVerboseAgentDoneDiagnosticsCoverContextAndCompletionPersistence(t *testing.T) {
	setupAgentDoneWorktreeRun(t)

	stdout, stderr := executeCommand(t, []string{
		"--verbose",
		"agent",
		"done",
		"--summary",
		"Record diagnostics",
		"--description",
		"Recorded completion diagnostics.",
		"--detailed-description",
		"## Details\n\nRecorded completion diagnostics.",
		"--technical-explanation",
		"Recorded the diagnostic completion path for a worktree run.",
	})

	require.Contains(t, stdout, "Recorded completion for op-1")
	for _, want := range []string{
		`component=state operation=mutation_lock semantic_operation="agent completion"`,
		`msg="agent completion context resolution finished"`,
		`msg="agent completion persistence finished"`,
		`component=taskstate operation=record_completion`,
		`component=taskstate operation=save`,
		`repo_id=alpha`,
		`task_id=op-1`,
		`attempt=1`,
	} {
		require.Contains(t, stderr, want)
	}
	for _, message := range []string{
		`msg="global mutation lock started"`,
		`msg="global mutation lock finished"`,
		`msg="global mutation lock held started"`,
		`msg="global mutation lock held finished"`,
	} {
		line := diagnosticLineContaining(t, stderr, message, `semantic_operation="agent completion"`)
		require.Contains(t, line, `repo_id=alpha`)
		require.Contains(t, line, `task_id=op-1`)
		require.Contains(t, line, `attempt=1`)
	}
	require.NotContains(t, stderr, "## Details")
}

func TestVerboseTaskRunDiagnosticsDistinguishAgentStartAndRuntimeFailures(t *testing.T) {
	paths, _ := setupVerboseTaskRunDiagnostics(t, "op-runtime", "Runtime diagnostics", "failing-agent", 7)
	writeTaskRunAgentConfig(t, paths, "failing", "failing-agent", nil)

	_, runtimeStderr, runtimeErr := executeCommandWithError(t, []string{"--verbose", "task", "run", "op-runtime"})

	require.Error(t, runtimeErr)
	runtimeLine := diagnosticLineContaining(t, runtimeStderr,
		`msg="attached agent process finished"`,
		`task_id=op-runtime`,
	)
	require.Contains(t, runtimeLine, `lifecycle_outcome=nonzero_exit`)
	require.Contains(t, runtimeLine, `exit_code=7`)

	paths, _ = setupVerboseTaskRunDiagnostics(t, "op-start", "Start diagnostics", "missing-agent", 0)
	writeTaskRunAgentConfig(t, paths, "missing", "definitely-missing-orpheus-agent", nil)

	_, startStderr, startErr := executeCommandWithError(t, []string{"--verbose", "task", "run", "op-start"})

	require.Error(t, startErr)
	startLine := diagnosticLineContaining(t, startStderr,
		`msg="attached agent process finished"`,
		`task_id=op-start`,
	)
	require.Contains(t, startLine, `lifecycle_outcome=start_failure`)
	require.NotContains(t, startLine, `exit_code=`)
}

func setupVerboseTaskRunDiagnostics(
	t *testing.T,
	taskID string,
	title string,
	agentCommand string,
	exitCode int,
) (state.Paths, string) {
	t.Helper()

	root := newTestState(t)
	paths := currentTestPaths(t)
	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", taskID))
	require.NoError(t, registry.NewStore(paths).Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: fmt.Sprintf(
			`[{"id":%q,"title":%q,"status":"open","priority":1,"issue_type":"task"}]`,
			taskID,
			title,
		)},
	})
	if agentCommand != "definitely-missing-orpheus-agent" {
		withFakeAgent(t, agentCommand, exitCode)
	}
	return paths, repoPath
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
