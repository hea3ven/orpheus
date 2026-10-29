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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sampleState struct {
	Name   string            `yaml:"name"`
	Count  int               `yaml:"count"`
	Labels map[string]string `yaml:"labels"`
}

func TestResolvePropagatesConfiguredAndFallbackRoots(t *testing.T) {
	for _, tt := range []struct {
		name         string
		opts         ResolveOptions
		config, data string
		wantError    string
	}{
		{name: "explicit XDG", opts: ResolveOptions{HomeDir: "/home/tester", Env: map[string]string{"XDG_CONFIG_HOME": "/fixture/xdg-config", "XDG_DATA_HOME": "/fixture/xdg-data"}}, config: "/fixture/xdg-config", data: "/fixture/xdg-data"},
		{name: "home fallback", opts: ResolveOptions{HomeDir: "/home/tester"}, config: "/home/tester/.config", data: "/home/tester/.local/share"},
		{name: "XDG without home", opts: ResolveOptions{Env: map[string]string{"XDG_CONFIG_HOME": "/fixture/xdg-config", "XDG_DATA_HOME": "/fixture/xdg-data"}}, config: "/fixture/xdg-config", data: "/fixture/xdg-data"},
		{name: "relative XDG config", opts: ResolveOptions{HomeDir: "/home/tester", Env: map[string]string{"XDG_CONFIG_HOME": "relative"}}, wantError: "XDG_CONFIG_HOME must be an absolute path"},
		{name: "relative XDG data", opts: ResolveOptions{HomeDir: "/home/tester", Env: map[string]string{"XDG_DATA_HOME": "relative"}}, wantError: "XDG_DATA_HOME must be an absolute path"},
		{name: "relative fallback", opts: ResolveOptions{HomeDir: "home/tester"}, wantError: "home directory must be an absolute path"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			paths, err := Resolve(tt.opts)
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
			assertPropagatedRoots(t, paths, tt.config, tt.data)
		})
	}
}

func TestNewPathsRejectsRelativeRoots(t *testing.T) {
	_, configErr := NewPaths("relative-config", "/fixture/data")
	_, dataErr := NewPaths("/fixture/config", "relative-data")

	assert.Error(t, configErr)
	assert.Error(t, dataErr)
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
			assert.Error(t, paths.CheckDataTree(rel))
			assert.Error(t, paths.RemoveDataTree(rel))
		})
	}
	for _, rel := range []string{"", ".", "nested/.."} {
		assert.ErrorContains(t, paths.CheckDataTree(rel), "refusing to remove the Orpheus data root")
		assert.ErrorContains(t, paths.RemoveDataTree(rel), "refusing to remove the Orpheus data root")
	}
}

func TestYAMLHelpersRoundTripConfigAndData(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	paths, err := NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	require.NoError(t, err)
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
	dataPath, err := paths.DataPath(filepath.Join("runs", "run.yaml"))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "data", "runs", "run.yaml"), dataPath)
	_, err = os.Stat(dataPath)
	require.NoError(t, err)
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
	for name, text := range map[string]string{
		"malformed first document":    "name: [unterminated\n",
		"second document":             "name: first\n---\nname: second\n",
		"malformed trailing document": "name: first\n---\nname: [unterminated\n",
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, os.WriteFile(path, []byte(text), 0o644))
			var got sampleState
			err := paths.ReadConfigYAML("bad.yaml", &got)
			require.Error(t, err, "ambiguous YAML must not silently select its first document")
			assert.ErrorContains(t, err, "parse")
			assert.ErrorContains(t, err, "bad.yaml")
		})
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

func TestWithGlobalMutationLockReleasesAfterCallback(t *testing.T) {
	for _, callbackError := range []error{nil, errors.New("mutation failed")} {
		t.Run(fmt.Sprint(callbackError), func(t *testing.T) {
			paths := newTestPaths(t)
			lockPath, err := paths.DataPath(filepath.Join("locks", "mutation.lock"))
			require.NoError(t, err)
			called := false

			err = WithGlobalMutationLock(paths, "test mutation", func() error {
				called = true
				info, statErr := os.Stat(lockPath)
				require.NoError(t, statErr)
				mode := info.Mode().Perm()
				if mode&0o600 != 0o600 || mode&^os.FileMode(0o644) != 0 {
					t.Fatalf("lock permissions = %o", mode)
				}
				return callbackError
			})

			require.ErrorIs(t, err, callbackError)
			assert.True(t, called)
			_, statErr := os.Stat(lockPath)
			require.ErrorIs(t, statErr, os.ErrNotExist)
		})
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

func TestDataLockProtectsOneDocumentIndependentlyAndReleasesAfterFailure(t *testing.T) {
	paths := newTestPaths(t)
	failure := errors.New("save refused")
	called := false
	err := WithGlobalMutationLock(paths, "progress workflow", func() error {
		return paths.WithDataLock("tasks/one.lock", func() error {
			err := paths.WithDataLock("tasks/one.lock", func() error { called = true; return nil })
			require.ErrorIs(t, err, os.ErrExist)
			require.NoError(t, paths.WithDataLock("tasks/two.lock", func() error { return nil }))
			return failure
		})
	})
	require.ErrorIs(t, err, failure)
	assert.False(t, called, "the contending writer must not run")
	require.NoError(t, paths.WithDataLock("tasks/one.lock", func() error { called = true; return nil }))
	assert.True(t, called, "failed operations must release their document lock")
}

func TestListDataFilesReturnsSortedDirectFiles(t *testing.T) {
	paths := newTestPaths(t)
	for _, rel := range []string{"inventory/z.yaml", "inventory/a.yaml", "inventory/nested.yaml/child.yaml"} {
		require.NoError(t, paths.WriteDataYAML(rel, sampleState{Name: rel}))
	}

	names, err := paths.ListDataFiles("inventory")

	require.NoError(t, err)
	assert.Equal(t, []string{"a.yaml", "z.yaml"}, names)
}

func TestListDataFilesRejectsMissingDirectoriesAndPathTraversal(t *testing.T) {
	paths := newTestPaths(t)

	_, missingErr := paths.ListDataFiles("missing")
	_, escapeErr := paths.ListDataFiles("../escape")

	assert.ErrorIs(t, missingErr, os.ErrNotExist)
	assert.Error(t, escapeErr)
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

func writeConfigYAML(paths Paths, rel string, value any) error {
	path, err := paths.configPath(rel)
	if err != nil {
		return err
	}
	return writeYAML(paths.backend, "config", rel, path, value)
}
