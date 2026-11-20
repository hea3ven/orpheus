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
	assert.NotContains(t, stdout, "  done")
	assert.Empty(t, stderr)

	completion, completionStderr, err := fixture.execute("__complete", "task", "r")
	require.NoError(t, err)
	assert.NotContains(t, completion, "ready")
	assert.NotContains(t, completion, "review")
	assert.Contains(t, completion, "run")
	assert.Contains(t, completionStderr, "Completion ended with directive")
	completion, _, err = fixture.execute("__complete", "task", "d")
	require.NoError(t, err)
	assert.Contains(t, completion, "dir\t")
	assert.NotContains(t, completion, "done")
	_, _, err = fixture.execute("task", "done", "op-1")
	require.ErrorContains(t, err, `unknown command "done" for "orpheus task"`)

	for _, flag := range []string{"--details", "--long"} {
		_, _, err := fixture.execute("task", "list", flag)
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
