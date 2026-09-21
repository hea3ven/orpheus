//go:build integration

package cli_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/registry"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/require"
)

type taskWorkflowRepo struct {
	id            string
	name          string
	prefix        string
	defaultBranch string
	path          string
	aliases       map[string]string
}

func taskWorkflowRepoSpec(id, name, prefix string) taskWorkflowRepo {
	return taskWorkflowRepo{id: id, name: name, prefix: prefix, defaultBranch: "main", path: "/fixture/repos/" + id}
}

func registryStoreForWorkflow(t *testing.T, fixture *workflowFixture) registry.Store {
	t.Helper()
	return registry.NewStore(fixture.paths)
}

func registryRepoWithPath(id, name, path, prefix string) registry.Repo {
	return registry.Repo{ID: id, Name: name, Path: path, DefaultBranch: "main", BeadsMode: registry.BeadsModeLocal, BeadsPrefix: prefix}
}

func registryRepoWithoutTaskSource(id, name string) registry.Repo {
	return registry.Repo{ID: id, Name: name, Path: "/fixture/repos/" + id}
}

func saveTaskWorkflowRepos(t *testing.T, fixture *workflowFixture, repos ...taskWorkflowRepo) map[string]string {
	t.Helper()
	registryRepos := make([]registry.Repo, 0, len(repos))
	pathsByID := make(map[string]string, len(repos))
	for _, repo := range repos {
		if repo.defaultBranch == "" {
			repo.defaultBranch = "main"
		}
		if repo.path == "" {
			repo.path = "/fixture/repos/" + repo.id
		}
		registryRepos = append(registryRepos, registry.Repo{
			ID:                    repo.id,
			Name:                  repo.name,
			Path:                  repo.path,
			DefaultBranch:         repo.defaultBranch,
			BeadsMode:             registry.BeadsModeLocal,
			BeadsPrefix:           repo.prefix,
			ReviewPipelineAliases: repo.aliases,
		})
		pathsByID[repo.id] = repo.path
	}
	require.NoError(t, registry.NewStore(fixture.paths).Save(registry.Registry{Repos: registryRepos}))
	return pathsByID
}

func taskWFTask(id, title string, status taskmodel.Status, issueType taskmodel.IssueType, opts ...func(*taskmodel.Task)) taskmodel.Task {
	item := taskmodel.Task{ID: id, Title: title, Status: status, IssueType: issueType}
	for _, opt := range opts {
		opt(&item)
	}
	return item
}

func taskWFPriority(priority int) func(*taskmodel.Task) {
	return func(item *taskmodel.Task) { item.Priority = priority }
}

func taskWFExternalRef(value string) func(*taskmodel.Task) {
	return func(item *taskmodel.Task) { item.ExternalRef = value }
}

func taskWFDetails(description, design, acceptance string) func(*taskmodel.Task) {
	return func(item *taskmodel.Task) {
		item.Description = description
		item.Design = design
		item.AcceptanceCriteria = acceptance
	}
}

func taskWFLabels(labels ...string) func(*taskmodel.Task) {
	return func(item *taskmodel.Task) { item.Labels = append([]string{}, labels...) }
}

func taskWFMetadata(values map[string]string) func(*taskmodel.Task) {
	return func(item *taskmodel.Task) { item.Metadata = taskmodel.Metadata(values).Clone() }
}

func taskWFParent(parentID string) func(*taskmodel.Task) {
	return func(item *taskmodel.Task) { item.Relations.ParentID = parentID }
}

func taskWFBlocks(ids ...string) func(*taskmodel.Task) {
	return func(item *taskmodel.Task) {
		item.Relations.DependencyIDs = append([]string{}, ids...)
		item.Relations.BlockedByCount = len(ids)
	}
}

func taskWFChildCount(count int) func(*taskmodel.Task) {
	return func(item *taskmodel.Task) { item.Relations.ChildCount = count }
}

func taskWFCreated(value string) func(*taskmodel.Task) {
	return func(item *taskmodel.Task) { item.CreatedAt = taskWFTimePtr(value) }
}

func taskWFUpdated(value string) func(*taskmodel.Task) {
	return func(item *taskmodel.Task) { item.UpdatedAt = taskWFTimePtr(value) }
}

func taskWFClosed(value string) func(*taskmodel.Task) {
	return func(item *taskmodel.Task) { item.ClosedAt = taskWFTimePtr(value) }
}

func taskWFTimePtr(value string) *time.Time {
	parsed := workflowTime(value)
	return &parsed
}

func (b workflowReadBackend) ListFiltered(_ context.Context, filter taskmodel.ListFilter) ([]taskmodel.Task, error) {
	b.reads.record(b.directory, "list_filtered", strings.TrimSpace(filter.ParentID))
	if b.result.err != nil {
		return nil, b.result.err
	}
	filter, err := filter.Normalized()
	if err != nil {
		return nil, err
	}
	items := make([]taskmodel.Task, 0, len(b.result.tasks))
	for _, item := range b.result.tasks {
		if filter.Matches(item) {
			items = append(items, item.Clone())
		}
	}
	return items, nil
}

func writeMismatchedTaskState(t *testing.T, fixture *workflowFixture, repoID, taskID string) {
	t.Helper()
	rel := filepath.Join("repos", repoID, "tasks", taskID+".yaml")
	require.NoError(t, fixture.paths.WriteDataYAML(rel, taskstate.TaskState{Version: 1, RepoID: "wrong", TaskID: taskID}))
}

type taskStatsWorkflowRunFixture struct {
	taskID     string
	model      string
	startedAt  time.Time
	finishedAt time.Time
	usage      *taskstate.AgentUsage
}

func recordWorkflowTaskStatsAggregateRun(
	t *testing.T,
	stateStore taskstate.Store,
	now *time.Time,
	repoDir string,
	fixture taskStatsWorkflowRunFixture,
) {
	t.Helper()
	*now = fixture.startedAt
	run, err := stateStore.StartRun("alpha", fixture.taskID, taskstate.StartRunOptions{
		Agent: "codex", Profile: "codex-profile", Harness: "codex", Model: fixture.model,
		Command: "codex", Args: []string{"exec", "--model", fixture.model}, Branch: "main", Worktree: repoDir,
	})
	require.NoError(t, err)
	if fixture.usage != nil {
		_, err = stateStore.RecordRunUsage("alpha", fixture.taskID, run.Attempt, taskstate.RecordRunUsageOptions{Usage: fixture.usage})
		require.NoError(t, err)
	}
	*now = fixture.finishedAt
	_, err = stateStore.FinishRun("alpha", fixture.taskID, run.Attempt, taskstate.RunStatusSucceeded)
	require.NoError(t, err)
}

func setupTaskShowReviewWorkflowRepo(t *testing.T, fixture *workflowFixture, taskID string) (taskstate.Store, string) {
	t.Helper()
	return setupTaskShowReviewWorkflowRepoWithStatus(t, fixture, taskID, taskmodel.StatusInProgress)
}

func setupTaskShowReviewWorkflowRepoWithStatus(t *testing.T, fixture *workflowFixture, taskID string, status taskmodel.Status) (taskstate.Store, string) {
	t.Helper()

	repos := saveTaskWorkflowRepos(t, fixture, taskWorkflowRepoSpec("alpha", "Alpha Repo", "op"))
	repoPath := repos["alpha"]
	withWorkflowSources(t, fixture, map[string]taskSourceResult{repoPath: {tasks: []taskmodel.Task{
		taskWFTask(taskID, "Ready for review show", status, taskmodel.IssueTypeTask, taskWFPriority(1), taskWFMetadata(map[string]string{taskmodel.MetadataBranch: "main", taskmodel.MetadataWorktree: repoPath})),
	}}})
	return taskstate.NewStore(fixture.paths), repoPath
}

func unsupportedTaskSourceError(kind taskmodel.IssueType) error {
	return fmt.Errorf("%w: issue type %q is not task or epic", taskmodel.ErrUnsupportedTaskSourceItem, kind)
}
