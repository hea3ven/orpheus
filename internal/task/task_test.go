package task_test

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/task"
)

type fakeReadBackend struct {
	tasks []task.Task
	ready []task.Task
}

func (b fakeReadBackend) Get(_ context.Context, id string) (task.Task, error) {
	for _, candidate := range b.tasks {
		if candidate.ID == id {
			return candidate.Clone(), nil
		}
	}
	return task.Task{}, task.ErrNotFound
}

func (b fakeReadBackend) List(context.Context) ([]task.Task, error) {
	return cloneTasks(b.tasks), nil
}

func (b fakeReadBackend) Ready(context.Context) ([]task.Task, error) {
	return cloneTasks(b.ready), nil
}

var _ task.ReadBackend = fakeReadBackend{}

func TestReadBackendContractIsReadOnly(t *testing.T) {
	backendType := reflect.TypeOf((*task.ReadBackend)(nil)).Elem()

	got := make([]string, 0, backendType.NumMethod())
	for i := range backendType.NumMethod() {
		got = append(got, backendType.Method(i).Name)
	}
	sort.Strings(got)

	expected := []string{"Get", "List", "Ready"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("ReadBackend methods = %v, want only %v", got, expected)
	}
}

func TestReadBackendCanGetListAndReady(t *testing.T) {
	backend := fakeReadBackend{
		tasks: []task.Task{
			{ID: "op-1", Title: "first", IssueType: task.IssueTypeTask, Status: task.StatusOpen},
			{ID: "op-2", Title: "second", IssueType: task.IssueTypeTask, Status: task.StatusInProgress},
		},
		ready: []task.Task{
			{ID: "op-1", Title: "first", IssueType: task.IssueTypeTask, Status: task.StatusOpen},
		},
	}

	got, err := backend.Get(context.Background(), "op-2")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.ID != "op-2" || got.Title != "second" {
		t.Fatalf("task = %#v, want op-2 second", got)
	}

	listed, err := backend.List(context.Background())
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("list returned %d tasks, want 2", len(listed))
	}

	ready, err := backend.Ready(context.Background())
	if err != nil {
		t.Fatalf("ready tasks: %v", err)
	}
	if len(ready) != 1 || ready[0].ID != "op-1" {
		t.Fatalf("ready tasks = %#v, want only op-1", ready)
	}
}

func TestTaskCloneCopiesMutableFields(t *testing.T) {
	createdAt := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 5, 24, 11, 0, 0, 0, time.UTC)
	original := task.Task{
		ID:        "op-1",
		Title:     "implement task model",
		Labels:    []string{"m2", "task"},
		Metadata:  task.Metadata{task.MetadataBranch: "task/op-1-model"},
		CreatedAt: &createdAt,
		UpdatedAt: &updatedAt,
		Relations: task.RelationSummary{
			ParentID:        "op",
			DependencyIDs:   []string{"op-0"},
			DependentIDs:    []string{"op-2"},
			DependencyCount: 1,
			DependentCount:  1,
		},
	}

	clone := original.Clone()
	clone.Labels[0] = "changed"
	clone.Metadata[task.MetadataBranch] = "changed"
	clone.CreatedAt = ptrTime(clone.CreatedAt.Add(time.Hour))
	clone.Relations.DependencyIDs[0] = "changed"
	clone.Relations.DependentIDs[0] = "changed"

	if original.Labels[0] != "m2" {
		t.Fatalf("original label changed to %q", original.Labels[0])
	}
	if original.Metadata[task.MetadataBranch] != "task/op-1-model" {
		t.Fatalf("original metadata changed to %q", original.Metadata[task.MetadataBranch])
	}
	if !original.CreatedAt.Equal(createdAt) {
		t.Fatalf("original created_at changed to %v", original.CreatedAt)
	}
	if original.Relations.DependencyIDs[0] != "op-0" {
		t.Fatalf("original dependency id changed to %q", original.Relations.DependencyIDs[0])
	}
	if original.Relations.DependentIDs[0] != "op-2" {
		t.Fatalf("original dependent id changed to %q", original.Relations.DependentIDs[0])
	}
}

func TestTaskOrpheusMetadataProjectsKnownKeys(t *testing.T) {
	taskItem := task.Task{Metadata: task.Metadata{
		task.MetadataBranch:   "task/op-1-model",
		task.MetadataWorktree: "/tmp/orpheus/op-1",
		task.MetadataPRURL:    "https://github.com/example/repo/pull/1",
		"backend.custom":      "preserved but not projected",
	}}

	got := taskItem.OrpheusMetadata()
	if got.Branch != "task/op-1-model" {
		t.Fatalf("branch = %q, want task/op-1-model", got.Branch)
	}
	if got.Worktree != "/tmp/orpheus/op-1" {
		t.Fatalf("worktree = %q, want /tmp/orpheus/op-1", got.Worktree)
	}
	if got.PRURL != "https://github.com/example/repo/pull/1" {
		t.Fatalf("pr url = %q, want GitHub PR URL", got.PRURL)
	}

	value, ok := taskItem.Metadata.Value("backend.custom")
	if !ok || value != "preserved but not projected" {
		t.Fatalf("custom metadata lookup = %q, %v", value, ok)
	}
}

func TestQueryResultRepresentsRowsAndRepoFailures(t *testing.T) {
	queryErr := errors.New("bd list failed")
	result := task.QueryResult{
		Rows: []task.RepoTask{{
			Repository: task.Repository{ID: "repo-a", Name: "Repo A", TaskIDPrefix: "ra"},
			Task: task.Task{
				ID:       "ra-1",
				Title:    "ready task",
				Labels:   []string{"m2"},
				Metadata: task.Metadata{task.MetadataWorktree: "/tmp/worktree"},
			},
		}},
		Failures: []task.RepoFailure{{
			Repository: task.Repository{ID: "repo-b", Name: "Repo B", TaskIDPrefix: "rb"},
			Err:        queryErr,
		}},
	}

	if !result.HasFailures() {
		t.Fatal("HasFailures returned false, want true")
	}
	if result.Rows[0].Repository.ID != "repo-a" || result.Rows[0].Task.ID != "ra-1" {
		t.Fatalf("row = %#v, want repo-a task ra-1", result.Rows[0])
	}
	if result.Failures[0].Repository.ID != "repo-b" || !errors.Is(result.Failures[0].Err, queryErr) {
		t.Fatalf("failure = %#v, want repo-b query error", result.Failures[0])
	}

	clone := result.Clone()
	clone.Rows[0].Task.Labels[0] = "changed"
	clone.Rows[0].Task.Metadata[task.MetadataWorktree] = "changed"

	if result.Rows[0].Task.Labels[0] != "m2" {
		t.Fatalf("original row label changed to %q", result.Rows[0].Task.Labels[0])
	}
	if result.Rows[0].Task.Metadata[task.MetadataWorktree] != "/tmp/worktree" {
		t.Fatalf("original row metadata changed to %q", result.Rows[0].Task.Metadata[task.MetadataWorktree])
	}
}

func cloneTasks(tasks []task.Task) []task.Task {
	clone := make([]task.Task, len(tasks))
	for i, taskItem := range tasks {
		clone[i] = taskItem.Clone()
	}
	return clone
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
