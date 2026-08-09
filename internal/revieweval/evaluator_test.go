//nolint:testpackage // Exercises unexported environment provisioning without launching live model harnesses.
package revieweval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunCleansUpCompletedRunRootBeforeNextRun(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var previousRunRoot string
	runCount := 0

	executor := func(_ context.Context, root string, opts Options, spec runSpec) RunResult {
		is.False(opts.KeepWorkdirs)
		if previousRunRoot != "" {
			is.NoDirExists(previousRunRoot)
		}
		runRoot := filepath.Join(root, runDirectoryName(spec))
		must.NoError(os.MkdirAll(runRoot, 0o755))
		must.NoError(os.WriteFile(filepath.Join(runRoot, "artifact.txt"), []byte("large run artifact"), 0o600))
		previousRunRoot = runRoot
		runCount++
		return newRunResult(opts, spec, scenarioByName(spec.Scenario))
	}

	err := runWithExecutor(
		context.Background(),
		Options{
			Harnesses:   []string{HarnessCodex},
			Variants:    []string{VariantLegacy},
			Scenarios:   []string{ScenarioGeneral},
			Repetitions: 2,
			CodexModel:  "test-codex",
			PiModel:     "test-pi",
		},
		&stdout,
		&stderr,
		executor,
	)
	must.NoError(err)
	is.Equal(2, runCount)
	is.NoDirExists(previousRunRoot)
	is.NotContains(stderr.String(), "warning: clean up")

	var report Report
	must.NoError(json.Unmarshal(stdout.Bytes(), &report))
	must.Len(report.Runs, 2)
	is.False(report.IsolatedRootKept)
	is.NoDirExists(report.IsolatedRoot)
}

func TestRunKeepsCompletedRunRootWhenRequested(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var runRoot string

	executor := func(_ context.Context, root string, opts Options, spec runSpec) RunResult {
		is.True(opts.KeepWorkdirs)
		runRoot = filepath.Join(root, runDirectoryName(spec))
		must.NoError(os.MkdirAll(runRoot, 0o755))
		must.NoError(os.WriteFile(filepath.Join(runRoot, "artifact.txt"), []byte("kept run artifact"), 0o600))
		return newRunResult(opts, spec, scenarioByName(spec.Scenario))
	}

	err := runWithExecutor(
		context.Background(),
		Options{
			Harnesses:    []string{HarnessCodex},
			Variants:     []string{VariantLegacy},
			Scenarios:    []string{ScenarioGeneral},
			Repetitions:  1,
			CodexModel:   "test-codex",
			PiModel:      "test-pi",
			KeepWorkdirs: true,
		},
		&stdout,
		&stderr,
		executor,
	)
	must.NoError(err)
	is.DirExists(runRoot)
	is.FileExists(filepath.Join(runRoot, "artifact.txt"))
	is.NotContains(stderr.String(), "warning: clean up")

	var report Report
	must.NoError(json.Unmarshal(stdout.Bytes(), &report))
	is.True(report.IsolatedRootKept)
	is.DirExists(report.IsolatedRoot)
	t.Cleanup(func() {
		_ = os.RemoveAll(report.IsolatedRoot)
	})
}

func TestExecuteRunReportsUsageAndCostUnknownWhenSetupFailsBeforeExecution(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	rootFile := filepath.Join(t.TempDir(), "root-file")
	must.NoError(os.WriteFile(rootFile, []byte("not a directory"), 0o600))

	result := executeRun(
		context.Background(),
		rootFile,
		Options{CodexModel: "test-codex", PiModel: "test-pi"},
		runSpec{Harness: HarnessCodex, Variant: VariantLegacy, Scenario: ScenarioGeneral, Repetition: 1},
	)

	is.NotEmpty(result.OperationalErr)
	is.Contains(result.OperationalErr, "create scenario repo")
	assertNoExecutionUsageAndCost(t, result)
	is.ElementsMatch([]string{
		"empty-token-auth-bypass",
		"json-decode-error-ignored",
		"cache-invalidation-removed",
	}, result.MissedSeededIDs)
	is.False(result.CompleteSession)
}

func TestExecuteRunReportsUsageAndCostUnknownWhenProvisioningFailsBeforeExecution(t *testing.T) {
	is := assert.New(t)
	fakeReviewEvalBD(t)
	t.Setenv("CODEX_HOME", "relative-codex-home")

	result := executeRun(
		context.Background(),
		t.TempDir(),
		Options{CodexModel: "test-codex", PiModel: "test-pi"},
		runSpec{Harness: HarnessCodex, Variant: VariantExhaustive, Scenario: ScenarioGeneral, Repetition: 1},
	)

	is.NotEmpty(result.OperationalErr)
	is.Contains(result.OperationalErr, "provision Codex auth/config")
	is.Contains(result.OperationalErr, "CODEX_HOME must be absolute")
	assertNoExecutionUsageAndCost(t, result)
	is.NotEmpty(result.RepoPath)
	is.NotEmpty(result.ConfigRoot)
	is.NotEmpty(result.DataRoot)
}

func TestPrepareRunInitializesRepoLocalBeadsWhenOperatorBeadsDirIsSet(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fakeReviewEvalBD(t)
	operatorBeadsDir := filepath.Join(t.TempDir(), "operator-beads")
	must.NoError(os.MkdirAll(operatorBeadsDir, 0o755))
	t.Setenv("BEADS_DIR", operatorBeadsDir)
	t.Setenv("BD_NON_INTERACTIVE", "0")

	setup, err := prepareRun(
		context.Background(),
		t.TempDir(),
		scenarioByName(ScenarioGeneral),
		runSpec{Harness: HarnessCodex, Variant: VariantLegacy, Scenario: ScenarioGeneral, Repetition: 1},
	)
	must.NoError(err)

	is.DirExists(filepath.Join(setup.repoPath, ".beads"))
	is.FileExists(filepath.Join(setup.repoPath, ".beads", "config.yaml"))
	is.NoFileExists(filepath.Join(operatorBeadsDir, "config.yaml"))
	is.NoDirExists(filepath.Join(operatorBeadsDir, ".beads"))
}

func TestEvaluatorPrimaryChildPIDPreservesConcurrentFinding(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(t.TempDir(), "config"), filepath.Join(t.TempDir(), "data"))
	require.NoError(t, err)
	store := taskstate.NewStore(paths)
	attempt, err := store.StartReviewWithOptions("review-eval", "eval-1", taskstate.StartReviewOptions{Pipeline: "evaluation", Step: "ai-review"})
	require.NoError(t, err)
	execution := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, Agent: "reviewer", StartedAt: attempt.StartedAt, SupervisorPID: 10}
	_, err = store.RecordReviewStep("review-eval", "eval-1", attempt.Attempt, taskstate.RecordReviewStepOptions{Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &execution})
	require.NoError(t, err)
	setup := runSetup{paths: paths, taskID: "eval-1", attempt: attempt, store: store}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		errs <- recordEvaluatorPrimaryChildPID(setup, "ai-review", 4242)
	}()
	go func() {
		defer wait.Done()
		<-start
		errs <- retryEvaluatorMutationLock(paths, "test evaluator finding", func() error {
			_, err := store.RecordReviewFinding("review-eval", "eval-1", attempt.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Step: "ai-review", Title: "immediate", Description: "finding"})
			return err
		})
	}()
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	loaded, err := store.Load("review-eval", "eval-1")
	require.NoError(t, err)
	latest, ok := taskstate.LatestReview(loaded)
	require.True(t, ok)
	assert.Equal(t, 4242, latest.Steps[0].Execution.ChildPID)
	assert.Len(t, latest.Findings, 1)
}

func retryEvaluatorMutationLock(paths state.Paths, operation string, mutate func() error) error {
	var err error
	for retry := 0; retry < 100; retry++ {
		err = state.WithGlobalMutationLock(paths, operation, mutate)
		if err == nil || !errors.Is(err, os.ErrExist) {
			return err
		}
		time.Sleep(time.Millisecond)
	}
	return err
}

func TestRunPipelineRecordsCodexPromptArgWithEvaluationSessionName(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fakeReviewEvalBD(t)
	root := t.TempDir()
	scenarioDef := architectureScenario()
	spec := runSpec{Harness: HarnessCodex, Variant: VariantLegacy, Scenario: ScenarioArchitecture, Repetition: 1}
	setup, err := prepareRun(context.Background(), root, scenarioDef, spec)
	must.NoError(err)

	binDir := filepath.Join(root, "bin")
	must.NoError(os.MkdirAll(binDir, 0o755))
	codexPath := filepath.Join(binDir, "codex")
	must.NoError(os.WriteFile(codexPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv(testguard.FakeAgentEnvKey("codex"), codexPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err = runPipeline(
		context.Background(),
		Options{CodexModel: "test-codex", PiModel: "test-pi"},
		spec,
		scenarioDef,
		setup,
	)
	must.NoError(err)

	stateResult := collectRunState(setup)
	must.NotNil(stateResult.execution)
	is.Equal(evaluationSessionName(setup.taskID, scenarioDef), stateResult.execution.SessionName)
	must.NotEmpty(stateResult.execution.Args)
	promptArg := stateResult.execution.Args[len(stateResult.execution.Args)-1]
	is.NotRegexp(`(?m)^ - `, promptArg)
	is.Contains(promptArg, stateResult.execution.SessionName+" - ")
}

func fakeReviewEvalBD(t *testing.T) {
	t.Helper()

	binDir := t.TempDir()
	path := filepath.Join(binDir, "bd")
	const script = `#!/bin/sh
case "$*" in
  *" init "*|"init "*)
    mkdir -p .beads
    printf 'issue_prefix: op\n' > .beads/config.yaml
    ;;
  "--json --sandbox create "*)
    printf '%s\n' '{"id":"op-eval","title":"Review evaluation task","status":"open","issue_type":"task"}'
    ;;
  "--json --readonly --sandbox show --id op-eval")
    printf '%s\n' '[{"id":"op-eval","title":"Review evaluation task","status":"open","issue_type":"task"}]'
    ;;
  "--json --sandbox update op-eval "*)
    ;;
  *)
    printf 'unexpected bd args: %s\n' "$*" >&2
    exit 64
    ;;
esac
`
	must := require.New(t)
	must.NoError(os.WriteFile(path, []byte(script), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestAggregateResultsReportsUsageTotals(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	aggregates := aggregateResults([]RunResult{
		aggregateUsageRun(ScenarioGeneral, 1, map[string]int{
			"input_tokens":  10,
			"output_tokens": 4,
			"total_tokens":  14,
		}),
		aggregateUsageRun(ScenarioGeneral, 0.5, map[string]int{
			"input_tokens":        7,
			"cached_input_tokens": 2,
			"total_tokens":        11,
		}),
		aggregateUnknownUsageRun(ScenarioGeneral, 0, "usage_not_recorded"),
		aggregateUnknownUsageRun(ScenarioArchitecture, 0, ""),
	})

	must.Len(aggregates, 2)
	general, ok := aggregateFor(aggregates, HarnessCodex, VariantLegacy, ScenarioGeneral)
	must.True(ok)
	is.Equal(3, general.Runs)
	is.Equal(2, general.UsageAvailableRuns)
	is.Equal(map[string]int{
		"cached_input_tokens": 2,
		"input_tokens":        17,
		"output_tokens":       4,
		"total_tokens":        25,
	}, general.UsageTokens)
	is.Equal(1, general.UnknownUsageRuns)
	is.Equal(map[string]int{
		"usage_not_recorded": 1,
	}, general.UnknownUsageReasons)

	architecture, ok := aggregateFor(aggregates, HarnessCodex, VariantLegacy, ScenarioArchitecture)
	must.True(ok)
	is.Equal(0, architecture.UsageAvailableRuns)
	is.Empty(architecture.UsageTokens)
	is.Equal(1, architecture.UnknownUsageRuns)
	is.Equal(map[string]int{"unknown": 1}, architecture.UnknownUsageReasons)
}

func TestAggregateResultsReportsCostSourcesAndUnknownReasons(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	aggregates := aggregateResults([]RunResult{
		aggregateKnownCostRun(ScenarioGeneral, "estimated", "codex-pricing-table", 1200),
		aggregateKnownCostRun(ScenarioGeneral, "estimated", "codex-pricing-table", 800),
		aggregateKnownCostRun(ScenarioGeneral, "reported", "pi-session", 500),
		aggregateUnknownCostRun(ScenarioGeneral, "pricing_metadata_missing"),
		aggregateUnknownCostRun(ScenarioGeneral, "pi_reported_cost_not_captured"),
		aggregateUnknownCostRun(ScenarioGeneral, ""),
		aggregateKnownCostRun(ScenarioArchitecture, "", "", 300),
	})

	must.Len(aggregates, 2)
	general, ok := aggregateFor(aggregates, HarnessCodex, VariantLegacy, ScenarioGeneral)
	must.True(ok)
	is.Equal(int64(2500), general.KnownCostMicroUSD)
	is.Equal("$0.002500", general.KnownCostUSD)
	is.Equal(map[string]int{
		"codex-pricing-table": 2,
		"pi-session":          1,
	}, general.KnownCostSources)
	is.Equal(map[string]int{
		"estimated": 2,
		"reported":  1,
	}, general.KnownCostKinds)
	is.Equal(3, general.UnknownCostRuns)
	is.Equal(map[string]int{
		"pi_reported_cost_not_captured": 1,
		"pricing_metadata_missing":      1,
		"unknown":                       1,
	}, general.UnknownCostReasons)

	architecture, ok := aggregateFor(aggregates, HarnessCodex, VariantLegacy, ScenarioArchitecture)
	must.True(ok)
	is.Equal(map[string]int{"unknown": 1}, architecture.KnownCostSources)
	is.Equal(map[string]int{"unknown": 1}, architecture.KnownCostKinds)
	is.Zero(architecture.UnknownCostRuns)
	is.Empty(architecture.UnknownCostReasons)
}

func TestScoreFindingsRequiresSuccessfulReviewCompletionForCompleteSession(t *testing.T) {
	tests := []struct {
		name           string
		reviewStatus   taskstate.ReviewStatus
		operationalErr string
		wantComplete   bool
	}{
		{
			name:         "blocked review with all seeded findings is complete",
			reviewStatus: taskstate.ReviewStatusBlocked,
			wantComplete: true,
		},
		{
			name:         "passed review with all seeded findings is complete",
			reviewStatus: taskstate.ReviewStatusPassed,
			wantComplete: true,
		},
		{
			name:         "failed review with all seeded findings is not complete",
			reviewStatus: taskstate.ReviewStatusFailed,
		},
		{
			name:           "operational error with all seeded findings is not complete",
			reviewStatus:   taskstate.ReviewStatusBlocked,
			operationalErr: "harness exited non-zero",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := RunResult{
				ReviewStatus:   string(tt.reviewStatus),
				OperationalErr: tt.operationalErr,
			}
			scoreFindings(&result, []knownFinding{
				{id: "seeded-defect", title: "Seeded defect", matches: [][]string{{"seeded defect"}}},
			}, []taskstate.ReviewFinding{{
				Title: "Seeded defect is present",
			}})

			assert.Equal(t, tt.wantComplete, result.CompleteSession)
			assert.Equal(t, []string{"seeded-defect"}, result.DetectedSeededIDs)
			assert.Empty(t, result.MissedSeededIDs)
		})
	}
}

func TestPiAcceptanceGateRequiresSuccessfulCompleteSessions(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	runs := []RunResult{}
	for repetition := 1; repetition <= 3; repetition++ {
		runs = append(runs,
			aggregateCompletionRun(HarnessPi, VariantLegacy, ScenarioGeneral, taskstate.ReviewStatusBlocked, "", false, 0.5),
			aggregateCompletionRun(HarnessPi, VariantLegacy, ScenarioArchitecture, taskstate.ReviewStatusBlocked, "", false, 0.5),
			aggregateCompletionRun(HarnessPi, VariantExhaustive, ScenarioArchitecture, taskstate.ReviewStatusBlocked, "", true, 1),
		)
	}
	runs = append(runs,
		aggregateCompletionRun(HarnessPi, VariantExhaustive, ScenarioGeneral, taskstate.ReviewStatusBlocked, "", true, 1),
		aggregateCompletionRun(HarnessPi, VariantExhaustive, ScenarioGeneral, taskstate.ReviewStatusFailed, "", true, 1),
		aggregateCompletionRun(HarnessPi, VariantExhaustive, ScenarioGeneral, taskstate.ReviewStatusBlocked, "usage capture failed", true, 1),
	)

	aggregates := aggregateResults(runs)
	exhaustiveGeneral, ok := aggregateFor(aggregates, HarnessPi, VariantExhaustive, ScenarioGeneral)
	must.True(ok)
	is.Equal(3, exhaustiveGeneral.Runs)
	is.Equal(1, exhaustiveGeneral.OperationalErrors)
	is.Equal(1, exhaustiveGeneral.CompleteSessions)

	gate := piAcceptanceGate(aggregates)
	is.True(gate.Applicable)
	is.False(gate.ExhaustiveCompletesTwoOfThreeGeneral)
	is.True(gate.ExhaustiveCompletesTwoOfThreeArch)
	is.True(gate.ExhaustiveRecallNotBelowLegacyGeneral)
	is.True(gate.ExhaustiveRecallNotBelowLegacyArch)
	is.False(gate.Passed)
}

func aggregateUsageRun(scenarioName string, recall float64, tokens map[string]int) RunResult {
	run := aggregateUnknownUsageRun(scenarioName, recall, "")
	run.Usage = UsageReport{
		Available: true,
		Tokens:    tokens,
	}
	return run
}

func aggregateUnknownUsageRun(scenarioName string, recall float64, reason string) RunResult {
	return RunResult{
		Harness:       HarnessCodex,
		Model:         "test-model",
		Variant:       VariantLegacy,
		Scenario:      scenarioName,
		FindingRecall: recall,
		Usage: UsageReport{
			UnknownReason: reason,
		},
	}
}

func aggregateKnownCostRun(scenarioName string, kind string, source string, amountMicroUSD int64) RunResult {
	run := aggregateUnknownUsageRun(scenarioName, 1, "")
	run.Cost = CostReport{
		Known:          true,
		Kind:           kind,
		AmountMicroUSD: amountMicroUSD,
		Source:         source,
	}
	return run
}

func aggregateUnknownCostRun(scenarioName string, reason string) RunResult {
	run := aggregateUnknownUsageRun(scenarioName, 0, "")
	run.Cost = CostReport{
		UnknownReason: reason,
	}
	return run
}

func aggregateCompletionRun(
	harness string,
	variant string,
	scenarioName string,
	reviewStatus taskstate.ReviewStatus,
	operationalErr string,
	complete bool,
	recall float64,
) RunResult {
	return RunResult{
		Harness:         harness,
		Model:           "test-model",
		Variant:         variant,
		Scenario:        scenarioName,
		ReviewStatus:    string(reviewStatus),
		OperationalErr:  operationalErr,
		FindingRecall:   recall,
		CompleteSession: complete,
		Usage: UsageReport{
			Available: true,
		},
		Cost: CostReport{
			Known:  true,
			Source: "test",
			Kind:   "estimated",
		},
	}
}

func TestWithRunEnvironmentProvisionsCodexConfigAndIsolatesSessions(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	sourceCodex := t.TempDir()
	must.NoError(os.WriteFile(filepath.Join(sourceCodex, "auth.json"), []byte(`{"token":"codex"}`), 0o600))
	must.NoError(os.WriteFile(filepath.Join(sourceCodex, "config.toml"), []byte("model = \"test\"\n"), 0o600))
	must.NoError(os.MkdirAll(filepath.Join(sourceCodex, "sessions"), 0o755))
	must.NoError(os.WriteFile(filepath.Join(sourceCodex, "sessions", "old.jsonl"), []byte("{}\n"), 0o600))

	t.Setenv("CODEX_HOME", sourceCodex)
	t.Setenv("PI_CODING_AGENT_DIR", "relative-pi-dir")

	setup := testRunSetup(t)
	err := withRunEnvironment(setup, runSpec{Harness: HarnessCodex, Variant: VariantExhaustive}, func() error {
		codexHome := os.Getenv("CODEX_HOME")

		is.NotEqual(sourceCodex, codexHome)
		is.Equal("1", os.Getenv("ORPHEUS_EXHAUSTIVE_REVIEW_CONTEXT"))
		is.FileExists(filepath.Join(codexHome, "auth.json"))
		is.FileExists(filepath.Join(codexHome, "config.toml"))
		is.NoFileExists(filepath.Join(codexHome, "sessions", "old.jsonl"))
		is.Equal(filepath.Join(setup.root, "pi-sessions"), os.Getenv("PI_CODING_AGENT_SESSION_DIR"))
		return nil
	})
	must.NoError(err)

	is.Equal(sourceCodex, os.Getenv("CODEX_HOME"))
	is.Equal("relative-pi-dir", os.Getenv("PI_CODING_AGENT_DIR"))
}

func TestCopyHarnessConfigDereferencesSymlinkedRegularFiles(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	source := t.TempDir()
	targetRoot := t.TempDir()
	operatorRepo := t.TempDir()
	operatorAuth := filepath.Join(operatorRepo, "operator-auth.json")
	must.NoError(os.WriteFile(operatorAuth, []byte(`{"token":"operator"}`), 0o600))
	must.NoError(os.Symlink(operatorAuth, filepath.Join(source, "auth.json")))

	must.NoError(copyHarnessConfig(source, targetRoot))

	targetAuth := filepath.Join(targetRoot, "auth.json")
	info, err := os.Lstat(targetAuth)
	must.NoError(err)
	is.Zero(info.Mode() & os.ModeSymlink)
	is.FileExists(targetAuth)
	is.FileExists(operatorAuth)

	copied, err := os.ReadFile(targetAuth)
	must.NoError(err)
	is.JSONEq(`{"token":"operator"}`, string(copied))

	must.NoError(os.WriteFile(targetAuth, []byte(`{"token":"isolated"}`), 0o600))
	original, err := os.ReadFile(operatorAuth)
	must.NoError(err)
	is.JSONEq(`{"token":"operator"}`, string(original))
}

func TestCopyHarnessConfigRejectsSymlinkedDirectories(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	source := t.TempDir()
	targetRoot := t.TempDir()
	operatorConfigDir := filepath.Join(t.TempDir(), "operator-config")
	must.NoError(os.MkdirAll(operatorConfigDir, 0o755))
	must.NoError(os.WriteFile(filepath.Join(operatorConfigDir, "settings.json"), []byte("{}\n"), 0o600))
	must.NoError(os.Symlink(operatorConfigDir, filepath.Join(source, "linked-config")))

	err := copyHarnessConfig(source, targetRoot)
	must.Error(err)
	is.Contains(err.Error(), "refuse symlinked harness config directory")
	is.NoDirExists(filepath.Join(targetRoot, "linked-config"))
}

func assertNoExecutionUsageAndCost(t *testing.T, result RunResult) {
	t.Helper()
	assert.False(t, result.Usage.Available)
	assert.Equal(t, noAgentExecutionRecordedReason, result.Usage.UnknownReason)
	assert.Equal(t, "unknown", result.Usage.CaptureStatus)
	assert.Contains(t, result.Usage.CaptureReason, "not recorded")
	assert.False(t, result.Cost.Known)
	assert.Equal(t, noAgentExecutionRecordedReason, result.Cost.UnknownReason)
}

func TestWithRunEnvironmentProvisionsPiConfigAndIsolatesSessions(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	sourcePi := t.TempDir()
	must.NoError(os.WriteFile(filepath.Join(sourcePi, "auth.json"), []byte(`{"token":"pi"}`), 0o600))
	must.NoError(os.WriteFile(filepath.Join(sourcePi, "settings.json"), []byte("{}\n"), 0o600))
	must.NoError(os.MkdirAll(filepath.Join(sourcePi, "sessions"), 0o755))
	must.NoError(os.WriteFile(filepath.Join(sourcePi, "sessions", "old.jsonl"), []byte("{}\n"), 0o600))
	oldPiSessionDir := filepath.Join(t.TempDir(), "operator-pi-sessions")

	t.Setenv("CODEX_HOME", "relative-codex-home")
	t.Setenv("PI_CODING_AGENT_DIR", sourcePi)
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", oldPiSessionDir)

	setup := testRunSetup(t)
	err := withRunEnvironment(setup, runSpec{Harness: HarnessPi, Variant: VariantExhaustive}, func() error {
		piDir := os.Getenv("PI_CODING_AGENT_DIR")
		piSessionDir := os.Getenv("PI_CODING_AGENT_SESSION_DIR")

		is.NotEqual(sourcePi, piDir)
		is.Equal(filepath.Join(setup.root, "pi-sessions"), piSessionDir)
		is.Equal("1", os.Getenv("ORPHEUS_EXHAUSTIVE_REVIEW_CONTEXT"))
		is.FileExists(filepath.Join(piDir, "auth.json"))
		is.FileExists(filepath.Join(piDir, "settings.json"))
		is.NoFileExists(filepath.Join(piDir, "sessions", "old.jsonl"))
		return nil
	})
	must.NoError(err)

	is.Equal("relative-codex-home", os.Getenv("CODEX_HOME"))
	is.Equal(sourcePi, os.Getenv("PI_CODING_AGENT_DIR"))
	is.Equal(oldPiSessionDir, os.Getenv("PI_CODING_AGENT_SESSION_DIR"))
}

func TestWithRunEnvironmentAllowsMissingHarnessConfigDirs(t *testing.T) {
	must := require.New(t)
	missing := filepath.Join(t.TempDir(), "missing")
	t.Setenv("CODEX_HOME", filepath.Join(missing, "codex"))
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(missing, "pi"))

	setup := testRunSetup(t)
	err := withRunEnvironment(setup, runSpec{Harness: HarnessCodex, Variant: VariantLegacy}, func() error {
		assert.DirExists(t, os.Getenv("CODEX_HOME"))
		assert.DirExists(t, os.Getenv("PI_CODING_AGENT_DIR"))
		assert.Equal(t, "0", os.Getenv("ORPHEUS_EXHAUSTIVE_REVIEW_CONTEXT"))
		return nil
	})
	must.NoError(err)
}

func testRunSetup(t *testing.T) runSetup {
	t.Helper()
	root := t.TempDir()
	return runSetup{
		root:       root,
		configBase: filepath.Join(root, "xdg-config"),
		dataBase:   filepath.Join(root, "xdg-data"),
	}
}
