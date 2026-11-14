//go:build integration

package cli_test

import (
	"context"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskRunPausedAutomatedFindingRepairStartsFreshReview(t *testing.T) {
	for _, kind := range []string{"check", "agent_review"} {
		for _, repairCase := range []struct {
			name      string
			completed bool
			staged    bool
		}{
			{name: "completed repair", completed: true},
			{name: "repair without completion"},
			{name: "completed repair with staged changes", completed: true, staged: true},
		} {
			t.Run(kind+"/"+repairCase.name, func(t *testing.T) {
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
				if repairCase.completed {
					firstResults = append(firstResults, checkResult{})
				}
				first := fixture.check("first", firstResults...)
				findingStep := map[string]any{"kind": kind, "name": "correctness"}
				if kind == "check" {
					results := []checkResult{{code: 7}}
					if repairCase.completed {
						results = append(results, checkResult{})
					}
					findingStep["command"] = fixture.check("correctness", results...)
				} else {
					findingStep["agent"] = "reviewer"
					fixture.agent.outcomes = append(fixture.agent.outcomes, semanticAgentOutcome{review: true, findings: []taskstate.ReviewFinding{aBlockingFinding()}})
				}
				if repairCase.completed {
					fixture.agent.outcomes = append(fixture.agent.outcomes, semanticAgentOutcome{
						completion: &repair,
						mutateCandidate: func() {
							fixture.candidate.hasStagedChanges = repairCase.staged
						},
					})
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
				// Native PR metadata is not an independent continuation authority.
				require.NoError(t, fixture.tasks.SetPRURL(context.Background(), "op-fresh", "https://example.test/pr/existing"))
				fixture.configureAgentProfiles(agent.AgentDefaults{Implementer: "other", Reviewer: "other"}, profiles)
				inserted := "must-not-run-before-repair"
				if repairCase.completed {
					inserted = fixture.check("inserted-before", checkResult{})
				}
				fixture.pipelines("standard", map[string][]map[string]any{"standard": {
					{"kind": "check", "name": "inserted-before", "command": inserted},
					{"kind": "check", "name": "first", "command": first}, findingStep,
				}})
				fixture.budget(2)
				item = fixture.backend.tasks["op-fresh"]
				item.Title = "Updated repair task"
				item.Description = "Evaluate repaired output against current requirements."
				item.AcceptanceCriteria = "Retain the original scope and capture this scope for the new review."
				fixture.backend.tasks[item.ID] = item

				stdout, stderr, err := fixture.runError("k\n", "task", "run", "op-fresh")
				require.NoError(t, err, "stderr: %s", stderr)

				assert.Contains(t, stderr, "Resuming review attempt 1 at automated blocker decision for step \"correctness\".")
				final, item := fixture.loadFinalTask("op-fresh")
				require.Len(t, final.Runs, 2)
				assert.Equal(t, "selected", final.Runs[1].Execution.Agent)
				require.NotNil(t, final.Runs[1].ReviewFollowUp)
				assert.Equal(t, 1, final.Runs[1].ReviewFollowUp.ReviewAttempt)
				assert.Equal(t, []int{0}, final.Runs[1].ReviewFollowUp.FindingIndexes)
				priorScope := paused.Work.Workflows[0].Phases[paused.Reviews[0].Address.Phase].(*work.ReviewPhase).Scope
				assert.Equal(t, priorScope, final.Work.Workflows[0].Phases[final.Reviews[0].Address.Phase].(*work.ReviewPhase).Scope)
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

				if !repairCase.completed {
					assert.Contains(t, stderr, "exited without completion")
					require.Len(t, final.Reviews, 1)
					assert.Nil(t, final.Runs[1].Completion)
					assert.Equal(t, "https://example.test/pr/existing", item.OrpheusMetadata().PRURL)
					assert.Nil(t, final.Finalization)
					assert.Empty(t, fixture.candidate.pushes)
					assert.Equal(t, wantChecks, fixture.checkCalls)
					return
				}
				assert.Contains(t, stdout, "Review passed for op-fresh")
				assert.NotContains(t, stderr, "budget exhausted")
				require.Len(t, final.Reviews, 2)
				fresh := final.Reviews[1]
				assert.Equal(t, 2, fresh.Attempt)
				assert.Equal(t, taskstate.ReviewStatusPassed, fresh.Status)
				freshScope := final.Work.Workflows[0].Phases[fresh.Address.Phase].(*work.ReviewPhase).Scope
				assert.Equal(t, "Updated repair task\n\nEvaluate repaired output against current requirements.\n\nRetain the original scope and capture this scope for the new review.", freshScope.Requirements)
				assert.NotEqual(t, priorScope.Requirements, freshScope.Requirements)
				require.Len(t, fresh.Steps, 3)
				assert.Equal(t, "inserted-before", fresh.Steps[0].Name)
				assert.Equal(t, "first", fresh.Steps[1].Name)
				assert.Equal(t, "correctness", fresh.Steps[2].Name)
				assert.Empty(t, fresh.Findings)
				if kind == "agent_review" {
					require.Len(t, fixture.agent.launches, 4, "implementation, reviewer, repair, configured reviewer")
					assert.Equal(t, "unused-reviewer", fixture.agent.launches[1].command.Command)
					assert.Equal(t, "unused-reviewer", fixture.agent.launches[3].command.Command)
				}
				assertUnconsentedPublication(t, final)
				assert.Equal(t, "https://example.test/pr/existing", item.OrpheusMetadata().PRURL)
				assert.Empty(t, fixture.candidate.pushes)
				assert.False(t, fixture.candidate.hasStagedChanges, "implementation capture committed its completed index before review")
				assert.Equal(t, append(append(wantChecks, "inserted-before"), wantChecks...), fixture.checkCalls)
			})
		}
	}
}
