//go:build integration

//nolint:testpackage // Exercises unexported evaluation setup through isolated fake processes.
package revieweval

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/testguard"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationExecuteRunReportsUsageAndCostUnknownWhenProvisioningFailsBeforeExecution(t *testing.T) {
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

func TestIntegrationPrepareRunInitializesRepoLocalBeadsWhenOperatorBeadsDirIsSet(t *testing.T) {
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

func TestIntegrationRunPipelineRecordsCodexPromptArgWithEvaluationSessionName(t *testing.T) {
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
	must.NoError(testguard.WriteExecutable(codexPath, []byte("#!/bin/sh\nexit 0\n")))
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
	must.NoError(testguard.WriteExecutable(path, []byte(script)))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
