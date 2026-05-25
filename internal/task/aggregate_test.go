package task_test

import (
	"context"
	"errors"
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

func TestAggregatorReadyQueriesReposAndPreservesContext(t *testing.T) {
	repos := []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"}, BackendDir: "/tmp/alpha"},
		{Repository: task.Repository{ID: "beta", Name: "Beta", TaskIDPrefix: "b"}, BackendDir: "/tmp/beta"},
	}
	backends := map[string]fakeReadBackend{
		"/tmp/alpha": {ready: []task.Task{{ID: "a-1", Title: "alpha ready", IssueType: task.IssueTypeTask, Status: task.StatusOpen}}},
		"/tmp/beta":  {ready: []task.Task{{ID: "b-1", Title: "beta ready", IssueType: task.IssueTypeTask, Status: task.StatusInProgress}}},
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

	got := aggregator.Ready(context.Background())

	if got.HasFailures() {
		t.Fatalf("failures = %#v, want none", got.Failures)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("rows = %#v, want two ready rows", got.Rows)
	}
	if got.Rows[0].Repository.ID != "alpha" || got.Rows[0].Repository.TaskIDPrefix != "a" || got.Rows[0].Task.ID != "a-1" {
		t.Fatalf("first row = %#v, want alpha/a/a-1", got.Rows[0])
	}
	if got.Rows[1].Repository.ID != "beta" || got.Rows[1].Repository.TaskIDPrefix != "b" || got.Rows[1].Task.ID != "b-1" {
		t.Fatalf("second row = %#v, want beta/b/b-1", got.Rows[1])
	}
}

func TestAggregatorFiltersToActiveTaskItems(t *testing.T) {
	repos := []task.RepositorySource{{Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"}, BackendDir: "/tmp/alpha"}}
	backend := fakeReadBackend{ready: []task.Task{
		{ID: "a-1", Title: "ready task", IssueType: task.IssueTypeTask, Status: task.StatusOpen},
		{ID: "a-2", Title: "closed task", IssueType: task.IssueTypeTask, Status: task.StatusClosed},
		{ID: "a-3", Title: "ready bug", IssueType: task.IssueTypeBug, Status: task.StatusOpen},
	}}

	aggregator, err := task.NewAggregator(repos, func(task.RepositorySource) (task.ReadBackend, error) {
		return backend, nil
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	got := aggregator.Ready(context.Background())

	if len(got.Rows) != 1 || got.Rows[0].Task.ID != "a-1" {
		t.Fatalf("rows = %#v, want only active issue_type=task item a-1", got.Rows)
	}
}

func TestAggregatorContinuesAfterRepoFailure(t *testing.T) {
	queryErr := errors.New("bd ready failed")
	repos := []task.RepositorySource{
		{Repository: task.Repository{ID: "broken", Name: "Broken", TaskIDPrefix: "br"}, BackendDir: "/tmp/broken"},
		{Repository: task.Repository{ID: "ok", Name: "OK", TaskIDPrefix: "ok"}, BackendDir: "/tmp/ok"},
	}

	aggregator, err := task.NewAggregator(repos, func(source task.RepositorySource) (task.ReadBackend, error) {
		if source.Repository.ID == "broken" {
			return failingReadBackend{err: queryErr}, nil
		}
		return fakeReadBackend{ready: []task.Task{{ID: "ok-1", Title: "still listed", IssueType: task.IssueTypeTask, Status: task.StatusOpen}}}, nil
	})
	if err != nil {
		t.Fatalf("create aggregator: %v", err)
	}

	got := aggregator.Ready(context.Background())

	if !got.HasFailures() || len(got.Failures) != 1 {
		t.Fatalf("failures = %#v, want one failure", got.Failures)
	}
	if got.Failures[0].Repository.ID != "broken" || !errors.Is(got.Failures[0].Err, queryErr) {
		t.Fatalf("failure = %#v, want broken query error", got.Failures[0])
	}
	if len(got.Rows) != 1 || got.Rows[0].Task.ID != "ok-1" {
		t.Fatalf("rows = %#v, want successful ready row", got.Rows)
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

	got := aggregator.Ready(context.Background())

	if len(got.Rows) != 0 {
		t.Fatalf("rows = %#v, want none", got.Rows)
	}
	if len(got.Failures) != 1 || got.Failures[0].Repository.ID != "broken" || !errors.Is(got.Failures[0].Err, factoryErr) {
		t.Fatalf("failures = %#v, want backend creation failure", got.Failures)
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

func (b failingReadBackend) Ready(context.Context) ([]task.Task, error) {
	return nil, b.err
}
