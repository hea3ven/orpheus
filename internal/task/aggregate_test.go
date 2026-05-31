package task_test

import (
	"context"
	"errors"
	"reflect"
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

func TestAggregatorListFiltersToActiveItemsAcrossIssueTypes(t *testing.T) {
	repos := []task.RepositorySource{{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"}, BackendDir: "/tmp/alpha"}}
	backend := fakeReadBackend{tasks: []task.Task{
		{ID: "a-1", Title: "active task", IssueType: task.IssueTypeTask, Status: task.StatusOpen},
		{ID: "a-2", Title: "closed task", IssueType: task.IssueTypeTask, Status: task.StatusClosed},
		{ID: "a-3", Title: "bug", IssueType: task.IssueTypeBug, Status: task.StatusOpen},
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
	expectedIDs := []string{"a-1", "a-3", "a-4"}
	if !reflect.DeepEqual(gotIDs, expectedIDs) {
		t.Fatalf("rows = %#v, want active items %v", got.Rows, expectedIDs)
	}
}

func TestAggregatorSnapshotPreservesAllVisibleBackendItems(t *testing.T) {
	repos := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"}, BackendDir: "/tmp/alpha"},
		{Repository: task.Repository{ID: "beta", Name: "Beta", TaskIDPrefix: "b"}, BackendDir: "/tmp/beta"},
	}
	backends := map[string]fakeReadBackend{
		"/tmp/alpha": {tasks: []task.Task{
			{ID: "a-1", Title: "alpha active", IssueType: task.IssueTypeTask, Status: task.StatusOpen},
			{ID: "a-closed", Title: "alpha closed", IssueType: task.IssueTypeTask, Status: task.StatusClosed},
			{ID: "a-bug", Title: "alpha bug", IssueType: task.IssueTypeBug, Status: task.StatusOpen},
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
	if len(got.Repositories[0].Tasks) != 3 {
		t.Fatalf("alpha tasks = %#v, want active, closed, and bug items", got.Repositories[0].Tasks)
	}
	if got.Repositories[1].Tasks[0].ID != "b-epic" {
		t.Fatalf("beta tasks = %#v, want epic preserved", got.Repositories[1].Tasks)
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

type failingReadBackend struct {
	err error
}

func (b failingReadBackend) Get(context.Context, string) (task.Task, error) {
	return task.Task{}, b.err
}

func (b failingReadBackend) List(context.Context) ([]task.Task, error) {
	return nil, b.err
}
