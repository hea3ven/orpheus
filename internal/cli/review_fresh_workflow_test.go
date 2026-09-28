//go:build integration

package cli_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskRunPausedAutomatedFindingRepairStartsFreshReview(t *testing.T) {
	for _, kind := range []string{"check", "agent_review"} {
		for _, completed := range []bool{true, false} {
			name := kind + "/completed repair"
			if !completed {
				name = kind + "/repair without completion"
			}
			t.Run(name, func(t *testing.T) {
				fixture := newReviewDispatchFixture(t, "op-fresh")
				fixture.budget(2)
				profiles := map[string]agent.Profile{
					"selected": {Command: "unused-implementer"},
					"other":    {Command: "unused-other"},
					"reviewer": {Command: "unused-reviewer"},
				}
				fixture.configureAgentProfiles(agent.AgentDefaults{Implementer: "selected", Reviewer: "reviewer"}, profiles)
				implementation, repair := aCompletion(), aRepairCompletion()
				fixture.agent.outcomes = []semanticAgentOutcome{{completion: &implementation}}
				firstResults := []checkResult{{}}
				if completed {
					firstResults = append(firstResults, checkResult{})
				}
				first := fixture.check("first", firstResults...)
				findingStep := map[string]any{"kind": kind, "name": "correctness"}
				if kind == "check" {
					results := []checkResult{{code: 7}}
					if completed {
						results = append(results, checkResult{})
					}
					findingStep["command"] = fixture.check("correctness", results...)
				} else {
					findingStep["agent"] = "reviewer"
					fixture.agent.outcomes = append(fixture.agent.outcomes, semanticAgentOutcome{review: true, findings: []taskstate.ReviewFinding{aBlockingFinding()}})
				}
				if completed {
					fixture.agent.outcomes = append(fixture.agent.outcomes, semanticAgentOutcome{completion: &repair})
					if kind == "agent_review" {
						fixture.agent.outcomes = append(fixture.agent.outcomes, semanticAgentOutcome{review: true})
					}
				} else {
					fixture.agent.outcomes = append(fixture.agent.outcomes, semanticAgentOutcome{exitWithoutCompletion: true})
				}
				fixture.pipelines("standard", map[string][]map[string]any{"standard": {
					{"kind": "check", "name": "first", "command": first}, findingStep,
				}})

				_, stderr := fixture.run("p\n", "task", "run", "--repo-root", "--agent", "selected", "op-fresh")
				assert.Contains(t, stderr, "Automated blocker decisions for op-fresh are paused")
				paused, item := fixture.loadFinalTask("op-fresh")
				require.Len(t, paused.Reviews, 1)
				assert.Equal(t, taskstate.ReviewStatusWaitingForAutomatedDecision, paused.Reviews[0].Status)
				assertTaskNotPublished(t, paused, item)
				fixture.configureAgentProfiles(agent.AgentDefaults{Implementer: "other", Reviewer: "reviewer"}, profiles)

				stdout, stderr := fixture.run("k\n", "task", "run", "op-fresh")

				assert.Contains(t, stderr, "Resuming review attempt 1 at automated blocker decision for step \"correctness\".")
				final, item := fixture.loadFinalTask("op-fresh")
				require.Len(t, final.Runs, 2)
				assert.Equal(t, "selected", final.Runs[1].Execution.Agent)
				require.NotNil(t, final.Runs[1].ReviewFollowUp)
				assert.Equal(t, 1, final.Runs[1].ReviewFollowUp.ReviewAttempt)
				assert.Equal(t, []int{0}, final.Runs[1].ReviewFollowUp.FindingIndexes)
				prior := final.Reviews[0]
				assert.Equal(t, paused.Reviews[0].StartedAt, prior.StartedAt)
				assert.Equal(t, paused.Reviews[0].Steps, prior.Steps)
				assert.True(t, prior.AutomatedBlockerDecisionKept)
				assert.Equal(t, taskstate.ReviewStatusBlocked, prior.Status)
				require.Len(t, prior.Findings, 1)
				assert.Equal(t, 2, prior.Findings[0].TargetedByRunAttempt)
				wantChecks := []string{"first"}
				if kind == "check" {
					wantChecks = append(wantChecks, "correctness")
				}
				if !completed {
					assert.Contains(t, stderr, "exited without completion")
					require.Len(t, final.Reviews, 1)
					assert.Nil(t, final.Runs[1].Completion)
					assertTaskNotPublished(t, final, item)
					assert.Equal(t, wantChecks, fixture.checkCalls)
					return
				}
				assert.Contains(t, stdout, "Published op-fresh")
				assert.NotContains(t, stderr, "budget exhausted")
				require.Len(t, final.Reviews, 2)
				fresh := final.Reviews[1]
				assert.Equal(t, 2, fresh.Attempt)
				assert.Equal(t, taskstate.ReviewStatusPassed, fresh.Status)
				require.Len(t, fresh.Steps, 2)
				assert.Equal(t, "first", fresh.Steps[0].Name)
				assert.Equal(t, "correctness", fresh.Steps[1].Name)
				assert.Empty(t, fresh.Findings)
				assert.NotEmpty(t, taskstate.FinalizationFacts(final).Commit)
				assert.True(t, item.OrpheusMetadata().HasPRURL)
				assert.Equal(t, append(wantChecks, wantChecks...), fixture.checkCalls)
			})
		}
	}
}
