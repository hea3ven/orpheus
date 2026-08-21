package publication_test

import (
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

func TestLoadIntegrationFlowConfig(t *testing.T) {
	paths, err := state.NewPaths(testutil.CanonicalTempDir(t), testutil.CanonicalTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.WriteConfigYAML("config.yaml", map[string]any{
		"publication": map[string]any{"integration_flow": "direct-merge"},
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
}
