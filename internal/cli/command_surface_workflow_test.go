//go:build integration

package cli_test

import (
	"strings"
	"testing"
)

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
