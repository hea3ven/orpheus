//go:build integration

package cli_test

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationVerboseRepoListReportsMissingRegistry(t *testing.T) {
	fixture := newWorkflowFixture(t, repoWorkflowConfigRoot, repoWorkflowDataRoot)

	stdout, stderr, err := fixture.execute("--verbose", "repo", "list")

	require.NoError(t, err)
	assert.Contains(t, stdout, "ID")
	assertDiagnosticLine(t, stderr, "component=registry", "operation=load", "status=expected_absence", "duration_ms=")
	assert.NotContains(t, stdout, "level=DEBUG")
}

func TestIntegrationVerboseTaskRunMissingRegistryReportsSetupFailure(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-missing"))
	fixture.withRegisteredRepos()

	stdout, stderr, err := fixture.execute("--verbose", "task", "run", "op-missing")

	require.Error(t, err)
	assert.Empty(t, stdout)
	assertDiagnosticLine(t, stderr, "component=registry", "operation=load", "duration_ms=")
	assert.Empty(t, fixture.git.setups)
	assert.Empty(t, fixture.agent.launches)
}

func TestIntegrationVerboseRepoAddReportsPersistenceAndPassesSafeAdapterIdentifiers(t *testing.T) {
	for _, mode := range []string{"local", "managed"} {
		t.Run(mode, func(t *testing.T) {
			repo := aGitRepository(t, "alpha")
			fixture := newRepoWorkflowFixture(t, repo)
			if mode == "local" {
				fixture.withLocalBeads(repo, "op")
			} else {
				fixture.withoutLocalBeads()
			}

			stdout, stderr, err := fixture.execute("--verbose", "repo", "add", repo.path)

			require.NoError(t, err)
			assert.Contains(t, stdout, "Added repo alpha")
			assert.NotContains(t, stdout, "level=DEBUG")
			assertDiagnosticLine(t, stderr, "component=state", "operation=mutation_lock", "status=success")
			assertDiagnosticLine(t, stderr, "component=registry", "operation=save", "status=success")
			assert.Contains(t, fixture.diagnosticAttrs, slog.String("repo_id", "alpha"))
			for _, secret := range []string{"--prefix", "--skip-agents", "BD_NON_INTERACTIVE", "BEADS_DIR"} {
				assert.NotContains(t, stderr, secret)
			}
		})
	}
}

func TestIntegrationVerboseTaskRunReportsDispatchAndPersistenceWithoutPromptContents(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-diag"))
	fixture.withAgentExitingWithoutCompletion(1)

	_, stderr, err := fixture.execute("--verbose", "task", "run", "op-diag")

	require.NoError(t, err)
	for _, message := range []string{"dispatch setup started", "git target preparation finished", "backend task mutation finished", "task run persistence finished"} {
		assertDiagnosticLine(t, stderr, message, "repo_id=alpha", "task_id=op-diag")
	}
	assertDiagnosticLine(t, stderr, "component=taskstate", "operation=start_run", "status=success")
	assertDiagnosticLine(t, stderr, "attached agent process finished", "task_id=op-diag", "attempt=1", "lifecycle_outcome=success", "exit_code=0")
	for _, secret := range []string{"ORPHEUS_AGENT_PROMPT", "Run `orpheus agent context`", "Task op-diag"} {
		assert.NotContains(t, stderr, secret)
	}
}

func TestIntegrationVerboseTaskRunDistinguishesStartAndRuntimeFailures(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		outcome   semanticAgentOutcome
		lifecycle string
	}{
		{name: "start failure", outcome: semanticAgentOutcome{startFailure: &agentexec.StartError{Err: errors.New("missing executable")}}, lifecycle: "start_failure"},
		{name: "runtime failure", outcome: semanticAgentOutcome{err: agentExitFailure(7)}, lifecycle: "nonzero_exit"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newTaskWorkflowFixture(t, anOpenTask("op-failure"))
			fixture.configureImplementer("recorder", agent.Profile{Command: "unused-agent"})
			fixture.agent.outcomes = []semanticAgentOutcome{scenario.outcome}

			_, stderr, err := fixture.execute("--verbose", "task", "run", "op-failure")

			require.Error(t, err)
			line := assertDiagnosticLine(t, stderr, "attached agent process finished", "task_id=op-failure", "lifecycle_outcome="+scenario.lifecycle)
			if scenario.outcome.startFailure != nil {
				assert.NotContains(t, line, "exit_code=")
			} else {
				assert.Contains(t, line, "exit_code=7")
			}
		})
	}
}

func assertDiagnosticLine(t *testing.T, diagnostics string, fields ...string) string {
	t.Helper()
	for _, line := range strings.Split(diagnostics, "\n") {
		matches := true
		for _, field := range fields {
			if !strings.Contains(line, field) {
				matches = false
				break
			}
		}
		if matches {
			return line
		}
	}
	require.FailNow(t, "missing diagnostic line", "fields: %q\ndiagnostics:\n%s", fields, diagnostics)
	return ""
}

type agentExitFailure int

func (e agentExitFailure) Error() string { return "agent exited unsuccessfully" }
func (e agentExitFailure) ExitCode() int { return int(e) }
