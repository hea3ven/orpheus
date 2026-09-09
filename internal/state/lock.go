package state

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/hea3ven/orpheus/internal/logging"
)

const (
	globalMutationLockDir  = "locks"
	globalMutationLockFile = "mutation.lock"
)

// LockAcquisitionError reports a failed fail-fast lock acquisition.
type LockAcquisitionError struct {
	Operation string
	Path      string
	Err       error
}

// Error returns an actionable lock acquisition failure.
func (e *LockAcquisitionError) Error() string {
	if e == nil {
		return "failed to acquire lock"
	}

	operation := strings.TrimSpace(e.Operation)
	if operation == "" {
		operation = "mutation"
	}

	if e.Path == "" {
		if e.Err == nil {
			return fmt.Sprintf("failed to acquire lock for %s", operation)
		}
		return fmt.Sprintf("failed to acquire lock for %s: %v", operation, e.Err)
	}
	if e.Err == nil {
		return fmt.Sprintf("failed to acquire lock for %s: %s", operation, e.Path)
	}
	return fmt.Sprintf("failed to acquire lock for %s: %s: %v", operation, e.Path, e.Err)
}

// Unwrap returns the underlying acquisition error.
func (e *LockAcquisitionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type mutationLock struct {
	path      string
	releaseFn func() error
	released  bool
}

// WithGlobalMutationLock runs mutate while holding the global mutation lock.
func WithGlobalMutationLock(paths Paths, operation string, mutate func() error) error {
	return WithGlobalMutationLockLogger(context.Background(), paths, operation, nil, mutate)
}

// WithGlobalMutationLockLogger runs mutate while holding the global mutation lock and emits diagnostics.
func WithGlobalMutationLockLogger(
	ctx context.Context,
	paths Paths,
	operation string,
	logger *slog.Logger,
	mutate func() error,
	attrs ...slog.Attr,
) (err error) {
	if mutate == nil {
		return errors.New("global mutation lock callback is nil")
	}

	lockPath := globalMutationLockPathCandidate(paths)
	span := logging.Start(ctx, logger, "global mutation lock", lockAttrs(operation, lockPath, attrs...)...)
	lock, err := paths.acquireGlobalMutationLock(operation)
	if err != nil {
		span.FinishError(ctx, err)
		return err
	}
	span.Finish(ctx, logging.StatusSuccess)

	held := logging.Start(ctx, logger, "global mutation lock held", lockAttrs(operation, lock.path, attrs...)...)
	defer func() {
		if releaseErr := lock.release(); releaseErr != nil {
			if err != nil {
				err = errors.Join(err, releaseErr)
				held.FinishError(ctx, err)
				return
			}
			err = releaseErr
			held.FinishError(ctx, releaseErr)
			return
		}
		held.FinishError(ctx, err)
	}()

	return mutate()
}

func lockAttrs(operation string, path string, attrs ...slog.Attr) []slog.Attr {
	lockAttrs := []slog.Attr{
		slog.String("component", "state"),
		slog.String("operation", "mutation_lock"),
		slog.String("semantic_operation", operation),
		slog.String("path", path),
	}
	lockAttrs = append(lockAttrs, attrs...)
	return lockAttrs
}

func (p Paths) acquireGlobalMutationLock(operation string) (*mutationLock, error) {
	lockPath, err := p.DataPath(filepath.Join(globalMutationLockDir, globalMutationLockFile))
	if err != nil {
		return nil, &LockAcquisitionError{
			Operation: operation,
			Path:      globalMutationLockPathCandidate(p),
			Err:       err,
		}
	}
	release, err := p.backend.acquireLock(lockPath, directoryMode, fileMode)
	if err != nil {
		return nil, &LockAcquisitionError{
			Operation: operation,
			Path:      lockPath,
			Err:       err,
		}
	}

	return &mutationLock{path: lockPath, releaseFn: release}, nil
}

func globalMutationLockPathCandidate(paths Paths) string {
	return filepath.Join(paths.dataRoot, globalMutationLockDir, globalMutationLockFile)
}

func (l *mutationLock) release() error {
	if l == nil || l.released {
		return nil
	}
	l.released = true

	if l.releaseFn == nil {
		return nil
	}
	return l.releaseFn()
}
