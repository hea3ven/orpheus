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
	return a.query(ctx, func(backend ReadBackend) ([]Task, error) {
		return backend.List(ctx)
	})
}

// Ready lists active issue_type=task items that their backend considers ready.
func (a Aggregator) Ready(ctx context.Context) QueryResult {
	return a.query(ctx, func(backend ReadBackend) ([]Task, error) {
		return backend.Ready(ctx)
	})
}

func (a Aggregator) query(ctx context.Context, query func(ReadBackend) ([]Task, error)) QueryResult {
	var result QueryResult
	for _, source := range a.sources {
		backend, err := a.factory(source)
		if err != nil {
			result.Failures = append(result.Failures, RepoFailure{Repository: source.Repository, Err: err})
			continue
		}

		tasks, err := query(backend)
		if err != nil {
			result.Failures = append(result.Failures, RepoFailure{Repository: source.Repository, Err: err})
			continue
		}

		for _, taskItem := range tasks {
			if !isM2TaskViewItem(taskItem) {
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

func isM2TaskViewItem(taskItem Task) bool {
	return taskItem.IssueType == IssueTypeTask && taskItem.Status != StatusClosed
}
