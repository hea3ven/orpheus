//go:build integration

package cli_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowReviewPipelineRestartsBlockedCheckInSameAttemptThroughCLI(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-retry-check", "Retry check", "Restart the failed check.")
	check := fixture.check("check", checkResult{code: 7}, checkResult{})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "check", "name": "unit", "command": check}}})

	stdout, stderr := fixture.run("r\n", "task", "run", "op-retry-check")

	is.Contains(stdout, "Finalized op-retry-check")
	is.Contains(stderr, "Decision for finding 1")
	is.Equal([]string{"check", "check"}, fixture.checkCalls)
	latest := loadLatestReview(t, fixture, "op-retry-check")
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Steps, 1)
	is.Empty(latest.Findings)
}

func TestIntegrationWorkflowReviewPipelineRestartedCheckRetainsFindingNumberThroughCLI(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-retry-number", "Retry numbering", "Keep blocker numbering stable.")
	check := fixture.check("check", checkResult{code: 7}, checkResult{code: 7})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "check", "name": "unit", "command": check}}})

	stdout, stderr := fixture.run("r\np\n", "task", "run", "op-retry-number")

	is.Empty(stdout)
	is.Equal(2, countOccurrences(stderr, "Finding 1:"))
	is.Contains(stderr, "Automated blocker decisions for op-retry-number are paused; resume with `orpheus task run op-retry-number`.")
	is.Equal([]string{"check", "check"}, fixture.checkCalls)
	latest := loadLatestReview(t, fixture, "op-retry-number")
	is.Equal(taskstate.ReviewStatusWaitingForAutomatedDecision, latest.Status)
	must.Len(latest.Steps, 1)
	must.Len(latest.Findings, 1)
	is.Equal("unit", latest.Findings[0].Step)
}

func TestIntegrationWorkflowReviewPipelineRestartFromResumedAutomatedDecisionRerunsCheckThroughCLI(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-resumed-retry", "Resumed retry", "Restart after resuming the decision.")
	check := fixture.check("check", checkResult{code: 7}, checkResult{})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "check", "name": "unit", "command": check}}})

	firstStdout, firstStderr := fixture.run("p\n", "task", "run", "op-resumed-retry")

	is.Empty(firstStdout)
	is.Contains(firstStderr, "Automated blocker decisions for op-resumed-retry are paused; resume with `orpheus task run op-resumed-retry`.")
	statePath := filepath.Join("repos", "alpha", "tasks", "op-resumed-retry.yaml")
	var pausedState taskstate.TaskState
	must.NoError(fixture.paths.ReadDataYAML(statePath, &pausedState))
	paused, ok := taskstate.LatestReview(pausedState)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusWaitingForAutomatedDecision, paused.Status)

	stdout, stderr := fixture.run("r\n", "task", "run", "op-resumed-retry")

	is.Contains(stdout, "Finalized op-resumed-retry")
	is.Contains(stderr, "Resuming review attempt 1 at automated blocker decision for step \"unit\".")
	is.Equal([]string{"check", "check"}, fixture.checkCalls)
	latest := loadLatestReview(t, fixture, "op-resumed-retry")
	is.Equal(paused.Attempt, latest.Attempt)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Steps, 1)
	is.Empty(latest.Findings)
}

func countOccurrences(text, substring string) int {
	count := 0
	for {
		index := strings.Index(text, substring)
		if index < 0 {
			return count
		}
		count++
		text = text[index+len(substring):]
	}
}
