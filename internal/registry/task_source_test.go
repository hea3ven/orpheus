package registry_test

import (
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
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

func TestTaskRepositorySourcesResolveRepositoryThenGlobalTemplates(t *testing.T) {
	tests := []struct {
		name            string
		globalConfig    map[string]any
		setRepository   func(*registry.Repo)
		resolvedSource  func(task.Repository) string
		globalValue     string
		repositoryValue string
	}{
		{
			name:         "publication title",
			globalConfig: map[string]any{"publication": map[string]any{"title_template": "[{{external_ref}}] {{summary}}"}},
			setRepository: func(repo *registry.Repo) {
				repo.TitleTemplate = "[REPO] {{summary}}"
			},
			resolvedSource:  func(repo task.Repository) string { return repo.TitleTemplate },
			globalValue:     "[{{external_ref}}] {{summary}}",
			repositoryValue: "[REPO] {{summary}}",
		},
		{
			name:         "task branch",
			globalConfig: map[string]any{"tasks": map[string]any{"branch_template": "global/{{task_id}}"}},
			setRepository: func(repo *registry.Repo) {
				repo.BranchTemplate = "repo/{{task_title}}"
			},
			resolvedSource:  func(repo task.Repository) string { return repo.BranchTemplate },
			globalValue:     "global/{{task_id}}",
			repositoryValue: "repo/{{task_title}}",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths, err := state.NewPaths(testutil.CanonicalTempDir(t), testutil.CanonicalTempDir(t))
			if err != nil {
				t.Fatal(err)
			}
			if err := paths.WriteConfigYAML("config.yaml", tt.globalConfig); err != nil {
				t.Fatal(err)
			}
			store := registry.NewStore(paths)
			repo := registry.Repo{ID: "alpha", Name: "Alpha", Path: testutil.CanonicalTempDir(t), BeadsMode: registry.BeadsModeManaged, BeadsPrefix: "op"}

			source, err := store.TaskRepositorySource(repo)
			if err != nil {
				t.Fatal(err)
			}
			if got := tt.resolvedSource(source.Repository); got != tt.globalValue {
				t.Fatalf("global value = %q, want %q", got, tt.globalValue)
			}

			tt.setRepository(&repo)
			source, err = store.TaskRepositorySource(repo)
			if err != nil {
				t.Fatal(err)
			}
			if got := tt.resolvedSource(source.Repository); got != tt.repositoryValue {
				t.Fatalf("repository value = %q, want %q", got, tt.repositoryValue)
			}
		})
	}
}
