//go:build integration

package cli_test

import (
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationTaskRunStructuredCodexProfileBuildsAttachedCommand(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-codex"))
	fixture.withAgentExitingWithoutCompletion(1)
	fixture.configureImplementer("codex-medium", agent.Profile{Harness: "codex", Model: "gpt-5.4", Thinking: "high", Interactive: true})

	_, stderr, err := fixture.execute("task", "run", "op-codex")

	require.NoError(t, err, "stderr: %s", stderr)
	launch := fixture.onlyAgentLaunch()
	prompt := "Implementing op-codex Task op-codex - " + agent.RenderBootstrapPrompt()
	args := []string{"--model", "gpt-5.4", "--dangerously-bypass-approvals-and-sandbox", "-c", "model_reasoning_effort=high", prompt}
	assert.Equal(t, "codex", launch.command.Command)
	assert.Equal(t, args, launch.command.Args)
	assert.Equal(t, prompt, launch.environment["ORPHEUS_AGENT_PROMPT"])
	_, worktree := fixture.expectedTarget("op-codex")
	assert.Equal(t, worktree, launch.dir)
	final, _ := fixture.loadFinalTask("op-codex")
	require.Len(t, final.Runs, 1)
	execution := final.Runs[0].Execution
	assert.Equal(t, "codex-medium", execution.Agent)
	assert.Equal(t, "codex", execution.Harness)
	assert.Equal(t, "codex", execution.Command)
	assert.Equal(t, "gpt-5.4", execution.Model)
	assert.True(t, execution.Interactive)
	assert.Equal(t, args, execution.Args)
}

func TestIntegrationTaskRunStructuredPiProfilePersistsCapturedUsage(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-pi"))
	fixture.withAgentExitingWithoutCompletion(1)
	fixture.configureImplementer("pi-medium", agent.Profile{Harness: "pi", Model: "openai-codex/gpt-5.5", Thinking: "high"})
	captured := aCapturedPiUsage()
	var captures []agent.UsageCaptureOptions
	fixture.options.Dependencies.CaptureUsage = func(opts agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
		captures = append(captures, opts)
		return captured
	}

	_, stderr, err := fixture.execute("task", "run", "op-pi")

	require.NoError(t, err, "stderr: %s", stderr)
	final, _ := fixture.loadFinalTask("op-pi")
	require.Len(t, final.Runs, 1)
	execution := final.Runs[0].Execution
	assert.Equal(t, taskstate.RunStatusSucceeded, execution.Status)
	assert.Equal(t, "pi-medium", execution.Agent)
	assert.False(t, execution.Interactive)
	assert.Equal(t, "pi", execution.Harness)
	assert.Equal(t, "openai-codex/gpt-5.5", execution.Model)
	assert.Equal(t, captured.Session, execution.Session)
	assert.Equal(t, captured.Usage, execution.Usage)
	assert.Equal(t, captured.UsageCost, execution.UsageCost)
	assert.Equal(t, captured.UsageCapture, execution.UsageCapture)
	require.Len(t, captures, 1)
	capture := captures[0]
	_, worktree := fixture.expectedTarget("op-pi")
	assert.Equal(t, "pi", capture.Harness)
	assert.Equal(t, worktree, capture.ExecutionDir)
	assert.Equal(t, execution.SessionName, capture.SessionName)
	assert.Equal(t, execution.StartedAt, capture.StartedAt)
	assert.Equal(t, "alpha", capture.RepoID)
	assert.Equal(t, "op-pi", capture.TaskID)
	assert.Equal(t, 1, capture.Attempt)
	assert.NotNil(t, capture.Logger)
}

func TestIntegrationTaskRunAgentFlagOverridesConfiguredDefault(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-selected"))
	fixture.withAgentExitingWithoutCompletion(1)
	fixture.configureAgentProfiles(agent.AgentDefaults{Implementer: "default"}, map[string]agent.Profile{
		"default": {Command: "must-not-run"},
		"custom":  {Command: "selected-agent", Args: []string{"selected", "{{prompt}}"}},
	})

	_, stderr, err := fixture.execute("task", "run", "--agent", "custom", "op-selected")

	require.NoError(t, err, "stderr: %s", stderr)
	launch := fixture.onlyAgentLaunch()
	assert.Equal(t, "custom", launch.command.Name)
	assert.Equal(t, "selected-agent", launch.command.Command)
	assert.Equal(t, []string{"selected", agent.RenderBootstrapPrompt()}, launch.command.Args)
	final, _ := fixture.loadFinalTask("op-selected")
	require.Len(t, final.Runs, 1)
	assert.Equal(t, "custom", final.Runs[0].Execution.Profile)
}

func TestIntegrationTaskRunUnknownProfilePreventsLaunch(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-unknown"))
	fixture.configureImplementer("known", agent.Profile{Command: "unused-agent"})

	stdout, stderr, err := fixture.execute("task", "run", "--agent", "missing", "op-unknown")

	assert.ErrorContains(t, err, "resolve agent profile")
	assert.ErrorContains(t, err, `agent profile "missing" is not configured`)
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	assert.Empty(t, fixture.agent.launches)
	final, _ := fixture.loadFinalTask("op-unknown")
	assert.Empty(t, final.Runs)
}

func aCapturedPiUsage() taskstate.RecordRunUsageOptions {
	capturedAt := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	return taskstate.RecordRunUsageOptions{
		Session:      &taskstate.AgentSession{ID: "task-run-session", LogPath: "/fixture/pi/session.jsonl"},
		Usage:        &taskstate.AgentUsage{InputTokens: 150, CachedInputTokens: 20, OutputTokens: 30, ReasoningOutputTokens: 5, TotalTokens: 180},
		UsageCost:    &taskstate.AgentUsageCost{Kind: "reported", Currency: "USD", AmountMicroUSD: 1240},
		UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: "matched_pi_session", CapturedAt: &capturedAt},
	}
}
