package task

import (
	"context"
	"errors"
	"log/slog"

	"github.com/hea3ven/orpheus/internal/logging"
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
	logger  *slog.Logger
}

// NewAggregator constructs a cross-repository task reader.
func NewAggregator(sources []RepositorySource, factory BackendFactory) (Aggregator, error) {
	return NewAggregatorWithLogger(sources, factory, nil)
}

// NewAggregatorWithLogger constructs a cross-repository task reader with diagnostics.
func NewAggregatorWithLogger(sources []RepositorySource, factory BackendFactory, logger *slog.Logger) (Aggregator, error) {
	if factory == nil {
		return Aggregator{}, errors.New("create task aggregator: backend factory is required")
	}

	copied := make([]RepositorySource, len(sources))
	copy(copied, sources)
	return Aggregator{sources: copied, factory: factory, logger: logger}, nil
}

// List lists active items across all configured repositories.
func (a Aggregator) List(ctx context.Context) QueryResult {
	return a.query(ctx, "list", func(backend ReadBackend) ([]Task, error) {
		return backend.List(ctx)
	})
}

// Snapshot reads visible task-backend snapshots for local status projection.
func (a Aggregator) Snapshot(ctx context.Context) SnapshotResult {
	span := logging.Start(ctx, a.logger, "multi-repository task snapshot",
		slog.String("component", "task"),
		slog.String("operation", "snapshot"),
		slog.Int("repo_count", len(a.sources)),
	)
	var result SnapshotResult
	for _, source := range a.sources {
		backend, err := a.factory(source)
		if err != nil {
			result.Failures = append(result.Failures, repoFailure(source.Repository, "task_backend", "create_backend", err))
			logRepoFailure(ctx, a.logger, source.Repository, "create_backend", err)
			continue
		}

		listed, err := backend.List(ctx)
		if err != nil {
			result.Failures = append(result.Failures, repoFailure(source.Repository, "task_backend", "snapshot", err))
			logRepoFailure(ctx, a.logger, source.Repository, "snapshot", err)
			continue
		}

		result.Repositories = append(result.Repositories, RepositorySnapshot{
			Repository: source.Repository,
			Tasks:      cloneTasks(listed),
		})
	}
	span.Finish(ctx, aggregationStatus(result.Failures),
		slog.Int("repository_count", len(result.Repositories)),
		slog.Int("failure_count", len(result.Failures)),
	)
	return result
}

func (a Aggregator) query(ctx context.Context, operation string, query func(ReadBackend) ([]Task, error)) QueryResult {
	span := logging.Start(ctx, a.logger, "multi-repository task query",
		slog.String("component", "task"),
		slog.String("operation", operation),
		slog.Int("repo_count", len(a.sources)),
	)
	var result QueryResult
	for _, source := range a.sources {
		backend, err := a.factory(source)
		if err != nil {
			result.Failures = append(result.Failures, repoFailure(source.Repository, "task_backend", "create_backend", err))
			logRepoFailure(ctx, a.logger, source.Repository, "create_backend", err)
			continue
		}

		tasks, err := query(backend)
		if err != nil {
			result.Failures = append(result.Failures, repoFailure(source.Repository, "task_backend", operation, err))
			logRepoFailure(ctx, a.logger, source.Repository, operation, err)
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
	span.Finish(ctx, aggregationStatus(result.Failures),
		slog.Int("row_count", len(result.Rows)),
		slog.Int("failure_count", len(result.Failures)),
	)
	return result
}

func aggregationStatus(failures []RepoFailure) string {
	if len(failures) > 0 {
		return logging.StatusFailure
	}
	return logging.StatusSuccess
}

func logRepoFailure(ctx context.Context, logger *slog.Logger, repository Repository, operation string, _ error) {
	if logger == nil || !logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	logger.DebugContext(ctx, "repository task query failed",
		slog.String("component", "task"),
		slog.String("operation", operation),
		slog.String("status", logging.StatusFailure),
		slog.String("repo_id", repository.ID),
	)
}

func repoFailure(repository Repository, source string, operation string, err error) RepoFailure {
	return RepoFailure{Repository: repository, Source: source, Operation: operation, Err: err}
}
