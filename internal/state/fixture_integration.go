//go:build integration

package state

import "errors"

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
