package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigResolvesImplementerDefaultAndInterpolatesPrompt(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths := newAgentTestPaths(t)

	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"implementer": "pi",
			},
			"profiles": map[string]any{
				"pi": map[string]any{
					"command": "pi",
					"args": []string{
						"--model",
						"test-model",
						"{{prompt}}",
						"literal",
					},
				},
			},
		},
	}))

	config, err := agent.LoadConfig(paths)
	must.NoError(err)

	snapshot, err := config.ResolveCommand("", "rendered prompt")
	must.NoError(err)
	is.Equal("pi", snapshot.AgentName)
	is.Equal("pi", snapshot.Command)
	is.Equal([]string{"--model", "test-model", "rendered prompt", "literal"}, snapshot.Args)

	_, profile, err := config.ResolveImplementerProfile("")
	must.NoError(err)
	is.True(profile.Interactive)
}

func TestLoadConfigPreservesExplicitNonInteractiveProfile(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths := newAgentTestPaths(t)

	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"implementer": "autonomous",
			},
			"profiles": map[string]any{
				"autonomous": map[string]any{
					"command":     "codex",
					"interactive": false,
				},
			},
		},
	}))

	config, err := agent.LoadConfig(paths)
	must.NoError(err)

	name, profile, err := config.ResolveImplementerProfile("")
	must.NoError(err)
	is.Equal("autonomous", name)
	is.False(profile.Interactive)
}

func TestLoadConfigInfersCodexHarnessAndModel(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths := newAgentTestPaths(t)

	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{"implementer": "codex"},
			"profiles": map[string]any{
				"codex": map[string]any{
					"command": "/usr/local/bin/codex",
					"args":    []string{"exec", "--model", "gpt-5.1", "{{prompt}}"},
				},
			},
		},
	}))

	config, err := agent.LoadConfig(paths)
	must.NoError(err)
	snapshot, err := config.ResolveCommand("", "prompt")
	must.NoError(err)

	is.Equal("codex", snapshot.Harness)
	is.Equal("gpt-5.1", snapshot.Model)
}

func TestResolveCommandInterpolatesPromptAndSessionName(t *testing.T) {
	is := assert.New(t)
	config := agent.Config{
		Defaults: agent.AgentDefaults{Implementer: "pi"},
		Agents: map[string]agent.Profile{
			"pi": {
				Command: "pi-{{session_name}}",
				Args: []string{
					"--name",
					"{{session_name}}",
					"--prompt",
					"{{session_name}} - {{prompt}}",
				},
			},
		},
	}

	snapshot, err := config.ResolveCommandWithValues("", agent.InterpolationValues{
		Prompt:      "rendered prompt",
		SessionName: "(op-1) Implement task",
	})

	require.NoError(t, err)
	is.Equal("pi-(op-1) Implement task", snapshot.Command)
	is.Equal([]string{
		"--name",
		"(op-1) Implement task",
		"--prompt",
		"(op-1) Implement task - rendered prompt",
	}, snapshot.Args)
}

func TestLoadConfigResolvesNestedImplementerDefault(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths := newAgentTestPaths(t)

	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"implementer": "impl",
			},
			"profiles": map[string]any{
				"impl": map[string]any{
					"command": "codex",
					"args":    []string{"{{prompt}}"},
				},
				"other": map[string]any{
					"command": "other",
				},
			},
		},
	}))

	config, err := agent.LoadConfig(paths)
	must.NoError(err)

	snapshot, err := config.ResolveImplementerCommand("", "follow-up prompt")
	must.NoError(err)
	is.Equal("impl", snapshot.AgentName)
	is.Equal("codex", snapshot.Command)
	is.Equal([]string{"follow-up prompt"}, snapshot.Args)

	override, err := config.ResolveImplementerCommand("other", "follow-up prompt")
	must.NoError(err)
	is.Equal("other", override.AgentName)
	is.Equal("other", override.Command)
}

func TestResolveReviewerCommandUsesReviewerDefaultOrOverride(t *testing.T) {
	is := assert.New(t)
	config := agent.Config{
		Defaults: agent.AgentDefaults{Implementer: "impl", Reviewer: "reviewer"},
		Agents: map[string]agent.Profile{
			"impl":     {Command: "impl-agent"},
			"reviewer": {Command: "review-agent", Args: []string{"{{session_name}}", "{{prompt}}"}},
			"custom":   {Command: "custom-review"},
		},
	}

	snapshot, err := config.ResolveReviewerCommandWithValues("", agent.InterpolationValues{
		Prompt:      "review prompt",
		SessionName: "Reviewing op-1 Review task",
	})

	require.NoError(t, err)
	is.Equal("reviewer", snapshot.AgentName)
	is.Equal("review-agent", snapshot.Command)
	is.Equal([]string{"Reviewing op-1 Review task", "review prompt"}, snapshot.Args)

	override, err := config.ResolveReviewerCommand("custom", "review prompt")
	require.NoError(t, err)
	is.Equal("custom", override.AgentName)
	is.Equal("custom-review", override.Command)
}

func TestResolveReviewerCommandRequiresReviewerDefaultWithoutOverride(t *testing.T) {
	config := agent.Config{
		Defaults: agent.AgentDefaults{Implementer: "impl"},
		Agents: map[string]agent.Profile{
			"impl": {Command: "impl-agent"},
		},
	}

	_, err := config.ResolveReviewerCommand("", "review prompt")

	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "agents.defaults.reviewer is required")
	}
}

func TestLoadConfigReportsMissingFileWithSetupGuidance(t *testing.T) {
	is := assert.New(t)
	paths := newAgentTestPaths(t)

	_, err := agent.LoadConfig(paths)

	if assert.Error(t, err) {
		is.ErrorIs(err, os.ErrNotExist)
		for _, want := range []string{"config.yaml", "agents.defaults", "agents.profiles"} {
			is.Contains(err.Error(), want)
		}
	}
}

func TestConfigValidationErrorsAreActionable(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
		want string
	}{
		{
			name: "missing implementer default",
			data: agentConfigYAML(nil, map[string]any{"pi": map[string]any{"command": "pi"}}),
			want: "agents.defaults.implementer is required",
		},
		{
			name: "missing agents",
			data: agentConfigYAML(map[string]any{"implementer": "pi"}, nil),
			want: "agents must define at least one",
		},
		{
			name: "unknown implementer default",
			data: agentConfigYAML(
				map[string]any{"implementer": "missing"},
				map[string]any{"pi": map[string]any{"command": "pi"}},
			),
			want: "agents.defaults.implementer \"missing\" does not match",
		},
		{
			name: "missing command",
			data: agentConfigYAML(
				map[string]any{"implementer": "pi"},
				map[string]any{"pi": map[string]any{}},
			),
			want: "agents.profiles.pi.command is required",
		},
		{
			name: "unsupported interpolation",
			data: agentConfigYAML(
				map[string]any{"implementer": "pi"},
				map[string]any{
					"pi": map[string]any{
						"command": "pi",
						"args":    []string{"{{task_id}}"},
					},
				},
			),
			want: "supported interpolation tokens: {{prompt}}, {{session_name}}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths := newAgentTestPaths(t)
			require.NoError(t, paths.WriteConfigYAML(agent.ConfigFile, tt.data))

			_, err := agent.LoadConfig(paths)

			if assert.Error(t, err) && !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestResolveCommandSelectsNamedAgent(t *testing.T) {
	is := assert.New(t)
	config := agent.Config{
		Defaults: agent.AgentDefaults{Implementer: "default"},
		Agents: map[string]agent.Profile{
			"default": {Command: "default-agent"},
			"custom":  {Command: "custom-agent", Args: []string{"{{prompt}}"}},
		},
	}

	snapshot, err := config.ResolveCommand(" custom ", "prompt text")

	require.NoError(t, err)
	is.Equal("custom", snapshot.AgentName)
	is.Equal("custom-agent", snapshot.Command)
	is.Equal([]string{"prompt text"}, snapshot.Args)
}

func newAgentTestPaths(t *testing.T) state.Paths {
	t.Helper()

	root := t.TempDir()
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	return paths
}

func agentConfigYAML(defaults map[string]any, profiles map[string]any) map[string]any {
	agents := map[string]any{}
	if defaults != nil {
		agents["defaults"] = defaults
	}
	if profiles != nil {
		agents["profiles"] = profiles
	}
	return map[string]any{"agents": agents}
}
