//go:build integration

package cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskCommandSurfaceKeepsCurrentCommandsAndRejectsRemovedFlags(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()

	stdout, stderr, err := fixture.execute("task", "--help")
	require.NoError(t, err)
	assert.Contains(t, stdout, "Available Commands:")
	assert.NotContains(t, stdout, "  ready")
	assert.NotContains(t, stdout, "  review")
	assert.Empty(t, stderr)

	completion, completionStderr, err := fixture.execute("__complete", "task", "r")
	require.NoError(t, err)
	assert.NotContains(t, completion, "ready")
	assert.NotContains(t, completion, "review")
	assert.Contains(t, completion, "run")
	assert.Contains(t, completionStderr, "Completion ended with directive")
	for _, args := range [][]string{{"task", "list", "--details"}, {"task", "list", "--long"}, {"task", "done", "op-1", "--details", "old details"}} {
		_, _, err := fixture.execute(args...)
		flag := args[len(args)-1]
		if args[1] == "done" {
			flag = "--details"
		}
		require.ErrorContains(t, err, "unknown flag: "+flag)
	}
}

func TestIntegrationWorkflowTaskCreateFailsWithoutRepositoryGuidance(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()

	_, _, err := fixture.execute(
		"task", "create", "--title", "Plan", "--description", "Description", "--acceptance", "Acceptance",
	)
	if err == nil || !strings.Contains(err.Error(), "pass --repo") || strings.Contains(strings.ToLower(err.Error()), "beads") {
		t.Fatalf("error = %v, want source-neutral repository guidance", err)
	}
}
