package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type backend interface {
	readFile(path string) ([]byte, error)
	listFiles(path string) ([]string, error)
	makeParentDirs(path string, mode os.FileMode) error
	replaceFile(path string, data []byte, mode os.FileMode) error
	acquireLock(path string, directoryMode, fileMode os.FileMode) (func() error, error)
}

type osBackend struct{}

func (osBackend) readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// listFiles returns sorted names of direct non-directory entries.
func (osBackend) listFiles(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names, nil
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
