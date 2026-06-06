package agent_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeContextBackend struct {
	task taskmodel.Task
	err  error
}

func (b fakeContextBackend) Get(ctx context.Context, id string) (taskmodel.Task, error) {
	if b.err != nil {
		return taskmodel.Task{}, b.err
	}
	return b.task, nil
}

func TestActiveContextResolverResolvesWorktreeTarget(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	worktree := fixture.expectedWorktree(t, "op-1")
	cwd := filepath.Join(worktree, "internal", "agent")
	must.NoError(testMkdirAll(cwd))

	taskItem := taskmodel.Task{
		ID:                 "op-1",
		Title:              "Resolve context",
		Description:        "Render active context.",
		AcceptanceCriteria: "Only active runs render.",
		Status:             taskmodel.StatusInProgress,
		Metadata: taskmodel.Metadata{
			taskmodel.MetadataBranch:   "orpheus/op-1",
			taskmodel.MetadataWorktree: worktree,
		},
	}
	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "orpheus/op-1",
		Worktree: worktree,
	})
	must.NoError(err)

	resolver := fixture.resolver(taskItem, map[string]string{
		"ORPHEUS_REPO_ID":  "alpha",
		"ORPHEUS_TASK_ID":  "op-1",
		"ORPHEUS_WORKTREE": worktree,
		"ORPHEUS_BRANCH":   "orpheus/op-1",
	}, cwd)

	got, err := resolver.Resolve(context.Background())

	must.NoError(err)
	is.Equal("alpha", got.Repository.ID)
	is.Equal("Alpha Repo", got.Repository.Name)
	is.Equal(fixture.repoPath, got.Repository.Root)
	is.Equal("main", got.Repository.DefaultBranch)
	is.Equal("op-1", got.Task.ID)
	is.Equal("Resolve context", got.Task.Title)
	is.Equal(1, got.Run.Attempt)
	is.Equal("recorder", got.Run.Agent)
	is.Equal(agent.ExecutionTargetWorktree, got.Target.Kind)
	is.Equal("orpheus/op-1", got.Target.Branch)
	is.Equal(worktree, got.Target.Path)
	is.Equal(cwd, got.Target.CurrentDirectory)
}

func TestActiveContextResolverResolvesMainTarget(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-main")
	cwd := filepath.Join(fixture.repoPath, "cmd", "orpheus")
	must.NoError(testMkdirAll(cwd))

	taskItem := taskmodel.Task{
		ID:     "op-main",
		Title:  "Main target",
		Status: taskmodel.StatusInProgress,
		Metadata: taskmodel.Metadata{
			taskmodel.MetadataBranch:   "main",
			taskmodel.MetadataWorktree: fixture.repoPath,
		},
	}
	_, err := fixture.store.StartRun("alpha", "op-main", taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "main",
		Worktree: fixture.repoPath,
	})
	must.NoError(err)

	resolver := fixture.resolver(taskItem, map[string]string{
		"ORPHEUS_REPO_ID":  "alpha",
		"ORPHEUS_TASK_ID":  "op-main",
		"ORPHEUS_WORKTREE": fixture.repoPath,
		"ORPHEUS_BRANCH":   "main",
	}, cwd)

	got, err := resolver.Resolve(context.Background())

	must.NoError(err)
	is.Equal(agent.ExecutionTargetMain, got.Target.Kind)
	is.Equal("main", got.Target.Branch)
	is.Equal(fixture.repoPath, got.Target.Path)
	is.Equal(cwd, got.Target.CurrentDirectory)
}

func TestActiveContextResolverRejectsLatestRunThatIsNotRunning(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	worktree := fixture.expectedWorktree(t, "op-1")
	taskItem := fixture.worktreeTask("op-1", worktree)
	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Branch:   "orpheus/op-1",
		Worktree: worktree,
	})
	must.NoError(err)
	_, err = fixture.store.FinishRun("alpha", "op-1", 1, taskstate.RunStatusSucceeded)
	must.NoError(err)

	resolver := fixture.resolver(taskItem, map[string]string{
		"ORPHEUS_REPO_ID":  "alpha",
		"ORPHEUS_TASK_ID":  "op-1",
		"ORPHEUS_WORKTREE": worktree,
		"ORPHEUS_BRANCH":   "orpheus/op-1",
	}, worktree)

	_, err = resolver.Resolve(context.Background())

	must.Error(err)
	is.Contains(err.Error(), "latest Orpheus run attempt 1")
	is.Contains(err.Error(), `expected "running"`)
}

func TestActiveContextResolverRejectsUnsafeOrInconsistentContext(t *testing.T) {
	type contextMutation func(
		fixture *activeContextFixture,
		worktree string,
		taskItem *taskmodel.Task,
		env map[string]string,
		cwd *string,
	)
	tests := []struct {
		name      string
		mutate    contextMutation
		wantError string
	}{
		{
			name: "environment worktree mismatch",
			mutate: func(
				fixture *activeContextFixture,
				worktree string,
				taskItem *taskmodel.Task,
				env map[string]string,
				cwd *string,
			) {
				env["ORPHEUS_WORKTREE"] = fixture.repoPath
			},
			wantError: "ORPHEUS_WORKTREE",
		},
		{
			name: "cwd outside target",
			mutate: func(
				fixture *activeContextFixture,
				worktree string,
				taskItem *taskmodel.Task,
				env map[string]string,
				cwd *string,
			) {
				*cwd = filepath.Dir(fixture.repoPath)
			},
			wantError: "outside the worktree/team execution target",
		},
		{
			name: "closed task",
			mutate: func(
				fixture *activeContextFixture,
				worktree string,
				taskItem *taskmodel.Task,
				env map[string]string,
				cwd *string,
			) {
				taskItem.Status = taskmodel.StatusClosed
			},
			wantError: "task op-1 is closed",
		},
		{
			name: "task already has pull request URL",
			mutate: func(
				fixture *activeContextFixture,
				worktree string,
				taskItem *taskmodel.Task,
				env map[string]string,
				cwd *string,
			) {
				taskItem.Metadata[taskmodel.MetadataPRURL] = "https://example.test/pr/1"
			},
			wantError: "already has a pull request URL recorded",
		},
		{
			name: "metadata mismatch",
			mutate: func(
				fixture *activeContextFixture,
				worktree string,
				taskItem *taskmodel.Task,
				env map[string]string,
				cwd *string,
			) {
				taskItem.Metadata[taskmodel.MetadataBranch] = "other"
			},
			wantError: "inconsistent Orpheus metadata",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			fixture := newActiveContextFixture(t, "op-1")
			worktree := fixture.expectedWorktree(t, "op-1")
			cwd := worktree
			taskItem := fixture.worktreeTask("op-1", worktree)
			env := map[string]string{
				"ORPHEUS_REPO_ID":  "alpha",
				"ORPHEUS_TASK_ID":  "op-1",
				"ORPHEUS_WORKTREE": worktree,
				"ORPHEUS_BRANCH":   "orpheus/op-1",
			}
			_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
				Branch:   "orpheus/op-1",
				Worktree: worktree,
			})
			must.NoError(err)

			tt.mutate(fixture, worktree, &taskItem, env, &cwd)
			resolver := fixture.resolver(taskItem, env, cwd)

			_, err = resolver.Resolve(context.Background())

			must.Error(err)
			is.Contains(err.Error(), tt.wantError)
		})
	}
}

func TestActiveContextResolverWrapsBackendErrors(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	worktree := fixture.expectedWorktree(t, "op-1")
	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Branch:   "orpheus/op-1",
		Worktree: worktree,
	})
	must.NoError(err)

	resolver := fixture.resolverWithBackend(fakeContextBackend{err: errors.New("backend unavailable")}, map[string]string{
		"ORPHEUS_REPO_ID":  "alpha",
		"ORPHEUS_TASK_ID":  "op-1",
		"ORPHEUS_WORKTREE": worktree,
		"ORPHEUS_BRANCH":   "orpheus/op-1",
	}, worktree)

	_, err = resolver.Resolve(context.Background())

	must.Error(err)
	is.Contains(err.Error(), "load task op-1 in repo alpha")
	is.Contains(err.Error(), "backend unavailable")
}

type activeContextFixture struct {
	paths    state.Paths
	repoPath string
	repo     registry.Repo
	source   taskmodel.RepositorySource
	store    taskstate.Store
}

func newActiveContextFixture(t *testing.T, taskID string) *activeContextFixture {
	t.Helper()
	must := require.New(t)

	root := t.TempDir()
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	must.NoError(err)
	repoPath := filepath.Join(root, "repo")
	must.NoError(testMkdirAll(repoPath))
	repo := registry.Repo{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}
	source := taskmodel.RepositorySource{
		Repository: taskmodel.Repository{
			ID:            repo.ID,
			Name:          repo.Name,
			TaskIDPrefix:  repo.BeadsPrefix,
			Path:          repo.Path,
			DefaultBranch: repo.DefaultBranch,
		},
		BackendDir: repo.Path,
	}

	return &activeContextFixture{
		paths:    paths,
		repoPath: repoPath,
		repo:     repo,
		source:   source,
		store:    taskstate.NewStore(paths),
	}
}

func (f *activeContextFixture) expectedWorktree(t *testing.T, taskID string) string {
	t.Helper()
	path, err := f.paths.DataPath(filepath.Join("repos", f.repo.ID, "worktrees", taskID))
	require.NoError(t, err)
	return path
}

func (f *activeContextFixture) worktreeTask(taskID string, worktree string) taskmodel.Task {
	return taskmodel.Task{
		ID:     taskID,
		Title:  "Worktree target",
		Status: taskmodel.StatusInProgress,
		Metadata: taskmodel.Metadata{
			taskmodel.MetadataBranch:   "orpheus/" + taskID,
			taskmodel.MetadataWorktree: worktree,
		},
	}
}

func (f *activeContextFixture) resolver(
	taskItem taskmodel.Task,
	env map[string]string,
	cwd string,
) agent.ActiveContextResolver {
	return f.resolverWithBackend(fakeContextBackend{task: taskItem}, env, cwd)
}

func (f *activeContextFixture) resolverWithBackend(
	backend fakeContextBackend,
	env map[string]string,
	cwd string,
) agent.ActiveContextResolver {
	return agent.ActiveContextResolver{
		Paths:          f.paths,
		Registry:       registry.Registry{Repos: []registry.Repo{f.repo}},
		Sources:        []taskmodel.RepositorySource{f.source},
		BackendFactory: func(source taskmodel.RepositorySource) (agent.ContextBackend, error) { return backend, nil },
		RunStore:       f.store,
		Env:            env,
		CWD:            cwd,
	}
}

func testMkdirAll(path string) error {
	return os.MkdirAll(path, 0o755)
}
