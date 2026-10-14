package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestInvocationDependenciesKeepEnvironmentScopedToInvocation(t *testing.T) {
	t.Parallel()

	root := testutil.CanonicalTempDir(t)
	configRoot := filepath.Join(root, "config", state.AppName)
	dataRoot := filepath.Join(root, "data", state.AppName)
	paths, err := state.NewPaths(configRoot, dataRoot)
	if err != nil {
		t.Fatalf("create paths: %v", err)
	}
	deps := newInvocationDependenciesWithPaths(paths, logging.Discard(), map[string]string{
		"CODEX_HOME": "/isolated/codex",
		"HOME":       "/isolated/home",
	})

	usage := deps.usageCaptureEnvironment()
	if usage["CODEX_HOME"] != "/isolated/codex" || usage["HOME"] != "/isolated/home" {
		t.Fatalf("usage environment = %#v, want invocation values", usage)
	}
	if deps.environmentValue("ORPHEUS_REPO_ID") != "" {
		t.Fatalf("unexpected process environment leaked into invocation")
	}

	environment := deps.invocationEnvironment([]string{"ORPHEUS_TASK_ID=op-1"})
	wantConfig := "XDG_CONFIG_HOME=" + filepath.Dir(configRoot)
	wantData := "XDG_DATA_HOME=" + filepath.Dir(dataRoot)
	if !containsEnvironment(environment, wantConfig) || !containsEnvironment(environment, wantData) {
		t.Fatalf("agent environment = %#v, want %q and %q", environment, wantConfig, wantData)
	}

}

func containsEnvironment(environment []string, want string) bool {
	for _, entry := range environment {
		if entry == want {
			return true
		}
	}
	return false
}

func TestInvocationSnapshotPreservesExplicitUsageRoots(t *testing.T) {
	want := map[string]string{"CODEX_HOME": "/isolated/codex", "HOME": "/isolated/home", "PI_CODING_AGENT_DIR": "/isolated/pi", "PI_CODING_AGENT_SESSION_DIR": "/isolated/pi/sessions", "ORPHEUS_REPO_ID": "process-repo"}
	for key, value := range want {
		t.Setenv(key, value)
	}

	snapshot := invocationEnvironmentSnapshot()

	for key, value := range want {
		if snapshot[key] != value {
			t.Fatalf("snapshot[%q] = %q, want %q", key, snapshot[key], value)
		}
	}
}

func TestInvocationScopedRepoListsRemainIsolatedDuringConcurrentExecution(t *testing.T) {
	type result struct {
		id, output string
		err        error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	for _, repoID := range []string{"alpha", "beta"} {
		root := testutil.CanonicalTempDir(t)
		paths, err := state.NewPaths(filepath.Join(root, "config", state.AppName), filepath.Join(root, "data", state.AppName))
		if err != nil {
			t.Fatal(err)
		}
		deps := newInvocationDependenciesWithPaths(paths, logging.Discard(), map[string]string{})
		if err := deps.registryStore.Save(registry.Registry{Repos: []registry.Repo{{ID: repoID, Name: repoID, Path: filepath.Join(root, "repo"), DefaultBranch: "main", BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "op"}}}); err != nil {
			t.Fatal(err)
		}
		command := newRootCommand(&rootOptions{logger: logging.Discard(), invocationDeps: deps})
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(new(bytes.Buffer))
		command.SetArgs([]string{"repo", "list"})
		go func() {
			<-start
			err := command.Execute()
			results <- result{id: repoID, output: output.String(), err: err}
		}()
	}
	close(start)
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatalf("repo %s: %v", got.id, got.err)
		}
		other := "alpha"
		if got.id == other {
			other = "beta"
		}
		if !strings.Contains(got.output, got.id) || strings.Contains(got.output, other) {
			t.Fatalf("repo %s output = %q, want own repository without %s", got.id, got.output, other)
		}
	}
}

func TestInvocationRejectsUnsupportedTaskSource(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	deps := newInvocationDependenciesWithPaths(paths, logging.Discard(), map[string]string{"PATH": "/nonexistent"})

	_, err = deps.taskBackendFactory(taskmodel.RepositorySource{Kind: "unsupported"})

	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported source error = %v", err)
	}
}
