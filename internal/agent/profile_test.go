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

func TestLoadConfigResolvesImplementerDefaultAndInterpolatesBootstrapPrompt(t *testing.T) {
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

	snapshot, err := config.ResolveCommand("")
	must.NoError(err)
	is.Equal("pi", snapshot.AgentName)
	is.Equal("pi", snapshot.Command)
	is.Equal([]string{"--model", "test-model", agent.RenderBootstrapPrompt(), "literal"}, snapshot.Args)

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
					"command":     "agent",
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

func TestLoadConfigBuildsStructuredCodexCommands(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths := newAgentTestPaths(t)

	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{"implementer": "codex-interactive", "reviewer": "codex-exec"},
			"profiles": map[string]any{
				"codex-interactive": map[string]any{
					"harness":     "codex",
					"model":       "gpt-5.4",
					"thinking":    "high",
					"interactive": true,
				},
				"codex-exec": map[string]any{
					"harness":     "codex",
					"model":       "gpt-5.4-mini",
					"interactive": false,
				},
			},
		},
	}))

	config, err := agent.LoadConfig(paths)
	must.NoError(err)
	impl, err := config.ResolveCommandWithValues("", agent.InterpolationValues{
		SessionName: "(op-1) Implement task",
	})
	must.NoError(err)

	is.Equal("codex-interactive", impl.AgentName)
	is.Equal("codex", impl.Command)
	is.Equal("codex", impl.Harness)
	is.Equal("gpt-5.4", impl.Model)
	is.Equal([]string{
		"--model",
		"gpt-5.4",
		"--dangerously-bypass-approvals-and-sandbox",
		"-c",
		"model_reasoning_effort=high",
		"(op-1) Implement task - " + agent.RenderBootstrapPrompt(),
	}, impl.Args)

	reviewer, err := config.ResolveReviewerCommandWithValues("", agent.InterpolationValues{
		SessionName: "Reviewing op-1 Implement task",
	})
	must.NoError(err)

	is.Equal("codex-exec", reviewer.AgentName)
	is.Equal("codex", reviewer.Command)
	is.Equal("codex", reviewer.Harness)
	is.Equal("gpt-5.4-mini", reviewer.Model)
	is.Equal([]string{
		"exec",
		"--model",
		"gpt-5.4-mini",
		"--dangerously-bypass-approvals-and-sandbox",
		"Reviewing op-1 Implement task - " + agent.RenderBootstrapPrompt(),
	}, reviewer.Args)
}

func TestLoadConfigLeavesRawCodexCommandGeneric(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths := newAgentTestPaths(t)

	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{"implementer": "codex"},
			"profiles": map[string]any{
				"codex": map[string]any{
					"command": "codex",
					"args":    []string{"exec", "--model", "gpt-5.1", "{{prompt}}"},
				},
			},
		},
	}))

	config, err := agent.LoadConfig(paths)
	must.NoError(err)
	snapshot, err := config.ResolveCommand("")
	must.NoError(err)

	is.Equal("codex", snapshot.Command)
	is.Equal([]string{"exec", "--model", "gpt-5.1", agent.RenderBootstrapPrompt()}, snapshot.Args)
	is.Empty(snapshot.Harness)
	is.Empty(snapshot.Model)
}

func TestResolveCommandInterpolatesBootstrapPromptAndSessionName(t *testing.T) {
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
		SessionName: "(op-1) Implement task",
	})

	require.NoError(t, err)
	is.Equal("pi-(op-1) Implement task", snapshot.Command)
	is.Equal([]string{
		"--name",
		"(op-1) Implement task",
		"--prompt",
		"(op-1) Implement task - " + agent.RenderBootstrapPrompt(),
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

	snapshot, err := config.ResolveImplementerCommand("")
	must.NoError(err)
	is.Equal("impl", snapshot.AgentName)
	is.Equal("codex", snapshot.Command)
	is.Equal([]string{agent.RenderBootstrapPrompt()}, snapshot.Args)

	override, err := config.ResolveImplementerCommand("other")
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
		SessionName: "Reviewing op-1 Review task",
	})

	require.NoError(t, err)
	is.Equal("reviewer", snapshot.AgentName)
	is.Equal("review-agent", snapshot.Command)
	is.Equal([]string{"Reviewing op-1 Review task", agent.RenderBootstrapPrompt()}, snapshot.Args)

	override, err := config.ResolveReviewerCommand("custom")
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

	_, err := config.ResolveReviewerCommand("")

	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "agents.defaults.reviewer is required")
	}
}

func TestResolveSyncConflictResolverCommandUsesDedicatedDefault(t *testing.T) {
	is := assert.New(t)
	config := agent.Config{
		Defaults: agent.AgentDefaults{
			Implementer:          "impl",
			SyncConflictResolver: "sync-resolver",
		},
		Agents: map[string]agent.Profile{
			"impl":          {Command: "impl-agent"},
			"sync-resolver": {Command: "resolver-agent", Args: []string{"{{session_name}}", "{{prompt}}"}},
		},
	}

	snapshot, err := config.ResolveSyncConflictResolverCommand(agent.InterpolationValues{
		SessionName: "sync-conflict-op-1",
	})

	require.NoError(t, err)
	is.Equal("sync-resolver", snapshot.AgentName)
	is.Equal("resolver-agent", snapshot.Command)
	is.Equal([]string{"sync-conflict-op-1", agent.RenderBootstrapPrompt()}, snapshot.Args)
}

func TestResolveSyncConflictResolverCommandFallsBackToImplementerDefault(t *testing.T) {
	is := assert.New(t)
	config := agent.Config{
		Defaults: agent.AgentDefaults{Implementer: "impl"},
		Agents: map[string]agent.Profile{
			"impl": {Command: "impl-agent"},
		},
	}

	snapshot, err := config.ResolveSyncConflictResolverCommand(agent.InterpolationValues{})

	require.NoError(t, err)
	is.Equal("impl", snapshot.AgentName)
	is.Equal("impl-agent", snapshot.Command)
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

//nolint:funlen // The validation matrix is clearer as one table of config failures.
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
			name: "unknown sync conflict resolver default",
			data: agentConfigYAML(
				map[string]any{"implementer": "pi", "sync_conflict_resolver": "missing"},
				map[string]any{"pi": map[string]any{"command": "pi"}},
			),
			want: "agents.defaults.sync_conflict_resolver \"missing\" does not match",
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
			name: "structured codex missing model",
			data: agentConfigYAML(
				map[string]any{"implementer": "codex"},
				map[string]any{"codex": map[string]any{"harness": "codex"}},
			),
			want: "agents.profiles.codex.model is required for harness: codex",
		},
		{
			name: "structured codex mixed with raw command",
			data: agentConfigYAML(
				map[string]any{"implementer": "codex"},
				map[string]any{
					"codex": map[string]any{
						"harness": "codex",
						"model":   "gpt-5.4",
						"command": "codex",
					},
				},
			),
			want: "mixes structured Codex configuration with raw command/args",
		},
		{
			name: "raw command cannot set model",
			data: agentConfigYAML(
				map[string]any{"implementer": "custom"},
				map[string]any{
					"custom": map[string]any{
						"command": "custom-agent",
						"model":   "gpt-5.4",
					},
				},
			),
			want: "model requires structured harness: codex",
		},
		{
			name: "raw command cannot set thinking",
			data: agentConfigYAML(
				map[string]any{"implementer": "custom"},
				map[string]any{
					"custom": map[string]any{
						"command":  "custom-agent",
						"thinking": "high",
					},
				},
			),
			want: "thinking requires structured harness: codex",
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

	snapshot, err := config.ResolveCommand(" custom ")

	require.NoError(t, err)
	is.Equal("custom", snapshot.AgentName)
	is.Equal("custom-agent", snapshot.Command)
	is.Equal([]string{agent.RenderBootstrapPrompt()}, snapshot.Args)
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
