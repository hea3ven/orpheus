package task

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/pathutil"
)

// maxConcurrentSnapshotWorkspaces caps default parallel Beads workspace reads.
const maxConcurrentSnapshotWorkspaces = 4

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

// List lists active task-source items across all configured repositories.
func (a Aggregator) List(ctx context.Context) QueryResult {
	return a.query(ctx, "list", func(backend ReadBackend) ([]Task, error) {
		return backend.List(ctx)
	})
}

// Snapshot reads visible task-backend snapshots for local status projection.
//
// Reads from distinct backend workspaces run concurrently. Sources that resolve
// to the same workspace stay in one worker's sequence because each Beads read
// starts a short-lived embedded engine for that workspace.
func (a Aggregator) Snapshot(ctx context.Context) SnapshotResult {
	span := logging.Start(ctx, a.logger, "multi-repository task snapshot",
		slog.String("component", "task"),
		slog.String("operation", "snapshot"),
		slog.Int("repo_count", len(a.sources)),
	)

	outcomes := make([]snapshotOutcome, len(a.sources))
	a.readSnapshotWorkspaces(ctx, outcomes)

	var result SnapshotResult
	for index, source := range a.sources {
		outcome := outcomes[index]
		if outcome.err != nil {
			result.Failures = append(result.Failures, repoFailure(source.Repository, "task_backend", outcome.operation, outcome.err))
			logRepoFailure(ctx, a.logger, source.Repository, outcome.operation, outcome.err)
			continue
		}
		result.Repositories = append(result.Repositories, RepositorySnapshot{
			Repository: source.Repository,
			Tasks:      outcome.tasks,
		})
	}
	span.Finish(ctx, aggregationStatus(result.Failures),
		slog.Int("repository_count", len(result.Repositories)),
		slog.Int("failure_count", len(result.Failures)),
	)
	return result
}

type snapshotOutcome struct {
	tasks     []Task
	operation string
	err       error
}

func (a Aggregator) readSnapshotWorkspaces(ctx context.Context, outcomes []snapshotOutcome) {
	workspaces := a.snapshotWorkspaceSources()
	workers := min(maxConcurrentSnapshotWorkspaces, len(workspaces))
	if workers == 0 {
		return
	}

	jobs := make(chan []int)
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			for indexes := range jobs {
				for _, index := range indexes {
					outcomes[index] = a.readSnapshotSource(ctx, a.sources[index])
				}
			}
		}()
	}
	for _, indexes := range workspaces {
		jobs <- indexes
	}
	close(jobs)
	group.Wait()
}

func (a Aggregator) snapshotWorkspaceSources() [][]int {
	workspaceIndexes := make(map[string]int, len(a.sources))
	workspaces := make([][]int, 0, len(a.sources))
	for index, source := range a.sources {
		workspace := normalizedWorkspace(source.BackendDir)
		workspaceIndex, ok := workspaceIndexes[workspace]
		if !ok {
			workspaceIndex = len(workspaces)
			workspaceIndexes[workspace] = workspaceIndex
			workspaces = append(workspaces, nil)
		}
		workspaces[workspaceIndex] = append(workspaces[workspaceIndex], index)
	}
	return workspaces
}

func normalizedWorkspace(dir string) string {
	normalized, err := pathutil.CanonicalAbs(dir)
	if err != nil {
		return dir
	}
	return normalized
}

func (a Aggregator) readSnapshotSource(ctx context.Context, source RepositorySource) snapshotOutcome {
	backend, err := a.factory(source)
	if err != nil {
		return snapshotOutcome{operation: "create_backend", err: err}
	}
	listed, err := backend.List(ctx)
	if err != nil {
		return snapshotOutcome{operation: "snapshot", err: err}
	}
	return snapshotOutcome{tasks: cloneTasks(listed)}
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
