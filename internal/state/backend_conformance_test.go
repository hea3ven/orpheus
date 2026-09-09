//nolint:testpackage // Conformance setup seeds malformed input through each private backend.
package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
)

type conformanceState struct {
	Name  string `yaml:"name"`
	Count int    `yaml:"count"`
}

type backendFixture struct {
	paths    Paths
	writeRaw func(t *testing.T, config bool, rel string, data []byte)
}

func TestBackendsConformToFileAndLockBehavior(t *testing.T) {
	factories := map[string]func(t *testing.T) backendFixture{
		"memory": newMemoryBackendFixture,
		"OS":     newOSBackendFixture,
	}

	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			t.Run("YAML writes round trip under both roots", func(t *testing.T) {
				testBackendYAMLRoundTrip(t, factory(t))
			})
			t.Run("missing YAML wraps not-exist", func(t *testing.T) {
				testBackendMissingYAML(t, factory(t))
			})
			t.Run("malformed YAML reports parse error", func(t *testing.T) {
				testBackendMalformedYAML(t, factory(t))
			})
			t.Run("lock contention fails fast and release permits retry", func(t *testing.T) {
				testBackendLockContention(t, factory(t))
			})
		})
	}
}

func testBackendYAMLRoundTrip(t *testing.T, fixture backendFixture) {
	t.Helper()
	want := conformanceState{Name: "saved", Count: 2}

	if err := writeConfigYAML(fixture.paths, filepath.Join("nested", "config.yaml"), want); err != nil {
		t.Fatalf("write config YAML: %v", err)
	}
	if err := fixture.paths.WriteDataYAML(filepath.Join("nested", "data.yaml"), want); err != nil {
		t.Fatalf("write data YAML: %v", err)
	}

	var gotConfig conformanceState
	if err := fixture.paths.ReadConfigYAML(filepath.Join("nested", "config.yaml"), &gotConfig); err != nil {
		t.Fatalf("read config YAML: %v", err)
	}
	var gotData conformanceState
	if err := fixture.paths.ReadDataYAML(filepath.Join("nested", "data.yaml"), &gotData); err != nil {
		t.Fatalf("read data YAML: %v", err)
	}
	if gotConfig != want || gotData != want {
		t.Fatalf("round trip config=%#v data=%#v, want %#v", gotConfig, gotData, want)
	}
}

func testBackendMissingYAML(t *testing.T, fixture backendFixture) {
	t.Helper()
	var got conformanceState

	err := fixture.paths.ReadDataYAML("missing.yaml", &got)

	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read missing YAML error = %v, want os.ErrNotExist", err)
	}
	if !strings.Contains(err.Error(), "file does not exist") {
		t.Fatalf("read missing YAML error = %v, want actionable message", err)
	}
}

func testBackendMalformedYAML(t *testing.T, fixture backendFixture) {
	t.Helper()
	fixture.writeRaw(t, true, "bad.yaml", []byte("name: [unterminated\n"))
	var got conformanceState

	err := fixture.paths.ReadConfigYAML("bad.yaml", &got)

	if err == nil || !strings.Contains(err.Error(), "parse config YAML") {
		t.Fatalf("read malformed YAML error = %v, want config parse error", err)
	}
}

func testBackendLockContention(t *testing.T, fixture backendFixture) {
	t.Helper()
	lockPath, err := fixture.paths.DataPath(filepath.Join(globalMutationLockDir, globalMutationLockFile))
	if err != nil {
		t.Fatalf("resolve lock path: %v", err)
	}
	innerRan := false

	err = WithGlobalMutationLock(fixture.paths, "outer mutation", func() error {
		innerErr := WithGlobalMutationLock(fixture.paths, "inner mutation", func() error {
			innerRan = true
			return nil
		})
		var acquisitionErr *LockAcquisitionError
		if !errors.As(innerErr, &acquisitionErr) {
			t.Fatalf("contended lock error = %v, want *LockAcquisitionError", innerErr)
		}
		if acquisitionErr.Path != lockPath || !strings.Contains(innerErr.Error(), "failed to acquire lock for inner mutation: "+lockPath) {
			t.Fatalf("contended lock error = %v, want operation and path %q", innerErr, lockPath)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("outer lock: %v", err)
	}
	if innerRan {
		t.Fatal("contended lock callback ran")
	}
	if err := WithGlobalMutationLock(fixture.paths, "retry mutation", func() error { return nil }); err != nil {
		t.Fatalf("acquire released lock: %v", err)
	}
}

func newMemoryBackendFixture(t *testing.T) backendFixture {
	t.Helper()

	paths, err := NewMemoryPaths("/fixture/config", "/fixture/data")
	if err != nil {
		t.Fatalf("new memory paths: %v", err)
	}
	return backendFixture{
		paths: paths,
		writeRaw: func(t *testing.T, config bool, rel string, data []byte) {
			t.Helper()
			path, err := fixturePath(paths, config, rel)
			if err != nil {
				t.Fatalf("resolve raw fixture path: %v", err)
			}
			if err := paths.backend.replaceFile(path, data, fileMode); err != nil {
				t.Fatalf("write raw memory fixture: %v", err)
			}
		},
	}
}

func newOSBackendFixture(t *testing.T) backendFixture {
	t.Helper()

	root := testutil.CanonicalTempDir(t)
	paths, err := NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("new OS paths: %v", err)
	}
	return backendFixture{
		paths: paths,
		writeRaw: func(t *testing.T, config bool, rel string, data []byte) {
			t.Helper()
			path, err := fixturePath(paths, config, rel)
			if err != nil {
				t.Fatalf("resolve raw fixture path: %v", err)
			}
			if err := os.MkdirAll(filepath.Dir(path), directoryMode); err != nil {
				t.Fatalf("create raw fixture parent: %v", err)
			}
			if err := os.WriteFile(path, data, fileMode); err != nil {
				t.Fatalf("write raw OS fixture: %v", err)
			}
		},
	}
}

func writeConfigYAML(paths Paths, rel string, value any) error {
	path, err := paths.configPath(rel)
	if err != nil {
		return err
	}
	return writeYAML(paths.backend, "config", rel, path, value)
}

func fixturePath(paths Paths, config bool, rel string) (string, error) {
	if config {
		return paths.configPath(rel)
	}
	return paths.DataPath(rel)
}
