//go:build integration

package cli_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/registry"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowRegistrationDefaultsToGigEvenWithLocalBeads(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "explicit"}[explicit], func(t *testing.T) {
			repo := aGitRepository(t, "alpha")
			fixture := newRepoWorkflowFixture(t, repo)
			fixture.withLocalBeads(repo, "local")
			var initialized []string
			fixture.options.Dependencies.InitializeGig = func(dir, prefix string) error {
				initialized = append(initialized, dir, prefix)
				return nil
			}
			args := []string{"repo", "add", repo.path}
			if explicit {
				args = append(args, "--task-source", "gig")
			}
			stdout, stderr, err := fixture.execute(args...)
			require.NoError(t, err, "stderr: %s", stderr)
			assert.Contains(t, stdout, "gig")
			assert.Empty(t, fixture.beadsInspections)
			assert.Empty(t, fixture.beadsInitializations)
			assert.Equal(t, []string{repoWorkflowDataRoot + "/repos/alpha/gig", "alpha"}, initialized)
			final := fixture.loadFinalRegistry()
			require.Len(t, final.Repos, 1)
			assert.Equal(t, "gig", final.Repos[0].TaskSource)
			assert.Equal(t, "managed", final.Repos[0].TaskMode)
			assert.Equal(t, "alpha", final.Repos[0].TaskPrefix)
			assert.Empty(t, final.Repos[0].BeadsMode)
			stdout, _, err = fixture.execute("repo", "list")
			require.NoError(t, err)
			assert.Contains(t, stdout, "TASK_SOURCE")
			assert.Contains(t, stdout, "gig")
			_, _, err = fixture.execute("repo", "beads-dir", "alpha")
			assert.ErrorContains(t, err, "applies only to Beads")
		})
	}
}

func TestIntegrationWorkflowGigRegistrationFailureDoesNotPersistSource(t *testing.T) {
	for _, source := range []string{"gig", "unknown"} {
		t.Run(source, func(t *testing.T) {
			repo := aGitRepository(t, "alpha")
			fixture := newRepoWorkflowFixture(t, repo)
			existing := aRegisteredRepo("legacy")
			fixture.withRegisteredRepos(existing)
			initErr := errors.New("gig initialization failed")
			fixture.options.Dependencies.InitializeGig = func(string, string) error { return initErr }
			stdout, _, err := fixture.execute("repo", "add", repo.path, "--task-source", source)
			require.Error(t, err)
			if source == "gig" {
				assert.ErrorIs(t, err, initErr)
			} else {
				assert.ErrorContains(t, err, "unsupported task source")
			}
			assert.Empty(t, stdout)
			assert.Equal(t, []registry.Repo{existing}, fixture.loadFinalRegistry().Repos)
			assert.Empty(t, fixture.beadsInspections)
		})
	}
}

func TestIntegrationWorkflowGigRegistrationToCompletionAndSyncInMixedWorkspace(t *testing.T) {
	const id = "alpha-implementation"
	f := newFinalizationFixture(t, id)
	f.backend.tasks[id] = anOpenTask(id)
	f.initialTasks[id] = f.backend.tasks[id].Clone()
	f.withRegisteredRepos(aRegisteredRepo("legacy"))
	repo := gitRepositoryFixture{name: "alpha", path: taskWorkflowRepoRoot, defaultBranch: "main"}
	discovery := withRepoDiscovery(f.workflowFixture, repo)
	discovery.withLocalBeads(repo, "ignored")
	initialized := false
	f.options.Dependencies.InitializeGig = func(dir, prefix string) error {
		assert.Equal(t, taskWorkflowDataRoot+"/repos/alpha/gig", dir)
		assert.Equal(t, "alpha", prefix)
		initialized = true
		return nil
	}
	legacy := newMemoryTaskBackend([]taskmodel.Task{anOpenTask("legacy-work")})
	f.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		if source.Repository.ID == "legacy" {
			assert.Equal(t, "beads", source.Kind)
			return legacy, nil
		}
		assert.True(t, initialized)
		assert.Equal(t, "gig", source.Kind)
		assert.Equal(t, "alpha", source.Repository.TaskIDPrefix)
		assert.True(t, source.MaintenanceOwned)
		assert.Equal(t, filepath.Join(taskWorkflowDataRoot, "repos", "alpha", "gig"), source.BackendDir)
		return f.mutations, nil
	}
	f.withCompletingAgent(aCompletion())

	f.run("", "repo", "add", repo.path)
	assert.Empty(t, discovery.beadsInspections)
	stdout, _ := f.run("", "task", "list")
	assert.Contains(t, stdout, id)
	assert.Contains(t, stdout, "legacy-work")
	stdout, _ = f.run("", "__complete", "task", "run", "alpha-")
	assert.Contains(t, stdout, id)
	assert.NotContains(t, stdout, "legacy-work")
	stdout, _ = f.run("", "task", "run", id)
	assert.Contains(t, stdout, "Recorded completion for "+id)
	f.assertIsTaskInAgentContext(id)
	state, item := f.loadFinalTask(id)
	assertWaitingForManualReview(t, state)
	assert.Equal(t, taskmodel.StatusInProgress, item.Status)
	require.Len(t, state.Runs, 1)
	assert.Equal(t, taskstate.RunStatusSucceeded, state.Runs[0].Status)
	f.passedReview(id)
	f.run("", "task", "done", id)
	f.assertPublishedPR(id)
	f.pr.state = pullrequest.StateMerged
	stdout, _ = f.run("", "task", "sync", id)
	assert.Contains(t, stdout, "Backend task was closed")
	state, item = f.loadFinalTask(id)
	assert.Equal(t, taskmodel.StatusClosed, item.Status)
	assert.NotEmpty(t, state.Events)
	stdout, _ = f.run("", "task", "stats", id)
	assert.Contains(t, stdout, "implementation")
	assert.Contains(t, stdout, "succeeded")
}
