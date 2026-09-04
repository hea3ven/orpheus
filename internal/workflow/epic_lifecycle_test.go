//nolint:testpackage // Keeps assertions concise beside the lifecycle API.
package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
)

type fakeEpicLifecycleBackend struct {
	items     map[string]task.Task
	listItems []task.Task
	getErr    error
	listErr   error
	started   []string
	closed    []string
	startErr  error
	closeErr  error
}

func (b *fakeEpicLifecycleBackend) Get(_ context.Context, id string) (task.Task, error) {
	if b.getErr != nil {
		return task.Task{}, b.getErr
	}
	item, ok := b.items[id]
	if !ok {
		return task.Task{}, task.ErrNotFound
	}
	return item, nil
}

func (b *fakeEpicLifecycleBackend) List(_ context.Context) ([]task.Task, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	return b.listItems, nil
}

func (b *fakeEpicLifecycleBackend) StartEpic(_ context.Context, id string) error {
	if b.startErr != nil {
		return b.startErr
	}
	b.started = append(b.started, id)
	return nil
}

func (b *fakeEpicLifecycleBackend) Close(_ context.Context, id string) error {
	if b.closeErr != nil {
		return b.closeErr
	}
	b.closed = append(b.closed, id)
	return nil
}

func TestEpicLifecycleStart(t *testing.T) {
	service := EpicLifecycleService{}
	for _, tt := range []struct {
		name       string
		backend    *fakeEpicLifecycleBackend
		wantErr    string
		wantStart  []string
		wantChange bool
	}{
		{
			name: "starts eligible epic",
			backend: lifecycleBackend(
				epic("op-epic", task.StatusOpen, "op-parent", []string{"op-dependency"}, 0, 0),
				epic("op-parent", task.StatusInProgress, "", nil, 0, 0),
				item("op-dependency", task.IssueTypeTask, task.StatusClosed, ""),
			),
			wantStart: []string{"op-epic"}, wantChange: true,
		},
		{
			name: "requires active parent epic",
			backend: lifecycleBackend(
				epic("op-epic", task.StatusOpen, "op-parent", nil, 0, 0),
				epic("op-parent", task.StatusOpen, "", nil, 0, 0),
			),
			wantErr: "parent epic op-parent must be in progress",
		},
		{
			name: "requires closed dependencies",
			backend: lifecycleBackend(
				epic("op-epic", task.StatusOpen, "", []string{"op-first", "op-second"}, 0, 0),
				item("op-first", task.IssueTypeTask, task.StatusOpen, ""),
				item("op-second", task.IssueTypeTask, task.StatusInProgress, ""),
			),
			wantErr: "blocking dependencies are not closed: op-first, op-second",
		},
		{
			name:    "refuses incomplete dependency details",
			backend: lifecycleBackend(epic("op-epic", task.StatusOpen, "", []string{"op-first"}, 2, 0), item("op-first", task.IssueTypeTask, task.StatusClosed, "")),
			wantErr: "source reports 2 blockers but only 1 identifiers could be inspected",
		},
		{
			name:    "in progress is idempotent",
			backend: lifecycleBackend(epic("op-epic", task.StatusInProgress, "", nil, 0, 0)),
		},
		{
			name:    "closed cannot restart",
			backend: lifecycleBackend(epic("op-epic", task.StatusClosed, "", nil, 0, 0)),
			wantErr: "is closed and cannot be started",
		},
		{
			name:    "ordinary task is rejected",
			backend: lifecycleBackend(item("op-epic", task.IssueTypeTask, task.StatusOpen, "")),
			wantErr: "item is not an epic",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := service.Start(context.Background(), tt.backend, "op-epic")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Start() error = %v, want containing %q", err, tt.wantErr)
				}
				if tt.name == "ordinary task is rejected" && !errors.Is(err, ErrNotEpic) {
					t.Fatalf("Start() error = %v, want ErrNotEpic", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			if result.Changed != tt.wantChange {
				t.Fatalf("Start() changed = %t, want %t", result.Changed, tt.wantChange)
			}
			if strings.Join(tt.backend.started, ",") != strings.Join(tt.wantStart, ",") {
				t.Fatalf("StartEpic calls = %v, want %v", tt.backend.started, tt.wantStart)
			}
		})
	}
}

func TestEpicLifecycleClose(t *testing.T) {
	service := EpicLifecycleService{}
	for _, tt := range []struct {
		name       string
		backend    *fakeEpicLifecycleBackend
		wantErr    string
		wantClose  []string
		wantChange bool
	}{
		{
			name: "closes epic with closed children",
			backend: lifecycleBackend(
				epic("op-epic", task.StatusInProgress, "", nil, 0, 2),
				item("op-child-a", task.IssueTypeTask, task.StatusClosed, "op-epic"),
				item("op-child-b", task.IssueTypeEpic, task.StatusClosed, "op-epic"),
			),
			wantClose: []string{"op-epic"}, wantChange: true,
		},
		{
			name: "identifies active children in order",
			backend: lifecycleBackend(
				epic("op-epic", task.StatusInProgress, "", nil, 0, 3),
				item("op-child-z", task.IssueTypeTask, task.StatusOpen, "op-epic"),
				item("op-child-a", task.IssueTypeTask, task.StatusInProgress, "op-epic"),
				item("op-child-done", task.IssueTypeTask, task.StatusClosed, "op-epic"),
			),
			wantErr: "direct child items are still active: op-child-a, op-child-z",
		},
		{
			name: "refuses incomplete children",
			backend: lifecycleBackend(
				epic("op-epic", task.StatusInProgress, "", nil, 0, 2),
				item("op-child", task.IssueTypeTask, task.StatusClosed, "op-epic"),
			),
			wantErr: "source reports 2 child items but only 1 could be inspected",
		},
		{
			name:    "closed is idempotent",
			backend: lifecycleBackend(epic("op-epic", task.StatusClosed, "", nil, 0, 0)),
		},
		{
			name:    "must be in progress",
			backend: lifecycleBackend(epic("op-epic", task.StatusOpen, "", nil, 0, 0)),
			wantErr: "can only be closed when in progress",
		},
		{
			name:    "ordinary task is rejected",
			backend: lifecycleBackend(item("op-epic", task.IssueTypeTask, task.StatusInProgress, "")),
			wantErr: "item is not an epic",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := service.Close(context.Background(), tt.backend, "op-epic")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Close() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if result.Changed != tt.wantChange {
				t.Fatalf("Close() changed = %t, want %t", result.Changed, tt.wantChange)
			}
			if strings.Join(tt.backend.closed, ",") != strings.Join(tt.wantClose, ",") {
				t.Fatalf("Close calls = %v, want %v", tt.backend.closed, tt.wantClose)
			}
		})
	}
}

func lifecycleBackend(items ...task.Task) *fakeEpicLifecycleBackend {
	index := make(map[string]task.Task, len(items))
	for _, item := range items {
		index[item.ID] = item
	}
	return &fakeEpicLifecycleBackend{items: index, listItems: items}
}

func epic(id string, status task.Status, parentID string, dependencies []string, blockedByCount int, childCount int) task.Task {
	return task.Task{
		ID: id, IssueType: task.IssueTypeEpic, Status: status,
		Relations: task.RelationSummary{ParentID: parentID, DependencyIDs: dependencies, BlockedByCount: blockedByCount, ChildCount: childCount},
	}
}

func item(id string, issueType task.IssueType, status task.Status, parentID string) task.Task {
	return task.Task{ID: id, IssueType: issueType, Status: status, Relations: task.RelationSummary{ParentID: parentID}}
}
