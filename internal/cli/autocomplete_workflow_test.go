//go:build integration

package cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowCompletionProtocolFiniteValuesAndFilesystemFallback(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	assertCompletionChoices(t, fixture, []string{"task", "create", "--type", ""}, []string{"epic", "task"})
	assertCompletionChoices(t, fixture, []string{"status", "--sort", ""}, []string{"created", "status", "updated"})
	assertCompletionChoices(t, fixture, []string{"task", "list", "--sort", ""}, []string{"created", "status", "updated"})
	assertCompletionChoices(t, fixture, []string{"task", "stats", "--group", ""}, []string{"day", "month", "week"})
	assertCompletionChoices(t, fixture, []string{"task", "stats", "--view", ""}, []string{"consumption", "implementation", "implementation-model", "model-pair", "review", "reviewer-model", "throughput"})
	assertCompletionChoices(t, fixture, []string{"agent", "review", "add", "--type", ""}, []string{"advisory", "blocking", "separate-task"})
	assertCompletionChoices(t, fixture, []string{"eval", "review-context", "--harness", ""}, []string{"all", "codex", "pi"})

	stdout, _, err := fixture.execute("__complete", "repo", "add", "")
	require.NoError(t, err)
	assert.Equal(t, ":0\n", stdout, "directory arguments retain normal filesystem completion")
}

func TestIntegrationWorkflowCompletionProtocolCompletesCommaSeparatedEvalSelections(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	assertCompletionChoices(t, fixture, []string{"eval", "review-context", "--harness", "pi,"}, []string{"pi,all", "pi,codex", "pi,pi"})
	assertCompletionChoices(t, fixture, []string{"eval", "review-context", "--harness", "pi,c"}, []string{"pi,codex"})
	assertCompletionChoices(t, fixture, []string{"eval", "review-context", "--harness", "pi,codex,"}, []string{"pi,codex,all", "pi,codex,codex", "pi,codex,pi"})
	assertCompletionChoices(t, fixture, []string{"eval", "review-context", "--variant", "legacy,"}, []string{"legacy,all", "legacy,exhaustive", "legacy,legacy"})
	assertCompletionChoices(t, fixture, []string{"eval", "review-context", "--scenario", "general,a"}, []string{"general,all", "general,architecture"})
}

func TestIntegrationWorkflowCompletionGeneratorsRemainCleanForAllSupportedShells(t *testing.T) {
	t.Parallel()
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			fixture := newCommandWorkflow(t)
			stdout, stderr := fixture.mustExecute("completion", shell)
			assert.NotEmpty(t, stdout)
			assert.Empty(t, stderr)
		})
	}
}
