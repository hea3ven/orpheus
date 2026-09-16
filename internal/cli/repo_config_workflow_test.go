//go:build integration

package cli_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationRepoConfigInspectsEffectivePublicationPolicy(t *testing.T) {
	is := assert.New(t)
	fixture := newRepoConfigFixture(t)

	stdout, stderr, err := fixture.execute("repo", "config", "get", "alpha")
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "CONFIG")
	is.Contains(stdout, "summary-guidance")
	is.Contains(stdout, "(not set)")
	is.Contains(stdout, "typed")
	is.Contains(stdout, "title-template")
	is.Contains(stdout, "completion summary")
	is.Contains(stdout, "review-pipeline")
	is.Contains(stdout, "default")
}

func TestIntegrationRepoConfigUsesGlobalPublicationPolicyFallbacks(t *testing.T) {
	is := assert.New(t)
	fixture := newRepoConfigFixture(t)
	must := require.New(t)

	fixture.setConfig("publication", map[string]any{
		"summary_guidance":       "Write a concise global release note.",
		"summary_guidance_style": registry.SummaryGuidanceStyleCapitalized,
		"title_template":         "[GLOBAL] {{summary}}",
	})

	stdout, stderr, err := fixture.execute("repo", "config", "get", "alpha")
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "Write a concise global release note.")
	is.Contains(stdout, "overridden by custom guidance")
	is.Contains(stdout, "[GLOBAL] {{summary}}")

	for _, args := range [][]string{
		{"repo", "config", "set", "alpha", "summary-guidance", "Write a repository release note."},
		{"repo", "config", "set", "alpha", "summary-style", registry.SummaryGuidanceStyleTyped},
		{"repo", "config", "set", "alpha", "title-template", "[REPO] {{summary}}"},
	} {
		_, configErr, err := fixture.execute(args...)
		require.NoError(t, err)
		is.Empty(configErr)
	}
	stdout, stderr, err = fixture.execute("repo", "config", "get", "alpha")
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "Write a repository release note.")
	is.Contains(stdout, "[REPO] {{summary}}")
	is.Contains(stdout, "overridden by custom guidance")

	for _, args := range [][]string{
		{"repo", "config", "set", "alpha", "summary-guidance", ""},
		{"repo", "config", "set", "alpha", "summary-style", ""},
		{"repo", "config", "set", "alpha", "title-template", ""},
	} {
		_, configErr, err := fixture.execute(args...)
		require.NoError(t, err)
		is.Empty(configErr)
	}
	stdout, stderr, err = fixture.execute("repo", "config", "get", "alpha")
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "Write a concise global release note.")
	is.Contains(stdout, "[GLOBAL] {{summary}}")

	reg := fixture.loadFinalRegistry()
	must.Len(reg.Repos, 1)
	is.Empty(reg.Repos[0].SummaryGuidance)
	is.Empty(reg.Repos[0].SummaryGuidanceStyle)
	is.Empty(reg.Repos[0].TitleTemplate)
}

func TestIntegrationRepoConfigUpdatesPublicationPolicyForExistingRepo(t *testing.T) {
	is := assert.New(t)
	fixture := newRepoConfigFixture(t)
	must := require.New(t)

	guidance := "Use sentence-case summaries without a type prefix."
	template := "[{{external_ref}}] {{summary}}"
	stdout, stderr, err := fixture.execute(
		"repo", "config", "set", "alpha", "summary-guidance", guidance,
	)
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, guidance)
	stdout, stderr, err = fixture.execute(
		"repo", "config", "set", "alpha", "summary-style", registry.SummaryGuidanceStyleCapitalized,
	)
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, registry.SummaryGuidanceStyleCapitalized)
	stdout, stderr, err = fixture.execute(
		"repo", "config", "set", "alpha", "title-template", template,
	)
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, template)

	reg := fixture.loadFinalRegistry()
	must.Len(reg.Repos, 1)
	is.Equal(guidance, reg.Repos[0].SummaryGuidance)
	is.Equal(registry.SummaryGuidanceStyleCapitalized, reg.Repos[0].SummaryGuidanceStyle)
	is.Equal(template, reg.Repos[0].TitleTemplate)
}

func TestIntegrationRepoConfigSetsAndClearsBranchTemplateWithGlobalFallback(t *testing.T) {
	is := assert.New(t)
	fixture := newRepoConfigFixture(t)
	must := require.New(t)

	fixture.setConfig("tasks", map[string]any{"branch_template": "global/{{task_title}}"})

	stdout, stderr, err := fixture.execute("repo", "config", "get", "alpha", "branch-template")
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "branch-template")
	is.Contains(stdout, "(not set)")
	is.Contains(stdout, "global/{{task_title}}")

	stdout, stderr, err = fixture.execute("repo", "config", "set", "alpha", "branch-template", "repo/{{external_ref}}/{{task_id}}")
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "repo/{{external_ref}}/{{task_id}}")

	reg := fixture.loadFinalRegistry()
	must.Len(reg.Repos, 1)
	is.Equal("repo/{{external_ref}}/{{task_id}}", reg.Repos[0].BranchTemplate)

	stdout, stderr, err = fixture.execute("repo", "config", "set", "alpha", "branch-template", "")
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "global/{{task_title}}")
	reg = fixture.loadFinalRegistry()
	is.Empty(reg.Repos[0].BranchTemplate)
}

func TestIntegrationRepoConfigSetsAndClearsIncludePRReviewProcess(t *testing.T) {
	is := assert.New(t)
	fixture := newRepoConfigFixture(t)
	must := require.New(t)

	stdout, stderr, err := fixture.execute(
		"repo", "config", "get", "alpha", "include-pr-review-process",
	)
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "include-pr-review-process")
	is.Contains(stdout, "(not set)")
	is.Contains(stdout, "true")

	stdout, stderr, err = fixture.execute(
		"repo", "config", "set", "alpha", "include-pr-review-process", "false",
	)
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "include-pr-review-process")
	is.Contains(stdout, "false")

	reg := fixture.loadFinalRegistry()
	must.Len(reg.Repos, 1)
	must.NotNil(reg.Repos[0].IncludePRReviewProcess)
	is.False(*reg.Repos[0].IncludePRReviewProcess)

	fixture.setConfig("reviews", map[string]any{"include_pr_review_process": false})
	stdout, stderr, err = fixture.execute(
		"repo", "config", "set", "alpha", "include-pr-review-process", "true",
	)
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "true")

	reg = fixture.loadFinalRegistry()
	must.Len(reg.Repos, 1)
	must.NotNil(reg.Repos[0].IncludePRReviewProcess)
	is.True(*reg.Repos[0].IncludePRReviewProcess)

	stdout, stderr, err = fixture.execute(
		"repo", "config", "set", "alpha", "include-pr-review-process", "",
	)
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "(not set)")
	is.Contains(stdout, "false")

	reg = fixture.loadFinalRegistry()
	must.Len(reg.Repos, 1)
	is.Nil(reg.Repos[0].IncludePRReviewProcess)
}

func TestIntegrationRepoConfigClearsPublicationPolicy(t *testing.T) {
	is := assert.New(t)
	fixture := newRepoConfigFixture(t)
	must := require.New(t)

	for _, args := range [][]string{
		{"repo", "config", "set", "alpha", "summary-guidance", "Use sentence-case summaries."},
		{"repo", "config", "set", "alpha", "summary-style", registry.SummaryGuidanceStyleCapitalized},
		{"repo", "config", "set", "alpha", "title-template", "[OPS] {{summary}}"},
	} {
		_, configErr, err := fixture.execute(args...)
		require.NoError(t, err)
		is.Empty(configErr)
	}

	_, stderr, err := fixture.execute(
		"repo", "config", "set", "alpha", "summary-guidance", "",
	)
	require.NoError(t, err)
	is.Empty(stderr)
	_, stderr, err = fixture.execute(
		"repo", "config", "set", "alpha", "summary-style", "",
	)
	require.NoError(t, err)
	is.Empty(stderr)
	_, stderr, err = fixture.execute(
		"repo", "config", "set", "alpha", "title-template", "",
	)
	require.NoError(t, err)
	is.Empty(stderr)
	stdout, stderr, err := fixture.execute("repo", "config", "get", "alpha")
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "(not set)")
	is.Contains(stdout, "typed")
	is.Contains(stdout, "completion summary")

	reg := fixture.loadFinalRegistry()
	must.Len(reg.Repos, 1)
	is.Empty(reg.Repos[0].SummaryGuidance)
	is.Empty(reg.Repos[0].SummaryGuidanceStyle)
	is.Empty(reg.Repos[0].TitleTemplate)
}

func TestIntegrationRepoConfigRejectsInvalidPolicyWithoutMutatingRegistry(t *testing.T) {
	is := assert.New(t)
	fixture := newRepoConfigFixture(t)
	must := require.New(t)

	_, configErr, err := fixture.execute("repo", "config", "set", "alpha", "title-template", "[OPS] {{summary}}")
	require.NoError(t, err)
	is.Empty(configErr)

	for _, args := range [][]string{
		{"repo", "config", "set", "alpha", "summary-style", "informal"},
		{"repo", "config", "set", "alpha", "title-template", "{{task_id}}: {{summary}}"},
		{"repo", "config", "set", "alpha", "branch-template", "{{summary}}"},
	} {
		stdout, _, err := fixture.execute(args...)
		must.Error(err)
		is.Empty(stdout)
	}

	reg := fixture.loadFinalRegistry()
	must.Len(reg.Repos, 1)
	is.Empty(reg.Repos[0].SummaryGuidanceStyle)
	is.Equal("[OPS] {{summary}}", reg.Repos[0].TitleTemplate)
}

func TestIntegrationRepoConfigSetInvalidGlobalPublicationFlowDoesNotMutateRegistry(t *testing.T) {
	is := assert.New(t)
	fixture := newRepoConfigFixture(t)
	must := require.New(t)

	before := fixture.loadFinalRegistry()
	fixture.setConfig("publication", map[string]any{"integration_flow": "invalid-flow"})

	stdout, _, err := fixture.execute(
		"repo", "config", "set", "alpha", "summary-guidance", "would otherwise persist",
	)
	must.Error(err)
	is.Empty(stdout)
	after := fixture.loadFinalRegistry()
	is.Equal(before, after)
}

func TestIntegrationRepoConfigRejectsUnknownConfigName(t *testing.T) {
	is := assert.New(t)
	fixture := newRepoConfigFixture(t)
	must := require.New(t)

	stdout, _, err := fixture.execute(
		"repo", "config", "set", "alpha", "unknown", "value",
	)
	must.Error(err)
	is.Empty(stdout)
}

func TestIntegrationRepoConfigGetOnePolicyValue(t *testing.T) {
	is := assert.New(t)
	fixture := newRepoConfigFixture(t)

	_, setErr, err := fixture.execute(
		"repo", "config", "set", "alpha", "title-template", "[OPS] {{summary}}",
	)
	require.NoError(t, err)
	is.Empty(setErr)

	stdout, stderr, err := fixture.execute("repo", "config", "get", "alpha", "title-template")
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "title-template")
	is.Contains(stdout, "[OPS] {{summary}}")
	is.NotContains(stdout, "summary-style")
}

func TestIntegrationRepoConfigSetsAndClearsReviewPipelineDefault(t *testing.T) {
	is := assert.New(t)
	fixture := newRepoConfigFixture(t)
	must := require.New(t)

	fixture.withReviewPipelines("standard", map[string][]map[string]any{
		"standard": {{"kind": "manual", "name": "standard-review"}},
		"go":       {{"kind": "manual", "name": "go-review"}},
	})

	stdout, stderr, err := fixture.execute("repo", "config", "set", "alpha", "review-pipeline", "go")
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "review-pipeline")
	is.Contains(stdout, "go")

	stdout, stderr, err = fixture.execute("repo", "config", "get", "alpha", "review-pipeline")
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "review-pipeline")
	is.Contains(stdout, "go")
	is.NotContains(stdout, "summary-guidance")

	reg := fixture.loadFinalRegistry()
	must.Len(reg.Repos, 1)
	is.Equal("go", reg.Repos[0].ReviewPipeline)

	stdout, stderr, err = fixture.execute("repo", "config", "set", "alpha", "review-pipeline", "")
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "(not set)")
	is.Contains(stdout, "standard")

	reg = fixture.loadFinalRegistry()
	must.Len(reg.Repos, 1)
	is.Empty(reg.Repos[0].ReviewPipeline)
}

func TestIntegrationRepoConfigSetsAndClearsReviewPipelineAlias(t *testing.T) {
	is := assert.New(t)
	fixture := newRepoConfigFixture(t)
	must := require.New(t)

	fixture.withReviewPipelines("standard", map[string][]map[string]any{
		"standard": {{"kind": "manual", "name": "standard-review"}},
		"go":       {{"kind": "manual", "name": "go-review"}},
	})

	stdout, stderr, err := fixture.execute(
		"repo", "config", "set", "alpha", "review-pipeline-alias.quick", "go",
	)
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "review-pipeline-alias.quick")
	is.Contains(stdout, "go")

	stdout, stderr, err = fixture.execute("repo", "config", "get", "alpha")
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "review-pipeline-alias.quick")
	is.Contains(stdout, "go")

	reg := fixture.loadFinalRegistry()
	must.Len(reg.Repos, 1)
	is.Equal(map[string]string{"quick": "go"}, reg.Repos[0].ReviewPipelineAliases)

	stdout, stderr, err = fixture.execute(
		"repo", "config", "set", "alpha", "review-pipeline-alias.quick", "",
	)
	require.NoError(t, err)
	is.Empty(stderr)
	is.Contains(stdout, "review-pipeline-alias.quick")
	is.Contains(stdout, "(not set)")

	reg = fixture.loadFinalRegistry()
	must.Len(reg.Repos, 1)
	is.Empty(reg.Repos[0].ReviewPipelineAliases)
}

func TestIntegrationRepoConfigRejectsUnknownReviewPipelineWithoutMutatingRegistry(t *testing.T) {
	is := assert.New(t)
	fixture := newRepoConfigFixture(t)
	must := require.New(t)

	fixture.withReviewPipelines("standard", map[string][]map[string]any{
		"standard": {{"kind": "manual", "name": "standard-review"}},
	})

	for _, args := range [][]string{
		{"repo", "config", "set", "alpha", "review-pipeline", "missing"},
		{"repo", "config", "set", "alpha", "review-pipeline-alias.quick", "missing"},
	} {
		stdout, _, err := fixture.execute(args...)
		must.Error(err)
		is.Empty(stdout)
		is.ErrorContains(err, "does not match a configured global review pipeline")
		is.ErrorContains(err, "configured pipelines: standard")
	}

	reg := fixture.loadFinalRegistry()
	must.Len(reg.Repos, 1)
	is.Empty(reg.Repos[0].ReviewPipeline)
	is.Empty(reg.Repos[0].ReviewPipelineAliases)
}

func (f *workflowFixture) withReviewPipelines(defaultPipeline string, pipelines map[string][]map[string]any) {
	f.t.Helper()
	configured := make(map[string]any, len(pipelines))
	for name, steps := range pipelines {
		configured[name] = map[string]any{"steps": steps}
	}
	f.setConfig("reviews", map[string]any{"default_pipeline": defaultPipeline, "pipelines": configured})
}
