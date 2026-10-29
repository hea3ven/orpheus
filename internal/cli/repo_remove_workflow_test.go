//go:build integration

package cli_test

import (
	"context"
	"errors"
	workmodel "github.com/hea3ven/orpheus/internal/work"
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/agentexec"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type repositoryRemovalGit struct {
	fixture       *workflowFixture
	worktrees     map[string]gitmeta.ClosedTaskWorktreeOutcome
	unsafe        map[string]string
	fail          map[string]string
	removed       []string
	discoverError error
	discoveries   int
}

func (g *repositoryRemovalGit) assertLockHeld() {
	g.fixture.t.Helper()
	err := state.WithGlobalMutationLock(g.fixture.paths, "competing mutation", func() error {
		g.fixture.t.Fatal("repository removal did not hold global lock")
		return nil
	})
	var lockError *state.LockAcquisitionError
	require.ErrorAs(g.fixture.t, err, &lockError)
}

func (g *repositoryRemovalGit) DiscoverRepositoryWorktrees(_ context.Context, opts gitmeta.TaskWorktreeOptions) ([]gitmeta.TaskWorktreeOptions, error) {
	g.assertLockHeld()
	g.discoveries++
	if g.discoverError != nil {
		return nil, g.discoverError
	}
	var result []gitmeta.TaskWorktreeOptions
	for _, id := range []string{"a-closed", "a-open", "a-orphan"} {
		if _, ok := g.worktrees[id]; ok {
			candidate := opts
			candidate.TaskID = id
			result = append(result, candidate)
		}
	}
	return result, nil
}

func (g *repositoryRemovalGit) InspectClosedTaskWorktree(_ context.Context, opts gitmeta.ClosedTaskWorktreeOptions) gitmeta.ClosedTaskWorktreeInspection {
	g.assertLockHeld()
	path, err := opts.Paths.DataPath(filepath.Join("repos", opts.RepoID, "worktrees", opts.TaskID))
	require.NoError(g.fixture.t, err)
	if reason := g.unsafe[opts.TaskID]; reason != "" {
		return gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeUnsafe, Worktree: path, Reason: reason}
	}
	return gitmeta.ClosedTaskWorktreeInspection{Outcome: g.worktrees[opts.TaskID], Worktree: path}
}

func (g *repositoryRemovalGit) RemoveClosedTaskWorktree(ctx context.Context, opts gitmeta.ClosedTaskWorktreeOptions) gitmeta.ClosedTaskWorktreeRemoval {
	inspection := g.InspectClosedTaskWorktree(ctx, opts)
	result := gitmeta.ClosedTaskWorktreeRemoval(inspection)
	if inspection.Outcome == gitmeta.ClosedTaskWorktreeAbsent {
		return result
	}
	if reason := g.fail[opts.TaskID]; reason != "" {
		result.Outcome, result.Reason = gitmeta.ClosedTaskWorktreeFailed, reason
		return result
	}
	g.removed = append(g.removed, opts.TaskID)
	g.worktrees[opts.TaskID] = gitmeta.ClosedTaskWorktreeAbsent
	result.Outcome = gitmeta.ClosedTaskWorktreeRemoved
	return result
}

func newRepositoryRemovalFixture(t *testing.T, repo registry.Repo) (*workflowFixture, *repositoryRemovalGit, *ownershipAuthoringBackend) {
	t.Helper()
	f := newWorkflowFixture(t, repoWorkflowConfigRoot, repoWorkflowDataRoot)
	f.withRegisteredRepos(repo, aRegisteredRepo("beta"))
	backend := newOwnershipAuthoringBackend(anOpenTask("a-open"), aClosedTask("a-closed"))
	g := &repositoryRemovalGit{fixture: f, worktrees: map[string]gitmeta.ClosedTaskWorktreeOutcome{
		"a-open": gitmeta.ClosedTaskWorktreeEligible, "a-closed": gitmeta.ClosedTaskWorktreeEligible, "a-orphan": gitmeta.ClosedTaskWorktreeEligible,
	}, unsafe: make(map[string]string), fail: make(map[string]string)}
	f.options.Dependencies.TaskBackendFactory = func(source task.RepositorySource) (task.ReadBackend, error) {
		g.assertLockHeld()
		if source.Repository.ID == repo.ID {
			if repo.StorageMode() == "local" {
				assert.Equal(t, repo.Path, source.BackendDir)
			}
			return backend, nil
		}
		return newOwnershipAuthoringBackend(anOpenTask("beta-work")), nil
	}
	f.options.Dependencies.RepositoryRemovalGit = g
	f.options.Dependencies.ProcessProbe = func(int) (agentexec.ProcessLiveness, error) {
		t.Fatal("removal must not probe processes")
		return "", nil
	}
	for _, rel := range []string{"repos/alpha/tasks/a-open.yaml", "repos/alpha/" + repo.Source() + "/owned.yaml", "repos/beta/tasks/beta-work.yaml", "repos/alphabet/tasks/keep.yaml"} {
		if rel == "repos/alpha/tasks/a-open.yaml" {
			require.NoError(t, f.paths.WriteDataYAML(rel, taskstate.TaskState{Work: &workmodel.State{RepositoryID: "alpha", TaskID: "a-open", Version: 1}}))
		} else {
			require.NoError(t, f.paths.WriteDataYAML(rel, map[string]string{"keep": rel}))
		}
	}
	return f, g, backend
}

func removableRepository() registry.Repo {
	repo := aRegisteredRepo("alpha")
	repo.Name, repo.BeadsPrefix = "Alpha Repo", "a"
	return repo
}

func assertRemovalLockReleased(t *testing.T, f *workflowFixture) {
	t.Helper()
	require.NoError(t, state.WithGlobalMutationLock(f.paths, "after removal", func() error { return nil }))
}

func assertRemovalDataRetained(t *testing.T, f *workflowFixture, repo registry.Repo) {
	t.Helper()
	assert.Len(t, f.loadFinalRegistry().Repos, 2)
	var taskDocument taskstate.TaskState
	require.NoError(t, f.paths.ReadDataYAML("repos/alpha/tasks/a-open.yaml", &taskDocument))
	require.NotNil(t, taskDocument.Work)
	assert.Equal(t, "alpha", taskDocument.Work.RepositoryID)
	var owned map[string]string
	rel := "repos/alpha/" + repo.Source() + "/owned.yaml"
	require.NoError(t, f.paths.ReadDataYAML(rel, &owned))
	assert.Equal(t, map[string]string{"keep": rel}, owned)

	assertRemovalLockReleased(t, f)
}

func TestIntegrationWorkflowRepositoryRemovalDeletesOwnedDataAndAllowsFreshRegistration(t *testing.T) {
	for _, kind := range []string{"gig", "managed beads", "local beads"} {
		t.Run(kind, func(t *testing.T) {
			repo := removableRepository()
			token := repo.ID
			switch kind {
			case "gig":
				repo.TaskSource, repo.TaskMode, repo.TaskPrefix, repo.BeadsMode, repo.BeadsPrefix = "gig", "managed", "a", "", ""
			case "managed beads":
				repo.BeadsMode, token = registry.BeadsModeManaged, repo.Name
			case "local beads":
				token = repo.Prefix()
			}
			f, g, backend := newRepositoryRemovalFixture(t, repo)
			// Stale running facts do not introduce an active-execution gate.
			store := taskstate.NewStore(f.paths)
			run, err := store.StartRun(repo.ID, "a-open", taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: repo.Path, TaskBranch: "main", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}}, Agent: "pi", Branch: "main", Worktree: repo.Path})
			require.NoError(t, err)
			_, err = seedFixtureUsage(store, repo.ID, "a-open", run.Attempt, taskstate.RecordRunUsageOptions{
				Usage:        &taskstate.AgentUsage{InputTokens: 100, OutputTokens: 40, TotalTokens: 140},
				UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured},
			})
			require.NoError(t, err)
			history := readerDeliveredHistory(t, repo.ID, "a-closed", repo.Path, "orpheus/a-closed", workflowTime("2026-07-07T10:00:00Z"))
			saveReaderHistory(t, f, history)
			stdout, _, err := f.execute("__complete", "repo", "remove", "a")
			require.NoError(t, err)
			assert.Contains(t, stdout, "alpha\tAlpha Repo")
			assert.Zero(t, g.discoveries, "completion is read-only")

			stdout, _, err = f.execute("repo", "remove", token)

			require.NoError(t, err)
			assert.Contains(t, stdout, "Removed repository")
			assert.ElementsMatch(t, []string{"a-open", "a-closed", "a-orphan"}, g.removed)
			assert.Equal(t, []registry.Repo{aRegisteredRepo("beta")}, f.loadFinalRegistry().Repos)
			for _, rel := range []string{"repos/alpha/tasks/a-open.yaml", "repos/alpha/tasks/a-closed.yaml", "repos/alpha/" + repo.Source() + "/owned.yaml"} {
				var value any
				assert.ErrorIs(t, f.paths.ReadDataYAML(rel, &value), os.ErrNotExist)
			}
			for _, rel := range []string{"repos/beta/tasks/beta-work.yaml", "repos/alphabet/tasks/keep.yaml"} {
				var value map[string]string
				require.NoError(t, f.paths.ReadDataYAML(rel, &value))
				assert.Equal(t, map[string]string{"keep": rel}, value)
			}
			assert.Zero(t, backend.mutationCount(), "task-source tasks and relationships are not rewritten")
			assertRemovalLockReleased(t, f)

			discovery := withRepoDiscovery(f, gitRepositoryFixture{name: "alpha", path: repo.Path, defaultBranch: "main"})
			discovery.withoutLocalBeads()
			_, _, err = discovery.execute("repo", "add", repo.Path, "--prefix", "fresh")
			require.NoError(t, err)
			reg := f.loadFinalRegistry()
			require.Len(t, reg.Repos, 2)
			assert.Equal(t, "fresh", reg.Repos[1].Prefix())
		})
	}
}

func TestIntegrationWorkflowRepositoryRemovalRefusesBeforeCleanupAndReleasesLock(t *testing.T) {
	for _, scenario := range []string{"unknown", "locked", "references", "read failure", "discovery failure", "unsafe", "checkout in data"} {
		t.Run(scenario, func(t *testing.T) {
			repo := removableRepository()
			if scenario == "checkout in data" {
				repo.Path = repoWorkflowDataRoot + "/repos/alpha/checkout"
			}
			f, g, backend := newRepositoryRemovalFixture(t, repo)
			token := repo.ID
			failure := errors.New("source or discovery unavailable")
			var release func()
			switch scenario {
			case "unknown":
				token = "missing"
			case "locked":
				release = holdMutationLock(t, f.paths)
			case "references":
				backend.tasks["a-open"] = task.Task{ID: "a-open", Relations: task.RelationSummary{Complete: true, DependencyIDs: []string{"beta-work"}}}
			case "read failure":
				backend.listError = failure
			case "discovery failure":
				g.discoverError = failure
			case "unsafe":
				g.unsafe["a-orphan"] = "foreign worktree"
			}
			stdout, _, err := f.execute("repo", "remove", token)
			require.Error(t, err)
			assert.Empty(t, stdout)
			assert.Empty(t, g.removed)
			if scenario == "read failure" || scenario == "discovery failure" {
				assert.ErrorIs(t, err, failure)
			}
			if scenario == "unknown" || scenario == "locked" {
				assert.Zero(t, backend.listCalls)
				assert.Zero(t, g.discoveries)
			}
			if release != nil {
				release()
			}
			assertRemovalDataRetained(t, f, repo)
			if scenario == "references" {
				assert.ErrorContains(t, err, "alpha/a-open depends on beta/beta-work")
				assert.ErrorContains(t, err, "remove these relationships before deleting")
				assert.Zero(t, backend.mutationCount())
			}
		})
	}
}

func TestIntegrationWorkflowRepositoryRemovalRetainsRecordsOnPartialWorktreeFailureAndRetries(t *testing.T) {
	repo := removableRepository()
	f, g, _ := newRepositoryRemovalFixture(t, repo)
	g.fail["a-open"], g.fail["a-orphan"] = "contains modified files", "Git registration requires repair"

	stdout, _, err := f.execute("repo", "remove", repo.ID)

	require.Error(t, err)
	assert.Empty(t, stdout)
	assert.Equal(t, []string{"a-closed"}, g.removed)
	for _, text := range []string{"a-open", "modified files", "a-orphan", "Git registration", "retry", "retained"} {
		assert.ErrorContains(t, err, text)
	}
	assertRemovalDataRetained(t, f, repo)
	clear(g.fail)
	stdout, _, err = f.execute("repo", "remove", repo.ID)
	require.NoError(t, err)
	assert.Contains(t, stdout, "Removed repository")
	assert.ElementsMatch(t, []string{"a-closed", "a-open", "a-orphan"}, g.removed)
	assert.Equal(t, []registry.Repo{aRegisteredRepo("beta")}, f.loadFinalRegistry().Repos)
	assertRemovalLockReleased(t, f)
}

func TestIntegrationWorkflowRepositoryRemovalReportsDataAndRegistryPersistenceRecovery(t *testing.T) {
	for _, stage := range []string{"data", "registry local", "registry managed"} {
		t.Run(stage, func(t *testing.T) {
			repo := removableRepository()
			if stage == "registry managed" {
				repo.BeadsMode = registry.BeadsModeManaged
			}
			f, g, backend := newRepositoryRemovalFixture(t, repo)
			failure := errors.New("storage denied")
			if stage == "data" {
				require.NoError(t, state.SetMemoryDataRemoveError(f.paths, func(string) error { return failure }))
			} else {
				require.NoError(t, state.SetMemoryDataWriteError(f.paths, func(rel string, _ []byte) error {
					if rel == "registry.yaml" {
						return failure
					}
					return nil
				}))
			}
			stdout, _, err := f.execute("repo", "remove", repo.ID)
			require.ErrorIs(t, err, failure)
			assert.Empty(t, stdout)
			assert.Len(t, g.removed, 3)
			assert.Len(t, f.loadFinalRegistry().Repos, 2)
			assert.ErrorContains(t, err, "registration")
			assert.ErrorContains(t, err, "retry")
			if stage == "data" {
				assertRemovalDataRetained(t, f, repo)
			} else {
				var value any
				assert.ErrorIs(t, f.paths.ReadDataYAML("repos/alpha/tasks/a-open.yaml", &value), os.ErrNotExist)
			}
			assertRemovalLockReleased(t, f)
			require.NoError(t, state.SetMemoryDataRemoveError(f.paths, nil))
			require.NoError(t, state.SetMemoryDataWriteError(f.paths, nil))
			if stage == "registry managed" {
				assert.ErrorContains(t, err, "manually remove only this entry")
				backend.listError = os.ErrNotExist
				_, _, err = f.execute("repo", "remove", repo.ID)
				require.ErrorIs(t, err, os.ErrNotExist)
				assert.ErrorContains(t, err, "cannot complete cross-repository relationship check")
				assert.Len(t, f.loadFinalRegistry().Repos, 2)
				return
			}
			_, _, err = f.execute("repo", "remove", repo.ID)
			require.NoError(t, err, "local Beads remains readable after data deletion")
			assert.Equal(t, []registry.Repo{aRegisteredRepo("beta")}, f.loadFinalRegistry().Repos)
		})
	}
}
