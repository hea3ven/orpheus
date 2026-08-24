//go:build integration

//nolint:testpackage // Completion protocol tests configure invocation dependencies.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationCompletionProtocolSuggestsContextAwareValues(t *testing.T) {
	t.Parallel()
	paths, alpha, beta := setupCompletionFixture(t, false)
	require.NoError(t, paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{"implementer": "builder"},
			"profiles": map[string]any{"builder": map[string]any{"command": "agent"}, "fast": map[string]any{"command": "agent"}},
		},
		"reviews": map[string]any{
			"pipelines": map[string]any{"standard": map[string]any{"steps": []map[string]any{{"kind": "manual", "name": "check"}}}},
		},
	}))
	_ = alpha
	_ = beta

	assertCompletionChoices(t, []string{"task", "show", ""}, []string{
		"ar-closed\tClosed task (alpha)",
		"ar-epic-open\tOpen epic (alpha)",
		"ar-open\tOpen task (alpha)",
		"br-epic-running\tRunning epic (beta)",
		"br-open\tBeta task (beta)",
	})
	assertCompletionChoices(t, []string{"task", "stats", ""}, []string{
		"ar-closed\tClosed task (alpha)",
		"ar-epic-open\tOpen epic (alpha)",
		"ar-open\tOpen task (alpha)",
		"br-epic-running\tRunning epic (beta)",
		"br-open\tBeta task (beta)",
	})
	assertCompletionChoices(t, []string{"task", "review", "show", ""}, []string{
		"ar-closed\tClosed task (alpha)",
		"ar-epic-open\tOpen epic (alpha)",
		"ar-open\tOpen task (alpha)",
		"br-epic-running\tRunning epic (beta)",
		"br-open\tBeta task (beta)",
	})
	assertCompletionChoices(t, []string{"task", "run", ""}, []string{"ar-open\tOpen task (alpha)", "br-open\tBeta task (beta)"})
	assertCompletionChoices(t, []string{"task", "edit", ""}, []string{
		"ar-epic-open\tOpen epic (alpha)",
		"ar-open\tOpen task (alpha)",
		"br-epic-running\tRunning epic (beta)",
		"br-open\tBeta task (beta)",
	})
	assertCompletionChoices(t, []string{"task", "start", ""}, []string{"ar-epic-open\tOpen epic (alpha)"})
	assertCompletionChoices(t, []string{"task", "close", ""}, []string{"br-epic-running\tRunning epic (beta)"})
	assertCompletionChoices(t, []string{"task", "list", "--repo", ""}, []string{
		completionWithDescription("alpha", "Alpha Repo ("+alpha+")"),
		completionWithDescription("beta", "Beta Repo ("+beta+")"),
	})
	assertCompletionChoices(t, []string{"task", "create", "--repo", "alpha", "--parent", ""}, []string{"ar-epic-open\tOpen epic (alpha)"})
	assertCompletionChoices(t, []string{"task", "edit", "ar-open", "--add-block", ""}, []string{"ar-epic-open\tOpen epic (alpha)"})
	assertCompletionChoices(t, []string{"task", "run", "ar-open", "--agent", ""}, []string{"builder", "fast"})
	assertCompletionChoices(t, []string{"task", "review", "ar-open", "--pipeline", ""}, []string{"local", "standard"})
	assertCompletionChoices(t, []string{"repo", "config", "set", "alpha", "integration-flow", ""}, []string{"direct-merge", "pull-request"})
}

func TestIntegrationCompletionProtocolExcludesEditedEpicFromParentCandidates(t *testing.T) {
	t.Parallel()
	_, alpha, beta := setupCompletionFixture(t, false)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: alpha, args: "--json --readonly --sandbox list --all --limit 0", stdout: `[
			{"id":"ar-epic-open","title":"Edited epic","status":"open","issue_type":"epic"},
			{"id":"ar-epic-other","title":"Other epic","status":"in_progress","issue_type":"epic"}
		]`},
		{dir: beta, args: "--json --readonly --sandbox list --all --limit 0", stdout: `[
			{"id":"br-epic-running","title":"Beta epic","status":"in_progress","issue_type":"epic"}
		]`},
	})

	assertCompletionChoices(t, []string{"task", "edit", "ar-epic-open", "--parent", ""}, []string{
		"ar-epic-other\tOther epic (alpha)",
	})
}

func TestIntegrationCompletionProtocolUsesOneSnapshotAndToleratesRepositoryFailure(t *testing.T) {
	t.Parallel()
	_, alpha, _ := setupCompletionFixture(t, true)

	stdout, stderr, err := executeCommandWithError(t, []string{"__complete", "task", "create", "--repo", "alpha", "--blocked-by", ""})
	require.NoError(t, err)
	assert.Contains(t, stdout, "ar-open\tOpen task (alpha)")
	assert.NotContains(t, stdout, "br-open")
	assert.NotContains(t, stderr, "broken backend")

	log, err := os.ReadFile(testInvocationFor(t).environment["FAKE_BD_LOG"])
	require.NoError(t, err)
	assert.Equal(t, 3, strings.Count(string(log), "--json --readonly --sandbox list --all --limit 0"))
	assert.Contains(t, string(log), alpha)
}

func TestIntegrationCompletionProtocolSkipsUnprojectableRepositorySource(t *testing.T) {
	t.Parallel()
	paths, _, _ := setupCompletionFixture(t, false)
	store := registry.NewStore(paths)
	registered, err := store.Load()
	require.NoError(t, err)
	registered.Repos = append(registered.Repos, registry.Repo{
		ID: "legacy", Name: "Legacy Repo", Path: testutil.CanonicalTempDir(t),
	})
	require.NoError(t, store.Save(registered))

	assertCompletionChoices(t, []string{"task", "show", ""}, []string{
		"ar-closed\tClosed task (alpha)",
		"ar-epic-open\tOpen epic (alpha)",
		"ar-open\tOpen task (alpha)",
		"br-epic-running\tRunning epic (beta)",
		"br-open\tBeta task (beta)",
	})
}

func TestIntegrationCompletionProtocolScopesCreateRelationsToCurrentDirectory(t *testing.T) {
	paths, alpha, _ := setupCompletionFixture(t, false)
	nested := filepath.Join(alpha, "nested")
	require.NoError(t, os.MkdirAll(nested, 0o755))
	t.Chdir(nested)

	assertCompletionChoices(t, []string{"task", "create", "--parent", ""}, []string{"ar-epic-open\tOpen epic (alpha)"})
	assertCompletionChoices(t, []string{"task", "create", "--blocked-by", ""}, []string{
		"ar-epic-open\tOpen epic (alpha)",
		"ar-open\tOpen task (alpha)",
	})

	t.Chdir(testutil.CanonicalTempDir(t))
	assertCompletionChoices(t, []string{"task", "create", "--parent", ""}, []string{})
	assertCompletionChoices(t, []string{"task", "create", "--blocked-by", ""}, []string{})

	t.Chdir(nested)
	store := registry.NewStore(paths)
	registered, err := store.Load()
	require.NoError(t, err)
	registered.Repos = append(registered.Repos, registry.Repo{
		ID: "nested", Name: "Nested Repo", Path: nested,
		BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "nr",
	})
	require.NoError(t, store.Save(registered))
	assertCompletionChoices(t, []string{"task", "create", "--parent", ""}, []string{})
	assertCompletionChoices(t, []string{"task", "create", "--blocked-by", ""}, []string{})
}

func TestIntegrationCompletionProtocolFiniteValuesAndFilesystemFallback(t *testing.T) {
	t.Parallel()
	assertCompletionChoices(t, []string{"task", "create", "--type", ""}, []string{"epic", "task"})
	assertCompletionChoices(t, []string{"status", "--sort", ""}, []string{"created", "status", "updated"})
	assertCompletionChoices(t, []string{"task", "list", "--sort", ""}, []string{"created", "status", "updated"})
	assertCompletionChoices(t, []string{"task", "stats", "--group", ""}, []string{"day", "month", "week"})
	assertCompletionChoices(t, []string{"task", "stats", "--view", ""}, []string{"consumption", "implementation", "implementation-model", "model-pair", "review", "reviewer-model", "throughput"})
	assertCompletionChoices(t, []string{"agent", "review", "add", "--type", ""}, []string{"advisory", "blocking", "separate-task"})
	assertCompletionChoices(t, []string{"eval", "review-context", "--harness", ""}, []string{"all", "codex", "pi"})

	stdout, _, err := executeCommandWithError(t, []string{"__complete", "repo", "add", ""})
	require.NoError(t, err)
	assert.Equal(t, ":0\n", stdout, "directory arguments retain normal filesystem completion")
}

func TestIntegrationCompletionProtocolCompletesCommaSeparatedEvalSelections(t *testing.T) {
	t.Parallel()
	assertCompletionChoices(t, []string{"eval", "review-context", "--harness", "pi,"}, []string{"pi,all", "pi,codex", "pi,pi"})
	assertCompletionChoices(t, []string{"eval", "review-context", "--harness", "pi,c"}, []string{"pi,codex"})
	assertCompletionChoices(t, []string{"eval", "review-context", "--harness", "pi,codex,"}, []string{"pi,codex,all", "pi,codex,codex", "pi,codex,pi"})
	assertCompletionChoices(t, []string{"eval", "review-context", "--variant", "legacy,"}, []string{"legacy,all", "legacy,exhaustive", "legacy,legacy"})
	assertCompletionChoices(t, []string{"eval", "review-context", "--scenario", "general,a"}, []string{"general,all", "general,architecture"})
}

func TestIntegrationCompletionProtocolTruncatesUnicodeDescriptionsOnRuneBoundaries(t *testing.T) {
	t.Parallel()
	_, alpha, beta := setupCompletionFixture(t, false)
	longTitle := strings.Repeat("猫", 100)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: alpha, args: "--json --readonly --sandbox list --all --limit 0", stdout: `[
			{"id":"ar-open","title":"` + longTitle + `","status":"open","issue_type":"task"}
		]`},
		{dir: beta, args: "--json --readonly --sandbox list --all --limit 0", stdout: `[
			{"id":"br-open","title":"Beta task","status":"open","issue_type":"task"}
		]`},
	})

	stdout, _, err := executeCommandWithError(t, []string{"__complete", "task", "show", "ar-o"})
	require.NoError(t, err)
	assert.True(t, utf8.ValidString(stdout), "completion protocol output must be valid UTF-8")
	assert.Equal(t, "ar-open\t"+strings.Repeat("猫", 93)+"...\n:4\n", stdout)
}

func TestIntegrationCompletionGeneratorsRemainCleanForAllSupportedShells(t *testing.T) {
	t.Parallel()
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			stdout, stderr := executeCommand(t, []string{"completion", shell})
			assert.NotEmpty(t, stdout)
			assert.NotContains(t, stdout, "level=DEBUG")
			assert.Empty(t, stderr)
		})
	}
}

func TestIntegrationCompletionDocumentationPreservesExistingPowerShellProfile(t *testing.T) {
	t.Parallel()

	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	require.NoError(t, err)

	instructions := string(readme)
	assert.Contains(t, instructions, "$profileDirectory = Split-Path -Parent $PROFILE")
	assert.Contains(t, instructions, "New-Item -ItemType Directory -Path $profileDirectory -Force | Out-Null")
	assert.Contains(t, instructions, "if (-not (Test-Path -LiteralPath $PROFILE)) {\n  New-Item -ItemType File -Path $PROFILE | Out-Null\n}")
	assert.Contains(t, instructions, "Add-Content -LiteralPath $PROFILE")
	assert.NotContains(t, instructions, "New-Item -ItemType File -Force $PROFILE")
}

func setupCompletionFixture(t *testing.T, brokenBeta bool) (paths state.Paths, alpha string, beta string) {
	t.Helper()
	newTestState(t)
	statePaths := currentTestPaths(t)
	alpha = filepath.Join(testutil.CanonicalTempDir(t), "alpha")
	beta = filepath.Join(testutil.CanonicalTempDir(t), "beta")
	require.NoError(t, os.MkdirAll(alpha, 0o755))
	require.NoError(t, os.MkdirAll(beta, 0o755))
	require.NoError(t, registry.NewStore(statePaths).Save(registry.Registry{Repos: []registry.Repo{
		{ID: "alpha", Name: "Alpha Repo", Path: alpha, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "ar", ReviewPipelineAliases: map[string]string{"local": "standard"}},
		{ID: "beta", Name: "Beta Repo", Path: beta, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "br"},
	}}))

	betaResponse := fakeBDCommandResponse{dir: beta, args: "--json --readonly --sandbox list --all --limit 0", stdout: `[
		{"id":"br-open","title":"Beta task","status":"open","issue_type":"task"},
		{"id":"br-epic-running","title":"Running epic","status":"in_progress","issue_type":"epic"}
	]`}
	if brokenBeta {
		betaResponse = fakeBDCommandResponse{dir: beta, args: "--json --readonly --sandbox list --all --limit 0", stderr: "broken backend", exitCode: 1}
	}
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: alpha, args: "--json --readonly --sandbox list --all --limit 0", stdout: `[
			{"id":"ar-open","title":"Open task","status":"open","issue_type":"task"},
			{"id":"ar-closed","title":"Closed task","status":"closed","issue_type":"task"},
			{"id":"ar-epic-open","title":"Open epic","status":"open","issue_type":"epic"},
			{"id":"ar-bug","title":"Active bug","status":"open","issue_type":"bug"},
			{"id":"ar-chore","title":"Active chore","status":"in_progress","issue_type":"chore"},
			{"id":"ar-unknown","title":"Unknown item","status":"open"}
		]`},
		betaResponse,
	})

	return statePaths, alpha, beta
}

func assertCompletionChoices(t *testing.T, args []string, want []string) {
	t.Helper()
	stdout, stderr, err := executeCommandWithError(t, append([]string{"__complete"}, args...))
	require.NoError(t, err)
	assert.Contains(t, stderr, "Completion ended with directive")
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	require.NotEmpty(t, lines)
	assert.Equal(t, ":4", lines[len(lines)-1])
	assert.Equal(t, want, lines[:len(lines)-1])
}
