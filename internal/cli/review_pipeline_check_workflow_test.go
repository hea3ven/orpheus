//go:build integration

package cli_test

import (
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowReviewPipelineRestartedStepRetainsFindingNumberThroughCLI(t *testing.T) {
	for _, kind := range []string{"check", "agent_review"} {
		t.Run(kind, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			fixture := newReviewWorkflowFixture(t, "op-retry-number", "Retry numbering", "Keep blocker numbering stable.")
			step := map[string]any{"kind": kind, "name": "unit"}
			if kind == "check" {
				step["command"] = fixture.check("check", checkResult{code: 7}, checkResult{code: 7})
			} else {
				fixture.reviewers(
					semanticAgentOutcome{findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Title: "Stale blocker", Description: "Discard on restart.", SuggestedAction: "Restart the review."}}},
					semanticAgentOutcome{findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Title: "Current blocker", Description: "Keep the new result.", SuggestedAction: "Fix the current defect."}}},
				)
			}
			fixture.pipelines("standard", map[string][]map[string]any{"standard": {step}})

			stdout, stderr := fixture.run("r\np\n", "task", "run", "op-retry-number")

			if kind == "check" {
				is.Empty(stdout)
			} else {
				is.Equal(2, strings.Count(stdout, "Recorded blocking review finding 1 for op-retry-number."))
			}
			is.Equal(2, strings.Count(stderr, "Finding 1:"))
			is.Contains(stderr, "Automated blocker decisions for op-retry-number are paused; resume with `orpheus task run op-retry-number`.")
			if kind == "check" {
				is.Equal([]string{"check", "check"}, fixture.checkCalls)
			} else {
				must.Len(fixture.agent.launches, 2)
				is.Equal("review-agent", fixture.agent.launches[0].command.Command)
				is.Equal("review-agent", fixture.agent.launches[1].command.Command)
			}
			latest := loadLatestReview(t, fixture, "op-retry-number")
			is.Equal(taskstate.ReviewStatusWaitingForAutomatedDecision, latest.Status)
			must.Len(latest.Steps, 1)
			must.Len(latest.Findings, 1)
			is.Equal("unit", latest.Findings[0].Step)
			if kind == "agent_review" {
				is.Equal("Current blocker", latest.Findings[0].Title)
			}
		})
	}
}
