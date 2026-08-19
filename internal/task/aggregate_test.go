package task_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
)

func TestAggregatorListQueriesReposAndPreservesContext(t *testing.T) {
	repos := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"}, BackendDir: "/tmp/alpha"},
		{Repository: task.Repository{ID: "beta", Name: "Beta", TaskIDPrefix: "b"}, BackendDir: "/tmp/beta"},
	}
	backends := map[string]fakeReadBackend{
		"/tmp/alpha": {tasks: []task.Task{{ID: "a-1", Title: "alpha task", IssueType: task.IssueTypeTask, Status: task.StatusOpen}}},
		"/tmp/beta":  {tasks: []task.Task{{ID: "b-1", Title: "beta task", IssueType: task.IssueTypeTask, Status: task.StatusInProgress}}},
	}

	aggregator, err := task.NewAggregator(repos, func(source task.RepositorySource) (task.ReadBackend, error) {
		backend, ok := backends[source.BackendDir]
		if !ok {
			return nil, errors.New("unexpected backend dir")
		}
		return backend, nil
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	got := aggregator.List(context.Background())

	if got.HasFailures() {
		t.Fatalf("failures = %#v, want none", got.Failures)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("rows = %#v, want two task rows", got.Rows)
	}
	if got.Rows[0].Repository.ID != "alpha" || got.Rows[0].Repository.TaskIDPrefix != "a" || got.Rows[0].Task.ID != "a-1" {
		t.Fatalf("first row = %#v, want alpha/a/a-1", got.Rows[0])
	}
	if got.Rows[1].Repository.ID != "beta" || got.Rows[1].Repository.TaskIDPrefix != "b" || got.Rows[1].Task.ID != "b-1" {
		t.Fatalf("second row = %#v, want beta/b/b-1", got.Rows[1])
	}
}

func TestAggregatorListFiltersClosedTaskSourceItems(t *testing.T) {
	repos := []task.RepositorySource{{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"}, BackendDir: "/tmp/alpha"}}
	backend := fakeReadBackend{tasks: []task.Task{
		{ID: "a-1", Title: "active task", IssueType: task.IssueTypeTask, Status: task.StatusOpen},
		{ID: "a-2", Title: "closed task", IssueType: task.IssueTypeTask, Status: task.StatusClosed},
		{ID: "a-4", Title: "epic", IssueType: task.IssueTypeEpic, Status: task.StatusInProgress},
	}}

	aggregator, err := task.NewAggregator(repos, func(task.RepositorySource) (task.ReadBackend, error) {
		return backend, nil
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	got := aggregator.List(context.Background())

	gotIDs := []string{}
	for _, row := range got.Rows {
		gotIDs = append(gotIDs, row.Task.ID)
	}
	expectedIDs := []string{"a-1", "a-4"}
	if !reflect.DeepEqual(gotIDs, expectedIDs) {
		t.Fatalf("rows = %#v, want active task-source items %v", got.Rows, expectedIDs)
	}
}

func TestAggregatorSnapshotPreservesTaskSourceItems(t *testing.T) {
	repos := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"}, BackendDir: "/tmp/alpha"},
		{Repository: task.Repository{ID: "beta", Name: "Beta", TaskIDPrefix: "b"}, BackendDir: "/tmp/beta"},
	}
	backends := map[string]fakeReadBackend{
		"/tmp/alpha": {tasks: []task.Task{
			{ID: "a-1", Title: "alpha active", IssueType: task.IssueTypeTask, Status: task.StatusOpen},
			{ID: "a-closed", Title: "alpha closed", IssueType: task.IssueTypeTask, Status: task.StatusClosed},
		}},
		"/tmp/beta": {tasks: []task.Task{{ID: "b-epic", Title: "beta epic", IssueType: task.IssueTypeEpic, Status: task.StatusOpen}}},
	}

	aggregator, err := task.NewAggregator(repos, func(source task.RepositorySource) (task.ReadBackend, error) {
		backend, ok := backends[source.BackendDir]
		if !ok {
			return nil, errors.New("unexpected backend dir")
		}
		return backend, nil
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	got := aggregator.Snapshot(context.Background())

	if got.HasFailures() {
		t.Fatalf("failures = %#v, want none", got.Failures)
	}
	if len(got.Repositories) != 2 {
		t.Fatalf("repositories = %#v, want two snapshots", got.Repositories)
	}
	if len(got.Repositories[0].Tasks) != 2 {
		t.Fatalf("alpha tasks = %#v, want active and closed task items", got.Repositories[0].Tasks)
	}
	if got.Repositories[0].Tasks[1].ID != "a-closed" || got.Repositories[0].Tasks[1].Status != task.StatusClosed {
		t.Fatalf("alpha tasks = %#v, want closed task retained", got.Repositories[0].Tasks)
	}
	if got.Repositories[1].Tasks[0].ID != "b-epic" {
		t.Fatalf("beta tasks = %#v, want epic preserved", got.Repositories[1].Tasks)
	}
}

func TestAggregatorFilteredSnapshotSeparatesCandidatesFromParentAndDependencyContext(t *testing.T) {
	repo := task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"}
	backend := &recordingFilteredReadBackend{tasks: []task.Task{
		{ID: "a-child", Title: "matching child", IssueType: task.IssueTypeTask, Status: task.StatusOpen, Relations: task.RelationSummary{ParentID: "a-epic", DependencyIDs: []string{"a-dependency"}}},
		{ID: "a-epic", Title: "excluded parent", IssueType: task.IssueTypeEpic, Status: task.StatusInProgress},
		{ID: "a-dependency", Title: "excluded dependency", IssueType: task.IssueTypeTask, Status: task.StatusClosed},
	}}
	aggregator, err := task.NewAggregator([]task.RepositorySource{{Repository: repo, BackendDir: t.TempDir()}}, func(task.RepositorySource) (task.ReadBackend, error) {
		return backend, nil
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	got, err := aggregator.FilteredSnapshot(context.Background(), task.ListFilter{Query: "matching"})
	if err != nil {
		t.Fatalf("filtered snapshot: %v", err)
	}
	if got.Snapshot.HasFailures() {
		t.Fatalf("failures = %#v, want none", got.Snapshot.Failures)
	}
	if gotIDs := repoTaskIDs(got.Candidates); !reflect.DeepEqual(gotIDs, []string{"a-child"}) {
		t.Fatalf("candidates = %v, want selected child only", gotIDs)
	}
	if gotIDs := snapshotTaskIDsForRepository(t, got.Snapshot, "alpha"); !reflect.DeepEqual(gotIDs, []string{"a-child", "a-dependency", "a-epic"}) {
		t.Fatalf("snapshot tasks = %v, want selected child with parent and dependency context", gotIDs)
	}
	if len(backend.filters) != 1 || backend.filters[0].Query != "matching" {
		t.Fatalf("source filters = %#v, want one pushed candidate query", backend.filters)
	}
}

func TestAggregatorFilteredSnapshotAddsNonmatchingEpicChildrenAsContext(t *testing.T) {
	repo := task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"}
	backend := &recordingFilteredReadBackend{tasks: []task.Task{
		{ID: "a-epic", Title: "matching epic", IssueType: task.IssueTypeEpic, Status: task.StatusOpen, Relations: task.RelationSummary{ChildCount: 1}},
		{ID: "a-child", Title: "excluded child", IssueType: task.IssueTypeTask, Status: task.StatusClosed, Relations: task.RelationSummary{ParentID: "a-epic"}},
	}}
	aggregator, err := task.NewAggregator([]task.RepositorySource{{Repository: repo, BackendDir: t.TempDir()}}, func(task.RepositorySource) (task.ReadBackend, error) {
		return backend, nil
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	got, err := aggregator.FilteredSnapshot(context.Background(), task.ListFilter{Query: "matching"})
	if err != nil {
		t.Fatalf("filtered snapshot: %v", err)
	}
	if gotIDs := repoTaskIDs(got.Candidates); !reflect.DeepEqual(gotIDs, []string{"a-epic"}) {
		t.Fatalf("candidates = %v, want selected epic only", gotIDs)
	}
	if gotIDs := snapshotTaskIDsForRepository(t, got.Snapshot, "alpha"); !reflect.DeepEqual(gotIDs, []string{"a-child", "a-epic"}) {
		t.Fatalf("snapshot tasks = %v, want selected epic with child context", gotIDs)
	}
	if len(backend.filters) != 2 || backend.filters[1].ParentID != "a-epic" || backend.filters[1].Query != "" {
		t.Fatalf("source filters = %#v, want an unfiltered child-context query", backend.filters)
	}
}

func TestAggregatorFilteredSnapshotPreservesCrossRepositoryRelationshipOwner(t *testing.T) {
	alpha := task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"}
	beta := task.Repository{ID: "beta", Name: "Beta", TaskIDPrefix: "b"}
	alphaBackend := &recordingFilteredReadBackend{tasks: []task.Task{{
		ID:        "a-selected",
		Title:     "matching task",
		IssueType: task.IssueTypeTask,
		Status:    task.StatusOpen,
		Relations: task.RelationSummary{DependencyIDs: []string{"b-dependency"}},
	}}}
	betaBackend := &recordingFilteredReadBackend{tasks: []task.Task{{
		ID:        "b-dependency",
		Title:     "other dependency",
		IssueType: task.IssueTypeTask,
		Status:    task.StatusClosed,
	}}}
	alphaSource := task.RepositorySource{Repository: alpha, BackendDir: t.TempDir()}
	betaSource := task.RepositorySource{Repository: beta, BackendDir: t.TempDir()}
	aggregator, err := task.NewAggregator([]task.RepositorySource{alphaSource, betaSource}, func(source task.RepositorySource) (task.ReadBackend, error) {
		switch source.Repository.ID {
		case alpha.ID:
			return alphaBackend, nil
		case beta.ID:
			return betaBackend, nil
		default:
			return nil, fmt.Errorf("unexpected repository %q", source.Repository.ID)
		}
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	got, err := aggregator.FilteredSnapshot(context.Background(), task.ListFilter{Query: "matching"})
	if err != nil {
		t.Fatalf("filtered snapshot: %v", err)
	}
	if got.Snapshot.HasFailures() {
		t.Fatalf("failures = %#v, want none", got.Snapshot.Failures)
	}
	if gotIDs := repoTaskIDs(got.Candidates); !reflect.DeepEqual(gotIDs, []string{"a-selected"}) {
		t.Fatalf("candidates = %v, want selected task only", gotIDs)
	}

	var alphaSnapshot task.RepositorySnapshot
	for _, repository := range got.Snapshot.Repositories {
		if repository.Repository.ID == alpha.ID {
			alphaSnapshot = repository
			break
		}
	}
	wantFailures := []task.RelationshipContextFailure{{
		TaskID:              "a-selected",
		ReferenceID:         "b-dependency",
		ReferenceRepository: beta,
	}}
	if !reflect.DeepEqual(alphaSnapshot.RelationshipContextFailures, wantFailures) {
		t.Fatalf("relationship context failures = %#v, want %#v", alphaSnapshot.RelationshipContextFailures, wantFailures)
	}
}

func TestAggregatorFilteredSnapshotDistinguishesRelationshipQueryFailure(t *testing.T) {
	repo := task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"}
	backend := &recordingFilteredReadBackend{
		tasks:  []task.Task{{ID: "a-child", Title: "matching", IssueType: task.IssueTypeTask, Relations: task.RelationSummary{DependencyIDs: []string{"a-dependency"}}}},
		getErr: map[string]error{"a-dependency": errors.New("backend unavailable")},
	}
	aggregator, err := task.NewAggregator([]task.RepositorySource{{Repository: repo, BackendDir: t.TempDir()}}, func(task.RepositorySource) (task.ReadBackend, error) {
		return backend, nil
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	got, err := aggregator.FilteredSnapshot(context.Background(), task.ListFilter{Query: "matching"})
	if err != nil {
		t.Fatalf("filtered snapshot: %v", err)
	}
	if len(got.Snapshot.Failures) != 1 || got.Snapshot.Failures[0].Operation != "relationship_context" || !strings.Contains(got.Snapshot.Failures[0].Err.Error(), "backend unavailable") {
		t.Fatalf("failures = %#v, want relationship-context query failure", got.Snapshot.Failures)
	}
	failures := got.Snapshot.Repositories[0].RelationshipContextFailures
	if !reflect.DeepEqual(failures, []task.RelationshipContextFailure{{
		TaskID:              "a-child",
		ReferenceID:         "a-dependency",
		ReferenceRepository: repo,
	}}) {
		t.Fatalf("relationship context failures = %#v, want selected dependency lookup failure", failures)
	}
}

func TestAggregatorFilteredSnapshotRecordsParentFailureAfterEarlierDependencyFailure(t *testing.T) {
	repo := task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"}
	backend := &recordingFilteredReadBackend{
		tasks: []task.Task{{
			ID:        "a-child",
			Title:     "matching",
			IssueType: task.IssueTypeTask,
			Relations: task.RelationSummary{
				ParentID:      "a-parent",
				DependencyIDs: []string{"a-dependency"},
			},
		}},
		getErr: map[string]error{
			"a-dependency": errors.New("dependency backend unavailable"),
			"a-parent":     errors.New("parent backend unavailable"),
		},
	}
	aggregator, err := task.NewAggregator([]task.RepositorySource{{Repository: repo, BackendDir: t.TempDir()}}, func(task.RepositorySource) (task.ReadBackend, error) {
		return backend, nil
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	got, err := aggregator.FilteredSnapshot(context.Background(), task.ListFilter{Query: "matching"})
	if err != nil {
		t.Fatalf("filtered snapshot: %v", err)
	}
	if len(got.Snapshot.Failures) != 2 {
		t.Fatalf("failures = %#v, want dependency and parent lookup failures", got.Snapshot.Failures)
	}
	wantFailures := []task.RelationshipContextFailure{
		{TaskID: "a-child", ReferenceID: "a-dependency", ReferenceRepository: repo},
		{TaskID: "a-child", ReferenceID: "a-parent", ReferenceRepository: repo},
	}
	if failures := got.Snapshot.Repositories[0].RelationshipContextFailures; !reflect.DeepEqual(failures, wantFailures) {
		t.Fatalf("relationship context failures = %#v, want %#v", failures, wantFailures)
	}
}

func TestAggregatorSnapshotContinuesAfterRepoFailure(t *testing.T) {
	queryErr := errors.New("bd list failed")
	repos := []task.RepositorySource{
		{Repository: task.Repository{ID: "broken", Name: "Broken", TaskIDPrefix: "br"}, BackendDir: "/tmp/broken"},
		{Repository: task.Repository{ID: "ok", Name: "OK", TaskIDPrefix: "ok"}, BackendDir: "/tmp/ok"},
	}

	aggregator, err := task.NewAggregator(repos, func(source task.RepositorySource) (task.ReadBackend, error) {
		if source.Repository.ID == "broken" {
			return failingReadBackend{err: queryErr}, nil
		}
		return fakeReadBackend{tasks: []task.Task{{ID: "ok-1", Title: "still listed", IssueType: task.IssueTypeTask, Status: task.StatusOpen}}}, nil
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	got := aggregator.Snapshot(context.Background())

	if !got.HasFailures() || len(got.Failures) != 1 {
		t.Fatalf("failures = %#v, want one failure", got.Failures)
	}
	failure := got.Failures[0]
	if failure.Repository.ID != "broken" || failure.Source != "task_backend" || failure.Operation != "snapshot" || !errors.Is(failure.Err, queryErr) {
		t.Fatalf("failure = %#v, want structured broken snapshot error", failure)
	}
	if len(got.Repositories) != 1 || got.Repositories[0].Tasks[0].ID != "ok-1" {
		t.Fatalf("repositories = %#v, want successful snapshot row", got.Repositories)
	}
}

func TestAggregatorReportsBackendCreationFailure(t *testing.T) {
	factoryErr := errors.New("backend unavailable")
	repos := []task.RepositorySource{{Repository: task.Repository{ID: "broken", Name: "Broken", TaskIDPrefix: "br"}, BackendDir: "/tmp/broken"}}

	aggregator, err := task.NewAggregator(repos, func(task.RepositorySource) (task.ReadBackend, error) {
		return nil, factoryErr
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	got := aggregator.Snapshot(context.Background())

	if len(got.Repositories) != 0 {
		t.Fatalf("repositories = %#v, want none", got.Repositories)
	}
	if len(got.Failures) != 1 || got.Failures[0].Repository.ID != "broken" || got.Failures[0].Source != "task_backend" || got.Failures[0].Operation != "create_backend" || !errors.Is(got.Failures[0].Err, factoryErr) {
		t.Fatalf("failures = %#v, want structured backend creation failure", got.Failures)
	}
}

func repoTaskIDs(rows []task.RepoTask) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.Task.ID)
	}
	return ids
}

func snapshotTaskIDsForRepository(t *testing.T, snapshot task.SnapshotResult, repositoryID string) []string {
	t.Helper()
	for _, repository := range snapshot.Repositories {
		if repository.Repository.ID != repositoryID {
			continue
		}
		ids := make([]string, 0, len(repository.Tasks))
		for _, taskItem := range repository.Tasks {
			ids = append(ids, taskItem.ID)
		}
		sort.Strings(ids)
		return ids
	}
	t.Fatalf("repository %q not found in %#v", repositoryID, snapshot.Repositories)
	return nil
}

type recordingFilteredReadBackend struct {
	tasks   []task.Task
	filters []task.ListFilter
	getErr  map[string]error
}

func (b *recordingFilteredReadBackend) Get(_ context.Context, id string) (task.Task, error) {
	if err := b.getErr[id]; err != nil {
		return task.Task{}, err
	}
	for _, candidate := range b.tasks {
		if candidate.ID == id {
			return candidate.Clone(), nil
		}
	}
	return task.Task{}, task.ErrNotFound
}

func (b *recordingFilteredReadBackend) List(context.Context) ([]task.Task, error) {
	return cloneTasks(b.tasks), nil
}

func (b *recordingFilteredReadBackend) ListFiltered(_ context.Context, filter task.ListFilter) ([]task.Task, error) {
	b.filters = append(b.filters, filter)
	filtered := make([]task.Task, 0, len(b.tasks))
	for _, candidate := range b.tasks {
		if filter.Matches(candidate) {
			filtered = append(filtered, candidate.Clone())
		}
	}
	return filtered, nil
}

var _ task.ReadBackend = (*recordingFilteredReadBackend)(nil)
var _ task.FilteredLister = (*recordingFilteredReadBackend)(nil)

type failingReadBackend struct {
	err error
}

func (b failingReadBackend) Get(context.Context, string) (task.Task, error) {
	return task.Task{}, b.err
}

func (b failingReadBackend) List(context.Context) ([]task.Task, error) {
	return nil, b.err
}

func TestAggregatorSnapshotOverlapsDistinctWorkspacesAndPreservesOrder(t *testing.T) {
	repos := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha"}, BackendDir: t.TempDir()},
		{Repository: task.Repository{ID: "beta"}, BackendDir: t.TempDir()},
		{Repository: task.Repository{ID: "gamma"}, BackendDir: t.TempDir()},
	}
	started := make(chan string, 2)
	release := make(chan struct{})

	aggregator, err := task.NewAggregator(repos, func(source task.RepositorySource) (task.ReadBackend, error) {
		return signalReadBackend{
			id:      source.Repository.ID,
			started: started,
			release: release,
		}, nil
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	result := make(chan task.SnapshotResult, 1)
	go func() {
		result <- aggregator.Snapshot(context.Background())
	}()

	first, second := <-started, <-started
	if first == second {
		t.Fatalf("concurrent starts = %q, %q; want distinct workspaces", first, second)
	}
	close(release)
	got := <-result

	if got.HasFailures() {
		t.Fatalf("failures = %#v, want none", got.Failures)
	}
	gotIDs := make([]string, 0, len(got.Repositories))
	for _, repository := range got.Repositories {
		gotIDs = append(gotIDs, repository.Repository.ID)
	}
	wantIDs := []string{"alpha", "beta", "gamma"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("repository order = %v, want %v", gotIDs, wantIDs)
	}
}

func TestAggregatorSnapshotPreservesFailureOrderAcrossConcurrentReads(t *testing.T) {
	createErr := errors.New("create alpha backend")
	listErr := errors.New("list beta backend")
	repos := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha"}, BackendDir: t.TempDir()},
		{Repository: task.Repository{ID: "beta"}, BackendDir: t.TempDir()},
		{Repository: task.Repository{ID: "gamma"}, BackendDir: t.TempDir()},
	}

	aggregator, err := task.NewAggregator(repos, func(source task.RepositorySource) (task.ReadBackend, error) {
		switch source.Repository.ID {
		case "alpha":
			return nil, createErr
		case "beta":
			return failingReadBackend{err: listErr}, nil
		default:
			return fakeReadBackend{tasks: []task.Task{{ID: "gamma-1", IssueType: task.IssueTypeTask}}}, nil
		}
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	got := aggregator.Snapshot(context.Background())

	if len(got.Failures) != 2 {
		t.Fatalf("failures = %#v, want two", got.Failures)
	}
	if failure := got.Failures[0]; failure.Repository.ID != "alpha" || failure.Operation != "create_backend" || !errors.Is(failure.Err, createErr) {
		t.Fatalf("first failure = %#v, want alpha backend creation error", failure)
	}
	if failure := got.Failures[1]; failure.Repository.ID != "beta" || failure.Operation != "snapshot" || !errors.Is(failure.Err, listErr) {
		t.Fatalf("second failure = %#v, want beta snapshot error", failure)
	}
	if len(got.Repositories) != 1 || got.Repositories[0].Repository.ID != "gamma" || got.Repositories[0].Tasks[0].ID != "gamma-1" {
		t.Fatalf("repositories = %#v, want successful gamma snapshot", got.Repositories)
	}
}

func TestAggregatorSnapshotBoundsDistinctWorkspaceReads(t *testing.T) {
	repos := make([]task.RepositorySource, 5)
	for i := range repos {
		repos[i] = task.RepositorySource{
			Repository: task.Repository{ID: fmt.Sprintf("repo-%d", i)},
			BackendDir: t.TempDir(),
		}
	}
	tracker := &boundedReadTracker{
		started: make(chan struct{}, 4),
		release: make(chan struct{}),
	}
	aggregator, err := task.NewAggregator(repos, func(source task.RepositorySource) (task.ReadBackend, error) {
		return trackedReadBackend{id: source.Repository.ID, tracker: &tracker.concurrentReadTracker, started: tracker.started, release: tracker.release}, nil
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	result := make(chan task.SnapshotResult, 1)
	go func() {
		result <- aggregator.Snapshot(context.Background())
	}()
	for range cap(tracker.started) {
		<-tracker.started
	}
	close(tracker.release)
	got := <-result

	if got.HasFailures() {
		t.Fatalf("failures = %#v, want none", got.Failures)
	}
	if maximum := tracker.max.Load(); maximum != 4 {
		t.Fatalf("maximum simultaneous reads = %d, want bounded maximum of 4", maximum)
	}
}

func TestAggregatorSnapshotSerializesNormalizedDuplicateWorkspaces(t *testing.T) {
	workspace := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace-alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatalf("create workspace symlink: %v", err)
	}
	repos := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha"}, BackendDir: workspace},
		{Repository: task.Repository{ID: "beta"}, BackendDir: alias},
	}
	tracker := &boundedReadTracker{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}

	aggregator, err := task.NewAggregator(repos, func(source task.RepositorySource) (task.ReadBackend, error) {
		return trackedReadBackend{
			id:      source.Repository.ID,
			tracker: &tracker.concurrentReadTracker,
			started: tracker.started,
			release: tracker.release,
		}, nil
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	result := make(chan task.SnapshotResult, 1)
	go func() {
		result <- aggregator.Snapshot(context.Background())
	}()
	<-tracker.started
	close(tracker.release)
	got := <-result

	if got.HasFailures() {
		t.Fatalf("failures = %#v, want none", got.Failures)
	}
	if maximum := tracker.max.Load(); maximum != 1 {
		t.Fatalf("maximum simultaneous reads = %d, want 1 for one normalized workspace", maximum)
	}
}

func TestAggregatorSnapshotCancellationWaitsForWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{}, 4)
	tracker := &cancellationReadTracker{started: started}
	repos := make([]task.RepositorySource, 6)
	for i := range repos {
		repos[i] = task.RepositorySource{
			Repository: task.Repository{ID: fmt.Sprintf("repo-%d", i)},
			BackendDir: t.TempDir(),
		}
	}
	aggregator, err := task.NewAggregator(repos, func(task.RepositorySource) (task.ReadBackend, error) {
		return tracker, nil
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	result := make(chan task.SnapshotResult, 1)
	go func() {
		result <- aggregator.Snapshot(ctx)
	}()
	for range cap(started) {
		<-started
	}
	cancel()
	got := <-result

	if active := tracker.active.Load(); active != 0 {
		t.Fatalf("active reads after Snapshot returns = %d, want no workers", active)
	}
	if calls := tracker.calls.Load(); calls != int32(len(repos)) {
		t.Fatalf("List calls = %d, want %d to preserve partial-result behavior", calls, len(repos))
	}
	if len(got.Failures) != len(repos) {
		t.Fatalf("failures = %#v, want one canceled snapshot failure per repository", got.Failures)
	}
	for i, failure := range got.Failures {
		if failure.Repository.ID != repos[i].Repository.ID || failure.Operation != "snapshot" || !errors.Is(failure.Err, context.Canceled) {
			t.Fatalf("failure[%d] = %#v, want canceled snapshot failure for %q", i, failure, repos[i].Repository.ID)
		}
	}
}

type signalReadBackend struct {
	id      string
	started chan<- string
	release <-chan struct{}
}

func (b signalReadBackend) Get(context.Context, string) (task.Task, error) {
	return task.Task{}, task.ErrNotFound
}

func (b signalReadBackend) List(context.Context) ([]task.Task, error) {
	b.started <- b.id
	<-b.release
	return []task.Task{{ID: b.id + "-1", IssueType: task.IssueTypeTask}}, nil
}

type concurrentReadTracker struct {
	active atomic.Int32
	max    atomic.Int32
}

type boundedReadTracker struct {
	concurrentReadTracker
	started chan struct{}
	release chan struct{}
}

type trackedReadBackend struct {
	id      string
	tracker *concurrentReadTracker
	started chan<- struct{}
	release <-chan struct{}
}

func (b trackedReadBackend) Get(context.Context, string) (task.Task, error) {
	return task.Task{}, task.ErrNotFound
}

func (b trackedReadBackend) List(context.Context) ([]task.Task, error) {
	active := b.tracker.active.Add(1)
	defer b.tracker.active.Add(-1)
	for {
		maximum := b.tracker.max.Load()
		if active <= maximum || b.tracker.max.CompareAndSwap(maximum, active) {
			break
		}
	}
	if b.started != nil {
		b.started <- struct{}{}
	}
	if b.release != nil {
		<-b.release
	}
	return []task.Task{{ID: b.id + "-1", IssueType: task.IssueTypeTask}}, nil
}

type cancellationReadTracker struct {
	started chan<- struct{}
	active  atomic.Int32
	calls   atomic.Int32
}

func (b *cancellationReadTracker) Get(context.Context, string) (task.Task, error) {
	return task.Task{}, task.ErrNotFound
}

func (b *cancellationReadTracker) List(ctx context.Context) ([]task.Task, error) {
	b.calls.Add(1)
	b.active.Add(1)
	defer b.active.Add(-1)
	select {
	case b.started <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

var _ task.ReadBackend = signalReadBackend{}
var _ task.ReadBackend = trackedReadBackend{}
var _ task.ReadBackend = (*cancellationReadTracker)(nil)
