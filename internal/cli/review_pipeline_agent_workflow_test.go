//go:build integration

package cli_test

import (
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowReviewPipelinePassesPromptModelEnvironmentAndChildPIDThroughCLI(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-review", "Review prompt", "Check review prompt plumbing.")
	promptAppend := "Review architecture boundaries.\nCall out dependency direction risks."
	fixture.configureAgentProfiles(agent.AgentDefaults{Implementer: "impl", Reviewer: "reviewer"}, map[string]agent.Profile{
		"impl": {Command: "impl"},
		"reviewer": {
			Harness:      "pi",
			Model:        "openai-codex/gpt-5.4-mini",
			PromptAppend: promptAppend,
		},
	})
	fixture.agent.outcomes = append(fixture.agent.outcomes, semanticAgentOutcome{review: true})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "agent_review", "name": "ai-review"}}})

	stdout, _ := fixture.run("", "task", "run", "op-review")

	is.Contains(stdout, "Finalized op-review")
	must.Len(fixture.agent.launches, 1)
	launch := fixture.agent.launches[0]
	wantPrompt := agent.RenderEffectivePrompt(promptAppend)
	is.Equal("review", launch.environment["ORPHEUS_AGENT_PURPOSE"])
	is.Equal("ai-review", launch.environment["ORPHEUS_REVIEW_STEP"])
	is.Empty(launch.environment["ORPHEUS_REVIEWER_ROLE"])
	is.Equal(wantPrompt, launch.environment["ORPHEUS_AGENT_PROMPT"])
	must.NotEmpty(launch.command.Args)
	is.Equal(wantPrompt, launch.command.Args[len(launch.command.Args)-1])

	var state taskstate.TaskState
	must.NoError(fixture.paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-review.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	must.Len(latest.Steps, 1)
	must.NotNil(latest.Steps[0].Execution)
	execution := latest.Steps[0].Execution
	is.Equal(taskstate.RunStatusSucceeded, execution.Status)
	is.Equal("pi", execution.Harness)
	is.Equal("openai-codex/gpt-5.4-mini", execution.Model)
	is.Equal(4242, execution.ChildPID)
	is.Positive(execution.SupervisorPID)
	is.Contains(execution.SessionName, "Reviewing op-review")
}
