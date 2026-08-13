package registry_test

import (
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
)

func TestTaskRepositorySourcesProjectsNormalizedRegistryValues(t *testing.T) {
	paths, err := state.NewPaths(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := registry.NewStore(paths)
	includeReview := true
	registered := registry.Registry{Repos: []registry.Repo{{
		ID: " alpha ", Name: " Alpha ", Path: filepath.Join(t.TempDir(), "repo"),
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
	if source.Repository.ID != "alpha" || source.Repository.Name != "Alpha" || source.Repository.TaskIDPrefix != "op" || source.Repository.DefaultBranch != "main" {
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
