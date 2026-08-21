package registry_test

import (
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestTaskRepositorySourcesProjectsNormalizedRegistryValues(t *testing.T) {
	paths, err := state.NewPaths(testutil.CanonicalTempDir(t), testutil.CanonicalTempDir(t))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := registry.NewStore(paths)
	includeReview := true
	registered := registry.Registry{Repos: []registry.Repo{{
		ID: " alpha ", Name: " Alpha ", Path: filepath.Join(testutil.CanonicalTempDir(t), "repo"),
		BeadsMode: registry.BeadsModeManaged, BeadsPrefix: " op ", DefaultBranch: " main ",
		IncludePRReviewProcess: &includeReview, ReviewPipelineAliases: map[string]string{" local ": " default "},
	}}}
	sources, err := store.TaskRepositorySources(registered)
	if err != nil {
		t.Fatalf("TaskRepositorySources() error = %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %#v", sources)
	}
	source := sources[0]
	if source.Repository.ID != "alpha" || source.Repository.Name != "Alpha" || source.Repository.TaskIDPrefix != "op" || source.Repository.DefaultBranch != "main" || source.Repository.BranchTemplate != "orpheus/{{task_id}}" {
		t.Fatalf("repository = %#v", source.Repository)
	}
	if source.BackendDir != filepath.Join(paths.DataRoot, "repos", "alpha", "beads") {
		t.Fatalf("backend dir = %q", source.BackendDir)
	}
	if !source.MaintenanceOwned {
		t.Fatal("managed source did not retain maintenance ownership")
	}
	source.Repository.ReviewPipelineAliases["local"] = "changed"
	if source.Repository.IncludePRReviewProcess != nil {
		*source.Repository.IncludePRReviewProcess = false
	}
	if registered.Repos[0].ReviewPipelineAliases[" local "] != " default " || !*registered.Repos[0].IncludePRReviewProcess {
		t.Fatalf("task source aliases or bool pointer were not independently cloned")
	}
}

func TestTaskRepositorySourcesResolveRepositoryThenGlobalBranchTemplate(t *testing.T) {
	paths, err := state.NewPaths(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.WriteConfigYAML("config.yaml", map[string]any{
		"tasks": map[string]any{"branch_template": "global/{{task_id}}"},
	}); err != nil {
		t.Fatal(err)
	}
	store := registry.NewStore(paths)
	base := registry.Repo{ID: "alpha", Name: "Alpha", Path: t.TempDir(), BeadsMode: registry.BeadsModeManaged, BeadsPrefix: "op"}

	source, err := store.TaskRepositorySource(base)
	if err != nil {
		t.Fatal(err)
	}
	if source.Repository.BranchTemplate != "global/{{task_id}}" {
		t.Fatalf("global template = %q", source.Repository.BranchTemplate)
	}

	base.BranchTemplate = "repo/{{task_title}}"
	source, err = store.TaskRepositorySource(base)
	if err != nil {
		t.Fatal(err)
	}
	if source.Repository.BranchTemplate != "repo/{{task_title}}" {
		t.Fatalf("repository template = %q", source.Repository.BranchTemplate)
	}
}
