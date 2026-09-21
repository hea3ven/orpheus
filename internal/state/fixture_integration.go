//go:build integration

package state

import (
	"errors"
	"path/filepath"
	"strings"
)

// SeedMemoryConfigYAML seeds configuration for cross-package integration fixtures.
// It is excluded from normal builds and refuses OS-backed paths.
func SeedMemoryConfigYAML(paths Paths, rel string, value any) error {
	if _, ok := paths.backend.(*memoryBackend); !ok {
		return errors.New("config fixture requires memory-backed paths")
	}
	path, err := paths.configPath(rel)
	if err != nil {
		return err
	}
	return writeYAML(paths.backend, "config", rel, path, value)
}

// SetMemoryDataWriteError injects failures at the storage boundary while keeping
// real serializers and stores in workflow tests. The callback receives a relative
// data path and a copy of the proposed bytes. It must not reenter these Paths.
// A nil callback clears the fault. OS-backed paths are refused.
func SetMemoryDataWriteError(paths Paths, fail func(string, []byte) error) error {
	backend, ok := paths.backend.(*memoryBackend)
	if !ok {
		return errors.New("write fault requires memory-backed paths")
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if fail == nil {
		backend.writeError = nil
		return nil
	}
	backend.writeError = func(path string, data []byte) error {
		rel, err := filepath.Rel(paths.dataRoot, path)
		if err != nil {
			return err
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
		return fail(rel, data)
	}
	return nil
}
