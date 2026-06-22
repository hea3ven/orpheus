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

type contextMutation func(
	fixture *activeContextFixture,
	worktree string,
	taskItem *taskmodel.Task,
	env map[string]string,
	cwd *string,
)

func TestActiveContextResolverResolvesWorktreeTarget(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	fixture.repo.SummaryGuidance = "Use sentence-case summaries without a type prefix."
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
	is.Equal(fixture.repo.SummaryGuidance, got.Repository.SummaryGuidance)
	is.Equal("op-1", got.Task.ID)
	is.Equal("Resolve context", got.Task.Title)
	is.Equal(1, got.Run.Attempt)
	is.Equal("recorder", got.Run.Agent)
	is.Equal(agent.ExecutionTargetWorktree, got.Target.Kind)
	is.Equal("orpheus/op-1", got.Target.Branch)
	is.Equal(worktree, got.Target.Path)
	is.Equal(cwd, got.Target.CurrentDirectory)
}

func TestActiveContextResolverResolvesRepoRootTargets(t *testing.T) {
	for _, tt := range []struct {
		name       string
		taskID     string
		branch     string
		cwdRel     string
		targetKind agent.ExecutionTarget
	}{
		{
			name:       "main",
			taskID:     "op-main",
			branch:     "main",
			cwdRel:     filepath.Join("cmd", "orpheus"),
			targetKind: agent.ExecutionTargetMain,
		},
		{
			name:       "task branch",
			taskID:     "op-root",
			branch:     "orpheus/op-root",
			cwdRel:     filepath.Join("internal", "cli"),
			targetKind: agent.ExecutionTargetRepoRoot,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			fixture := newActiveContextFixture(t, tt.taskID)
			cwd := filepath.Join(fixture.repoPath, tt.cwdRel)
			must.NoError(testMkdirAll(cwd))

			taskItem := taskmodel.Task{
				ID:     tt.taskID,
				Status: taskmodel.StatusInProgress,
				Metadata: taskmodel.Metadata{
					taskmodel.MetadataBranch:   tt.branch,
					taskmodel.MetadataWorktree: fixture.repoPath,
				},
			}
			_, err := fixture.store.StartRun("alpha", tt.taskID, taskstate.StartRunOptions{
				Agent:    "recorder",
				Branch:   tt.branch,
				Worktree: fixture.repoPath,
			})
			must.NoError(err)

			resolver := fixture.resolver(taskItem, map[string]string{
				"ORPHEUS_REPO_ID":  "alpha",
				"ORPHEUS_TASK_ID":  tt.taskID,
				"ORPHEUS_WORKTREE": fixture.repoPath,
				"ORPHEUS_BRANCH":   tt.branch,
			}, cwd)

			got, err := resolver.Resolve(context.Background())

			must.NoError(err)
			is.Equal(tt.targetKind, got.Target.Kind)
			is.Equal(tt.branch, got.Target.Branch)
			is.Equal(fixture.repoPath, got.Target.Path)
			is.Equal(cwd, got.Target.CurrentDirectory)
		})
	}
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
	for _, tt := range unsafeContextCases() {
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

func unsafeContextCases() []struct {
	name      string
	mutate    contextMutation
	wantError string
} {
	return []struct {
		name      string
		mutate    contextMutation
		wantError string
	}{
		{
			name:      "environment worktree mismatch",
			mutate:    mutateEnvWorktreeMismatch,
			wantError: "ORPHEUS_WORKTREE",
		},
		{
			name:      "cwd outside target",
			mutate:    mutateCWDOutsideTarget,
			wantError: "outside the worktree/team execution target",
		},
		{
			name:      "closed task",
			mutate:    mutateClosedTask,
			wantError: "task op-1 is closed",
		},
		{
			name:      "task already has pull request URL",
			mutate:    mutateTaskWithPRURL,
			wantError: "already has a pull request URL recorded",
		},
		{
			name:      "metadata mismatch",
			mutate:    mutateMetadataMismatch,
			wantError: "inconsistent Orpheus metadata",
		},
	}
}

func mutateEnvWorktreeMismatch(
	fixture *activeContextFixture,
	_ string,
	_ *taskmodel.Task,
	env map[string]string,
	_ *string,
) {
	env["ORPHEUS_WORKTREE"] = fixture.repoPath
}

func mutateCWDOutsideTarget(
	fixture *activeContextFixture,
	_ string,
	_ *taskmodel.Task,
	_ map[string]string,
	cwd *string,
) {
	*cwd = filepath.Dir(fixture.repoPath)
}

func mutateClosedTask(
	_ *activeContextFixture,
	_ string,
	taskItem *taskmodel.Task,
	_ map[string]string,
	_ *string,
) {
	taskItem.Status = taskmodel.StatusClosed
}

func mutateTaskWithPRURL(
	_ *activeContextFixture,
	_ string,
	taskItem *taskmodel.Task,
	_ map[string]string,
	_ *string,
) {
	taskItem.Metadata[taskmodel.MetadataPRURL] = "https://example.test/pr/1"
}

func mutateMetadataMismatch(
	_ *activeContextFixture,
	_ string,
	taskItem *taskmodel.Task,
	_ map[string]string,
	_ *string,
) {
	taskItem.Metadata[taskmodel.MetadataBranch] = "other"
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
