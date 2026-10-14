//nolint:testpackage // Keeps assertions concise beside the lifecycle API.
package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
)

type fakeEpicLifecycleBackend struct {
	items    map[string]task.Task
	gets     []string
	getErr   error
	started  []string
	closed   []string
	startErr error
	closeErr error
}

func (b *fakeEpicLifecycleBackend) Get(_ context.Context, id string) (task.Task, error) {
	b.gets = append(b.gets, id)
	if b.getErr != nil {
		return task.Task{}, b.getErr
	}
	item, ok := b.items[id]
	if !ok {
		return task.Task{}, task.ErrNotFound
	}
	return item, nil
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

func lifecycleBackend(items ...task.Task) *fakeEpicLifecycleBackend {
	index := make(map[string]task.Task, len(items))
	for _, item := range items {
		index[item.ID] = item
	}
	for id, parent := range index {
		parent.Relations.Complete = true
		for _, child := range items {
			if child.Relations.ParentID == id {
				parent.Relations.ChildIDs = append(parent.Relations.ChildIDs, child.ID)
				parent.RelatedItems = append(parent.RelatedItems, child)
			}
		}
		index[id] = parent
	}
	return &fakeEpicLifecycleBackend{items: index}
}

func epic(id string, status task.Status, parentID string, dependencies []string, blockedByCount int, childCount int) task.Task {
	return task.Task{
		ID: id, IssueType: task.IssueTypeEpic, Status: status,
		Relations: task.RelationSummary{ParentID: parentID, DependencyIDs: dependencies, BlockedByCount: blockedByCount, ChildCount: childCount},
	}
}

func TestEpicLifecycleStartReusesSuppliedParentAndBlockingState(t *testing.T) {
	item := epic("op-epic", task.StatusOpen, "op-parent", []string{"op-dependency"}, 1, 0)
	item.RelatedItems = []task.Task{
		epic("op-parent", task.StatusInProgress, "", nil, 0, 0),
		{ID: "op-dependency", IssueType: task.IssueTypeTask, Status: task.StatusClosed},
	}
	backend := lifecycleBackend(item)

	result, err := (EpicLifecycleService{}).Start(context.Background(), backend, item.ID)

	if err != nil || !result.Changed {
		t.Fatalf("Start() = %#v, %v", result, err)
	}
	if strings.Join(backend.gets, ",") != "op-epic" {
		t.Fatalf("Get calls = %v, want only epic detail", backend.gets)
	}
}

func TestEpicLifecycleCloseReadsOnlyMissingChildStateAndRefusesUnavailableChild(t *testing.T) {
	for _, available := range []bool{true, false} {
		t.Run(fmt.Sprint(available), func(t *testing.T) {
			item := epic("op-epic", task.StatusInProgress, "", nil, 0, 2)
			item.Relations.ChildIDs = []string{"op-supplied", "op-missing"}
			item.RelatedItems = []task.Task{{ID: "op-supplied", Status: task.StatusClosed, IssueType: task.IssueTypeTask}}
			backend := lifecycleBackend(item)
			if available {
				backend.items["op-missing"] = task.Task{ID: "op-missing", Status: task.StatusClosed, IssueType: task.IssueTypeTask}
			}

			result, err := (EpicLifecycleService{}).Close(context.Background(), backend, item.ID)

			if available && (err != nil || !result.Changed) {
				t.Fatalf("Close() = %#v, %v", result, err)
			}
			if !available && (!errors.Is(err, task.ErrNotFound) || len(backend.closed) != 0) {
				t.Fatalf("Close() = %#v, %v, closed %v", result, err, backend.closed)
			}
			if strings.Join(backend.gets, ",") != "op-epic,op-missing" {
				t.Fatalf("Get calls = %v", backend.gets)
			}
		})
	}
}
