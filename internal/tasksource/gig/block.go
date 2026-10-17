package gig

import (
	"context"
	"fmt"
	"strconv"

	sdk "github.com/NeerajG03/gig"
	"github.com/hea3ven/orpheus/internal/task"
)

const blockEventCursorKey = "orpheus.gig.block_after_event"

// Block saves restoration facts before the native transition, so a failed
// status write leaves a retryable open/in-progress item rather than a lost hold.
func (b Backend) Block(ctx context.Context, id string) error {
	return b.mutate(ctx, id, func(store *sdk.Store, stored *storedTask) error {
		item := &stored.item
		if err := task.ValidateBlock(*item); err != nil {
			return err
		}
		if item.Status == task.StatusBlocked {
			return nil
		}
		events, err := store.Events(id)
		if err != nil {
			return err
		}
		var cursor int64
		for _, event := range events {
			cursor = max(cursor, event.ID)
		}
		item.Metadata[task.MetadataPreBlockStatus] = string(item.Status)
		item.Metadata[blockEventCursorKey] = strconv.FormatInt(cursor, 10)
		if err := saveMetadata(ctx, store, *stored); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return store.UpdateStatus(id, sdk.StatusBlocked, "orpheus")
	})
}

// Unblock restores only the original blocked interval. Gig may automatically
// reopen dependents; any intervening status event invalidates restoration, even
// if an external writer subsequently blocks the item again in the same second.
func (b Backend) Unblock(ctx context.Context, id string) error {
	return b.mutate(ctx, id, func(store *sdk.Store, stored *storedTask) error {
		if stored.item.Status != task.StatusBlocked {
			return nil
		}
		status, err := blockRestorationStatus(store, stored.item)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := store.UpdateStatus(id, sdk.Status(status), "orpheus"); err != nil {
			return err
		}
		delete(stored.item.Metadata, task.MetadataPreBlockStatus)
		delete(stored.item.Metadata, blockEventCursorKey)
		if err := saveMetadata(ctx, store, *stored); err != nil {
			return fmt.Errorf("item unblocked but restoration metadata cleanup failed; inspect item before retrying: %w", err)
		}
		return nil
	})
}

func blockRestorationStatus(store *sdk.Store, item task.Task) (task.Status, error) {
	value, ok := item.Metadata[blockEventCursorKey]
	if !ok {
		return task.StatusOpen, nil
	}
	cursor, err := strconv.ParseInt(value, 10, 64)
	if err != nil || cursor < 0 {
		return task.StatusUnknown, conflict(item.ID, "invalid block event cursor; inspect task-source metadata before retrying")
	}
	events, err := store.Events(item.ID)
	if err != nil {
		return task.StatusUnknown, err
	}
	var transition *sdk.Event
	for _, event := range events {
		if event.ID <= cursor || event.Type != sdk.EventStatusChanged {
			continue
		}
		if transition != nil {
			return task.StatusOpen, nil
		}
		transition = event
	}
	if transition == nil || transition.NewValue != string(task.StatusBlocked) || transition.OldValue != item.Metadata[task.MetadataPreBlockStatus] {
		return task.StatusOpen, nil
	}
	return task.PreBlockStatus(item)
}

var _ task.BlockMutator = Backend{}
