//go:build integration

package cli_test

import (
	"fmt"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowRepoConfigUsesGlobalPublicationPolicyFallbacks(t *testing.T) {
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

func TestIntegrationWorkflowRepoConfigSetsAndClearsBranchTemplateWithGlobalFallback(t *testing.T) {
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

func TestIntegrationWorkflowRepoConfigSetsAndClearsIncludePRReviewProcess(t *testing.T) {
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

func TestIntegrationWorkflowRepoConfigSetInvalidGlobalPublicationFlowDoesNotMutateRegistry(t *testing.T) {
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

func TestIntegrationWorkflowRepoConfigSetsAndClearsReviewPipelineDefault(t *testing.T) {
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

func TestIntegrationWorkflowRepoConfigSetsAndClearsReviewPipelineAlias(t *testing.T) {
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

func TestIntegrationWorkflowRepoConfigRejectsUnknownReviewPipelineWithoutMutatingRegistry(t *testing.T) {
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
func TestIntegrationWorkflowRepoConfigPublicationPolicyLifecycle(t *testing.T) {
	for _, global := range []bool{false, true} {
		t.Run(fmt.Sprintf("global_%t", global), func(t *testing.T) {
			fixture := newRepoConfigFixture(t)
			fallbackGuidance, fallbackTitle := "typed", "completion summary"
			if global {
				fallbackGuidance, fallbackTitle = "Write a concise global release note.", "[GLOBAL] {{summary}}"
				fixture.setConfig("publication", map[string]any{"summary_guidance": fallbackGuidance, "summary_guidance_style": registry.SummaryGuidanceStyleCapitalized, "title_template": fallbackTitle})
			}
			stdout, stderr, err := fixture.execute("repo", "config", "get", "alpha")
			require.NoError(t, err)
			assert.Empty(t, stderr)
			for _, want := range []string{"CONFIG", "summary-guidance", "(not set)", fallbackGuidance, "title-template", fallbackTitle, "review-pipeline", "default"} {
				assert.Contains(t, stdout, want)
			}
			if global {
				assert.Contains(t, stdout, "overridden by custom guidance")
			}
			guidance, template := "Use sentence-case summaries without a type prefix.", "[{{external_ref}}] {{summary}}"
			for _, setting := range [][2]string{{"summary-guidance", guidance}, {"summary-style", registry.SummaryGuidanceStyleCapitalized}, {"title-template", template}} {
				stdout, stderr, err = fixture.execute("repo", "config", "set", "alpha", setting[0], setting[1])
				require.NoError(t, err)
				assert.Empty(t, stderr)
				assert.Contains(t, stdout, setting[1])
			}
			reg := fixture.loadFinalRegistry()
			require.Len(t, reg.Repos, 1)
			assert.Equal(t, guidance, reg.Repos[0].SummaryGuidance)
			assert.Equal(t, registry.SummaryGuidanceStyleCapitalized, reg.Repos[0].SummaryGuidanceStyle)
			assert.Equal(t, template, reg.Repos[0].TitleTemplate)
			stdout, stderr, err = fixture.execute("repo", "config", "get", "alpha", "title-template")
			require.NoError(t, err)
			assert.Empty(t, stderr)
			assert.Contains(t, stdout, "title-template")
			assert.Contains(t, stdout, template)
			assert.NotContains(t, stdout, "summary-style")
			for _, key := range []string{"summary-guidance", "summary-style", "title-template"} {
				_, stderr, err = fixture.execute("repo", "config", "set", "alpha", key, "")
				require.NoError(t, err)
				assert.Empty(t, stderr)
			}
			stdout, stderr, err = fixture.execute("repo", "config", "get", "alpha")
			require.NoError(t, err)
			assert.Empty(t, stderr)
			assert.Contains(t, stdout, fallbackGuidance)
			assert.Contains(t, stdout, fallbackTitle)
			reg = fixture.loadFinalRegistry()
			assert.Empty(t, reg.Repos[0].SummaryGuidance)
			assert.Empty(t, reg.Repos[0].SummaryGuidanceStyle)
			assert.Empty(t, reg.Repos[0].TitleTemplate)
		})
	}
}

func TestIntegrationWorkflowRepoConfigRejectsInvalidPublicationSettingWithoutMutation(t *testing.T) {
	fixture := newRepoConfigFixture(t)
	_, _, err := fixture.execute("repo", "config", "set", "alpha", "title-template", "[OPS] {{summary}}")
	require.NoError(t, err)
	before := fixture.loadFinalRegistry()
	for _, test := range []struct{ key, value, want string }{
		{"summary-style", "informal", "invalid"},
		{"title-template", "{{task_id}}: {{summary}}", "unsupported placeholder"},
		{"branch-template", "{{summary}}", "unsupported placeholder"},
		{"unknown", "value", "unknown"},
	} {
		t.Run(test.key, func(t *testing.T) {
			stdout, _, err := fixture.execute("repo", "config", "set", "alpha", test.key, test.value)
			require.ErrorContains(t, err, test.want)
			assert.Empty(t, stdout)
			assert.Equal(t, before, fixture.loadFinalRegistry())
		})
	}
}
