package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/status"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
)

func TestRenderTaskViewJSONUsesSelectedProjectionRowsAndSemanticValues(t *testing.T) {
	createdAt := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Hour)
	repository := taskmodel.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"}
	projection := status.Projection{Groups: []status.Group{
		{
			ID: status.GroupNeedsAttention,
			Entries: []status.Entry{{
				Kind:       status.EntryRepoFailure,
				Repository: repository,
				Failure:    errors.New("backend unavailable"),
				SemanticDetail: status.Detail{
					Kind:      status.DetailRepoFailure,
					Source:    "task_backend",
					Operation: "snapshot",
				},
			}},
		},
		{
			ID: status.GroupReadyToRun,
			Entries: []status.Entry{{
				Kind:       status.EntryTask,
				Repository: repository,
				Task: taskmodel.Task{
					ID:        "a-ready",
					Title:     "Ready task",
					Status:    taskmodel.StatusInProgress,
					Priority:  0,
					IssueType: taskmodel.IssueTypeTask,
					CreatedAt: &createdAt,
					UpdatedAt: &updatedAt,
				},
				SemanticDetail: status.Detail{
					Kind:  status.DetailBlockedDependencies,
					IDs:   []string{"a-first", "a-second"},
					Count: 2,
				},
			}},
		},
	}}

	var output bytes.Buffer
	if err := renderTaskViewJSON(&output, projection, true, taskViewSortStatus, false); err != nil {
		t.Fatalf("render task view JSON: %v", err)
	}

	var entries []map[string]any
	if err := json.Unmarshal(output.Bytes(), &entries); err != nil {
		t.Fatalf("parse JSON output: %v\n%s", err, output.String())
	}
	if len(entries) != 2 {
		t.Fatalf("JSON entries = %#v, want repository failure and task", entries)
	}

	assertTaskViewJSONRepoFailure(t, entries[0])
	assertTaskViewJSONTaskEntry(t, entries[1])
}

func assertTaskViewJSONRepoFailure(t *testing.T, entry map[string]any) {
	t.Helper()

	if entry["kind"] != "repo_failure" || entry["status"] != "needs_attention" {
		t.Fatalf("failure entry = %#v, want distinguishable projected repository failure", entry)
	}
	detail, ok := entry["detail"].(map[string]any)
	if !ok {
		t.Fatalf("failure entry detail = %#v, want JSON object", entry["detail"])
	}
	if detail["kind"] != "repo_failure" || detail["source"] != "task_backend" ||
		detail["operation"] != "snapshot" || detail["message"] != "backend unavailable" {
		t.Fatalf("failure detail = %#v, want semantic diagnostic", detail)
	}
}

func assertTaskViewJSONTaskEntry(t *testing.T, entry map[string]any) {
	t.Helper()

	if entry["kind"] != "task" || entry["status"] != "ready" || entry["priority"] != float64(0) {
		t.Fatalf("task entry = %#v, want projected ready status and zero priority", entry)
	}
	if _, exposed := entry["task_source_status"]; exposed {
		t.Fatalf("task entry = %#v, must not expose task-source lifecycle status", entry)
	}
	repo, ok := entry["repository"].(map[string]any)
	if !ok || !reflect.DeepEqual(repo, map[string]any{"id": "alpha", "name": "Alpha", "task_id_prefix": "a"}) {
		t.Fatalf("repository = %#v, want repository identity", entry["repository"])
	}
	detail, ok := entry["detail"].(map[string]any)
	if !ok {
		t.Fatalf("task entry detail = %#v, want JSON object", entry["detail"])
	}
	if detail["kind"] != "blocked_dependencies" || detail["count"] != float64(2) ||
		!reflect.DeepEqual(detail["ids"], []any{"a-first", "a-second"}) {
		t.Fatalf("task detail = %#v, want semantic detail values", detail)
	}
	if entry["created_at"] != "2026-07-01T12:00:00Z" || entry["updated_at"] != "2026-07-01T13:00:00Z" {
		t.Fatalf("task timestamps = created %q updated %q, want RFC3339 timestamps", entry["created_at"], entry["updated_at"])
	}
}

func TestRenderTaskViewJSONTaskListExcludesNonTaskEntriesAndUsesTreeOrder(t *testing.T) {
	repository := taskmodel.Repository{ID: "alpha", Name: "Alpha"}
	projection := status.Projection{Groups: []status.Group{
		{
			ID: status.GroupReadyToRun,
			Entries: []status.Entry{
				{
					Kind:       status.EntryTask,
					Repository: repository,
					Task: taskmodel.Task{
						ID: "a-epic", Title: "Epic", IssueType: taskmodel.IssueTypeEpic, Priority: 1,
					},
					EpicProgress: status.Detail{Kind: status.DetailEpicProgress, Completed: 1, Total: 2},
				},
				{
					Kind:       status.EntryTask,
					Repository: repository,
					Task: taskmodel.Task{
						ID: "a-child", Title: "Child", IssueType: taskmodel.IssueTypeTask, Priority: 2,
						Relations: taskmodel.RelationSummary{ParentID: "a-epic"},
					},
				},
			},
		},
		{
			ID: status.GroupNeedsAttention,
			Entries: []status.Entry{{
				Kind:       status.EntryRepoFailure,
				Repository: repository,
				Failure:    errors.New("unavailable"),
				SemanticDetail: status.Detail{
					Kind: status.DetailRepoFailure,
				},
			}},
		},
	}}

	var output bytes.Buffer
	if err := renderTaskViewJSON(&output, projection, true, taskViewSortStatus, true); err != nil {
		t.Fatalf("render task-list JSON: %v", err)
	}

	var entries []struct {
		Kind         string                    `json:"kind"`
		ID           string                    `json:"id"`
		Status       string                    `json:"status"`
		EpicProgress *taskViewJSONEpicProgress `json:"epic_progress"`
	}
	if err := json.Unmarshal(output.Bytes(), &entries); err != nil {
		t.Fatalf("parse JSON output: %v\n%s", err, output.String())
	}
	if got, want := jsonTaskEntryIDs(entries), []string{"a-epic", "a-child"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("task-list JSON IDs = %v, want selected tree order %v", got, want)
	}
	if entries[0].Status != "ready" || entries[0].EpicProgress == nil ||
		entries[0].EpicProgress.Completed != 1 || entries[0].EpicProgress.Total != 2 {
		t.Fatalf("epic JSON entry = %#v, want semantic projected status and progress", entries[0])
	}
}

func TestTaskViewJSONDetailDoesNotExposeTaskSourceLifecycleStates(t *testing.T) {
	for _, detail := range []status.Detail{
		{Kind: status.DetailUnknownTaskStatus, State: "deferred"},
		{Kind: status.DetailParentNotReady, State: "in_progress"},
	} {
		if got := taskViewJSONDetailFor(detail, nil).State; got != "" {
			t.Fatalf("detail %q state = %q, must not expose task-source lifecycle state", detail.Kind, got)
		}
	}

	localState := taskViewJSONDetailFor(status.Detail{
		Kind:  status.DetailReviewUnknownState,
		State: "interrupted",
	}, nil)
	if localState.State != "interrupted" {
		t.Fatalf("local review state = %q, want Orpheus-owned state", localState.State)
	}
}

func TestRenderTaskViewJSONEmptyProjectionIsArray(t *testing.T) {
	var output bytes.Buffer
	if err := renderTaskViewJSON(&output, status.Projection{}, false, taskViewSortStatus, false); err != nil {
		t.Fatalf("render empty task view JSON: %v", err)
	}
	if output.String() != "[]\n" {
		t.Fatalf("empty JSON output = %q, want []", output.String())
	}
}

func jsonTaskEntryIDs(entries []struct {
	Kind         string                    `json:"kind"`
	ID           string                    `json:"id"`
	Status       string                    `json:"status"`
	EpicProgress *taskViewJSONEpicProgress `json:"epic_progress"`
}) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return ids
}
