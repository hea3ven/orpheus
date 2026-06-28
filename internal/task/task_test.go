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

var _ task.ReadBackend = fakeReadBackend{}

func TestReadBackendContractIsReadOnly(t *testing.T) {
	backendType := reflect.TypeOf((*task.ReadBackend)(nil)).Elem()

	got := make([]string, 0, backendType.NumMethod())
	for i := range backendType.NumMethod() {
		got = append(got, backendType.Method(i).Name)
	}
	sort.Strings(got)

	expected := []string{"Get", "List"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("ReadBackend methods = %v, want only %v", got, expected)
	}
}

func TestReadBackendCanGetAndList(t *testing.T) {
	backend := fakeReadBackend{
		tasks: []task.Task{
			{ID: "op-1", Title: "first", IssueType: task.IssueTypeTask, Status: task.StatusOpen},
			{ID: "op-2", Title: "second", IssueType: task.IssueTypeTask, Status: task.StatusInProgress},
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

func TestTaskSessionName(t *testing.T) {
	tests := []struct {
		name string
		task task.Task
		want string
	}{
		{
			name: "with title",
			task: task.Task{ID: "op-1", Title: "Implement attached run"},
			want: "(op-1) Implement attached run",
		},
		{
			name: "without title",
			task: task.Task{ID: "op-2"},
			want: "(op-2)",
		},
		{
			name: "blank title",
			task: task.Task{ID: "op-3", Title: "  "},
			want: "(op-3)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.task.SessionName()

			if got != tt.want {
				t.Fatalf("SessionName() = %q, want %q", got, tt.want)
			}
		})
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
	expected := task.OrpheusMetadata{
		Branch:      "task/op-1-model",
		HasBranch:   true,
		Worktree:    "/tmp/orpheus/op-1",
		HasWorktree: true,
		PRURL:       "https://github.com/example/repo/pull/1",
		HasPRURL:    true,
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("orpheus metadata = %#v, want %#v", got, expected)
	}

	value, ok := taskItem.Metadata.Value("backend.custom")
	if !ok || value != "preserved but not projected" {
		t.Fatalf("custom metadata lookup = %q, %v", value, ok)
	}
}

func TestTaskOrpheusMetadataRepresentsAbsentFields(t *testing.T) {
	tests := []struct {
		name     string
		metadata task.Metadata
		expected task.OrpheusMetadata
	}{
		{
			name:     "nil metadata",
			metadata: nil,
			expected: task.OrpheusMetadata{},
		},
		{
			name:     "empty metadata",
			metadata: task.Metadata{},
			expected: task.OrpheusMetadata{},
		},
		{
			name: "irrelevant metadata only",
			metadata: task.Metadata{
				"backend.custom": "preserved but not projected",
			},
			expected: task.OrpheusMetadata{},
		},
		{
			name: "partial metadata",
			metadata: task.Metadata{
				task.MetadataWorktree: "/tmp/orpheus/op-1",
			},
			expected: task.OrpheusMetadata{
				Worktree:    "/tmp/orpheus/op-1",
				HasWorktree: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := task.Task{Metadata: tt.metadata}.OrpheusMetadata()
			if !reflect.DeepEqual(got, tt.expected) {
				t.Fatalf("orpheus metadata = %#v, want %#v", got, tt.expected)
			}
		})
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
