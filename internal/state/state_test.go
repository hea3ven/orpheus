//nolint:testpackage // OS contracts exercise private state persistence boundaries.
package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
)

type sampleState struct {
	Name   string            `yaml:"name"`
	Count  int               `yaml:"count"`
	Labels map[string]string `yaml:"labels"`
}

func TestResolveUsesXDGRoots(t *testing.T) {
	paths, err := Resolve(ResolveOptions{
		HomeDir: "/home/tester",
		Env: map[string]string{
			"XDG_CONFIG_HOME": "/fixture/xdg-config",
			"XDG_DATA_HOME":   "/fixture/xdg-data",
		},
	})
	if err != nil {
		t.Fatalf("resolve paths: %v", err)
	}

	assertPropagatedRoots(t, paths, "/fixture/xdg-config", "/fixture/xdg-data")
}

func TestResolveFallsBackToHome(t *testing.T) {
	paths, err := Resolve(ResolveOptions{HomeDir: "/home/tester"})
	if err != nil {
		t.Fatalf("resolve paths: %v", err)
	}

	assertPropagatedRoots(
		t,
		paths,
		filepath.Join("/home/tester", ".config"),
		filepath.Join("/home/tester", ".local", "share"),
	)
}

func TestResolveAllowsXDGWithoutHome(t *testing.T) {
	paths, err := Resolve(ResolveOptions{
		Env: map[string]string{
			"XDG_CONFIG_HOME": "/fixture/xdg-config",
			"XDG_DATA_HOME":   "/fixture/xdg-data",
		},
	})
	if err != nil {
		t.Fatalf("resolve paths without home: %v", err)
	}
	assertPropagatedRoots(t, paths, "/fixture/xdg-config", "/fixture/xdg-data")
}

func TestResolveRejectsRelativeInputs(t *testing.T) {
	tests := []struct {
		name string
		opts ResolveOptions
		want string
	}{
		{
			name: "relative XDG config",
			opts: ResolveOptions{HomeDir: "/home/tester", Env: map[string]string{"XDG_CONFIG_HOME": "relative"}},
			want: "XDG_CONFIG_HOME must be an absolute path",
		},
		{
			name: "relative XDG data",
			opts: ResolveOptions{HomeDir: "/home/tester", Env: map[string]string{"XDG_DATA_HOME": "relative"}},
			want: "XDG_DATA_HOME must be an absolute path",
		},
		{
			name: "relative home fallback",
			opts: ResolveOptions{HomeDir: "home/tester"},
			want: "home directory must be an absolute path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Resolve(tt.opts)
			if err == nil {
				t.Fatal("resolve paths succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func TestPathConstructorsRejectRelativeRoots(t *testing.T) {
	constructors := map[string]func(string, string) (Paths, error){
		"OS":     NewPaths,
		"memory": NewMemoryPaths,
	}

	for name, constructor := range constructors {
		t.Run(name, func(t *testing.T) {
			if _, err := constructor("relative-config", "/fixture/data"); err == nil {
				t.Fatal("constructor accepted relative config root, want error")
			}
			if _, err := constructor("/fixture/config", "relative-data"); err == nil {
				t.Fatal("constructor accepted relative data root, want error")
			}
		})
	}
}

func TestDataPathResolvesRelativePath(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	paths, err := NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}

	dataPath, err := paths.DataPath(filepath.Join("runs", "task-1", "run.yaml"))
	if err != nil {
		t.Fatalf("data path: %v", err)
	}
	wantData := filepath.Join(root, "data", "runs", "task-1", "run.yaml")
	if dataPath != wantData {
		t.Fatalf("data path = %q, want %q", dataPath, wantData)
	}
}

func TestRelativePathHelpersRejectEscapes(t *testing.T) {
	paths := newTestPaths(t)

	tests := []string{
		filepath.Join("..", "outside.yaml"),
		filepath.Join("nested", "..", "..", "outside.yaml"),
		filepath.Join(string(os.PathSeparator), "tmp", "outside.yaml"),
	}

	for _, rel := range tests {
		t.Run(rel, func(t *testing.T) {
			var config sampleState
			if err := paths.ReadConfigYAML(rel, &config); err == nil {
				t.Fatal("config read succeeded, want path error")
			}
			if _, err := paths.DataPath(rel); err == nil {
				t.Fatal("data path succeeded, want error")
			}
		})
	}
}

func TestYAMLHelpersRoundTripConfigAndData(t *testing.T) {
	paths := newTestPaths(t)
	want := sampleState{Name: "example", Count: 2, Labels: map[string]string{"role": "test"}}

	if err := writeConfigYAML(paths, filepath.Join("nested", "config.yaml"), want); err != nil {
		t.Fatalf("write config YAML: %v", err)
	}
	var gotConfig sampleState
	if err := paths.ReadConfigYAML(filepath.Join("nested", "config.yaml"), &gotConfig); err != nil {
		t.Fatalf("read config YAML: %v", err)
	}
	assertSampleState(t, gotConfig, want)

	if err := paths.WriteDataYAML(filepath.Join("runs", "run.yaml"), want); err != nil {
		t.Fatalf("write data YAML: %v", err)
	}
	var gotData sampleState
	if err := paths.ReadDataYAML(filepath.Join("runs", "run.yaml"), &gotData); err != nil {
		t.Fatalf("read data YAML: %v", err)
	}
	assertSampleState(t, gotData, want)
}

func TestReadYAMLMissingFileIsActionable(t *testing.T) {
	paths := newTestPaths(t)

	var got sampleState
	err := paths.ReadConfigYAML("missing.yaml", &got)
	if err == nil {
		t.Fatal("read missing YAML succeeded, want error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error does not wrap os.ErrNotExist: %v", err)
	}
	if !strings.Contains(err.Error(), "missing.yaml") || !strings.Contains(err.Error(), "file does not exist") {
		t.Fatalf("error is not actionable: %v", err)
	}
}

func TestReadYAMLMalformedFileIsActionable(t *testing.T) {
	fixture := newTestPathsFixture(t)
	paths := fixture.paths
	path := filepath.Join(fixture.configRoot, "bad.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("name: [unterminated\n"), 0o644); err != nil {
		t.Fatalf("write malformed YAML: %v", err)
	}

	var got sampleState
	err := paths.ReadConfigYAML("bad.yaml", &got)
	if err == nil {
		t.Fatal("read malformed YAML succeeded, want error")
	}
	if !strings.Contains(err.Error(), "parse config YAML") || !strings.Contains(err.Error(), "bad.yaml") {
		t.Fatalf("error is not actionable: %v", err)
	}
}

func TestWriteYAMLFailureLeavesExistingTargetIntact(t *testing.T) {
	fixture := newTestPathsFixture(t)
	paths := fixture.paths
	initial := sampleState{Name: "safe", Count: 1}
	if err := writeConfigYAML(paths, "config.yaml", initial); err != nil {
		t.Fatalf("write initial YAML: %v", err)
	}
	path := filepath.Join(fixture.configRoot, "config.yaml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	err = writeConfigYAML(paths, "config.yaml", map[string]any{"bad": make(chan int)})
	if err == nil {
		t.Fatal("write unsupported YAML succeeded, want error")
	}
	if !strings.Contains(err.Error(), "encode config YAML") {
		t.Fatalf("error is not actionable: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("target changed after failed write:\nbefore:%s\nafter:%s", before, after)
	}
}

func TestOSWriteYAMLUsesStateFileAndDirectoryPermissions(t *testing.T) {
	fixture := newTestPathsFixture(t)
	paths := fixture.paths

	if err := writeConfigYAML(paths, filepath.Join("nested", "config.yaml"), sampleState{Name: "example"}); err != nil {
		t.Fatalf("write config YAML: %v", err)
	}

	path := filepath.Join(fixture.configRoot, "nested", "config.yaml")
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config YAML: %v", err)
	}
	if got, want := fileInfo.Mode().Perm(), os.FileMode(0o644); got != want {
		t.Fatalf("config YAML permissions = %o, want %o", got, want)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat config directory: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got&0o700 != 0o700 || got&^os.FileMode(0o755) != 0 {
		t.Fatalf("config directory permissions = %o, want owner rwx and no bits beyond 755", got)
	}
}

func TestOSWriteYAMLAtomicReplacementFailurePreservesTarget(t *testing.T) {
	fixture := newTestPathsFixture(t)
	paths := fixture.paths
	path := filepath.Join(fixture.configRoot, "config.yaml")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("create target directory: %v", err)
	}
	marker := filepath.Join(path, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o644); err != nil {
		t.Fatalf("write target marker: %v", err)
	}

	err := writeConfigYAML(paths, "config.yaml", sampleState{Name: "replacement"})

	if err == nil || !strings.Contains(err.Error(), "write config YAML") {
		t.Fatalf("atomic replacement error = %v, want write error", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read preserved target marker: %v", err)
	}
	if string(data) != "keep" {
		t.Fatalf("target marker = %q, want keep", data)
	}
	matches, err := filepath.Glob(filepath.Join(fixture.configRoot, ".config.yaml.tmp-*"))
	if err != nil {
		t.Fatalf("glob temporary files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files after failed replacement = %v, want none", matches)
	}
}

func TestWriteYAMLParentCreationFailureIsActionable(t *testing.T) {
	fixture := newTestPathsFixture(t)
	paths := fixture.paths
	blockingFile := filepath.Join(fixture.configRoot, "blocked")
	if err := os.MkdirAll(fixture.configRoot, 0o755); err != nil {
		t.Fatalf("mkdir config root: %v", err)
	}
	if err := os.WriteFile(blockingFile, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	err := writeConfigYAML(paths, filepath.Join("blocked", "config.yaml"), sampleState{Name: "example"})
	if err == nil {
		t.Fatal("write YAML succeeded, want error")
	}
	if !strings.Contains(err.Error(), "create parent directory") || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("error is not actionable: %v", err)
	}
}

func TestWithGlobalMutationLockRunsAndReleases(t *testing.T) {
	paths := newTestPaths(t)
	lockPath, err := paths.DataPath(filepath.Join("locks", "mutation.lock"))
	if err != nil {
		t.Fatalf("global mutation lock path: %v", err)
	}

	var lockExisted bool
	err = WithGlobalMutationLock(paths, "test mutation", func() error {
		info, statErr := os.Stat(lockPath)
		lockExisted = statErr == nil
		if statErr == nil {
			mode := info.Mode().Perm()
			if mode&0o600 != 0o600 || mode&^os.FileMode(0o644) != 0 {
				return fmt.Errorf("lock permissions = %o, want owner rw and no bits beyond 644", mode)
			}
		}
		return statErr
	})
	if err != nil {
		t.Fatalf("with global mutation lock: %v", err)
	}
	if !lockExisted {
		t.Fatal("lock file did not exist while callback ran")
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file still exists after release: %v", err)
	}
}

func TestWithGlobalMutationLockReleasesAfterCallbackError(t *testing.T) {
	paths := newTestPaths(t)
	lockPath, err := paths.DataPath(filepath.Join("locks", "mutation.lock"))
	if err != nil {
		t.Fatalf("global mutation lock path: %v", err)
	}
	wantErr := errors.New("mutation failed")

	err = WithGlobalMutationLock(paths, "test mutation", func() error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock file still exists after callback error: %v", err)
	}
}

func TestWithGlobalMutationLockFailsFastOnContention(t *testing.T) {
	paths := newTestPaths(t)
	lockPath, err := paths.DataPath(filepath.Join("locks", "mutation.lock"))
	if err != nil {
		t.Fatalf("global mutation lock path: %v", err)
	}

	err = WithGlobalMutationLock(paths, "outer mutation", func() error {
		err := WithGlobalMutationLock(paths, "inner mutation", func() error {
			t.Fatal("contended mutation callback ran")
			return nil
		})
		if err == nil {
			t.Fatal("contended lock acquisition succeeded, want error")
		}
		var acquisitionErr *LockAcquisitionError
		if !errors.As(err, &acquisitionErr) {
			t.Fatalf("error type = %T, want *LockAcquisitionError", err)
		}
		if acquisitionErr.Path != lockPath {
			t.Fatalf("lock path = %q, want %q", acquisitionErr.Path, lockPath)
		}
		if !strings.Contains(err.Error(), "failed to acquire lock for inner mutation: "+lockPath) {
			t.Fatalf("error is not actionable: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("outer lock: %v", err)
	}
}

type testPathsFixture struct {
	paths      Paths
	configRoot string
}

func newTestPaths(t *testing.T) Paths {
	t.Helper()
	return newTestPathsFixture(t).paths
}

func newTestPathsFixture(t *testing.T) testPathsFixture {
	t.Helper()

	root := testutil.CanonicalTempDir(t)
	configRoot := filepath.Join(root, "config")
	paths, err := NewPaths(configRoot, filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	return testPathsFixture{paths: paths, configRoot: configRoot}
}

func assertPropagatedRoots(t *testing.T, paths Paths, wantConfig, wantData string) {
	t.Helper()

	environment := map[string]string{"PRESERVED": "value"}
	paths.PropagateEnvironment(environment)
	if got := environment["XDG_CONFIG_HOME"]; got != wantConfig {
		t.Fatalf("XDG_CONFIG_HOME = %q, want %q", got, wantConfig)
	}
	if got := environment["XDG_DATA_HOME"]; got != wantData {
		t.Fatalf("XDG_DATA_HOME = %q, want %q", got, wantData)
	}
	if got := environment["PRESERVED"]; got != "value" {
		t.Fatalf("unrelated environment = %q, want preserved value", got)
	}
}

func assertSampleState(t *testing.T, got, want sampleState) {
	t.Helper()
	if got.Name != want.Name || got.Count != want.Count || got.Labels["role"] != want.Labels["role"] {
		t.Fatalf("state = %#v, want %#v", got, want)
	}
}
