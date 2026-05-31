package task

import (
	"context"
	"errors"
)

// RepositorySource connects a registered repository identity to its task backend workspace.
type RepositorySource struct {
	Repository Repository
	BackendDir string
}

// BackendFactory creates a read backend for one repository source.
type BackendFactory func(RepositorySource) (ReadBackend, error)

// Aggregator performs read-only task queries across registered repository sources.
type Aggregator struct {
	sources []RepositorySource
	factory BackendFactory
}

// NewAggregator constructs a cross-repository task reader.
func NewAggregator(sources []RepositorySource, factory BackendFactory) (Aggregator, error) {
	if factory == nil {
		return Aggregator{}, errors.New("create task aggregator: backend factory is required")
	}

	copied := make([]RepositorySource, len(sources))
	copy(copied, sources)
	return Aggregator{sources: copied, factory: factory}, nil
}

// List lists active issue_type=task items across all configured repositories.
func (a Aggregator) List(ctx context.Context) QueryResult {
	return a.query(ctx, "list", func(backend ReadBackend) ([]Task, error) {
		return backend.List(ctx)
	})
}

// Snapshot reads visible task-backend snapshots for local status projection.
func (a Aggregator) Snapshot(ctx context.Context) SnapshotResult {
	var result SnapshotResult
	for _, source := range a.sources {
		backend, err := a.factory(source)
		if err != nil {
			result.Failures = append(result.Failures, repoFailure(source.Repository, "task_backend", "create_backend", err))
			continue
		}

		listed, err := backend.List(ctx)
		if err != nil {
			result.Failures = append(result.Failures, repoFailure(source.Repository, "task_backend", "snapshot", err))
			continue
		}

		result.Repositories = append(result.Repositories, RepositorySnapshot{
			Repository: source.Repository,
			Tasks:      cloneTasks(listed),
		})
	}
	return result
}

func (a Aggregator) query(ctx context.Context, operation string, query func(ReadBackend) ([]Task, error)) QueryResult {
	var result QueryResult
	for _, source := range a.sources {
		backend, err := a.factory(source)
		if err != nil {
			result.Failures = append(result.Failures, repoFailure(source.Repository, "task_backend", "create_backend", err))
			continue
		}

		tasks, err := query(backend)
		if err != nil {
			result.Failures = append(result.Failures, repoFailure(source.Repository, "task_backend", operation, err))
			continue
		}

		for _, taskItem := range tasks {
			if !IsM2TaskViewItem(taskItem) {
				continue
			}
			result.Rows = append(result.Rows, RepoTask{
				Repository: source.Repository,
				Task:       taskItem.Clone(),
			})
		}
	}
	return result
}

func repoFailure(repository Repository, source string, operation string, err error) RepoFailure {
	return RepoFailure{Repository: repository, Source: source, Operation: operation, Err: err}
}
