package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigResolvesImplementerDefaultAndInterpolatesBootstrapPrompt(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths := newAgentTestPaths(t)

	must.NoError(testutil.WriteConfigYAML(paths, agent.ConfigFile, map[string]any{
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
	is.True(snapshot.Interactive)

	_, profile, err := config.ResolveImplementerProfile("")
	must.NoError(err)
	is.True(profile.Interactive)
}

func TestLoadConfigPreservesExplicitNonInteractiveProfile(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths := newAgentTestPaths(t)

	must.NoError(testutil.WriteConfigYAML(paths, agent.ConfigFile, map[string]any{
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

	snapshot, err := config.ResolveCommand("")
	must.NoError(err)
	is.False(snapshot.Interactive)
}

func TestLoadConfigBuildsStructuredCodexCommands(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths := newAgentTestPaths(t)

	must.NoError(testutil.WriteConfigYAML(paths, agent.ConfigFile, map[string]any{
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
	is.True(impl.Interactive)
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
	is.False(reviewer.Interactive)
	is.Equal([]string{
		"exec",
		"--model",
		"gpt-5.4-mini",
		"--dangerously-bypass-approvals-and-sandbox",
		"Reviewing op-1 Implement task - " + agent.RenderBootstrapPrompt(),
	}, reviewer.Args)
}

func TestLoadConfigBuildsStructuredPiCommands(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths := newAgentTestPaths(t)

	must.NoError(testutil.WriteConfigYAML(paths, agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{"implementer": "pi-interactive", "reviewer": "pi-print"},
			"profiles": map[string]any{
				"pi-interactive": map[string]any{
					"harness":     "pi",
					"model":       "openai-codex/gpt-5.5",
					"thinking":    "high",
					"interactive": true,
				},
				"pi-print": map[string]any{
					"harness":     "pi",
					"model":       "openai-codex/gpt-5.4-mini",
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

	is.Equal("pi-interactive", impl.AgentName)
	is.Equal("pi", impl.Command)
	is.Equal("pi", impl.Harness)
	is.Equal("openai-codex/gpt-5.5", impl.Model)
	is.True(impl.Interactive)
	is.Equal([]string{
		"--model",
		"openai-codex/gpt-5.5",
		"--thinking",
		"high",
		"--name",
		"(op-1) Implement task",
		agent.RenderBootstrapPrompt(),
	}, impl.Args)

	reviewer, err := config.ResolveReviewerCommandWithValues("", agent.InterpolationValues{
		SessionName: "Reviewing op-1 Implement task",
	})
	must.NoError(err)

	is.Equal("pi-print", reviewer.AgentName)
	is.Equal("pi", reviewer.Command)
	is.Equal("pi", reviewer.Harness)
	is.Equal("openai-codex/gpt-5.4-mini", reviewer.Model)
	is.False(reviewer.Interactive)
	is.Equal([]string{
		"--print",
		"--model",
		"openai-codex/gpt-5.4-mini",
		"--name",
		"Reviewing op-1 Implement task",
		agent.RenderBootstrapPrompt(),
	}, reviewer.Args)
}

func TestLoadedStructuredCommandsAppendInstructionsAfterBootstrap(t *testing.T) {
	for _, tc := range []struct {
		name, harness, model, append string
		interactive                  bool
	}{
		{"codex custom", "codex", "gpt-5.4", "Focus on architecture boundaries.", true},
		{"pi custom", "pi", "openai-codex/gpt-5.4-mini", "Review module boundaries.\nCall out dependency direction risks.\n", false},
		{"pi blank", "pi", "openai-codex/gpt-5.5", " \n\t ", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths := newAgentTestPaths(t)
			require.NoError(t, testutil.WriteConfigYAML(paths, agent.ConfigFile, map[string]any{
				"agents": map[string]any{
					"defaults": map[string]any{"implementer": "configured"},
					"profiles": map[string]any{"configured": map[string]any{
						"harness": tc.harness, "model": tc.model, "interactive": tc.interactive, "prompt_append": tc.append,
					}},
				},
			}))
			config, err := agent.LoadConfig(paths)
			require.NoError(t, err)

			snapshot, err := config.ResolveCommandWithValues("", agent.InterpolationValues{SessionName: "Architecture task"})

			require.NoError(t, err)
			require.NotEmpty(t, snapshot.Args)
			prompt := snapshot.Args[len(snapshot.Args)-1]
			assert.Equal(t, snapshot.Prompt, prompt)
			assert.Equal(t, tc.harness, snapshot.Harness)
			assert.Equal(t, tc.model, snapshot.Model)
			assert.Contains(t, snapshot.Args, tc.model)
			assert.Contains(t, prompt, "Run `orpheus agent context` now")
			assert.Equal(t, 1, strings.Count(prompt, "Run `orpheus agent context` now"))
			marker := strings.TrimSpace(tc.append)
			if marker == "" {
				assert.NotContains(t, prompt, "Supplemental instructions:")
				assert.True(t, strings.HasSuffix(prompt, agent.RenderBootstrapPrompt()))
			} else {
				assert.Equal(t, 1, strings.Count(prompt, marker))
				assert.Contains(t, prompt, "\nSupplemental instructions:\n"+marker)
				assert.Less(t, strings.Index(prompt, "Run `orpheus agent context` now"), strings.Index(prompt, marker))
			}
			if tc.harness == "pi" {
				assert.Contains(t, snapshot.Args, "--name")
				assert.Contains(t, snapshot.Args, "Architecture task")
				if tc.interactive {
					assert.NotContains(t, snapshot.Args, "--print")
				} else {
					assert.Contains(t, snapshot.Args, "--print")
				}
			} else {
				assert.True(t, strings.HasPrefix(prompt, "Architecture task - "))
			}
		})
	}
}

func TestLoadConfigLeavesRawCodexCommandGeneric(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths := newAgentTestPaths(t)

	must.NoError(testutil.WriteConfigYAML(paths, agent.ConfigFile, map[string]any{
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

	must.NoError(testutil.WriteConfigYAML(paths, agent.ConfigFile, map[string]any{
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

	for _, tc := range []struct{ override, wantName, wantCommand string }{
		{"", "impl", "codex"}, {"other", "other", "other"}, {" other ", "other", "other"},
	} {
		snapshot, err := config.ResolveImplementerCommand(tc.override)
		must.NoError(err)
		is.Equal(tc.wantName, snapshot.AgentName)
		is.Equal(tc.wantCommand, snapshot.Command)
		if tc.wantName == "impl" {
			is.Equal([]string{agent.RenderBootstrapPrompt()}, snapshot.Args)
		}
	}
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

func TestResolveSyncConflictResolverCommandPrecedence(t *testing.T) {
	for _, tc := range []struct{ name, dedicated, wantName, wantCommand string }{
		{"dedicated", "sync-resolver", "sync-resolver", "resolver-agent"},
		{"implementer fallback", "", "impl", "impl-agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := agent.Config{
				Defaults: agent.AgentDefaults{Implementer: "impl", SyncConflictResolver: tc.dedicated},
				Agents: map[string]agent.Profile{
					"impl":          {Command: "impl-agent", Args: []string{"{{session_name}}", "{{prompt}}"}},
					"sync-resolver": {Command: "resolver-agent", Args: []string{"{{session_name}}", "{{prompt}}"}},
				},
			}

			snapshot, err := config.ResolveSyncConflictResolverCommand(agent.InterpolationValues{SessionName: "sync-conflict-op-1"})

			require.NoError(t, err)
			assert.Equal(t, tc.wantName, snapshot.AgentName)
			assert.Equal(t, tc.wantCommand, snapshot.Command)
			assert.Equal(t, []string{"sync-conflict-op-1", agent.RenderBootstrapPrompt()}, snapshot.Args)
		})
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
			name: "structured pi missing model",
			data: agentConfigYAML(
				map[string]any{"implementer": "pi"},
				map[string]any{"pi": map[string]any{"harness": "pi"}},
			),
			want: "agents.profiles.pi.model is required for harness: pi",
		},
		{
			name: "structured pi mixed with raw command",
			data: agentConfigYAML(
				map[string]any{"implementer": "pi"},
				map[string]any{
					"pi": map[string]any{
						"harness": "pi",
						"model":   "openai-codex/gpt-5.5",
						"command": "pi",
					},
				},
			),
			want: "mixes structured Pi configuration with raw command/args",
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
			want: "model requires structured harness: codex or pi",
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
			want: "thinking requires structured harness: codex or pi",
		},
		{
			name: "raw command cannot set prompt append",
			data: agentConfigYAML(
				map[string]any{"implementer": "custom"},
				map[string]any{
					"custom": map[string]any{
						"command":       "custom-agent",
						"prompt_append": "Review architecture.",
					},
				},
			),
			want: "prompt_append requires structured harness: codex or pi",
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
			require.NoError(t, testutil.WriteConfigYAML(paths, agent.ConfigFile, tt.data))

			_, err := agent.LoadConfig(paths)

			if assert.Error(t, err) && !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func newAgentTestPaths(t *testing.T) state.Paths {
	t.Helper()

	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(
		filepath.Join(root, "config", state.AppName),
		filepath.Join(root, "data", state.AppName),
	)
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
