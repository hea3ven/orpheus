package review

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func restoreSnapshotIndex(snapshot candidateSnapshot) (err error) {
	// Use Git's own index-lock protocol and atomically install the original
	// bytes, including index flags and staged content.
	lockPath := snapshot.indexPath + ".lock"
	file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("lock review index for restoration: %w", err)
	}
	defer func() {
		if removeErr := os.Remove(lockPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	_, writeErr := file.Write(snapshot.index)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("restore review index: %w", err)
	}
	if err := os.Rename(lockPath, snapshot.indexPath); err != nil {
		return fmt.Errorf("install restored review index: %w", err)
	}
	return nil
}

func snapshotPath(workdir, path string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(path))
	if !filepath.IsAbs(workdir) || filepath.Clean(workdir) != workdir {
		return "", fmt.Errorf("review requires a clean absolute work directory")
	}
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe review path %q", path)
	}
	full := filepath.Join(workdir, clean)
	for parent := filepath.Dir(full); parent != workdir; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspect review path %q parent: %w", path, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("review path %q has unsafe parent %q", path, parent)
		}
	}
	return full, nil
}
