package state

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
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
	path     string
	file     *os.File
	released bool
}

// GlobalMutationLockPath returns the absolute path for the reusable global
// mutation lock file.
func (p Paths) GlobalMutationLockPath() (string, error) {
	return p.DataPath(filepath.Join(globalMutationLockDir, globalMutationLockFile))
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
) (err error) {
	if mutate == nil {
		return errors.New("global mutation lock callback is nil")
	}

	lockPath := globalMutationLockPathCandidate(paths)
	span := logging.Start(ctx, logger, "global mutation lock", lockAttrs(operation, lockPath)...)
	lock, err := acquireGlobalMutationLock(paths, operation)
	if err != nil {
		span.FinishError(ctx, err)
		return err
	}
	span.Finish(ctx, logging.StatusSuccess)

	held := logging.Start(ctx, logger, "global mutation lock held", lockAttrs(operation, lock.path)...)
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

func lockAttrs(operation string, path string) []slog.Attr {
	return []slog.Attr{
		slog.String("component", "state"),
		slog.String("operation", "mutation_lock"),
		slog.String("semantic_operation", operation),
		slog.String("path", path),
	}
}

func acquireGlobalMutationLock(paths Paths, operation string) (*mutationLock, error) {
	lockPath, err := paths.GlobalMutationLockPath()
	if err != nil {
		return nil, &LockAcquisitionError{
			Operation: operation,
			Path:      globalMutationLockPathCandidate(paths),
			Err:       err,
		}
	}

	if err := os.MkdirAll(filepath.Dir(lockPath), directoryMode); err != nil {
		return nil, &LockAcquisitionError{
			Operation: operation,
			Path:      lockPath,
			Err:       fmt.Errorf("create lock directory: %w", err),
		}
	}

	file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, fileMode)
	if err != nil {
		return nil, &LockAcquisitionError{
			Operation: operation,
			Path:      lockPath,
			Err:       err,
		}
	}

	return &mutationLock{
		path: lockPath,
		file: file,
	}, nil
}

func globalMutationLockPathCandidate(paths Paths) string {
	return filepath.Join(paths.DataRoot, globalMutationLockDir, globalMutationLockFile)
}

func (l *mutationLock) release() error {
	if l == nil || l.released {
		return nil
	}
	l.released = true

	var releaseErr error
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			releaseErr = errors.Join(releaseErr, fmt.Errorf("close global mutation lock %s: %w", l.path, err))
		}
	}
	if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		releaseErr = errors.Join(releaseErr, fmt.Errorf("remove global mutation lock %s: %w", l.path, err))
	}
	return releaseErr
}
