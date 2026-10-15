package cli

import (
	"reflect"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/status"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/spf13/cobra"
)

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
			taskViewSortEntry("alpha", "a-root-old", task.IssueTypeEpic, 1, timeAt(2026, 1, 1), timeAt(2026, 2, 5), ""),
			taskViewSortEntry("alpha", "a-child-old", task.IssueTypeTask, 2, timeAt(2026, 1, 2), timeAt(2026, 2, 4), "a-root-new"),
			taskViewSortEntry("alpha", "a-grandchild", task.IssueTypeTask, 1, timeAt(2026, 1, 3), timeAt(2026, 2, 3), "b-child-new"),
			taskViewSortEntry("beta", "b-context-child", task.IssueTypeTask, 0, timeAt(2026, 1, 6), timeAt(2026, 2, 6), "a-filtered-parent"),
		),
		statusGroup(status.GroupWorking,
			taskViewSortEntry("alpha", "a-root-new", task.IssueTypeEpic, 1, timeAt(2026, 1, 5), timeAt(2026, 2, 1), ""),
			taskViewSortEntry("beta", "b-child-new", task.IssueTypeEpic, 1, timeAt(2026, 1, 4), timeAt(2026, 2, 2), "a-root-new"),
		),
		statusGroup(status.GroupDoneClosed,
			taskViewSortEntry("alpha", "a-filtered-parent", task.IssueTypeEpic, 1, timeAt(2026, 1, 7), timeAt(2026, 2, 7), ""),
		),
	)

	tests := []struct {
		sort      taskViewSort
		wantIDs   []string
		wantTree  []string
		wantDepth []int
	}{
		{
			sort:      taskViewSortStatus,
			wantIDs:   []string{"b-context-child", "a-root-old", "a-root-new", "a-child-old", "b-child-new", "a-grandchild"},
			wantTree:  []string{"b-context-child", "a-root-old", "a-root-new", "├─ a-child-old", "└─ b-child-new", "  └─ a-grandchild"},
			wantDepth: []int{0, 0, 0, 1, 1, 2},
		},
		{
			sort:      taskViewSortCreated,
			wantIDs:   []string{"b-context-child", "a-root-new", "b-child-new", "a-grandchild", "a-child-old", "a-root-old"},
			wantTree:  []string{"b-context-child", "a-root-new", "├─ b-child-new", "│ └─ a-grandchild", "└─ a-child-old", "a-root-old"},
			wantDepth: []int{0, 0, 1, 2, 1, 0},
		},
		{
			sort:      taskViewSortUpdated,
			wantIDs:   []string{"b-context-child", "a-root-old", "a-root-new", "a-child-old", "b-child-new", "a-grandchild"},
			wantTree:  []string{"b-context-child", "a-root-old", "a-root-new", "├─ a-child-old", "└─ b-child-new", "  └─ a-grandchild"},
			wantDepth: []int{0, 0, 0, 1, 1, 2},
		},
	}
	for _, tt := range tests {
		t.Run(string(tt.sort), func(t *testing.T) {
			rows := statusDisplayRowsForSort(visibleStatusGroups(projection.Groups, false), tt.sort)
			assertTaskViewSortIDs(t, rows, tt.wantIDs)
			for i, row := range rows {
				if row.TaskID != tt.wantTree[i] || row.TreeDepth != tt.wantDepth[i] {
					t.Errorf("row %s tree = (%q, %d), want (%q, %d)", row.Entry.Task.ID, row.TaskID, row.TreeDepth, tt.wantTree[i], tt.wantDepth[i])
				}
			}
		})
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
