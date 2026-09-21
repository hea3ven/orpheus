//go:build integration

package cli_test

import (
	"bytes"
	"os"
	"sync"
	"testing"

	"github.com/hea3ven/orpheus/internal/cli"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const noExecutablePath = "/nonexistent"

type workflowFixture struct {
	t             *testing.T
	paths         state.Paths
	options       cli.CommandOptions
	registryStore registry.Store
	config        map[string]any
}

func newWorkflowFixture(t *testing.T, configRoot, dataRoot string) *workflowFixture {
	t.Helper()
	paths, err := state.NewMemoryPaths(configRoot, dataRoot)
	require.NoError(t, err, "create memory paths")
	fixture := &workflowFixture{
		t:             t,
		paths:         paths,
		options:       cli.CommandOptions{Paths: &paths, Environment: map[string]string{"PATH": noExecutablePath}},
		registryStore: registry.NewStore(paths),
		config:        make(map[string]any),
	}
	fixture.mustRemainOffDisk(configRoot, dataRoot)
	return fixture
}

func (f *workflowFixture) execute(args ...string) (string, string, error) {
	f.t.Helper()
	return executeRootCommand(cli.NewRootCommandWithOptions(f.options), args...)
}

func (f *workflowFixture) loadFinalRegistry() registry.Registry {
	f.t.Helper()
	finalRegistry, err := f.registryStore.Load()
	require.NoError(f.t, err, "load final registry")
	return finalRegistry
}

func (f *workflowFixture) mustRemainOffDisk(roots ...string) {
	f.t.Helper()
	f.t.Cleanup(func() {
		for _, root := range roots {
			_, err := os.Stat(root)
			assert.ErrorIs(f.t, err, os.ErrNotExist, "fixture root %q must not exist on disk", root)
		}
	})
}

func executeRootCommand(command *cobra.Command, args ...string) (string, string, error) {
	var stdout, stderr bytes.Buffer
	command.SetIn(bytes.NewReader(nil))
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs(args)
	err := command.Execute()
	return stdout.String(), stderr.String(), err
}

func (f *workflowFixture) setConfig(section string, value any) {
	f.t.Helper()
	f.config[section] = value
	f.saveConfig()
}

func (f *workflowFixture) saveConfig() {
	f.t.Helper()
	require.NoError(f.t, state.SeedMemoryConfigYAML(f.paths, "config.yaml", f.config))
}

func (f *workflowFixture) withRegisteredRepos(repos ...registry.Repo) {
	f.t.Helper()
	require.NoError(f.t, f.registryStore.Save(registry.Registry{Repos: repos}))
}

func aRegisteredRepo(id string) registry.Repo {
	return registry.Repo{ID: id, Name: id, Path: "/fixture/repos/" + id, DefaultBranch: "main", BeadsMode: registry.BeadsModeLocal, BeadsPrefix: id}
}

func newRepoConfigFixture(t *testing.T) *workflowFixture {
	t.Helper()
	fixture := newWorkflowFixture(t, repoWorkflowConfigRoot, repoWorkflowDataRoot)
	fixture.withRegisteredRepos(aRegisteredRepo("alpha"))
	return fixture
}

// holdMutationLock synchronizes a competing invocation without sleeps or subprocesses.
func holdMutationLock(t *testing.T, paths state.Paths) func() {
	t.Helper()
	acquired := make(chan error, 1)
	release := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		err := state.WithGlobalMutationLock(paths, "competing invocation", func() error {
			acquired <- nil
			<-release
			return nil
		})
		if err != nil {
			acquired <- err
		}
		finished <- err
	}()
	var once sync.Once
	unlock := func() { once.Do(func() { close(release); require.NoError(t, <-finished) }) }
	t.Cleanup(unlock)
	require.NoError(t, <-acquired)
	return unlock
}
