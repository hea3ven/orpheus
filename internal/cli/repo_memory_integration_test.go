//go:build integration

//nolint:testpackage // Invocation-scoped collaborators require internal composition wiring.
package cli

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/beads"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testutil"
)

const (
	memoryConfigRoot = "/fixture/orpheus-memory/xdg-config/orpheus"
	memoryDataRoot   = "/fixture/orpheus-memory/xdg-data/orpheus"
	noExecutablePath = "/nonexistent"
)

func TestIntegrationRepoAddAndListUseMemoryStateWithoutExecutables(t *testing.T) {
	t.Setenv("PATH", noExecutablePath)
	fixture := newRepoMemoryFixture(t)

	addOutput, addStderr := fixture.execute("repo", "add", fixture.repoPath)
	listOutput, listStderr := fixture.execute("repo", "list")

	fixture.assertAddOutput(addOutput)
	fixture.assertEmptyStderr("repo add", addStderr)
	fixture.assertListOutput(listOutput)
	fixture.assertEmptyStderr("repo list", listStderr)
	fixture.assertPersistedRepo()
	fixture.assertSemanticCalls()
	fixture.assertNoStateOnDisk()
}

type repoMemoryFixture struct {
	t                    *testing.T
	paths                state.Paths
	deps                 *invocationDependencies
	repoPath             string
	gitInspections       int
	beadsInspections     int
	beadsInitializations int
	initializedDir       string
}

func newRepoMemoryFixture(t *testing.T) *repoMemoryFixture {
	t.Helper()

	paths, err := state.NewMemoryPaths(memoryConfigRoot, memoryDataRoot)
	if err != nil {
		t.Fatalf("create memory paths: %v", err)
	}
	workingRoot := testutil.CanonicalTempDir(t)
	repoPath := filepath.Join(workingRoot, "alpha")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("create repository working tree: %v", err)
	}
	fixture := &repoMemoryFixture{
		t:        t,
		paths:    paths,
		deps:     newInvocationDependenciesWithPaths(paths, logging.Discard(), map[string]string{"PATH": noExecutablePath}),
		repoPath: repoPath,
	}
	fixture.installSemanticCollaborators()
	return fixture
}

func (f *repoMemoryFixture) installSemanticCollaborators() {
	f.deps.inspectGit = func(_ context.Context, path string) (gitmeta.Inspection, error) {
		f.gitInspections++
		if path != f.repoPath {
			f.t.Fatalf("Git inspection path = %q, want %q", path, f.repoPath)
		}
		return gitmeta.Inspection{
			Root:                   f.repoPath,
			RemoteCandidate:        "git@example.com:org/alpha.git",
			RemoteCandidateName:    "origin",
			DefaultBranchCandidate: "main",
			DefaultBranchSource:    gitmeta.DefaultBranchSourceOriginHEAD,
		}, nil
	}
	f.deps.inspectLocalBeads = func(path string, _ ...slog.Attr) (beads.LocalInspection, error) {
		f.beadsInspections++
		if path != f.repoPath {
			f.t.Fatalf("Beads inspection path = %q, want %q", path, f.repoPath)
		}
		return beads.LocalInspection{}, beads.ErrNoLocal
	}
	f.deps.initializeBeads = func(path string, prefix string, _ ...slog.Attr) error {
		f.beadsInitializations++
		f.initializedDir = path
		if prefix != "alpha" {
			f.t.Fatalf("managed Beads prefix = %q, want alpha", prefix)
		}
		return nil
	}
}

func (f *repoMemoryFixture) execute(args ...string) (string, string) {
	f.t.Helper()

	command := newRootCommand(&rootOptions{logger: logging.Discard(), invocationDeps: f.deps})
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	command.SetIn(strings.NewReader(""))
	command.SetOut(stdout)
	command.SetErr(stderr)
	command.SetArgs(args)
	if err := command.Execute(); err != nil {
		f.t.Fatalf("execute %q: %v\nstderr: %s", strings.Join(args, " "), err, stderr)
	}
	return stdout.String(), stderr.String()
}

func (f *repoMemoryFixture) assertAddOutput(output string) {
	f.t.Helper()
	f.assertContains(output, "repo add", []string{
		"Added repo alpha", f.repoPath, "git@example.com:org/alpha.git", "main", "managed", "alpha",
	})
}

func (f *repoMemoryFixture) assertListOutput(output string) {
	f.t.Helper()
	f.assertContains(output, "repo list", []string{
		"ID", "NAME", "PATH", "REMOTE", "DEFAULT_BRANCH", "BEADS_MODE", "BEADS_PREFIX",
		"alpha", f.repoPath, "git@example.com:org/alpha.git", "main", "managed",
	})
}

func (f *repoMemoryFixture) assertEmptyStderr(operation, output string) {
	f.t.Helper()
	if output != "" {
		f.t.Fatalf("%s stderr = %q, want empty", operation, output)
	}
}

func (f *repoMemoryFixture) assertContains(output, operation string, values []string) {
	f.t.Helper()
	for _, value := range values {
		if !strings.Contains(output, value) {
			f.t.Fatalf("%s output = %q, want substring %q", operation, output, value)
		}
	}
}

func (f *repoMemoryFixture) assertPersistedRepo() {
	f.t.Helper()

	persisted, err := f.deps.registryStore.Load()
	if err != nil {
		f.t.Fatalf("load persisted registry: %v", err)
	}
	want := registry.Repo{
		ID:            "alpha",
		Name:          "alpha",
		Path:          f.repoPath,
		Remote:        "git@example.com:org/alpha.git",
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeManaged,
		BeadsPrefix:   "alpha",
	}
	if len(persisted.Repos) != 1 || !reflect.DeepEqual(persisted.Repos[0], want) {
		f.t.Fatalf("persisted repos = %#v, want %#v", persisted.Repos, []registry.Repo{want})
	}
}

func (f *repoMemoryFixture) assertSemanticCalls() {
	f.t.Helper()

	if f.gitInspections != 1 || f.beadsInspections != 1 || f.beadsInitializations != 1 {
		f.t.Fatalf(
			"semantic calls: Git=%d Beads inspect=%d Beads init=%d, want 1 each",
			f.gitInspections,
			f.beadsInspections,
			f.beadsInitializations,
		)
	}
	wantDir, err := registry.ManagedBeadsDir(f.paths, "alpha")
	if err != nil {
		f.t.Fatalf("resolve managed Beads directory: %v", err)
	}
	if f.initializedDir != wantDir {
		f.t.Fatalf("initialized Beads directory = %q, want %q", f.initializedDir, wantDir)
	}
}

func (f *repoMemoryFixture) assertNoStateOnDisk() {
	f.t.Helper()
	for _, root := range []string{memoryConfigRoot, memoryDataRoot} {
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			f.t.Fatalf("memory state root %q exists on disk: %v", root, err)
		}
	}
}
