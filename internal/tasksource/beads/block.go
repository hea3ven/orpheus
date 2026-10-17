package beads

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/hea3ven/orpheus/internal/task"
)

// Block records the prior lifecycle alongside Beads' native blocked status.
func (b TaskBackend) Block(ctx context.Context, id string) error {
	return b.setBlocked(ctx, id, true)
}

// Unblock restores the recorded lifecycle only while the item remains blocked.
func (b TaskBackend) Unblock(ctx context.Context, id string) error {
	return b.setBlocked(ctx, id, false)
}

func (b TaskBackend) setBlocked(ctx context.Context, id string, blocked bool) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("change Beads blocking: item id is required")
	}
	current, err := b.Get(ctx, id)
	if err != nil {
		return err
	}
	if blocked && current.Status == task.StatusBlocked || !blocked && current.Status != task.StatusBlocked {
		return nil
	}
	status := task.StatusBlocked
	metadataArgs := []string{"--set-metadata", task.MetadataPreBlockStatus + "=" + string(current.Status)}
	if blocked {
		if err := task.ValidateBlock(current); err != nil {
			return err
		}
	} else {
		status, err = task.PreBlockStatus(current)
		if err != nil {
			return err
		}
		metadataArgs = []string{"--unset-metadata", task.MetadataPreBlockStatus}
	}
	args := append([]string{"update", id, "--status", string(status)}, metadataArgs...)
	_, err = b.runWriteWithAttrs(ctx, "change blocking", []slog.Attr{slog.String("task_id", id)}, args...)
	return err
}

var _ task.BlockMutator = TaskBackend{}
