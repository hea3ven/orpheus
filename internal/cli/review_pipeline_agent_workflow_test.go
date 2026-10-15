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
	fixture.configureAgentProfiles(agent.AgentDefaults{Implementer: "impl", Reviewer: "other"}, map[string]agent.Profile{
		"impl":  {Command: "impl"},
		"other": {Command: "must-not-run"},
		"reviewer": {
			Harness:      "pi",
			Model:        "openai-codex/gpt-5.4-mini",
			PromptAppend: promptAppend,
		},
	})
	fixture.agent.outcomes = append(fixture.agent.outcomes, semanticAgentOutcome{review: true})
	fixture.options.Dependencies.CaptureUsage = func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
		return taskstate.RecordRunUsageOptions{Usage: &taskstate.AgentUsage{TotalTokens: 42}}
	}
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "agent_review", "name": "ai-review", "agent": "reviewer"}}})

	stdout, _ := fixture.run("", "task", "run", "op-review")

	is.Contains(stdout, "Finalized op-review")
	must.Len(fixture.agent.launches, 1)
	launch := fixture.agent.launches[0]
	wantPrompt := agent.RenderEffectivePrompt(promptAppend)
	is.Equal("pi", launch.command.Command)
	is.Equal("review", launch.environment["ORPHEUS_AGENT_PURPOSE"])
	is.Equal("ai-review", launch.environment["ORPHEUS_REVIEW_STEP"])
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
	show, _ := fixture.run("", "task", "show", "review", "op-review", "1")
	for _, want := range []string{"Execution:", "Profile: reviewer", "Harness: pi", "Model: openai-codex/gpt-5.4-mini", "Child PID: 4242", "Usage: input=0 cached_input=0 output=0 reasoning_output=0 total=42"} {
		is.Contains(show, want)
	}
	is.NotContains(show, "Primary")
	is.NotContains(show, "Alternate")
	is.Positive(execution.SupervisorPID)
	is.Contains(execution.SessionName, "Reviewing op-review")
}
