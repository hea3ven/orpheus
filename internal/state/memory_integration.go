//go:build integration

package state

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// NewMemoryPaths validates already-resolved Orpheus roots and returns isolated,
// in-process state for integration fixtures. It is excluded from normal builds.
// Paths copied from the returned value share that state.
func NewMemoryPaths(configRoot, dataRoot string) (Paths, error) {
	return newPaths(configRoot, dataRoot, newMemoryBackend())
}

type memoryBackend struct {
	mu    sync.Mutex
	files map[string][]byte
	locks map[string]struct{}
	dirs  map[string]struct{}
}

func newMemoryBackend() *memoryBackend {
	return &memoryBackend{
		files: make(map[string][]byte),
		locks: make(map[string]struct{}),
		dirs:  make(map[string]struct{}),
	}
}

func (b *memoryBackend) readFile(path string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	data, ok := b.files[path]
	if !ok {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
	}
	return append([]byte(nil), data...), nil
}

func (b *memoryBackend) listFiles(path string) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.dirs[path]; !ok {
		return nil, &os.PathError{Op: "readdir", Path: path, Err: os.ErrNotExist}
	}
	names := []string{}
	for file := range b.files {
		if filepath.Dir(file) == path {
			names = append(names, filepath.Base(file))
		}
	}
	for lock := range b.locks {
		if filepath.Dir(lock) == path {
			names = append(names, filepath.Base(lock))
		}
	}
	sort.Strings(names)
	return names, nil
}

func (b *memoryBackend) makeParentDirs(path string, _ os.FileMode) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recordParentDirs(path)
	return nil
}

// recordParentDirs requires b.mu to be held.
func (b *memoryBackend) recordParentDirs(path string) {
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		b.dirs[dir] = struct{}{}
		if parent := filepath.Dir(dir); parent == dir {
			return
		}
	}
}

func (b *memoryBackend) replaceFile(path string, data []byte, _ os.FileMode) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.files[path] = append([]byte(nil), data...)
	return nil
}

func (b *memoryBackend) acquireLock(path string, _, _ os.FileMode) (func() error, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, exists := b.locks[path]; exists {
		return nil, &os.PathError{Op: "open", Path: path, Err: os.ErrExist}
	}
	b.recordParentDirs(path)
	b.locks[path] = struct{}{}

	return func() error {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.locks, path)
		return nil
	}, nil
}
