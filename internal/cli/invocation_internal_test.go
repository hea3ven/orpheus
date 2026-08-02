package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
)

func TestInvocationDependenciesKeepEnvironmentScopedToInvocation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	paths, err := state.NewPaths(filepath.Join(root, "config", state.AppName), filepath.Join(root, "data", state.AppName))
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
	wantConfig := "XDG_CONFIG_HOME=" + filepath.Dir(paths.ConfigRoot)
	wantData := "XDG_DATA_HOME=" + filepath.Dir(paths.DataRoot)
	if !containsEnvironment(environment, wantConfig) || !containsEnvironment(environment, wantData) {
		t.Fatalf("agent environment = %#v, want %q and %q", environment, wantConfig, wantData)
	}

}

func TestInvocationSnapshotUsesIsolatedUsageRoots(t *testing.T) {
	t.Parallel()

	snapshot := invocationEnvironmentSnapshot()
	for key, want := range agent.UsageCaptureEnvironment() {
		if got := snapshot[key]; got != want {
			t.Fatalf("snapshot[%q] = %q, want isolated usage root %q", key, got, want)
		}
	}
}

func TestInvocationScopedRepoListWorkflowsRunInParallel(t *testing.T) {
	for _, repoID := range []string{"alpha", "beta"} {
		t.Run(repoID, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			paths, err := state.NewPaths(filepath.Join(root, "config", state.AppName), filepath.Join(root, "data", state.AppName))
			if err != nil {
				t.Fatalf("create paths: %v", err)
			}
			deps := newInvocationDependenciesWithPaths(paths, logging.Discard(), map[string]string{})
			repoPath := filepath.Join(root, "repo")
			if err := deps.registryStore.Save(registry.Registry{Repos: []registry.Repo{{ID: repoID, Name: repoID, Path: repoPath, DefaultBranch: "main", BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "op"}}}); err != nil {
				t.Fatalf("save registry: %v", err)
			}

			command := newRootCommand(&rootOptions{logger: logging.Discard(), invocationDeps: deps})
			output := new(bytes.Buffer)
			command.SetOut(output)
			command.SetErr(new(bytes.Buffer))
			command.SetArgs([]string{"repo", "list"})
			if err := command.Execute(); err != nil {
				t.Fatalf("execute repo list: %v", err)
			}
			if !strings.Contains(output.String(), repoID) {
				t.Fatalf("repo list output = %q, want %q", output.String(), repoID)
			}
		})
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
