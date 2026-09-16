//go:build integration

//nolint:testpackage // Invocation-scoped fixture requires internal composition wiring.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/stretchr/testify/require"
)

func TestIntegrationVerboseTaskShowDiagnosticsIncludeTaskStateAndBackendBoundaries(t *testing.T) {
	t.Parallel()
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

func TestIntegrationVerboseTaskShowBeadsFailureDiagnosticsIncludeRepoAndTask(t *testing.T) {
	t.Parallel()
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

func TestIntegrationVerboseTaskSyncCLIDiagnosticsUsesSyncStatusKey(t *testing.T) {
	t.Parallel()
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

func TestIntegrationVerboseAgentDoneDiagnosticsCoverContextAndCompletionPersistence(t *testing.T) {
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
