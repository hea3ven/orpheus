//go:build integration

package cli_test

import (
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowReviewPipelineRestartedCheckRetainsFindingNumberThroughCLI(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-retry-number", "Retry numbering", "Keep blocker numbering stable.")
	check := fixture.check("check", checkResult{code: 7}, checkResult{code: 7})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "check", "name": "unit", "command": check}}})

	stdout, stderr := fixture.run("r\np\n", "task", "run", "op-retry-number")

	is.Empty(stdout)
	is.Equal(2, strings.Count(stderr, "Finding 1:"))
	is.Contains(stderr, "Automated blocker decisions for op-retry-number are paused; resume with `orpheus task run op-retry-number`.")
	is.Equal([]string{"check", "check"}, fixture.checkCalls)
	latest := loadLatestReview(t, fixture, "op-retry-number")
	is.Equal(taskstate.ReviewStatusWaitingForAutomatedDecision, latest.Status)
	must.Len(latest.Steps, 1)
	must.Len(latest.Findings, 1)
	is.Equal("unit", latest.Findings[0].Step)
}
