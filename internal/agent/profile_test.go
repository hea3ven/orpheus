package agent_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfigResolvesDefaultAgentAndInterpolatesPrompt(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	paths := newAgentTestPaths(t)

	must.NoError(paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"default_agent": "pi",
		"agents": map[string]any{
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
	}))

	config, err := agent.LoadConfig(paths)
	must.NoError(err)

	snapshot, err := config.ResolveCommand("", "rendered prompt")
	must.NoError(err)
	is.Equal("pi", snapshot.AgentName)
	is.Equal("pi", snapshot.Command)
	is.Equal([]string{"--model", "test-model", "rendered prompt", "literal"}, snapshot.Args)
}

func TestLoadConfigReportsMissingFileWithSetupGuidance(t *testing.T) {
	is := assert.New(t)
	paths := newAgentTestPaths(t)

	_, err := agent.LoadConfig(paths)

	if assert.Error(t, err) {
		is.True(errors.Is(err, os.ErrNotExist), "error should wrap os.ErrNotExist")
		for _, want := range []string{"config.yaml", "default_agent", "agents"} {
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
			name: "missing default",
			data: map[string]any{
				"agents": map[string]any{"pi": map[string]any{"command": "pi"}},
			},
			want: "default_agent is required",
		},
		{
			name: "missing agents",
			data: map[string]any{"default_agent": "pi"},
			want: "agents must define at least one",
		},
		{
			name: "unknown default",
			data: map[string]any{
				"default_agent": "missing",
				"agents":        map[string]any{"pi": map[string]any{"command": "pi"}},
			},
			want: "default_agent \"missing\" does not match",
		},
		{
			name: "missing command",
			data: map[string]any{
				"default_agent": "pi",
				"agents":        map[string]any{"pi": map[string]any{}},
			},
			want: "agents.pi.command is required",
		},
		{
			name: "unsupported interpolation",
			data: map[string]any{
				"default_agent": "pi",
				"agents": map[string]any{
					"pi": map[string]any{
						"command": "pi",
						"args":    []string{"{{task_id}}"},
					},
				},
			},
			want: "M3 supports only {{prompt}}",
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
		DefaultAgent: "default",
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
