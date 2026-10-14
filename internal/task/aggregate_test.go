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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestAggregatorListQueriesReposAndPreservesContext(t *testing.T) {
	repos := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"}, BackendDir: "/fixture/alpha"},
		{Repository: task.Repository{ID: "beta", Name: "Beta", TaskIDPrefix: "b"}, BackendDir: "/fixture/beta"},
	}
	backends := map[string]fakeReadBackend{
		"/fixture/alpha": {tasks: []task.Task{{ID: "a-1", Title: "alpha task", IssueType: task.IssueTypeTask, Status: task.StatusOpen}, {ID: "a-closed", IssueType: task.IssueTypeTask, Status: task.StatusClosed}, {ID: "a-epic", IssueType: task.IssueTypeEpic, Status: task.StatusInProgress}}},
		"/fixture/beta":  {tasks: []task.Task{{ID: "b-1", Title: "beta task", IssueType: task.IssueTypeTask, Status: task.StatusInProgress}}},
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
	if len(got.Rows) != 3 {
		t.Fatalf("rows = %#v, want active task and epic rows", got.Rows)
	}
	if got.Rows[0].Repository.ID != "alpha" || got.Rows[0].Repository.TaskIDPrefix != "a" || got.Rows[0].Task.ID != "a-1" {
		t.Fatalf("first row = %#v, want alpha/a/a-1", got.Rows[0])
	}
	if !reflect.DeepEqual(repoTaskIDs(got.Rows), []string{"a-1", "a-epic", "b-1"}) {
		t.Fatalf("selected rows = %#v", got.Rows)
	}
	if got.Rows[2].Repository.ID != "beta" || got.Rows[2].Repository.TaskIDPrefix != "b" || got.Rows[2].Task.ID != "b-1" {
		t.Fatalf("last row = %#v, want beta/b/b-1", got.Rows[2])
	}
}

func TestAggregatorSnapshotPreservesTaskSourceItems(t *testing.T) {
	repos := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"}, BackendDir: "/fixture/alpha"},
		{Repository: task.Repository{ID: "beta", Name: "Beta", TaskIDPrefix: "b"}, BackendDir: "/fixture/beta"},
	}
	backends := map[string]fakeReadBackend{
		"/fixture/alpha": {tasks: []task.Task{
			{ID: "a-1", Title: "alpha active", IssueType: task.IssueTypeTask, Status: task.StatusOpen},
			{ID: "a-closed", Title: "alpha closed", IssueType: task.IssueTypeTask, Status: task.StatusClosed},
		}},
		"/fixture/beta": {tasks: []task.Task{{ID: "b-epic", Title: "beta epic", IssueType: task.IssueTypeEpic, Status: task.StatusOpen}}},
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
	aggregator, err := task.NewAggregator([]task.RepositorySource{{Repository: repo, BackendDir: testutil.CanonicalTempDir(t)}}, func(task.RepositorySource) (task.ReadBackend, error) {
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
	aggregator, err := task.NewAggregator([]task.RepositorySource{{Repository: repo, BackendDir: testutil.CanonicalTempDir(t)}}, func(task.RepositorySource) (task.ReadBackend, error) {
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

func TestAggregatorFilteredSnapshotDistinguishesRelationshipQueryFailure(t *testing.T) {
	repo := task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"}
	backend := &recordingFilteredReadBackend{
		tasks:  []task.Task{{ID: "a-child", Title: "matching", IssueType: task.IssueTypeTask, Relations: task.RelationSummary{DependencyIDs: []string{"a-dependency"}}}},
		getErr: map[string]error{"a-dependency": errors.New("backend unavailable")},
	}
	aggregator, err := task.NewAggregator([]task.RepositorySource{{Repository: repo, BackendDir: testutil.CanonicalTempDir(t)}}, func(task.RepositorySource) (task.ReadBackend, error) {
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
	aggregator, err := task.NewAggregator([]task.RepositorySource{{Repository: repo, BackendDir: testutil.CanonicalTempDir(t)}}, func(task.RepositorySource) (task.ReadBackend, error) {
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
	gets    []string
}

func (b *recordingFilteredReadBackend) Get(_ context.Context, id string) (task.Task, error) {
	b.gets = append(b.gets, id)
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
		{Repository: task.Repository{ID: "alpha"}, BackendDir: testutil.CanonicalTempDir(t)},
		{Repository: task.Repository{ID: "beta"}, BackendDir: testutil.CanonicalTempDir(t)},
		{Repository: task.Repository{ID: "gamma"}, BackendDir: testutil.CanonicalTempDir(t)},
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
		{Repository: task.Repository{ID: "alpha"}, BackendDir: testutil.CanonicalTempDir(t)},
		{Repository: task.Repository{ID: "beta"}, BackendDir: testutil.CanonicalTempDir(t)},
		{Repository: task.Repository{ID: "gamma"}, BackendDir: testutil.CanonicalTempDir(t)},
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
	if failure := got.Failures[0]; failure.Repository.ID != "alpha" || failure.Source != "task_backend" || failure.Operation != "create_backend" || !errors.Is(failure.Err, createErr) {
		t.Fatalf("first failure = %#v, want alpha backend creation error", failure)
	}
	if failure := got.Failures[1]; failure.Repository.ID != "beta" || failure.Source != "task_backend" || failure.Operation != "snapshot" || !errors.Is(failure.Err, listErr) {
		t.Fatalf("second failure = %#v, want beta snapshot error", failure)
	}
	if len(got.Repositories) != 1 || got.Repositories[0].Repository.ID != "gamma" || got.Repositories[0].Tasks[0].ID != "gamma-1" {
		t.Fatalf("repositories = %#v, want successful gamma snapshot", got.Repositories)
	}
}

func TestAggregatorSnapshotBoundsDistinctWorkspaceReads(t *testing.T) {
	repos := make([]task.RepositorySource, 32)
	for i := range repos {
		repos[i] = task.RepositorySource{Repository: task.Repository{ID: fmt.Sprintf("repo-%d", i)}, BackendDir: testutil.CanonicalTempDir(t)}
	}
	tracker := &boundedReadTracker{started: make(chan struct{}, len(repos)), release: make(chan struct{})}
	var release sync.Once
	unblock := func() { release.Do(func() { close(tracker.release) }) }
	t.Cleanup(unblock)
	aggregator, err := task.NewAggregator(repos, func(source task.RepositorySource) (task.ReadBackend, error) {
		return trackedReadBackend{id: source.Repository.ID, tracker: &tracker.concurrentReadTracker, started: tracker.started, release: tracker.release}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := make(chan task.SnapshotResult, 1)
	go func() { result <- aggregator.Snapshot(ctx) }()

	// Keep every entered read blocked until the pool is saturated.
	started := 0
	for started < 2 {
		select {
		case <-tracker.started:
			started++
		case <-ctx.Done():
			t.Fatal("distinct workspace reads did not overlap before deadline")
		}
	}
	idle := time.NewTimer(100 * time.Millisecond)
	defer idle.Stop()
waiting:
	for {
		select {
		case <-tracker.started:
			started++
			idle.Reset(100 * time.Millisecond)
		case <-idle.C:
			break waiting
		case <-ctx.Done():
			t.Fatal("snapshot did not reach a bounded pool before deadline")
		}
	}
	if started < 2 || started >= len(repos) {
		t.Fatalf("overlapping reads = %d for %d workspaces, want bounded parallelism", started, len(repos))
	}
	unblock()
	select {
	case got := <-result:
		if got.HasFailures() || len(got.Repositories) != len(repos) {
			t.Fatalf("snapshot = %#v", got)
		}
	case <-ctx.Done():
		t.Fatal("snapshot workers did not finish")
	}
}

func TestAggregatorSnapshotSerializesNormalizedDuplicateWorkspaces(t *testing.T) {
	workspace := testutil.CanonicalTempDir(t)
	alias := filepath.Join(testutil.CanonicalTempDir(t), "workspace-alias")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Fatal(err)
	}
	repos := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha"}, BackendDir: workspace},
		{Repository: task.Repository{ID: "beta"}, BackendDir: alias},
		{Repository: task.Repository{ID: "independent"}, BackendDir: testutil.CanonicalTempDir(t)},
	}
	tracker := &boundedReadTracker{started: make(chan struct{}, 2), release: make(chan struct{})}
	independent := make(chan struct{}, 1)
	var release sync.Once
	unblock := func() { release.Do(func() { close(tracker.release) }) }
	t.Cleanup(unblock)
	aggregator, err := task.NewAggregator(repos, func(source task.RepositorySource) (task.ReadBackend, error) {
		if source.Repository.ID == "independent" {
			return trackedReadBackend{id: source.Repository.ID, tracker: &concurrentReadTracker{}, started: independent}, nil
		}
		return trackedReadBackend{id: source.Repository.ID, tracker: &tracker.concurrentReadTracker, started: tracker.started, release: tracker.release}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	result := make(chan task.SnapshotResult, 1)
	go func() { result <- aggregator.Snapshot(ctx) }()

	for _, signal := range []<-chan struct{}{tracker.started, independent} {
		select {
		case <-signal:
		case <-ctx.Done():
			t.Fatal("readers did not start before deadline")
		}
	}
	select {
	case <-tracker.started:
		t.Error("both aliases entered the same workspace concurrently")
	case <-time.After(100 * time.Millisecond):
	case <-ctx.Done():
		t.Fatal("workspace serialization deadline exceeded")
	}
	unblock()
	select {
	case got := <-result:
		if got.HasFailures() || len(got.Repositories) != 3 {
			t.Fatalf("snapshot = %#v", got)
		}
	case <-ctx.Done():
		t.Fatal("serialized readers did not finish")
	}
	if maximum := tracker.max.Load(); maximum != 1 {
		t.Fatalf("maximum simultaneous alias reads = %d, want 1", maximum)
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
			BackendDir: testutil.CanonicalTempDir(t),
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

func (b trackedReadBackend) List(ctx context.Context) ([]task.Task, error) {
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
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
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

func TestAggregatorFilteredSnapshotReusesCompleteRelationshipsAndSuppliedRows(t *testing.T) {
	child := task.Task{ID: "a-child", IssueType: task.IssueTypeTask, Status: task.StatusClosed, Relations: task.RelationSummary{ParentID: "a-epic"}}
	parent := task.Task{ID: "a-parent", IssueType: task.IssueTypeEpic, Status: task.StatusInProgress}
	backend := &recordingFilteredReadBackend{tasks: []task.Task{
		{ID: "a-epic", Title: "matching epic", IssueType: task.IssueTypeEpic, Relations: task.RelationSummary{Complete: true, ParentID: parent.ID, ChildIDs: []string{child.ID}, DependencyIDs: []string{"a-blocker"}}, RelatedItems: []task.Task{child, parent}},
		{ID: "a-blocker", IssueType: task.IssueTypeTask, Status: task.StatusClosed},
	}}
	aggregator, err := task.NewAggregator([]task.RepositorySource{{Repository: task.Repository{ID: "alpha", TaskIDPrefix: "a"}, BackendDir: "/fixture/alpha"}}, func(task.RepositorySource) (task.ReadBackend, error) { return backend, nil })
	if err != nil {
		t.Fatal(err)
	}

	got, err := aggregator.FilteredSnapshot(context.Background(), task.ListFilter{Query: "matching"})

	if err != nil || got.Snapshot.HasFailures() {
		t.Fatalf("FilteredSnapshot() = %#v, %v", got, err)
	}
	if ids := snapshotTaskIDsForRepository(t, got.Snapshot, "alpha"); !reflect.DeepEqual(ids, []string{"a-blocker", "a-child", "a-epic", "a-parent"}) {
		t.Fatalf("snapshot IDs = %v", ids)
	}
	if ids := repoTaskIDs(got.Candidates); !reflect.DeepEqual(ids, []string{"a-epic"}) {
		t.Fatalf("candidate IDs = %v", ids)
	}
	if !reflect.DeepEqual(backend.gets, []string{"a-blocker"}) {
		t.Fatalf("Get calls = %v, want only missing blocker state", backend.gets)
	}
	if len(backend.filters) != 1 {
		t.Fatalf("filters = %#v, want no child query", backend.filters)
	}
}

func TestAggregatorFilteredSnapshotReusesRowsSuppliedByContextRead(t *testing.T) {
	backend := &recordingFilteredReadBackend{tasks: []task.Task{
		{ID: "a-task", Title: "matching task", IssueType: task.IssueTypeTask, Relations: task.RelationSummary{DependencyIDs: []string{"a-first", "a-second"}}},
		{ID: "a-first", IssueType: task.IssueTypeTask, RelatedItems: []task.Task{{ID: "a-second", IssueType: task.IssueTypeTask, Status: task.StatusClosed}}},
	}}
	aggregator, err := task.NewAggregator([]task.RepositorySource{{Repository: task.Repository{ID: "alpha", TaskIDPrefix: "a"}, BackendDir: "/fixture/alpha"}}, func(task.RepositorySource) (task.ReadBackend, error) { return backend, nil })
	if err != nil {
		t.Fatal(err)
	}

	got, err := aggregator.FilteredSnapshot(context.Background(), task.ListFilter{Query: "matching"})

	if err != nil || got.Snapshot.HasFailures() {
		t.Fatalf("FilteredSnapshot() = %#v, %v", got, err)
	}
	if ids := snapshotTaskIDsForRepository(t, got.Snapshot, "alpha"); !reflect.DeepEqual(ids, []string{"a-first", "a-second", "a-task"}) {
		t.Fatalf("snapshot IDs = %v", ids)
	}
	if !reflect.DeepEqual(backend.gets, []string{"a-first"}) {
		t.Fatalf("Get calls = %v, want only first dependency", backend.gets)
	}
}
