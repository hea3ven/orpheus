package publication_test

import (
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestResolveIntegrationFlowPrecedence(t *testing.T) {
	if got := publication.ResolveIntegrationFlow("", "", ""); got != publication.IntegrationFlowPullRequest {
		t.Fatalf("default = %q", got)
	}
	if got := publication.ResolveIntegrationFlow("", publication.IntegrationFlowDirectMerge, publication.IntegrationFlowPullRequest); got != publication.IntegrationFlowDirectMerge {
		t.Fatalf("repository flow = %q", got)
	}
	if got := publication.ResolveIntegrationFlow(publication.IntegrationFlowPullRequest, publication.IntegrationFlowDirectMerge, publication.IntegrationFlowDirectMerge); got != publication.IntegrationFlowPullRequest {
		t.Fatalf("manual flow = %q", got)
	}
}

func TestLoadPublicationConfig(t *testing.T) {
	paths, err := state.NewPaths(testutil.CanonicalTempDir(t), testutil.CanonicalTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.WriteConfigYAML("config.yaml", map[string]any{
		"publication": map[string]any{
			"integration_flow":       "direct-merge",
			"summary_guidance":       "  Write a release-note summary.  ",
			"summary_guidance_style": publication.SummaryGuidanceStyleCapitalized,
			"title_template":         "  [{{external_ref}}] {{summary}}  ",
		},
	}); err != nil {
		t.Fatal(err)
	}
	config, err := publication.LoadConfig(paths)
	if err != nil {
		t.Fatal(err)
	}
	if config.IntegrationFlow != publication.IntegrationFlowDirectMerge {
		t.Fatalf("flow = %q", config.IntegrationFlow)
	}
	if config.SummaryGuidance != "Write a release-note summary." {
		t.Fatalf("summary guidance = %q", config.SummaryGuidance)
	}
	if config.SummaryGuidanceStyle != publication.SummaryGuidanceStyleCapitalized {
		t.Fatalf("summary guidance style = %q", config.SummaryGuidanceStyle)
	}
	if config.TitleTemplate != "[{{external_ref}}] {{summary}}" {
		t.Fatalf("title template = %q", config.TitleTemplate)
	}
}

func TestLoadPublicationConfigRejectsInvalidPolicy(t *testing.T) {
	for _, tt := range []struct {
		name        string
		publication map[string]any
		want        string
	}{
		{
			name:        "summary style",
			publication: map[string]any{"summary_guidance_style": "informal"},
			want:        `summary_guidance_style "informal" is invalid`,
		},
		{
			name:        "title template",
			publication: map[string]any{"title_template": "{{task_id}}: {{summary}}"},
			want:        "publication title_template is invalid",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			paths, err := state.NewPaths(testutil.CanonicalTempDir(t), testutil.CanonicalTempDir(t))
			if err != nil {
				t.Fatal(err)
			}
			if err := paths.WriteConfigYAML("config.yaml", map[string]any{"publication": tt.publication}); err != nil {
				t.Fatal(err)
			}

			_, err = publication.LoadConfig(paths)

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestResolvePolicyPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		repository publication.Policy
		global     publication.Policy
		want       publication.Policy
	}{
		{
			name: "compatibility defaults",
			want: publication.Policy{SummaryGuidanceStyle: publication.SummaryGuidanceStyleTyped},
		},
		{
			name:   "global only",
			global: publication.Policy{SummaryGuidance: "Write a release note.", SummaryGuidanceStyle: publication.SummaryGuidanceStyleCapitalized, TitleTemplate: "[GLOBAL] {{summary}}"},
			want:   publication.Policy{SummaryGuidance: "Write a release note.", SummaryGuidanceStyle: publication.SummaryGuidanceStyleCapitalized, TitleTemplate: "[GLOBAL] {{summary}}"},
		},
		{
			name:       "repository values override global independently",
			repository: publication.Policy{SummaryGuidance: "Write a repo summary.", SummaryGuidanceStyle: publication.SummaryGuidanceStyleTyped, TitleTemplate: "[REPO] {{summary}}"},
			global:     publication.Policy{SummaryGuidance: "Write a global summary.", SummaryGuidanceStyle: publication.SummaryGuidanceStyleCapitalized, TitleTemplate: "[GLOBAL] {{summary}}"},
			want:       publication.Policy{SummaryGuidance: "Write a repo summary.", SummaryGuidanceStyle: publication.SummaryGuidanceStyleTyped, TitleTemplate: "[REPO] {{summary}}"},
		},
		{
			name:       "cleared repository values inherit global",
			repository: publication.Policy{SummaryGuidance: "   ", SummaryGuidanceStyle: " ", TitleTemplate: "  "},
			global:     publication.Policy{SummaryGuidance: "Write a global summary.", SummaryGuidanceStyle: publication.SummaryGuidanceStyleCapitalized, TitleTemplate: "[GLOBAL] {{summary}}"},
			want:       publication.Policy{SummaryGuidance: "Write a global summary.", SummaryGuidanceStyle: publication.SummaryGuidanceStyleCapitalized, TitleTemplate: "[GLOBAL] {{summary}}"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := publication.ResolvePolicy(tt.repository, tt.global); got != tt.want {
				t.Fatalf("policy = %#v, want %#v", got, tt.want)
			}
		})
	}
}
