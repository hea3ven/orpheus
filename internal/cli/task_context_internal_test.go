package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestLoadTaskListContextScopesSelectedRepositoryBeforeBackendCreation(t *testing.T) {
	paths, err := state.NewPaths(testutil.CanonicalTempDir(t), testutil.CanonicalTempDir(t))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	deps := newInvocationDependenciesWithPaths(paths, logging.Discard(), map[string]string{})
	alphaPath := filepath.Join(testutil.CanonicalTempDir(t), "alpha")
	betaPath := filepath.Join(testutil.CanonicalTempDir(t), "beta")
	for _, path := range []string{alphaPath, betaPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create repository path %q: %v", path, err)
		}
	}
	if err := deps.registryStore.Save(registry.Registry{Repos: []registry.Repo{
		{ID: "alpha", Name: "Alpha Repo", Path: alphaPath, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "a"},
		{ID: "beta", Name: "Beta Repo", Path: betaPath, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "b"},
	}}); err != nil {
		t.Fatalf("save registry: %v", err)
	}

	var createdFor []string
	deps.taskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		createdFor = append(createdFor, source.Repository.ID)
		if source.Repository.ID == "beta" {
			return nil, errors.New("excluded backend was created")
		}
		return taskListContextBackend{}, nil
	}

	for _, repository := range []string{"alpha", "Alpha Repo", "a"} {
		t.Run(repository, func(t *testing.T) {
			createdFor = nil
			ctx, err := loadTaskListContextFromInvocation(deps, repository)
			if err != nil {
				t.Fatalf("load task-list context for %q: %v", repository, err)
			}
			if len(ctx.Sources) != 1 || ctx.Sources[0].Repository.ID != "alpha" {
				t.Fatalf("sources = %#v, want only alpha", ctx.Sources)
			}

			snapshot := ctx.Aggregator.Snapshot(context.Background())
			if snapshot.HasFailures() {
				t.Fatalf("snapshot failures = %#v", snapshot.Failures)
			}
			if len(createdFor) != 1 || createdFor[0] != "alpha" {
				t.Fatalf("backend creation = %v, want only alpha", createdFor)
			}
		})
	}

	createdFor = nil
	_, err = loadTaskListContextFromInvocation(deps, "missing")
	if err == nil {
		t.Fatal("load task-list context for unknown repository succeeded")
	}
	if createdFor != nil {
		t.Fatalf("backend creation for unknown repository = %v, want none", createdFor)
	}
}

type taskListContextBackend struct{}

func (taskListContextBackend) Get(context.Context, string) (taskmodel.Task, error) {
	return taskmodel.Task{}, taskmodel.ErrNotFound
}

func (taskListContextBackend) List(context.Context) ([]taskmodel.Task, error) {
	return nil, nil
}
