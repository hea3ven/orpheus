package task

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
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

	// MaintenanceOwned authorizes Orpheus to perform narrowly scoped backend
	// maintenance for this source. It is projected from the registry rather than
	// inferred from BackendDir, because filesystem location is not ownership.
	MaintenanceOwned bool
}

// BackendFactory creates a read backend for one repository source.
type BackendFactory func(RepositorySource) (ReadBackend, error)

// Aggregator performs read-only task queries across registered repository sources.
type Aggregator struct {
	sources []RepositorySource
	factory BackendFactory
	logger  *slog.Logger
}

// FilteredSnapshotResult keeps task-list output candidates separate from the
// relationship context needed to project their status correctly.
type FilteredSnapshotResult struct {
	Snapshot   SnapshotResult
	Candidates []RepoTask
}

// Clone returns a deep copy of a filtered snapshot result.
func (r FilteredSnapshotResult) Clone() FilteredSnapshotResult {
	return FilteredSnapshotResult{
		Snapshot:   r.Snapshot.Clone(),
		Candidates: cloneRows(r.Candidates),
	}
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

// FilteredSnapshot finds source-filtered output candidates, then completes
// only the repository-local relationship context required to project them.
// Context rows must never be rendered as task-list output candidates.
func (a Aggregator) FilteredSnapshot(ctx context.Context, filter ListFilter) (FilteredSnapshotResult, error) {
	filter, err := filter.Normalized()
	if err != nil {
		return FilteredSnapshotResult{}, err
	}

	span := logging.Start(ctx, a.logger, "filtered multi-repository task snapshot",
		slog.String("component", "task"),
		slog.String("operation", "filtered_snapshot"),
		slog.Int("repo_count", len(a.sources)),
	)
	outcomes := make([]snapshotOutcome, len(a.sources))
	a.readFilteredSnapshotWorkspaces(ctx, outcomes, filter)

	result := FilteredSnapshotResult{}
	for index, source := range a.sources {
		outcome := outcomes[index]
		if outcome.err != nil {
			result.Snapshot.Failures = append(result.Snapshot.Failures, repoFailure(source.Repository, "task_backend", outcome.operation, outcome.err))
			logRepoFailure(ctx, a.logger, source.Repository, outcome.operation, outcome.err)
			continue
		}
		snapshot := RepositorySnapshot{Repository: source.Repository, Tasks: cloneTasks(outcome.tasks)}
		result.Snapshot.Repositories = append(result.Snapshot.Repositories, snapshot)
		for _, taskItem := range outcome.tasks {
			result.Candidates = append(result.Candidates, RepoTask{Repository: source.Repository, Task: taskItem.Clone()})
		}
	}
	a.completeFilteredSnapshotContext(ctx, &result.Snapshot, result.Candidates)

	span.Finish(ctx, aggregationStatus(result.Snapshot.Failures),
		slog.Int("repository_count", len(result.Snapshot.Repositories)),
		slog.Int("candidate_count", len(result.Candidates)),
		slog.Int("failure_count", len(result.Snapshot.Failures)),
	)
	return result, nil
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

func (a Aggregator) readFilteredSnapshotWorkspaces(ctx context.Context, outcomes []snapshotOutcome, filter ListFilter) {
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
					outcomes[index] = a.readFilteredSnapshotSource(ctx, a.sources[index], filter)
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

func (a Aggregator) readFilteredSnapshotSource(ctx context.Context, source RepositorySource, filter ListFilter) snapshotOutcome {
	backend, err := a.factory(source)
	if err != nil {
		return snapshotOutcome{operation: "create_backend", err: err}
	}
	lister, ok := backend.(FilteredLister)
	if !ok {
		return snapshotOutcome{operation: "list", err: errors.New("task backend does not support filtered listing")}
	}
	listed, err := lister.ListFiltered(ctx, filter)
	if err != nil {
		return snapshotOutcome{operation: "list", err: err}
	}
	return snapshotOutcome{tasks: cloneTasks(listed)}
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

// completeFilteredSnapshotContext adds only the relationship rows required to
// classify selected tasks. It deliberately does not use the output filter for
// these lookups: an excluded parent, dependency, or child must still affect
// classification and epic progress without becoming an output row.
func (a Aggregator) completeFilteredSnapshotContext(ctx context.Context, snapshot *SnapshotResult, candidates []RepoTask) {
	seen := snapshotTaskIDs(snapshot)
	backends := make(map[string]ReadBackend)
	backendFor := func(source RepositorySource) (ReadBackend, error) {
		if backend, ok := backends[source.Repository.ID]; ok {
			return backend, nil
		}
		backend, err := a.factory(source)
		if err != nil {
			return nil, err
		}
		backends[source.Repository.ID] = backend
		return backend, nil
	}

	for _, candidate := range candidates {
		source, ok := a.sourceForRepository(candidate.Repository.ID)
		if !ok {
			continue
		}
		backend, err := backendFor(source)
		if err != nil {
			a.appendRelationshipFailure(snapshot, source.Repository, candidate.Task.ID, "", Repository{}, err)
			continue
		}

		if candidate.Task.IssueType == IssueTypeEpic {
			a.addEpicChildrenContext(ctx, snapshot, seen, source, backend, candidate.Task.ID)
		}
		for _, referenceID := range relationshipReferenceIDs(candidate.Task) {
			a.addRelationshipContext(ctx, snapshot, seen, source, backend, candidate.Task.ID, referenceID)
		}
	}
}

func (a Aggregator) sourceForRepository(repositoryID string) (RepositorySource, bool) {
	for _, source := range a.sources {
		if source.Repository.ID == repositoryID {
			return source, true
		}
	}
	return RepositorySource{}, false
}

func snapshotTaskIDs(snapshot *SnapshotResult) map[string]map[string]struct{} {
	seen := make(map[string]map[string]struct{}, len(snapshot.Repositories))
	for _, repository := range snapshot.Repositories {
		ids := make(map[string]struct{}, len(repository.Tasks))
		for _, taskItem := range repository.Tasks {
			if id := strings.TrimSpace(taskItem.ID); id != "" {
				ids[id] = struct{}{}
			}
		}
		seen[repository.Repository.ID] = ids
	}
	return seen
}

func (a Aggregator) addEpicChildrenContext(
	ctx context.Context,
	snapshot *SnapshotResult,
	seen map[string]map[string]struct{},
	source RepositorySource,
	backend ReadBackend,
	epicID string,
) {
	lister, ok := backend.(FilteredLister)
	if !ok {
		a.appendRelationshipFailure(snapshot, source.Repository, "", "", Repository{}, errors.New("task backend does not support relationship-context listing"))
		return
	}
	children, err := lister.ListFiltered(ctx, ListFilter{ParentID: epicID})
	if err != nil {
		a.appendRelationshipFailure(snapshot, source.Repository, "", "", Repository{}, err)
		return
	}
	for _, child := range children {
		appendSnapshotContextTask(snapshot, seen, source.Repository, child)
	}
}

func (a Aggregator) addRelationshipContext(
	ctx context.Context,
	snapshot *SnapshotResult,
	seen map[string]map[string]struct{},
	source RepositorySource,
	backend ReadBackend,
	taskID string,
	referenceID string,
) {
	referenceID = strings.TrimSpace(referenceID)
	if referenceID == "" {
		return
	}

	resolved, err := ResolveTaskSource(a.sources, referenceID)
	if err != nil {
		// An unresolved source is unavailable context, not confirmation that the
		// referenced task is missing from a source.
		a.appendRelationshipContextUnavailable(snapshot, source.Repository, taskID, referenceID, Repository{})
		return
	}
	if resolved.Source.Repository.ID != source.Repository.ID {
		// Cross-repository relationship projection is not currently supported.
		// Preserve the registered target identity and make the unavailable
		// context explicit so it cannot become a false missing diagnostic.
		a.appendRelationshipContextUnavailable(snapshot, source.Repository, taskID, referenceID, resolved.Source.Repository)
		return
	}
	if _, ok := seen[source.Repository.ID][referenceID]; ok {
		return
	}

	contextTask, err := backend.Get(ctx, referenceID)
	if err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnsupportedTaskSourceItem) {
			// The source confirmed this reference is unavailable to the task
			// model. Leave it absent so status projection renders its existing
			// missing-relationship diagnostic.
			return
		}
		a.appendRelationshipFailure(snapshot, source.Repository, taskID, referenceID, resolved.Source.Repository, err)
		return
	}
	appendSnapshotContextTask(snapshot, seen, source.Repository, contextTask)
}

func relationshipReferenceIDs(taskItem Task) []string {
	ids := make([]string, 0, 1+len(taskItem.Relations.DependencyIDs))
	if parentID := strings.TrimSpace(taskItem.Relations.ParentID); parentID != "" {
		ids = append(ids, parentID)
	}
	ids = append(ids, taskItem.Relations.DependencyIDs...)
	sort.Strings(ids)

	unique := ids[:0]
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || (len(unique) > 0 && unique[len(unique)-1] == id) {
			continue
		}
		unique = append(unique, id)
	}
	return unique
}

func appendSnapshotContextTask(
	snapshot *SnapshotResult,
	seen map[string]map[string]struct{},
	repository Repository,
	taskItem Task,
) {
	if !IsTaskSourceItem(taskItem) {
		return
	}
	id := strings.TrimSpace(taskItem.ID)
	if id == "" {
		return
	}
	ids, ok := seen[repository.ID]
	if !ok {
		return
	}
	if _, exists := ids[id]; exists {
		return
	}
	for index := range snapshot.Repositories {
		if snapshot.Repositories[index].Repository.ID == repository.ID {
			snapshot.Repositories[index].Tasks = append(snapshot.Repositories[index].Tasks, taskItem.Clone())
			ids[id] = struct{}{}
			return
		}
	}
}

func (a Aggregator) appendRelationshipContextUnavailable(
	snapshot *SnapshotResult,
	repository Repository,
	taskID string,
	referenceID string,
	referenceRepository Repository,
) {
	for index := range snapshot.Repositories {
		if snapshot.Repositories[index].Repository.ID != repository.ID {
			continue
		}
		if strings.TrimSpace(taskID) != "" {
			snapshot.Repositories[index].RelationshipContextFailures = append(
				snapshot.Repositories[index].RelationshipContextFailures,
				RelationshipContextFailure{
					TaskID:              strings.TrimSpace(taskID),
					ReferenceID:         strings.TrimSpace(referenceID),
					ReferenceRepository: referenceRepository,
				},
			)
		}
		break
	}
}

func (a Aggregator) appendRelationshipFailure(
	snapshot *SnapshotResult,
	repository Repository,
	taskID string,
	referenceID string,
	referenceRepository Repository,
	err error,
) {
	a.appendRelationshipContextUnavailable(snapshot, repository, taskID, referenceID, referenceRepository)
	snapshot.Failures = append(snapshot.Failures, repoFailure(repository, "task_backend", "relationship_context", err))
	logRepoFailure(context.Background(), a.logger, repository, "relationship_context", err)
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
