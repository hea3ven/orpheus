//go:build integration

package cli_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowRepoAddInheritsGlobalSummaryStyleInAgentContext(t *testing.T) {
	repo := aGitRepository(t, "alpha")
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-style"))
	fixture.withRegisteredRepos()
	discovery := withRepoDiscovery(fixture.workflowFixture, repo)
	discovery.withLocalBeads(repo, "op")
	fixture.setConfig("publication", map[string]any{"summary_guidance_style": registry.SummaryGuidanceStyleCapitalized})
	fixture.configureImplementer("recorder", agent.Profile{Command: "unused-agent"})
	fixture.agent.outcomes = []semanticAgentOutcome{{exitWithoutCompletion: true, captureContext: true}}

	addOutput, addStderr, addErr := fixture.execute("repo", "add", repo.path)
	require.NoError(t, addErr)
	_, runStderr, runErr := fixture.execute("task", "run", "op-style")

	require.NoError(t, runErr, "stderr: %s", runStderr)
	assert.Contains(t, addOutput, "Added repo alpha")
	assert.Empty(t, addStderr)
	final := fixture.loadFinalRegistry()
	require.Len(t, final.Repos, 1)
	assert.Empty(t, final.Repos[0].SummaryGuidanceStyle, "registration must preserve global inheritance")
	fixture.assertIsTaskInAgentContext("op-style")
	require.Len(t, fixture.agent.contexts, 1)
	assert.Contains(t, fixture.agent.contexts[0], "Use one capitalized plain-English summary line")
}

func TestIntegrationWorkflowTaskRunDeprecatedMainFlagExplainsReplacement(t *testing.T) {
	fixture := newWorkflowFixture(t, taskWorkflowConfigRoot, taskWorkflowDataRoot)

	_, _, err := fixture.execute("task", "run", "--main", "op-main")

	assert.ErrorContains(t, err, "--main is no longer supported")
	assert.ErrorContains(t, err, "--repo-root")
}
