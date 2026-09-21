//go:build integration

package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/cli"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/revieweval"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowEvalReviewContextSkipsNormalInvocationState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative")
	t.Setenv("XDG_DATA_HOME", testutil.CanonicalTempDir(t))

	stdout, stderr, err := executeEvalCommand(t, cli.CommandOptions{}, "eval", "review-context", "--repetitions", "0")

	require.ErrorContains(t, err, "repetitions must be positive, got 0")
	require.NotContains(t, err.Error(), "XDG_CONFIG_HOME must be an absolute path")
	require.Empty(t, stdout)
	require.Empty(t, stderr)
}

func TestIntegrationWorkflowEvalReviewContextReportsUnknownExecutionWhenEnvironmentFails(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	launcher := &recordingEvalLauncher{}
	provisionErr := errors.New("provision Codex auth/config: CODEX_HOME must be absolute")
	var observed revieweval.Environment

	effects := revieweval.Effects{
		InitializeBeads: func(string, string) error { return nil },
		SeedRepo:        func(context.Context, string, string) error { return nil },
		ProvisionTask:   func(context.Context, string, string) (string, error) { return "op-eval", nil },
		RunEnvironment: func(env revieweval.Environment, _ func() error) error {
			observed = env
			is.Equal(revieweval.HarnessCodex, env.Harness)
			is.Equal(revieweval.VariantExhaustive, env.Variant)
			is.Equal(revieweval.ScenarioGeneral, env.Scenario)
			return provisionErr
		},
		AgentLauncher: launcher,
	}

	stdout, stderr, err := executeEvalCommand(t, cli.CommandOptions{Dependencies: cli.Dependencies{EvaluationEffects: effects}},
		"eval", "review-context",
		"--harness", "codex",
		"--variant", "exhaustive",
		"--scenario", "general",
		"--repetitions", "1",
		"--codex-model", "test-codex",
		"--pi-model", "test-pi",
	)
	must.NoError(err, "stderr: %s", stderr)
	is.Empty(launcher.launches())

	var report revieweval.Report
	must.NoError(json.Unmarshal([]byte(stdout), &report))
	must.Len(report.Runs, 1)
	run := report.Runs[0]
	is.Contains(run.OperationalErr, "provision Codex auth/config")
	is.Contains(run.OperationalErr, "CODEX_HOME must be absolute")
	assertNoExecutionUsageAndCost(t, run)
	is.Equal(observed.RepoPath, run.RepoPath)
	is.Equal(filepath.Join(observed.ConfigBase, state.AppName), run.ConfigRoot)
	is.Equal(filepath.Join(observed.DataBase, state.AppName), run.DataRoot)
}

func TestIntegrationWorkflowEvalReviewContextKeepsOperatorBeadsEnvOutOfSeededRepo(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	operatorBeadsDir := filepath.Join(testutil.CanonicalTempDir(t), "operator-beads")
	must.NoError(os.MkdirAll(operatorBeadsDir, 0o755))
	t.Setenv("BEADS_DIR", operatorBeadsDir)
	t.Setenv("BD_NON_INTERACTIVE", "0")
	launcher := &recordingEvalLauncher{}
	var operations []string
	var provisionedRepo string
	var observedBeadsDir string
	var observedNonInteractive string

	effects := revieweval.Effects{
		InitializeBeads: func(repoPath string, prefix string) error {
			is.Equal("op", prefix)
			is.Contains(filepath.ToSlash(repoPath), "/codex-legacy-general-01/repo")
			operations = append(operations, "initialize")
			return nil
		},
		SeedRepo: func(context.Context, string, string) error {
			operations = append(operations, "seed-repo")
			return nil
		},
		ProvisionTask: func(_ context.Context, repoPath string, _ string) (string, error) {
			operations = append(operations, "seed-task")
			provisionedRepo = repoPath
			observedBeadsDir = os.Getenv("BEADS_DIR")
			observedNonInteractive = os.Getenv("BD_NON_INTERACTIVE")
			return "op-eval", nil
		},
		RunEnvironment: func(revieweval.Environment, func() error) error { return nil },
		AgentLauncher:  launcher,
	}

	stdout, stderr, err := executeEvalCommand(t, cli.CommandOptions{Dependencies: cli.Dependencies{EvaluationEffects: effects}},
		"eval", "review-context",
		"--harness", "codex",
		"--variant", "legacy",
		"--scenario", "general",
		"--repetitions", "1",
	)
	must.NoError(err, "stderr: %s", stderr)

	var report revieweval.Report
	must.NoError(json.Unmarshal([]byte(stdout), &report))
	must.Len(report.Runs, 1)
	is.Equal([]string{"initialize", "seed-repo", "seed-task"}, operations)
	is.Equal(report.Runs[0].RepoPath, provisionedRepo)
	is.NotEqual(operatorBeadsDir, provisionedRepo)
	is.Equal(operatorBeadsDir, observedBeadsDir)
	is.Equal("0", observedNonInteractive)
	is.NoFileExists(filepath.Join(operatorBeadsDir, "config.yaml"))
	is.NoDirExists(filepath.Join(operatorBeadsDir, ".beads"))
	is.Empty(launcher.launches())
}

func TestIntegrationWorkflowEvalReviewContextPersistsExecutionSessionArgsAndPromptEnv(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	launcher := &recordingEvalLauncher{}

	effects := revieweval.Effects{
		InitializeBeads: func(string, string) error { return nil },
		SeedRepo:        func(context.Context, string, string) error { return nil },
		ProvisionTask:   func(context.Context, string, string) (string, error) { return "op-eval", nil },
		RunEnvironment: func(_ revieweval.Environment, run func() error) error {
			return run()
		},
		ReviewEffects: review.Effects{
			CaptureCandidate: func(context.Context, string, *slog.Logger, ...slog.Attr) (review.CandidateCheck, error) {
				return func() error { return nil }, nil
			},
			CaptureUsage: func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
				return taskstate.RecordRunUsageOptions{UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureUnknown, Reason: "no supplied usage"}}
			},
		},
		AgentLauncher: launcher,
	}

	stdout, stderr, err := executeEvalCommand(t, cli.CommandOptions{Dependencies: cli.Dependencies{EvaluationEffects: effects}},
		"eval", "review-context",
		"--harness", "codex",
		"--variant", "legacy",
		"--scenario", "architecture",
		"--repetitions", "1",
		"--codex-model", "test-codex",
		"--pi-model", "test-pi",
		"--keep-workdirs",
	)
	must.NoError(err, "stderr: %s", stderr)

	var report revieweval.Report
	must.NoError(json.Unmarshal([]byte(stdout), &report))
	must.Len(report.Runs, 1)
	run := report.Runs[0]
	is.Equal("passed", run.ReviewStatus)
	is.True(report.IsolatedRootKept)
	t.Cleanup(func() { _ = os.RemoveAll(report.IsolatedRoot) })

	launches := launcher.launches()
	must.Len(launches, 1)
	launch := launches[0]
	execution := loadEvalReviewExecution(t, run, "op-eval")
	wantSessionName := "Reviewing op-eval Review order architecture boundaries"
	is.Equal(wantSessionName, execution.SessionName)
	is.Equal(launch.command.Args, execution.Args)
	is.Equal(run.RepoPath, launch.options.Dir)
	promptArg := execution.Args[len(execution.Args)-1]
	is.NotRegexp(`(?m)^ - `, promptArg)
	is.Contains(promptArg, wantSessionName+" - ")
	is.Equal(promptArg, envValue(launch.options.Env, "ORPHEUS_AGENT_PROMPT"))
}

func TestIntegrationWorkflowEvalReviewContextRunsPipelineWithSafeBoundaries(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	launcher := &recordingEvalLauncher{}
	var operations []string

	effects := revieweval.Effects{
		InitializeBeads: func(repoPath string, prefix string) error {
			is.Equal("op", prefix)
			is.Contains(filepath.ToSlash(repoPath), "/codex-exhaustive-architecture-01/repo")
			operations = append(operations, "initialize")
			return nil
		},
		SeedRepo: func(_ context.Context, repoPath string, scenario string) error {
			is.Equal(revieweval.ScenarioArchitecture, scenario)
			is.Contains(filepath.ToSlash(repoPath), "/codex-exhaustive-architecture-01/repo")
			operations = append(operations, "seed-repo")
			return nil
		},
		ProvisionTask: func(_ context.Context, repoPath string, scenario string) (string, error) {
			is.Equal(revieweval.ScenarioArchitecture, scenario)
			is.Contains(filepath.ToSlash(repoPath), "/codex-exhaustive-architecture-01/repo")
			operations = append(operations, "seed-task")
			return "op-eval", nil
		},
		RunEnvironment: func(_ revieweval.Environment, run func() error) error {
			return run()
		},
		ReviewEffects: review.Effects{
			CaptureCandidate: func(context.Context, string, *slog.Logger, ...slog.Attr) (review.CandidateCheck, error) {
				return func() error { return nil }, nil
			},
			CaptureUsage: func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
				return taskstate.RecordRunUsageOptions{
					Usage: &taskstate.AgentUsage{InputTokens: 10, CachedInputTokens: 2, OutputTokens: 4, TotalTokens: 14},
					UsageCost: &taskstate.AgentUsageCost{
						Kind:           "reported",
						Currency:       "USD",
						AmountMicroUSD: 123,
						Source:         "eval fixture",
					},
					UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: "fixture usage"},
					Model:        "fixture-codex",
				}
			},
		},
		AgentLauncher: launcher,
	}
	stdout, stderr, err := executeEvalCommand(t, cli.CommandOptions{Dependencies: cli.Dependencies{EvaluationEffects: effects}},
		"eval", "review-context",
		"--harness", "codex",
		"--variant", "exhaustive",
		"--scenario", "architecture",
		"--repetitions", "1",
		"--codex-model", "fixture-codex",
		"--pi-model", "fixture-pi",
		"--thinking", "low",
		"--keep-workdirs",
	)
	must.NoError(err, "stderr: %s", stderr)

	is.Equal([]string{"initialize", "seed-repo", "seed-task"}, operations)
	is.Contains(stderr, "running codex/exhaustive/architecture repetition 1")
	must.Len(launcher.launches(), 1)
	launch := launcher.launches()[0]
	is.Equal("codex", launch.command.Harness)
	is.Contains(launch.command.Args, "fixture-codex")
	is.Contains(launch.command.Args, "model_reasoning_effort=low")
	is.Contains(filepath.ToSlash(launch.options.Dir), "/codex-exhaustive-architecture-01/repo")
	prompt := envValue(launch.options.Env, "ORPHEUS_AGENT_PROMPT")
	is.NotEmpty(prompt)
	is.Equal(prompt, launch.command.Args[len(launch.command.Args)-1])
	is.Contains(prompt, "Reviewing op-eval Review order architecture boundaries - ")
	is.Contains(prompt, "Review from an architecture perspective.")
	is.Equal("review", envValue(launch.options.Env, "ORPHEUS_AGENT_PURPOSE"))
	is.Equal("ai-review", envValue(launch.options.Env, "ORPHEUS_REVIEW_STEP"))

	var report revieweval.Report
	must.NoError(json.Unmarshal([]byte(stdout), &report))
	must.Len(report.Runs, 1)
	run := report.Runs[0]
	is.Equal(revieweval.HarnessCodex, run.Harness)
	is.Equal("fixture-codex", run.Model)
	is.Equal(revieweval.VariantExhaustive, run.Variant)
	is.Equal(revieweval.ScenarioArchitecture, run.Scenario)
	is.Equal("passed", run.ReviewStatus)
	is.Empty(run.OperationalErr)
	is.Equal([]string{"domain-imports-http", "api-bypasses-service-store", "global-shared-store"}, run.MissedSeededIDs)
	is.False(run.CompleteSession)
	is.True(run.Usage.Available)
	is.Equal(14, run.Usage.Tokens["total_tokens"])
	is.True(run.Cost.Known)
	is.Equal(int64(123), run.Cost.AmountMicroUSD)
	is.Equal("eval fixture", run.Cost.Source)
	is.True(report.IsolatedRootKept)
	is.DirExists(report.IsolatedRoot)
	t.Cleanup(func() { _ = os.RemoveAll(report.IsolatedRoot) })
}

func executeEvalCommand(t *testing.T, options cli.CommandOptions, args ...string) (string, string, error) {
	t.Helper()
	return executeRootCommand(cli.NewRootCommandWithOptions(options), args...)
}

func loadEvalReviewExecution(t *testing.T, run revieweval.RunResult, taskID string) taskstate.AgentExecution {
	t.Helper()
	paths, err := state.NewPaths(run.ConfigRoot, run.DataRoot)
	require.NoError(t, err)
	loaded, err := taskstate.NewStore(paths).Load("review-eval", taskID)
	require.NoError(t, err)
	latest, ok := taskstate.LatestReview(loaded)
	require.True(t, ok)
	for index := len(latest.Steps) - 1; index >= 0; index-- {
		if latest.Steps[index].Execution != nil {
			return *latest.Steps[index].Execution
		}
	}
	require.FailNow(t, "review execution was not persisted")
	return taskstate.AgentExecution{}
}

func assertNoExecutionUsageAndCost(t *testing.T, result revieweval.RunResult) {
	t.Helper()
	assert.False(t, result.Usage.Available)
	assert.Equal(t, "agent_execution_not_recorded", result.Usage.UnknownReason)
	assert.Equal(t, "unknown", result.Usage.CaptureStatus)
	assert.Contains(t, result.Usage.CaptureReason, "not recorded")
	assert.False(t, result.Cost.Known)
	assert.Equal(t, "agent_execution_not_recorded", result.Cost.UnknownReason)
}

type recordingEvalLauncher struct {
	mu       sync.Mutex
	recorded []evalLaunch
}

type evalLaunch struct {
	command agentexec.Command
	options agentexec.LaunchOptions
}

func (l *recordingEvalLauncher) Run(ctx context.Context, command agentexec.Command, opts agentexec.LaunchOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if opts.OnStart != nil {
		if err := opts.OnStart(4343); err != nil {
			return fmt.Errorf("record child pid: %w", err)
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recorded = append(l.recorded, evalLaunch{command: command, options: opts})
	return nil
}

func (l *recordingEvalLauncher) launches() []evalLaunch {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]evalLaunch{}, l.recorded...)
}

func envValue(entries []string, key string) string {
	prefix := key + "="
	for _, entry := range entries {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}
