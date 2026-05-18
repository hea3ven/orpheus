package state_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/state"
)

type sampleState struct {
	Name   string            `yaml:"name"`
	Count  int               `yaml:"count"`
	Labels map[string]string `yaml:"labels"`
}

func TestResolveUsesXDGRoots(t *testing.T) {
	paths, err := state.Resolve(state.ResolveOptions{
		HomeDir: "/home/tester",
		Env: map[string]string{
			"XDG_CONFIG_HOME": "/tmp/xdg-config",
			"XDG_DATA_HOME":   "/tmp/xdg-data",
		},
	})
	if err != nil {
		t.Fatalf("resolve paths: %v", err)
	}

	wantConfig := filepath.Join("/tmp/xdg-config", state.AppName)
	wantData := filepath.Join("/tmp/xdg-data", state.AppName)
	if paths.ConfigRoot != wantConfig {
		t.Fatalf("config root = %q, want %q", paths.ConfigRoot, wantConfig)
	}
	if paths.DataRoot != wantData {
		t.Fatalf("data root = %q, want %q", paths.DataRoot, wantData)
	}
}

func TestResolveFallsBackToHome(t *testing.T) {
	paths, err := state.Resolve(state.ResolveOptions{HomeDir: "/home/tester"})
	if err != nil {
		t.Fatalf("resolve paths: %v", err)
	}

	wantConfig := filepath.Join("/home/tester", ".config", state.AppName)
	wantData := filepath.Join("/home/tester", ".local", "share", state.AppName)
	if paths.ConfigRoot != wantConfig {
		t.Fatalf("config root = %q, want %q", paths.ConfigRoot, wantConfig)
	}
	if paths.DataRoot != wantData {
		t.Fatalf("data root = %q, want %q", paths.DataRoot, wantData)
	}
}

func TestResolveAllowsXDGWithoutHome(t *testing.T) {
	paths, err := state.Resolve(state.ResolveOptions{
		Env: map[string]string{
			"XDG_CONFIG_HOME": "/tmp/xdg-config",
			"XDG_DATA_HOME":   "/tmp/xdg-data",
		},
	})
	if err != nil {
		t.Fatalf("resolve paths without home: %v", err)
	}
	if paths.ConfigRoot != filepath.Join("/tmp/xdg-config", state.AppName) {
		t.Fatalf("config root = %q", paths.ConfigRoot)
	}
}

func TestResolveRejectsRelativeInputs(t *testing.T) {
	tests := []struct {
		name string
		opts state.ResolveOptions
		want string
	}{
		{
			name: "relative XDG config",
			opts: state.ResolveOptions{HomeDir: "/home/tester", Env: map[string]string{"XDG_CONFIG_HOME": "relative"}},
			want: "XDG_CONFIG_HOME must be an absolute path",
		},
		{
			name: "relative XDG data",
			opts: state.ResolveOptions{HomeDir: "/home/tester", Env: map[string]string{"XDG_DATA_HOME": "relative"}},
			want: "XDG_DATA_HOME must be an absolute path",
		},
		{
			name: "relative home fallback",
			opts: state.ResolveOptions{HomeDir: "home/tester"},
			want: "home directory must be an absolute path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := state.Resolve(tt.opts)
			if err == nil {
				t.Fatal("resolve paths succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want substring %q", err, tt.want)
			}
		})
	}
}

func TestNewPathsRejectsRelativeRoots(t *testing.T) {
	if _, err := state.NewPaths("relative-config", "/tmp/data"); err == nil {
		t.Fatal("NewPaths accepted relative config root, want error")
	}
	if _, err := state.NewPaths("/tmp/config", "relative-data"); err == nil {
		t.Fatal("NewPaths accepted relative data root, want error")
	}
}

func TestRelativePathHelpers(t *testing.T) {
	paths := newTestPaths(t)
	root := filepath.Dir(paths.ConfigRoot)

	configPath, err := paths.ConfigPath(filepath.Join("prompts", "implementation.md"))
	if err != nil {
		t.Fatalf("config path: %v", err)
	}
	wantConfig := filepath.Join(root, "config", "prompts", "implementation.md")
	if configPath != wantConfig {
		t.Fatalf("config path = %q, want %q", configPath, wantConfig)
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
			if _, err := paths.ConfigPath(rel); err == nil {
				t.Fatal("config path succeeded, want error")
			}
			if _, err := paths.DataPath(rel); err == nil {
				t.Fatal("data path succeeded, want error")
			}
		})
	}
}

func TestEnsureDirectoriesCreatesOnDemand(t *testing.T) {
	paths := newTestPaths(t)

	configDir, err := paths.EnsureConfigDir(filepath.Join("prompts", "nested"))
	if err != nil {
		t.Fatalf("ensure config dir: %v", err)
	}
	if info, err := os.Stat(configDir); err != nil || !info.IsDir() {
		t.Fatalf("config dir was not created: info=%v err=%v", info, err)
	}

	dataDir, err := paths.EnsureDataDir(filepath.Join("runs", "task-1"))
	if err != nil {
		t.Fatalf("ensure data dir: %v", err)
	}
	if info, err := os.Stat(dataDir); err != nil || !info.IsDir() {
		t.Fatalf("data dir was not created: info=%v err=%v", info, err)
	}
}

func TestYAMLHelpersRoundTripConfigAndData(t *testing.T) {
	paths := newTestPaths(t)
	want := sampleState{Name: "example", Count: 2, Labels: map[string]string{"role": "test"}}

	if err := paths.WriteConfigYAML(filepath.Join("nested", "config.yaml"), want); err != nil {
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
	paths := newTestPaths(t)
	path, err := paths.ConfigPath("bad.yaml")
	if err != nil {
		t.Fatalf("config path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("name: [unterminated\n"), 0o644); err != nil {
		t.Fatalf("write malformed YAML: %v", err)
	}

	var got sampleState
	err = paths.ReadConfigYAML("bad.yaml", &got)
	if err == nil {
		t.Fatal("read malformed YAML succeeded, want error")
	}
	if !strings.Contains(err.Error(), "parse config YAML") || !strings.Contains(err.Error(), "bad.yaml") {
		t.Fatalf("error is not actionable: %v", err)
	}
}

func TestWriteYAMLFailureLeavesExistingTargetIntact(t *testing.T) {
	paths := newTestPaths(t)
	initial := sampleState{Name: "safe", Count: 1}
	if err := paths.WriteConfigYAML("config.yaml", initial); err != nil {
		t.Fatalf("write initial YAML: %v", err)
	}
	path, err := paths.ConfigPath("config.yaml")
	if err != nil {
		t.Fatalf("config path: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	err = paths.WriteConfigYAML("config.yaml", map[string]any{"bad": make(chan int)})
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

func TestWriteYAMLParentCreationFailureIsActionable(t *testing.T) {
	paths := newTestPaths(t)
	blockingFile := filepath.Join(paths.ConfigRoot, "blocked")
	if err := os.MkdirAll(paths.ConfigRoot, 0o755); err != nil {
		t.Fatalf("mkdir config root: %v", err)
	}
	if err := os.WriteFile(blockingFile, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write blocking file: %v", err)
	}

	err := paths.WriteConfigYAML(filepath.Join("blocked", "config.yaml"), sampleState{Name: "example"})
	if err == nil {
		t.Fatal("write YAML succeeded, want error")
	}
	if !strings.Contains(err.Error(), "create parent directory") || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("error is not actionable: %v", err)
	}
}

func newTestPaths(t *testing.T) state.Paths {
	t.Helper()

	root := t.TempDir()
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	return paths
}

func assertSampleState(t *testing.T, got, want sampleState) {
	t.Helper()
	if got.Name != want.Name || got.Count != want.Count || got.Labels["role"] != want.Labels["role"] {
		t.Fatalf("state = %#v, want %#v", got, want)
	}
}
