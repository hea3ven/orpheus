package cli

import (
	"reflect"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/status"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/spf13/cobra"
)

func TestNormalizeTaskViewSortUsesDefaultsAndRejectsUnknownValues(t *testing.T) {
	tests := []struct {
		name        string
		value       string
		defaultSort taskViewSort
		want        taskViewSort
		wantError   string
	}{
		{name: "status default", defaultSort: taskViewSortStatus, want: taskViewSortStatus},
		{name: "list default", defaultSort: taskViewSortCreated, want: taskViewSortCreated},
		{name: "trimmed case-insensitive", value: " UPDATED ", defaultSort: taskViewSortStatus, want: taskViewSortUpdated},
		{name: "unknown", value: "priority", defaultSort: taskViewSortStatus, wantError: `invalid --sort "priority"; expected status, created, or updated`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeTaskViewSort(tt.value, tt.defaultSort)
			if tt.wantError != "" {
				if err == nil || err.Error() != tt.wantError {
					t.Fatalf("normalizeTaskViewSort() error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeTaskViewSort() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("normalizeTaskViewSort() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTaskViewCommandsDocumentDefaultsAndRejectUnknownSort(t *testing.T) {
	tests := []struct {
		name        string
		command     *cobra.Command
		wantDefault string
	}{
		{name: "status", command: newStatusCommand(&rootOptions{}), wantDefault: "status"},
		{name: "task list", command: newTaskListCommand(&rootOptions{}), wantDefault: "created"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDefault, err := tt.command.Flags().GetString("sort")
			if err != nil {
				t.Fatalf("get --sort default: %v", err)
			}
			if gotDefault != tt.wantDefault {
				t.Fatalf("--sort default = %q, want %q", gotDefault, tt.wantDefault)
			}

			tt.command.SetArgs([]string{"--sort", "priority"})
			err = tt.command.Execute()
			if err == nil || err.Error() != `invalid --sort "priority"; expected status, created, or updated` {
				t.Fatalf("command error = %v, want invalid --sort guidance", err)
			}
		})
	}
}

func TestStatusDisplayRowsSortStatusByProjectionPriorityAndCreation(t *testing.T) {
	projection := taskViewSortProjection(
		statusGroup(status.GroupNeedsAttention,
			taskViewSortEntry("beta", "b-attention-p1", task.IssueTypeTask, 1, timeAt(2026, 1, 4), nil, ""),
			taskViewSortEntry("alpha", "a-attention-p0", task.IssueTypeTask, 0, timeAt(2026, 1, 1), nil, ""),
		),
		statusGroup(status.GroupInReview,
			taskViewSortEntry("alpha", "a-review", task.IssueTypeTask, 4, timeAt(2026, 1, 5), nil, ""),
		),
		statusGroup(status.GroupReadyToRun,
			taskViewSortEntry("alpha", "a-ready-p2", task.IssueTypeTask, 2, timeAt(2026, 1, 2), nil, ""),
			taskViewSortEntry("beta", "b-ready-p0-old", task.IssueTypeTask, 0, timeAt(2026, 1, 1), nil, ""),
			taskViewSortEntry("alpha", "a-ready-p0-new", task.IssueTypeTask, 0, timeAt(2026, 1, 3), nil, ""),
		),
	)

	rows := statusDisplayRowsForSort(projection.Groups, taskViewSortStatus)
	assertTaskViewSortIDs(t, rows, []string{
		"a-attention-p0",
		"b-attention-p1",
		"a-review",
		"a-ready-p0-new",
		"b-ready-p0-old",
		"a-ready-p2",
	})
}

func TestStatusDisplayRowsSortTimestampsGloballyAndDeterministically(t *testing.T) {
	projection := taskViewSortProjection(
		statusGroup(status.GroupReadyToRun,
			taskViewSortEntry("beta", "b-tie", task.IssueTypeTask, 1, timeAt(2026, 1, 2), timeAt(2026, 2, 1), ""),
			taskViewSortEntry("alpha", "a-missing", task.IssueTypeTask, 1, nil, nil, ""),
			taskViewSortEntry("alpha", "a-tie-z", task.IssueTypeTask, 1, timeAt(2026, 1, 2), timeAt(2026, 2, 1), ""),
			taskViewSortEntry("alpha", "a-tie", task.IssueTypeTask, 1, timeAt(2026, 1, 2), timeAt(2026, 2, 1), ""),
		),
		statusGroup(status.GroupNeedsAttention,
			taskViewSortEntry("alpha", "a-new", task.IssueTypeTask, 3, timeAt(2026, 1, 3), timeAt(2026, 1, 4), ""),
			taskViewSortEntry("beta", "b-updated", task.IssueTypeTask, 0, timeAt(2026, 1, 1), timeAt(2026, 2, 2), ""),
		),
	)

	tests := []struct {
		name string
		sort taskViewSort
		want []string
	}{
		{
			name: "created",
			sort: taskViewSortCreated,
			want: []string{"a-new", "a-tie", "a-tie-z", "b-tie", "b-updated", "a-missing"},
		},
		{
			name: "updated",
			sort: taskViewSortUpdated,
			want: []string{"b-updated", "a-tie", "a-tie-z", "b-tie", "a-new", "a-missing"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := statusDisplayRowsForSort(projection.Groups, tt.sort)
			assertTaskViewSortIDs(t, rows, tt.want)
		})
	}
}

func TestStatusDisplayRowsKeepSortedEpicSubtreesContiguous(t *testing.T) {
	projection := taskViewSortProjection(
		statusGroup(status.GroupReadyToRun,
			taskViewSortEntry("alpha", "a-root-old", task.IssueTypeEpic, 1, timeAt(2026, 1, 1), nil, ""),
			taskViewSortEntry("alpha", "a-root-new", task.IssueTypeEpic, 1, timeAt(2026, 1, 5), nil, ""),
			taskViewSortEntry("alpha", "a-child-old", task.IssueTypeTask, 1, timeAt(2026, 1, 2), nil, "a-root-new"),
			taskViewSortEntry("alpha", "a-child-new", task.IssueTypeEpic, 1, timeAt(2026, 1, 4), nil, "a-root-new"),
			taskViewSortEntry("alpha", "a-grandchild", task.IssueTypeTask, 1, timeAt(2026, 1, 3), nil, "a-child-new"),
			taskViewSortEntry("beta", "b-context-child", task.IssueTypeTask, 1, timeAt(2026, 1, 6), nil, "b-filtered-parent"),
		),
	)

	rows := statusDisplayRowsForSort(projection.Groups, taskViewSortCreated)
	assertTaskViewSortIDs(t, rows, []string{
		"b-context-child",
		"a-root-new",
		"a-child-new",
		"a-grandchild",
		"a-child-old",
		"a-root-old",
	})
	if rows[0].TaskID != "b-context-child" {
		t.Fatalf("context child TaskID = %q, want standalone task id", rows[0].TaskID)
	}
	if rows[2].TaskID != "├─ a-child-new" || rows[3].TaskID != "│ └─ a-grandchild" || rows[4].TaskID != "└─ a-child-old" {
		t.Fatalf("sorted tree markers = %#v, want nested contiguous subtree", []string{rows[2].TaskID, rows[3].TaskID, rows[4].TaskID})
	}
}

func taskViewSortProjection(groups ...status.Group) status.Projection {
	return status.Projection{Groups: groups}
}

func statusGroup(id status.GroupID, entries ...status.Entry) status.Group {
	return status.Group{ID: id, Title: string(id), Entries: entries}
}

func taskViewSortEntry(
	repositoryID string,
	taskID string,
	issueType task.IssueType,
	priority int,
	createdAt *time.Time,
	updatedAt *time.Time,
	parentID string,
) status.Entry {
	return status.Entry{
		Kind:       status.EntryTask,
		Repository: task.Repository{ID: repositoryID, Name: repositoryID},
		Task: task.Task{
			ID:        taskID,
			Title:     taskID,
			IssueType: issueType,
			Priority:  priority,
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
			Relations: task.RelationSummary{ParentID: parentID},
		},
	}
}

func timeAt(year int, month time.Month, day int) *time.Time {
	value := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	return &value
}

func assertTaskViewSortIDs(t *testing.T, rows []statusDisplayRow, want []string) {
	t.Helper()
	got := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Entry.Kind == status.EntryTask {
			got = append(got, row.Entry.Task.ID)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("task IDs = %#v, want %#v", got, want)
	}
}
