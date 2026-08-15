package testguard

import (
	"os"
	"path/filepath"
	"time"
)

const executableWriteCooldown = 5 * time.Millisecond

// WriteExecutable atomically publishes executable fixture content after its mode
// and complete contents are in place.
func WriteExecutable(path string, content []byte) (err error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = temporary.Close()
			_ = os.Remove(temporary.Name())
		}
	}()

	if err := temporary.Chmod(0o755); err != nil {
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}

	// Some filesystems briefly retain an executable-write exclusion after Close.
	// Let it clear before publishing the fixture for a concurrently running test.
	time.Sleep(executableWriteCooldown)
	return os.Rename(temporary.Name(), path)
}
