package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type backend interface {
	readFile(path string) ([]byte, error)
	makeParentDirs(path string, mode os.FileMode) error
	replaceFile(path string, data []byte, mode os.FileMode) error
	acquireLock(path string, directoryMode, fileMode os.FileMode) (func() error, error)
}

type osBackend struct{}

func (osBackend) readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (osBackend) makeParentDirs(path string, mode os.FileMode) error {
	return os.MkdirAll(filepath.Dir(path), mode)
}

func (osBackend) replaceFile(path string, data []byte, mode os.FileMode) error {
	return writeFileAtomically(path, data, mode)
}

func (osBackend) acquireLock(path string, dirMode, lockMode os.FileMode) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return nil, fmt.Errorf("create lock directory: %w", err)
	}

	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, lockMode)
	if err != nil {
		return nil, err
	}

	return func() error {
		var releaseErr error
		if err := file.Close(); err != nil {
			releaseErr = errors.Join(releaseErr, fmt.Errorf("close global mutation lock %s: %w", path, err))
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			releaseErr = errors.Join(releaseErr, fmt.Errorf("remove global mutation lock %s: %w", path, err))
		}
		return releaseErr
	}, nil
}

type memoryBackend struct {
	mu    sync.Mutex
	files map[string][]byte
	locks map[string]struct{}
}

func newMemoryBackend() *memoryBackend {
	return &memoryBackend{
		files: make(map[string][]byte),
		locks: make(map[string]struct{}),
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

func (*memoryBackend) makeParentDirs(string, os.FileMode) error {
	return nil
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
	b.locks[path] = struct{}{}

	return func() error {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.locks, path)
		return nil
	}, nil
}
