//go:build integration

package cli_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/require"
)

type ownershipAuthoringBackend struct {
	mu sync.Mutex

	tasks map[string]taskmodel.Task

	getCalls   []string
	listCalls  int
	creates    []taskmodel.CreateOptions
	updates    []taskmodel.UpdateOptions
	started    []string
	closed     []string
	createdID  string
	startError error
	closeError error
	updateErr  error
}

func newOwnershipAuthoringBackend(tasks ...taskmodel.Task) *ownershipAuthoringBackend {
	backend := &ownershipAuthoringBackend{tasks: make(map[string]taskmodel.Task, len(tasks)), createdID: "op-9"}
	for _, item := range tasks {
		backend.tasks[item.ID] = item.Clone()
	}
	return backend
}

func (b *ownershipAuthoringBackend) Get(_ context.Context, id string) (taskmodel.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.getCalls = append(b.getCalls, id)
	item, ok := b.tasks[id]
	if !ok {
		return taskmodel.Task{}, taskmodel.ErrNotFound
	}
	return item.Clone(), nil
}

func (b *ownershipAuthoringBackend) List(context.Context) ([]taskmodel.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listCalls++
	ids := make([]string, 0, len(b.tasks))
	for id := range b.tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]taskmodel.Task, 0, len(ids))
	for _, id := range ids {
		items = append(items, b.tasks[id].Clone())
	}
	return items, nil
}

func (b *ownershipAuthoringBackend) Create(_ context.Context, opts taskmodel.CreateOptions) (taskmodel.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.creates = append(b.creates, opts)
	id := strings.TrimSpace(b.createdID)
	if id == "" {
		id = fmt.Sprintf("op-%d", len(b.creates))
	}
	created := taskmodel.Task{
		ID:                 id,
		Title:              opts.Title,
		Description:        opts.Description,
		Design:             opts.Design,
		AcceptanceCriteria: opts.AcceptanceCriteria,
		ExternalRef:        opts.ExternalRef,
		IssueType:          opts.IssueType,
		Status:             taskmodel.StatusOpen,
		Relations: taskmodel.RelationSummary{
			ParentID:      opts.ParentID,
			DependencyIDs: append([]string(nil), opts.BlockingIDs...),
		},
	}
	b.tasks[id] = created.Clone()
	return created, nil
}

func (b *ownershipAuthoringBackend) Update(_ context.Context, opts taskmodel.UpdateOptions) (taskmodel.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.updates = append(b.updates, opts)
	if b.updateErr != nil {
		return taskmodel.Task{}, b.updateErr
	}
	item, ok := b.tasks[opts.ID]
	if !ok {
		return taskmodel.Task{}, taskmodel.ErrNotFound
	}
	if opts.Title != nil {
		item.Title = *opts.Title
	}
	if opts.Description != nil {
		item.Description = *opts.Description
	}
	if opts.Design != nil {
		item.Design = *opts.Design
	}
	if opts.AcceptanceCriteria != nil {
		item.AcceptanceCriteria = *opts.AcceptanceCriteria
	}
	if opts.ExternalRef != nil {
		item.ExternalRef = *opts.ExternalRef
	}
	if opts.ParentID != nil {
		item.Relations.ParentID = *opts.ParentID
	}
	item.Relations.DependencyIDs = append(item.Relations.DependencyIDs, opts.AddBlockingIDs...)
	if len(opts.RemoveBlockingIDs) > 0 {
		remove := make(map[string]struct{}, len(opts.RemoveBlockingIDs))
		for _, id := range opts.RemoveBlockingIDs {
			remove[id] = struct{}{}
		}
		kept := item.Relations.DependencyIDs[:0]
		for _, id := range item.Relations.DependencyIDs {
			if _, ok := remove[id]; !ok {
				kept = append(kept, id)
			}
		}
		item.Relations.DependencyIDs = kept
	}
	b.tasks[item.ID] = item.Clone()
	return item, nil
}

func (b *ownershipAuthoringBackend) StartEpic(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.started = append(b.started, id)
	if b.startError != nil {
		return b.startError
	}
	item, ok := b.tasks[id]
	if !ok {
		return taskmodel.ErrNotFound
	}
	item.Status = taskmodel.StatusInProgress
	b.tasks[id] = item
	return nil
}

func (b *ownershipAuthoringBackend) Close(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = append(b.closed, id)
	if b.closeError != nil {
		return b.closeError
	}
	item, ok := b.tasks[id]
	if !ok {
		return taskmodel.ErrNotFound
	}
	item.Status = taskmodel.StatusClosed
	b.tasks[id] = item
	return nil
}

func (b *ownershipAuthoringBackend) mutationCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.creates) + len(b.updates) + len(b.started) + len(b.closed)
}

func (b *ownershipAuthoringBackend) task(id string) taskmodel.Task {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tasks[id].Clone()
}

func setupOwnershipAuthoringWorkflow(t *testing.T, fixture *workflowFixture, repo registry.Repo, backend *ownershipAuthoringBackend) {
	t.Helper()
	require.NoError(t, registry.NewStore(fixture.paths).Save(registry.Registry{Repos: []registry.Repo{repo}}))
	fixture.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		if source.Repository.ID != repo.ID {
			return nil, fmt.Errorf("unexpected repository %s", source.Repository.ID)
		}
		return backend, nil
	}
}

func ownershipAuthoringRepo() registry.Repo {
	return registry.Repo{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          "/fixture/repos/alpha",
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}
}

func ownershipAuthoringGatedRepo() registry.Repo {
	repo := ownershipAuthoringRepo()
	repo.TitleTemplate = "[{{external_ref}}] {{summary}}"
	return repo
}

func ownershipAuthoringTask(id, title string, status taskmodel.Status, issueType taskmodel.IssueType) taskmodel.Task {
	return taskmodel.Task{
		ID:                 id,
		Title:              title,
		Description:        "Description",
		AcceptanceCriteria: "Acceptance",
		Status:             status,
		IssueType:          issueType,
	}
}

func ownershipAuthoringEpic(id string, status taskmodel.Status) taskmodel.Task {
	return ownershipAuthoringTask(id, "Epic "+id, status, taskmodel.IssueTypeEpic)
}

func writeOwnershipAgentProfileConfig(t *testing.T, fixture *workflowFixture, name string, interactive bool) {
	t.Helper()
	profile := map[string]any{"command": name}
	if !interactive {
		profile["interactive"] = false
	}
	require.NoError(t, state.SeedMemoryConfigYAML(fixture.paths, "config.yaml", map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{"implementer": name},
			"profiles": map[string]any{name: profile},
		},
	}))
}

func setupOwnershipAgentImplementation(t *testing.T, fixture *workflowFixture, interactive bool) (*ownershipAuthoringBackend, string, string, string) {
	t.Helper()

	repo := ownershipAuthoringRepo()
	worktree, err := fixture.paths.DataPath("repos/alpha/worktrees/op-1")
	require.NoError(t, err)
	cwd := worktree + "/internal"
	backend := newOwnershipAuthoringBackend(taskmodel.Task{
		ID:                 "op-1",
		Title:              "Render context",
		Description:        "Move detailed task instructions to agent context.",
		AcceptanceCriteria: "Only the latest running attempt can render context.",
		Status:             taskmodel.StatusInProgress,
		Priority:           2,
		IssueType:          taskmodel.IssueTypeTask,
		Metadata: taskmodel.Metadata{
			taskmodel.MetadataBranch:   "orpheus/op-1",
			taskmodel.MetadataWorktree: worktree,
		},
	})
	setupOwnershipAuthoringWorkflow(t, fixture, repo, backend)
	writeOwnershipAgentProfileConfig(t, fixture, "recorder", true)
	_, err = taskstate.NewStore(fixture.paths).StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:       "recorder",
		Interactive: interactive,
		Branch:      "orpheus/op-1",
		Worktree:    worktree,
	})
	require.NoError(t, err)
	fixture.options.AgentWorkingDirectory = cwd
	setWorkflowEnvironment(t, fixture, "ORPHEUS_REPO_ID", "alpha")
	setWorkflowEnvironment(t, fixture, "ORPHEUS_TASK_ID", "op-1")
	setWorkflowEnvironment(t, fixture, "ORPHEUS_WORKTREE", worktree)
	setWorkflowEnvironment(t, fixture, "ORPHEUS_BRANCH", "orpheus/op-1")
	return backend, repo.Path, worktree, cwd
}

func setupOwnershipAgentRepoRoot(t *testing.T, fixture *workflowFixture) (*ownershipAuthoringBackend, string) {
	t.Helper()

	repo := ownershipAuthoringRepo()
	backend := newOwnershipAuthoringBackend(taskmodel.Task{
		ID:        "op-root",
		Title:     "Render repo-root context",
		Status:    taskmodel.StatusInProgress,
		Priority:  2,
		IssueType: taskmodel.IssueTypeTask,
		Metadata: taskmodel.Metadata{
			taskmodel.MetadataBranch:   "orpheus/op-root",
			taskmodel.MetadataWorktree: repo.Path,
		},
	})
	setupOwnershipAuthoringWorkflow(t, fixture, repo, backend)
	writeOwnershipAgentProfileConfig(t, fixture, "recorder", true)
	_, err := taskstate.NewStore(fixture.paths).StartRun("alpha", "op-root", taskstate.StartRunOptions{
		Agent:       "recorder",
		Interactive: true,
		Branch:      "orpheus/op-root",
		Worktree:    repo.Path,
	})
	require.NoError(t, err)
	fixture.options.AgentWorkingDirectory = repo.Path
	setWorkflowEnvironment(t, fixture, "ORPHEUS_REPO_ID", "alpha")
	setWorkflowEnvironment(t, fixture, "ORPHEUS_TASK_ID", "op-root")
	setWorkflowEnvironment(t, fixture, "ORPHEUS_WORKTREE", repo.Path)
	setWorkflowEnvironment(t, fixture, "ORPHEUS_BRANCH", "orpheus/op-root")
	return backend, repo.Path
}

func setupOwnershipActiveReview(t *testing.T, fixture *workflowFixture, taskID string) (*ownershipAuthoringBackend, string, taskstate.ReviewAttempt) {
	t.Helper()

	fixture.options.Environment["ORPHEUS_REVIEWER_ROLE"] = ""
	repo := ownershipAuthoringRepo()
	backend := newOwnershipAuthoringBackend(taskmodel.Task{
		ID:        taskID,
		Title:     "Ready for task done",
		Status:    taskmodel.StatusInProgress,
		Priority:  1,
		IssueType: taskmodel.IssueTypeTask,
		Metadata: taskmodel.Metadata{
			taskmodel.MetadataBranch:   "main",
			taskmodel.MetadataWorktree: repo.Path,
		},
	})
	setupOwnershipAuthoringWorkflow(t, fixture, repo, backend)
	fixture.options.AgentWorkingDirectory = repo.Path
	store := taskstate.NewStore(fixture.paths)
	recordOwnershipMainCompletion(t, store, "alpha", taskID, repo.Path, "Review summary", "Review description.")
	review, err := store.StartReviewWithOptions("alpha", taskID, taskstate.StartReviewOptions{Pipeline: "standard", Step: "ai-review"})
	require.NoError(t, err)
	_, err = store.RecordReviewStep("alpha", taskID, review.Attempt, taskstate.RecordReviewStepOptions{Kind: "agent_review", Name: "ai-review"})
	require.NoError(t, err)
	setWorkflowEnvironment(t, fixture, "ORPHEUS_REPO_ID", "alpha")
	setWorkflowEnvironment(t, fixture, "ORPHEUS_TASK_ID", taskID)
	setWorkflowEnvironment(t, fixture, "ORPHEUS_WORKTREE", repo.Path)
	setWorkflowEnvironment(t, fixture, "ORPHEUS_BRANCH", "main")
	setWorkflowEnvironment(t, fixture, "ORPHEUS_AGENT_PURPOSE", "review")
	setWorkflowEnvironment(t, fixture, "ORPHEUS_REVIEW_ATTEMPT", "1")
	setWorkflowEnvironment(t, fixture, "ORPHEUS_REVIEW_STEP", "ai-review")
	return backend, repo.Path, review
}

func recordOwnershipMainCompletion(t *testing.T, store taskstate.Store, repoID string, taskID string, repoPath string, summary string, description string) {
	t.Helper()
	attempt, err := store.StartRun(repoID, taskID, taskstate.StartRunOptions{Agent: "recorder", Branch: "main", Worktree: repoPath})
	require.NoError(t, err)
	_, err = store.CompleteRun(repoID, taskID, attempt.Attempt, taskstate.CompleteRunOptions{
		Summary:              summary,
		Description:          description,
		DetailedDescription:  "Detailed PR body.",
		TechnicalExplanation: "Technical explanation.",
	})
	require.NoError(t, err)
	_, err = store.FinishRun(repoID, taskID, attempt.Attempt, taskstate.RunStatusSucceeded)
	require.NoError(t, err)
}

func seedOwnershipLegacyRunningAttempt(t *testing.T, fixture *workflowFixture, taskID string, branch string, worktree string) {
	t.Helper()
	rel := fmt.Sprintf("repos/alpha/tasks/%s.yaml", taskID)
	err := fixture.paths.WriteDataYAML(rel, map[string]any{
		"version":   4,
		"repo_id":   "alpha",
		"task_id":   taskID,
		"git_facts": map[string]any{"branch": branch, "worktree": worktree},
		"runs": []map[string]any{{
			"attempt": 1,
			"status":  "running",
			"execution": map[string]any{
				"purpose":    "implementation",
				"status":     "running",
				"started_at": "2026-06-03T10:00:00Z",
				"agent":      "recorder",
			},
		}},
	})
	require.NoError(t, err)
}

func sourceNeutralFailure(message string) error {
	return errors.New(message)
}
