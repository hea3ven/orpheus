package gig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/NeerajG03/gig"
	"github.com/hea3ven/orpheus/internal/task"
)

func (b Backend) Create(ctx context.Context, opts task.CreateOptions) (task.Task, error) {
	opts, err := task.NormalizeCreateOptions(opts)
	if err != nil {
		return task.Task{}, err
	}
	var created task.Task
	err = b.withStore(ctx, func(store *sdk.Store) error {
		if err := validateParent(ctx, store, "", opts.ParentID); err != nil {
			return err
		}
		for _, id := range opts.BlockingIDs {
			if _, err := readTask(ctx, store, id); err != nil {
				return err
			}
		}
		item := task.Task{Title: opts.Title, Description: opts.Description,
			Design: opts.Design, AcceptanceCriteria: opts.AcceptanceCriteria, ExternalRef: opts.ExternalRef}
		metadata, err := encodeMetadata(storedTask{item: item, metadata: make(map[string]json.RawMessage)})
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		native, err := store.Create(sdk.CreateParams{Title: opts.Title, Description: opts.Description,
			Type: sdk.TaskType(opts.IssueType), Priority: sdk.P2, ParentID: opts.ParentID,
			Metadata: metadata, CreatedBy: "orpheus"})
		if err != nil {
			return err
		}
		created = task.Task{ID: native.ID}
		for _, id := range opts.BlockingIDs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := store.AddDependency(native.ID, id, sdk.Blocks); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		var detail task.Task
		detail, err = b.Get(ctx, created.ID)
		if err == nil {
			return detail, nil
		}
	}
	if created.ID != "" {
		return created, fmt.Errorf("gig task %s was created but setup or readback failed; inspect it and repair dependencies with task edit, do not repeat task create: %w", created.ID, err)
	}
	return task.Task{}, err
}

func (b Backend) Update(ctx context.Context, opts task.UpdateOptions) (task.Task, error) {
	opts, err := task.NormalizeUpdateOptions(opts)
	if err != nil {
		return task.Task{}, err
	}
	err = b.mutate(ctx, opts.ID, func(store *sdk.Store, stored *storedTask) error {
		if stored.item.Status == task.StatusClosed {
			return conflict(opts.ID, "task is closed")
		}
		if opts.ParentID != nil {
			if err := validateParent(ctx, store, opts.ID, *opts.ParentID); err != nil {
				return err
			}
		}
		if err := preflightDependencies(ctx, store, opts); err != nil {
			return err
		}
		for _, field := range []struct {
			value  *string
			target *string
		}{{opts.Design, &stored.item.Design}, {opts.AcceptanceCriteria, &stored.item.AcceptanceCriteria}, {opts.ExternalRef, &stored.item.ExternalRef}} {
			if field.value != nil {
				*field.target = *field.value
			}
		}
		metadata, err := encodeMetadata(*stored)
		if err != nil {
			return err
		}
		params := sdk.UpdateParams{Title: opts.Title, Description: opts.Description, Metadata: &metadata, ParentID: opts.ParentID}
		if opts.ParentID != nil && *opts.ParentID == "" {
			params.ParentID, params.Orphan = nil, true
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := store.Update(opts.ID, params, "orpheus"); err != nil {
			return err
		}
		if err := updateDependencies(ctx, store, opts); err != nil {
			return fmt.Errorf("gig task %s: %w", opts.ID, task.PartialUpdateError{Cause: err})
		}
		return nil
	})
	if err != nil {
		return task.Task{}, err
	}
	return b.Get(ctx, opts.ID)
}

func (b Backend) mutate(ctx context.Context, id string, change func(*sdk.Store, *storedTask) error) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("gig task id is required")
	}
	return b.withStore(ctx, func(store *sdk.Store) error {
		stored, err := readTask(ctx, store, id)
		if err != nil {
			return err
		}
		return change(store, &stored)
	})
}

func saveMetadata(ctx context.Context, store *sdk.Store, stored storedTask) error {
	metadata, err := encodeMetadata(stored)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err = store.Update(stored.item.ID, sdk.UpdateParams{Metadata: &metadata}, "orpheus")
	return err
}

func conflict(id, reason string) error { return task.MutationConflictError{TaskID: id, Reason: reason} }

// Shared creation/edit services own full graph validation. These checks protect
// direct adapter calls without reproducing their traversal policy.
func validateParent(ctx context.Context, store *sdk.Store, id, parent string) error {
	if parent == "" {
		return nil
	}
	if parent == id {
		return conflict(id, "item cannot parent itself")
	}
	stored, err := readTask(ctx, store, parent)
	if err != nil {
		return err
	}
	if stored.item.IssueType != task.IssueTypeEpic || stored.item.Status == task.StatusClosed {
		return conflict(id, "parent must be an unclosed epic")
	}
	return nil
}

func preflightDependencies(ctx context.Context, store *sdk.Store, opts task.UpdateOptions) error {
	for _, id := range opts.AddBlockingIDs {
		if _, err := readTask(ctx, store, id); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	edges, err := store.ListDependencies(opts.ID)
	if err != nil {
		return err
	}
	for _, ids := range [][]string{opts.AddBlockingIDs, opts.RemoveBlockingIDs} {
		for _, id := range ids {
			for _, edge := range edges {
				if edge.ToID == id && edge.Type != sdk.Blocks {
					return conflict(opts.ID, fmt.Sprintf("relationship to %s is not blocking", id))
				}
			}
		}
	}
	return nil
}

func updateDependencies(ctx context.Context, store *sdk.Store, opts task.UpdateOptions) error {
	for _, id := range opts.AddBlockingIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := store.AddDependency(opts.ID, id, sdk.Blocks); err != nil {
			return err
		}
	}
	for _, id := range opts.RemoveBlockingIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := store.RemoveDependency(opts.ID, id); err != nil {
			return err
		}
	}
	return nil
}
