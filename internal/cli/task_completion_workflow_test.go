//go:build integration

package cli_test

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowCompletionProtocolSuggestsContextAwareValues(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	paths, alpha, beta, _ := setupCompletionWorkflowFixture(t, fixture, false)
	require.NoError(t, state.SeedMemoryConfigYAML(paths, agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{"implementer": "builder"},
			"profiles": map[string]any{"builder": map[string]any{"command": "agent"}, "fast": map[string]any{"command": "agent"}},
		},
		"reviews": map[string]any{
			"pipelines": map[string]any{"standard": map[string]any{"steps": []map[string]any{{"kind": "manual", "name": "check"}}}},
		},
	}))

	assertCompletionChoices(t, fixture, []string{"task", "show", ""}, []string{
		"review\tInspect persisted review history, an attempt, or an authoritative finding",
		"ar-closed\tClosed task (alpha)",
		"ar-epic-open\tOpen epic (alpha)",
		"ar-open\tOpen task (alpha)",
		"br-epic-running\tRunning epic (beta)",
		"br-open\tBeta task (beta)",
	})
	assertCompletionChoices(t, fixture, []string{"task", "stats", ""}, []string{
		"ar-closed\tClosed task (alpha)",
		"ar-epic-open\tOpen epic (alpha)",
		"ar-open\tOpen task (alpha)",
		"br-epic-running\tRunning epic (beta)",
		"br-open\tBeta task (beta)",
	})
	assertCompletionChoices(t, fixture, []string{"task", "show", "review", ""}, []string{
		"ar-closed\tClosed task (alpha)",
		"ar-epic-open\tOpen epic (alpha)",
		"ar-open\tOpen task (alpha)",
		"br-epic-running\tRunning epic (beta)",
		"br-open\tBeta task (beta)",
	})
	assertCompletionChoices(t, fixture, []string{"task", "run", ""}, []string{"ar-open\tOpen task (alpha)", "br-open\tBeta task (beta)"})
	assertCompletionChoices(t, fixture, []string{"task", "edit", ""}, []string{
		"ar-epic-open\tOpen epic (alpha)",
		"ar-open\tOpen task (alpha)",
		"br-epic-running\tRunning epic (beta)",
		"br-open\tBeta task (beta)",
	})
	assertCompletionChoices(t, fixture, []string{"task", "start", ""}, []string{"ar-epic-open\tOpen epic (alpha)"})
	assertCompletionChoices(t, fixture, []string{"task", "close", ""}, []string{"br-epic-running\tRunning epic (beta)"})
	assertCompletionChoices(t, fixture, []string{"task", "list", "--repo", ""}, []string{
		"alpha\tAlpha Repo (" + alpha + ")",
		"beta\tBeta Repo (" + beta + ")",
	})
	assertCompletionChoices(t, fixture, []string{"task", "create", "--repo", "alpha", "--parent", ""}, []string{"ar-epic-open\tOpen epic (alpha)"})
	assertCompletionChoices(t, fixture, []string{"task", "edit", "ar-open", "--add-block", ""}, []string{"ar-epic-open\tOpen epic (alpha)"})
	assertCompletionChoices(t, fixture, []string{"task", "run", "ar-open", "--agent", ""}, []string{"builder", "fast"})
	assertCompletionChoices(t, fixture, []string{"task", "run", "ar-open", "--pipeline", ""}, []string{"local", "standard"})
	assertCompletionChoices(t, fixture, []string{"repo", "config", "set", "alpha", "integration-flow", ""}, []string{"direct-merge", "pull-request"})
}

func TestIntegrationWorkflowCompletionProtocolExcludesEditedEpicFromParentCandidates(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	_, alpha, beta, _ := setupCompletionWorkflowFixture(t, fixture, false)
	withWorkflowSources(t, fixture, map[string]taskSourceResult{
		alpha: {tasks: []taskmodel.Task{
			taskWFTask("ar-epic-open", "Edited epic", taskmodel.StatusOpen, taskmodel.IssueTypeEpic),
			taskWFTask("ar-epic-other", "Other epic", taskmodel.StatusInProgress, taskmodel.IssueTypeEpic),
		}},
		beta: {tasks: []taskmodel.Task{taskWFTask("br-epic-running", "Beta epic", taskmodel.StatusInProgress, taskmodel.IssueTypeEpic)}},
	})

	assertCompletionChoices(t, fixture, []string{"task", "edit", "ar-epic-open", "--parent", ""}, []string{"ar-epic-other\tOther epic (alpha)"})
}

func TestIntegrationWorkflowCompletionProtocolUsesOneSnapshotAndToleratesRepositoryFailure(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	_, alpha, _, reads := setupCompletionWorkflowFixture(t, fixture, true)

	stdout, stderr, err := fixture.execute("__complete", "task", "create", "--repo", "alpha", "--blocked-by", "")

	require.NoError(t, err)
	assert.Contains(t, stdout, "ar-open\tOpen task (alpha)")
	assert.NotContains(t, stdout, "br-open")
	assert.NotContains(t, stderr, "broken backend")
	assert.Equal(t, 2, reads.count("list"))
	assert.Contains(t, reads.directories(), alpha)
}

func TestIntegrationWorkflowCompletionProtocolSkipsUnprojectableRepositorySource(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	setupCompletionWorkflowFixture(t, fixture, false)
	registered, err := registryStoreForWorkflow(t, fixture).Load()
	require.NoError(t, err)
	registered.Repos = append(registered.Repos, registryRepoWithoutTaskSource("legacy", "Legacy Repo"))
	require.NoError(t, registryStoreForWorkflow(t, fixture).Save(registered))

	assertCompletionChoices(t, fixture, []string{"task", "show", ""}, []string{
		"review\tInspect persisted review history, an attempt, or an authoritative finding",
		"ar-closed\tClosed task (alpha)",
		"ar-epic-open\tOpen epic (alpha)",
		"ar-open\tOpen task (alpha)",
		"br-epic-running\tRunning epic (beta)",
		"br-open\tBeta task (beta)",
	})
}

func TestIntegrationWorkflowCompletionProtocolScopesCreateRelationsToCurrentDirectory(t *testing.T) {
	fixture := newCommandWorkflow(t)
	_, alpha, _, _ := setupCompletionWorkflowFixture(t, fixture, false)
	nested := filepath.Join(alpha, "nested")
	fixture.options.TaskWorkingDirectory = nested

	assertCompletionChoices(t, fixture, []string{"task", "create", "--parent", ""}, []string{"ar-epic-open\tOpen epic (alpha)"})
	assertCompletionChoices(t, fixture, []string{"task", "create", "--blocked-by", ""}, []string{"ar-epic-open\tOpen epic (alpha)", "ar-open\tOpen task (alpha)"})

	fixture.options.TaskWorkingDirectory = "/fixture/outside"
	assertCompletionChoices(t, fixture, []string{"task", "create", "--parent", ""}, []string{})
	assertCompletionChoices(t, fixture, []string{"task", "create", "--blocked-by", ""}, []string{})

	fixture.options.TaskWorkingDirectory = nested
	registered, err := registryStoreForWorkflow(t, fixture).Load()
	require.NoError(t, err)
	registered.Repos = append(registered.Repos, registryRepoWithPath("nested", "Nested Repo", nested, "nr"))
	require.NoError(t, registryStoreForWorkflow(t, fixture).Save(registered))
	assertCompletionChoices(t, fixture, []string{"task", "create", "--parent", ""}, []string{})
	assertCompletionChoices(t, fixture, []string{"task", "create", "--blocked-by", ""}, []string{})
}

func TestIntegrationWorkflowCompletionProtocolTruncatesUnicodeDescriptionsOnRuneBoundaries(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	_, alpha, beta, _ := setupCompletionWorkflowFixture(t, fixture, false)
	longTitle := strings.Repeat("猫", 100)
	withWorkflowSources(t, fixture, map[string]taskSourceResult{
		alpha: {tasks: []taskmodel.Task{taskWFTask("ar-open", longTitle, taskmodel.StatusOpen, taskmodel.IssueTypeTask)}},
		beta:  {tasks: []taskmodel.Task{taskWFTask("br-open", "Beta task", taskmodel.StatusOpen, taskmodel.IssueTypeTask)}},
	})

	stdout, _, err := fixture.execute("__complete", "task", "show", "ar-o")

	require.NoError(t, err)
	assert.True(t, utf8.ValidString(stdout), "completion protocol output must be valid UTF-8")
	assert.Equal(t, "ar-open\t"+strings.Repeat("猫", 93)+"...\n:4\n", stdout)
}

func setupCompletionWorkflowFixture(t *testing.T, fixture *workflowFixture, brokenBeta bool) (state.Paths, string, string, *taskSourceReads) {
	t.Helper()

	paths := fixture.paths
	alphaSpec := taskWorkflowRepoSpec("alpha", "Alpha Repo", "ar")
	alphaSpec.aliases = map[string]string{"local": "standard"}
	registered := saveTaskWorkflowRepos(t, fixture, alphaSpec, taskWorkflowRepoSpec("beta", "Beta Repo", "br"))
	betaResult := taskSourceResult{tasks: []taskmodel.Task{
		taskWFTask("br-open", "Beta task", taskmodel.StatusOpen, taskmodel.IssueTypeTask),
		taskWFTask("br-epic-running", "Running epic", taskmodel.StatusInProgress, taskmodel.IssueTypeEpic),
	}}
	if brokenBeta {
		betaResult = taskSourceResult{err: assert.AnError}
	}
	reads := withWorkflowSources(t, fixture, map[string]taskSourceResult{
		registered["alpha"]: {tasks: []taskmodel.Task{
			taskWFTask("ar-open", "Open task", taskmodel.StatusOpen, taskmodel.IssueTypeTask),
			taskWFTask("ar-closed", "Closed task", taskmodel.StatusClosed, taskmodel.IssueTypeTask),
			taskWFTask("ar-epic-open", "Open epic", taskmodel.StatusOpen, taskmodel.IssueTypeEpic),
			taskWFTask("ar-bug", "Active bug", taskmodel.StatusOpen, taskmodel.IssueTypeBug),
			taskWFTask("ar-chore", "Active chore", taskmodel.StatusInProgress, taskmodel.IssueTypeChore),
			taskWFTask("ar-unknown", "Unknown item", taskmodel.StatusOpen, taskmodel.IssueTypeUnknown),
		}},
		registered["beta"]: betaResult,
	})
	return paths, registered["alpha"], registered["beta"], reads
}

func assertCompletionChoices(t *testing.T, fixture *workflowFixture, args []string, want []string) {
	t.Helper()
	stdout, stderr, err := fixture.execute(append([]string{"__complete"}, args...)...)
	require.NoError(t, err)
	assert.Contains(t, stderr, "Completion ended with directive")
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	require.NotEmpty(t, lines)
	assert.Equal(t, ":4", lines[len(lines)-1])
	assert.Equal(t, want, lines[:len(lines)-1])
}
