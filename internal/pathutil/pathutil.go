// Package pathutil provides shared filesystem path normalization helpers.
package pathutil

import (
	"path/filepath"
)

// CanonicalAbs returns an absolute, clean path with symlinked existing path
// components resolved. If the final path does not exist yet, the deepest
// existing ancestor is resolved and the missing suffix is reattached.
func CanonicalAbs(path string) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return canonicalClean(absolutePath), nil
}

func canonicalClean(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}

	missing := []string{}
	current := path
	for {
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		missing = append(missing, filepath.Base(current))
		current = parent
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved)
		}
	}
}
