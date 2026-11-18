package phaseexec

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/work"
)

// PIDCheckpoint waits through brief mutation contention from an immediately
// started child's handoff. The attached launcher kills and reaps on failure.
func PIDCheckpoint(paths state.Paths, logger *slog.Logger, operation string, owner work.AttemptRef, record func() error) error {
	var err error
	attrs := []slog.Attr{slog.String("repo_id", owner.RepositoryID), slog.String("task_id", owner.TaskID), slog.Int("attempt", owner.Phase)}
	for retry := 0; retry < 100; retry++ {
		err = state.WithGlobalMutationLockLogger(context.Background(), paths, operation, logger, record, attrs...)
		if err == nil || !errors.Is(err, os.ErrExist) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}
