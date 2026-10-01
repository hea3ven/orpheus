//go:build integration

package cli_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskRunRepeatedAutomatedPausePreservesDecisionTarget(t *testing.T) {
	for _, kind := range []string{"check", "agent_review"} {
		for _, action := range []string{"keep", "downgrade", "waive", "restart"} {
			t.Run(kind+"/"+action, func(t *testing.T) {
				fixture := newReviewWorkflowFixture(t, "op-resume", "Resume review", "Continue the existing decision.")
				first := fixture.check("first", checkResult{})
				pending := map[string]any{"name": "pending", "kind": kind}
				if kind == "check" {
					results := []checkResult{{code: 7}}
					if action == "restart" {
						results = append(results, checkResult{})
					}
					pending["command"] = fixture.check("pending", results...)
				} else {
					results := []semanticAgentOutcome{{findings: []taskstate.ReviewFinding{aBlockingFinding()}}}
					if action == "restart" {
						results = append(results, semanticAgentOutcome{})
					}
					fixture.reviewers(results...)
				}
				last := "must-not-run"
				if action != "keep" {
					last = fixture.check("last", checkResult{})
				}
				fixture.pipelines("standard", map[string][]map[string]any{"standard": {
					{"kind": "check", "name": "first", "command": first}, pending,
					{"kind": "check", "name": "last", "command": last},
				}})
				fixture.budget(1)
				fixture.run("p\n", "task", "run", "op-resume")
				paused := loadLatestReview(t, fixture, "op-resume")
				require.Equal(t, taskstate.ReviewStatusWaitingForAutomatedDecision, paused.Status)
				require.Len(t, paused.Findings, 1)
				require.Len(t, paused.Steps, 2)

				_, stderr := fixture.run("p\n", "task", "run", "op-resume")

				assert.Contains(t, stderr, "Resuming review attempt 1 at automated blocker decision for step \"pending\".")
				assert.Contains(t, stderr, paused.Findings[0].Title)
				pausedAgain := loadLatestReview(t, fixture, "op-resume")
				assert.Equal(t, paused, pausedAgain, "pausing the same decision must retain its execution and findings")
				inputs := map[string]string{"keep": "k\n", "downgrade": "d\nNot blocking.\n", "waive": "w\nAccepted risk.\n", "restart": "r\n"}

				fixture.run(inputs[action], "task", "run", "op-resume")

				latest := loadLatestReview(t, fixture, "op-resume")
				assert.Equal(t, paused.Attempt, latest.Attempt)
				assert.Equal(t, paused.StartedAt, latest.StartedAt)
				assert.Equal(t, paused.Steps[0], latest.Steps[0], "earlier step must not rerun")
				wantChecks := []string{"first"}
				wantReviews := 0
				if kind == "check" {
					wantChecks = append(wantChecks, "pending")
					if action == "restart" {
						wantChecks = append(wantChecks, "pending")
					}
				} else {
					wantReviews = 1
					if action == "restart" {
						wantReviews++
					}
				}
				if action == "keep" {
					assert.Equal(t, taskstate.ReviewStatusBlocked, latest.Status)
					assert.True(t, latest.AutomatedBlockerDecisionKept)
					assert.Equal(t, paused.Findings, latest.Findings)
				} else {
					assert.Equal(t, taskstate.ReviewStatusPassed, latest.Status)
					wantChecks = append(wantChecks, "last")
				}
				switch action {
				case "downgrade":
					require.Len(t, latest.Findings, 1)
					assert.Equal(t, taskstate.FindingTypeAdvisory, latest.Findings[0].Type)
					assert.Equal(t, "Not blocking.", latest.Findings[0].DowngradeReason)
				case "waive":
					require.Len(t, latest.Findings, 1)
					assert.Equal(t, "Accepted risk.", latest.Findings[0].Waiver)
				case "restart":
					assert.Empty(t, latest.Findings)
				}
				assert.Equal(t, wantChecks, fixture.checkCalls)
				assert.Len(t, fixture.agent.launches, wantReviews)
			})
		}
	}
}
