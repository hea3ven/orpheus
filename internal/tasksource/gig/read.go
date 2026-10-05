package gig

import (
	"context"
	"errors"
	"fmt"
	"sort"

	sdk "github.com/NeerajG03/gig"
	"github.com/hea3ven/orpheus/internal/task"
)

func translate(native *sdk.Task) (storedTask, error) {
	item := task.Task{ID: native.ID, Title: native.Title, Description: native.Description,
		Status: task.Status(native.Status), Priority: int(native.Priority), Assignee: native.Assignee,
		IssueType: task.IssueType(native.Type), CreatedBy: native.CreatedBy, Labels: native.Labels,
		CreatedAt: &native.CreatedAt, UpdatedAt: &native.UpdatedAt, ClosedAt: native.ClosedAt,
		Relations: task.RelationSummary{ParentID: native.ParentID}}
	if err := task.ValidateTaskSourceItem(item); err != nil {
		return storedTask{}, err
	}
	if item.ID == "" || item.Title == "" || native.CreatedAt.IsZero() || native.UpdatedAt.IsZero() {
		return storedTask{}, fmt.Errorf("gig task %q has missing required fields or timestamps", item.ID)
	}
	return decodeMetadata(item, native.Metadata)
}

func readNative(ctx context.Context, store *sdk.Store, id string) (*sdk.Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	native, err := store.Get(id)
	// v0.7.0 has no absence sentinel. Only Get's exact absence error maps here.
	if err != nil && err.Error() == "task not found" {
		err = task.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get gig task %q: %w", id, err)
	}
	return native, nil
}

func readTask(ctx context.Context, store *sdk.Store, id string) (storedTask, error) {
	native, err := readNative(ctx, store, id)
	if err != nil {
		return storedTask{}, err
	}
	return translate(native)
}

func (b Backend) Get(ctx context.Context, id string) (task.Task, error) {
	var item task.Task
	err := b.withStore(ctx, func(store *sdk.Store) error {
		stored, err := readTask(ctx, store, id)
		if err != nil {
			return err
		}
		item = stored.item
		if err := readRelations(ctx, store, &item); err != nil {
			return err
		}
		ids := append([]string{item.Relations.ParentID}, item.Relations.ChildIDs...)
		ids = append(ids, item.Relations.DependencyIDs...)
		ids = append(ids, item.Relations.DependentIDs...)
		seen := make(map[string]bool)
		for _, relatedID := range ids {
			if relatedID == "" || seen[relatedID] {
				continue
			}
			seen[relatedID] = true
			related, err := readTask(ctx, store, relatedID)
			if errors.Is(err, task.ErrUnsupportedTaskSourceItem) {
				continue
			}
			if err != nil {
				return err
			}
			item.RelatedItems = append(item.RelatedItems, related.item)
		}
		return nil
	})
	if err != nil {
		return task.Task{}, err
	}
	return item, nil
}

func (b Backend) List(ctx context.Context) ([]task.Task, error) {
	return b.ListFiltered(ctx, task.ListFilter{})
}

func (b Backend) ListFiltered(ctx context.Context, filter task.ListFilter) ([]task.Task, error) {
	filter, err := filter.Normalized()
	if err != nil {
		return nil, err
	}
	params := sdk.ListParams{}
	if filter.ParentID != "" {
		params.ParentID = &filter.ParentID
	}
	if len(filter.IssueTypes) == 1 {
		kind := sdk.TaskType(filter.IssueTypes[0])
		params.Type = &kind
	}
	var items []task.Task
	err = b.withStore(ctx, func(store *sdk.Store) error {
		all, err := store.List(params)
		if err != nil {
			return err
		}
		for _, native := range all {
			if err := ctx.Err(); err != nil {
				return err
			}
			stored, err := translate(native)
			if errors.Is(err, task.ErrUnsupportedTaskSourceItem) {
				continue
			}
			if err != nil {
				return err
			}
			item := stored.item
			if !filter.Matches(item) {
				continue
			}
			if err := readRelations(ctx, store, &item); err != nil {
				return err
			}
			items = append(items, item)
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

func readRelations(ctx context.Context, store *sdk.Store, item *task.Task) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	children, err := store.Children(item.ID)
	if err != nil {
		return err
	}
	for _, child := range children {
		item.Relations.ChildIDs = append(item.Relations.ChildIDs, child.ID)
	}
	if err := readBlockingRelations(ctx, store, item); err != nil {
		return err
	}
	sort.Strings(item.Relations.ChildIDs)
	sort.Strings(item.Relations.DependencyIDs)
	sort.Strings(item.Relations.DependentIDs)
	item.Relations.ChildCount = len(item.Relations.ChildIDs)
	item.Relations.DependencyCount = len(item.Relations.DependencyIDs)
	item.Relations.DependentCount = len(item.Relations.DependentIDs)
	item.Relations.Complete = true
	return ctx.Err()
}

func readBlockingRelations(ctx context.Context, store *sdk.Store, item *task.Task) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dependencies, err := store.ListDependencies(item.ID)
	if err != nil {
		return err
	}
	for _, edge := range dependencies {
		if edge.Type != sdk.Blocks {
			continue
		}
		dependency, err := readNative(ctx, store, edge.ToID)
		if err != nil {
			return err
		}
		item.Relations.DependencyIDs = append(item.Relations.DependencyIDs, edge.ToID)
		if dependency.Status != sdk.StatusClosed {
			item.Relations.BlockedByCount++
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dependents, err := store.ListDependents(item.ID)
	if err != nil {
		return err
	}
	for _, edge := range dependents {
		if edge.Type != sdk.Blocks {
			continue
		}
		dependent, err := readNative(ctx, store, edge.FromID)
		if err != nil {
			return err
		}
		item.Relations.DependentIDs = append(item.Relations.DependentIDs, edge.FromID)
		if item.Status != task.StatusClosed && dependent.Status != sdk.StatusClosed {
			item.Relations.BlockingCount++
		}
	}
	return nil
}
