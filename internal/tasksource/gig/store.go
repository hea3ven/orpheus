// Package gig adapts the embedded gig library to Orpheus task contracts.
package gig

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/NeerajG03/gig"
	"github.com/hea3ven/orpheus/internal/task"
)

const databaseName = "tasks.db"

// Backend holds only a location, not an open database. Each operation closes its
// connections, including on cancellation and failed initialization.
type Backend struct{ path, prefix string }

// New constructs a managed source without creating storage.
func New(source task.RepositorySource) (Backend, error) {
	if !source.MaintenanceOwned {
		return Backend{}, errors.New("gig supports only managed storage")
	}
	if !filepath.IsAbs(source.BackendDir) || strings.ContainsAny(source.BackendDir, "%?#") || strings.TrimSpace(source.Repository.TaskIDPrefix) == "" {
		return Backend{}, errors.New("gig requires an absolute storage directory without %, ?, or # and task prefix")
	}
	return Backend{path: filepath.Join(source.BackendDir, databaseName), prefix: source.Repository.TaskIDPrefix}, nil
}

// Initialize creates a new owned store. Existing directories are never adopted
// or removed. The caller must save registration only after this succeeds.
func Initialize(dir, prefix string) (err error) {
	// sdk.Open concatenates a file URI without escaping the path. Reject URI
	// metacharacters before filesystem changes so it opens only the literal file.
	if !filepath.IsAbs(dir) || strings.ContainsAny(dir, "%?#") || strings.TrimSpace(prefix) == "" {
		return errors.New("initialize gig: absolute directory without %, ?, or # and task prefix are required")
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return fmt.Errorf("create managed gig directory: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(dir))
		}
	}()
	path := filepath.Join(dir, databaseName)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	store, err := sdk.Open(path, sdk.WithPrefix(prefix))
	if err != nil {
		return fmt.Errorf("initialize gig: %w", err)
	}
	return store.Close()
}

// withStore guards existing managed storage before allowing SDK-owned opening
// and migrations. External replacement between the check and Open is unsupported.
func (b Backend) withStore(ctx context.Context, run func(*sdk.Store) error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.ContainsAny(b.path, "%?#") {
		return errors.New("gig storage path must not contain %, ?, or #")
	}
	info, err := os.Lstat(b.path)
	if err != nil {
		return fmt.Errorf("inspect gig store %q: %w", b.path, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return fmt.Errorf("gig store %q must be a nonempty regular file", b.path)
	}
	store, err := sdk.Open(b.path, sdk.WithPrefix(b.prefix))
	if err != nil {
		return fmt.Errorf("open gig store %q: %w", b.path, err)
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	if err := ctx.Err(); err != nil {
		return err
	}
	return run(store)
}
