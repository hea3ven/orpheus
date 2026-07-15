package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
)

func TestSyncConflictAgentUsageOptionsUnsupportedHarnessUsesStableReason(t *testing.T) {
	tests := []struct {
		name    string
		harness string
		want    string
	}{
		{name: "blank", want: "unsupported_harness:unknown"},
		{name: "trimmed", harness: " local ", want: "unsupported_harness:local"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := syncConflictAgentUsageOptions(
				agent.CommandSnapshot{Harness: tt.harness},
				t.TempDir(),
			)(taskstate.AgentExecution{}, nil)

			if options.UsageCapture.Status != taskstate.UsageCaptureUnknown {
				t.Fatalf("status = %q, want %q", options.UsageCapture.Status, taskstate.UsageCaptureUnknown)
			}
			if options.UsageCapture.Reason != tt.want {
				t.Fatalf("reason = %q, want %q", options.UsageCapture.Reason, tt.want)
			}
		})
	}
}

func TestSyncConflictAgentResolverUsesEffectivePromptInCommandAndEnvironment(t *testing.T) {
	promptAppend := "Resolve only conflict markers.\nKeep unrelated task work unchanged."
	wantPrompt := agent.RenderEffectivePrompt(promptAppend)
	paths := syncConflictPromptTestPaths(t, promptAppend)
	var gotPromptArg string
	var gotEnvPrompt string
	resolver := syncConflictPromptTestResolver(paths, &gotPromptArg, &gotEnvPrompt)

	prepared, err := resolver.PrepareSyncConflictResolution(context.Background(), workflow.SyncConflictResolutionOptions{
		Repository:    taskmodel.Repository{ID: "alpha"},
		Task:          taskmodel.Task{ID: "op-1"},
		Branch:        "orpheus/op-1",
		Worktree:      t.TempDir(),
		ConflictFiles: []string{"conflict.go"},
	})
	if err != nil {
		t.Fatalf("prepare conflict resolution: %v", err)
	}

	execution := prepared.Execution
	if execution.Harness != "pi" || execution.Model != "openai-codex/gpt-5.4-mini" {
		t.Fatalf("execution harness/model = %q/%q, want pi/openai-codex/gpt-5.4-mini", execution.Harness, execution.Model)
	}
	if got := execution.Args[len(execution.Args)-1]; got != wantPrompt {
		t.Fatalf("recorded prompt arg = %q, want %q", got, wantPrompt)
	}
	if err := prepared.Resolve(context.Background()); err != nil {
		t.Fatalf("resolve conflict: %v", err)
	}
	if gotPromptArg != wantPrompt {
		t.Fatalf("launch prompt arg = %q, want %q", gotPromptArg, wantPrompt)
	}
	if gotEnvPrompt != wantPrompt {
		t.Fatalf("env prompt = %q, want %q", gotEnvPrompt, wantPrompt)
	}
}

func syncConflictPromptTestPaths(t *testing.T, promptAppend string) state.Paths {
	t.Helper()

	paths, err := state.NewPaths(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	err = paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"implementer":            "impl",
				"sync_conflict_resolver": "sync-pi",
			},
			"profiles": map[string]any{
				"impl": map[string]any{"command": "impl"},
				"sync-pi": map[string]any{
					"harness":       "pi",
					"model":         "openai-codex/gpt-5.4-mini",
					"interactive":   false,
					"prompt_append": promptAppend,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("write config: %v", err)
	}
	return paths
}

func syncConflictPromptTestResolver(paths state.Paths, gotPromptArg *string, gotEnvPrompt *string) syncConflictAgentResolver {
	return syncConflictAgentResolver{
		paths: paths,
		launcher: syncConflictLauncherFunc(func(
			ctx context.Context,
			command agentexec.Command,
			opts agentexec.LaunchOptions,
		) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			*gotPromptArg = command.Args[len(command.Args)-1]
			*gotEnvPrompt = envValue(opts.Env, "ORPHEUS_AGENT_PROMPT")
			return nil
		}),
	}
}

type syncConflictLauncherFunc func(context.Context, agentexec.Command, agentexec.LaunchOptions) error

func (f syncConflictLauncherFunc) Run(ctx context.Context, command agentexec.Command, opts agentexec.LaunchOptions) error {
	return f(ctx, command, opts)
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}
