//go:build integration

package cli_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowReviewPipelineHunkManualCommandFailureRemainsOperationalErrorThroughCLI(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-hunk-fails", "Hunk command failure", "Fail before importing Hunk notes.")
	manual := fixture.hunkCommand("hunk-manual", hunkCommandResult{code: 42, stdout: "hunk stdout before failure\n"})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "manual", "name": "inspect", "command": manual, "hunk_notes": true}}})

	stdout, stderr, err := fixture.runError("\n", "task", "run", "--pipeline", "standard", "op-hunk-fails")

	must.Error(err)
	is.Contains(stdout, "hunk stdout before failure")
	is.Contains(stderr, "Run manual command for step \"inspect\"")
	is.NotContains(stderr, "Review action")
	is.ErrorContains(err, "run manual step \"inspect\"")
	is.Equal([]string{"hunk-manual"}, fixture.hunkCalls)
	latest := loadLatestReview(t, fixture, "op-hunk-fails")
	is.Equal(taskstate.ReviewStatusFailed, latest.Status)
	is.Empty(latest.Findings)
	loaded, task := fixture.loadFinalTask("op-hunk-fails")
	assertTaskNotPublished(t, loaded, task)
}
