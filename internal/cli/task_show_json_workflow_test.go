//go:build integration

package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskShowJSONSharesEverySummaryFieldWithTaskList(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	repos := saveTaskWorkflowRepos(t, fixture, taskWorkflowRepoSpec("alpha", "Alpha", "op"), taskWorkflowRepoSpec("other", "Other", "other"))
	items := []taskmodel.Task{
		taskWFTask("op-ready", "Ready", taskmodel.StatusOpen, taskmodel.IssueTypeTask,
			taskWFDetails("Description\nwith newline", "Design", "Acceptance"), taskWFExternalRef("REF-1"), taskWFLabels("one", "two"),
			func(item *taskmodel.Task) { item.Relations.DependentIDs = []string{"op-blocked"} },
			taskWFCreated("2026-07-01T12:00:00Z"), taskWFUpdated("2026-07-02T12:00:00Z")),
		taskWFTask("op-blocked", "Blocked", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFBlocks("op-ready")),
		taskWFTask("op-missing", "Missing dependency", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFBlocks("op-absent")),
		taskWFTask("op-epic", "Epic", taskmodel.StatusInProgress, taskmodel.IssueTypeEpic),
		taskWFTask("op-child", "Child", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFParent("op-epic")),
		taskWFTask("op-closed", "Closed child", taskmodel.StatusClosed, taskmodel.IssueTypeTask, taskWFParent("op-epic")),
		taskWFTask("op-pr", "Pull request", taskmodel.StatusInProgress, taskmodel.IssueTypeTask, taskWFMetadata(map[string]string{taskmodel.MetadataPRURL: "https://example.test/pr/1"})),
		taskWFTask("op-working", "Working", taskmodel.StatusInProgress, taskmodel.IssueTypeTask),
	}
	reads := withWorkflowSources(t, fixture, map[string]taskSourceResult{
		repos["alpha"]: {tasks: items}, repos["other"]: {err: errors.New("unrelated source must not be read")},
	})
	_, err := taskstate.NewStore(fixture.paths).StartRun("alpha", "op-working", taskstate.StartRunOptions{Agent: "implementer", Branch: "main", Worktree: repos["alpha"]})
	require.NoError(t, err)

	listed, stderr := fixture.mustExecute("task", "list", "--repo", "alpha", "--json")
	assert.Empty(t, stderr)
	closed, stderr := fixture.mustExecute("task", "list", "--repo", "alpha", "--status", "closed", "--json")
	assert.Empty(t, stderr)
	var summaries, closedSummaries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(listed), &summaries))
	require.NoError(t, json.Unmarshal([]byte(closed), &closedSummaries))
	summaries = append(summaries, closedSummaries...)
	require.Len(t, summaries, len(items))
	wantStatuses := map[string]string{"op-ready": "ready", "op-blocked": "blocked", "op-missing": "needs_attention", "op-closed": "closed", "op-pr": "reviewing", "op-working": "working"}
	for _, summary := range summaries {
		id := summary["id"].(string)
		t.Run(id, func(t *testing.T) {
			shown, stderr := fixture.mustExecute("task", "show", id, "--json")
			assert.Empty(t, stderr)
			var detail map[string]any
			require.NoError(t, json.Unmarshal([]byte(shown), &detail))
			for field, value := range summary {
				assert.Contains(t, detail, field)
				assert.Equal(t, value, detail[field], "summary field %s", field)
			}
			assert.Len(t, detail, len(summary)+6, "only the six documented fields extend TaskSummary")
			if want, ok := wantStatuses[id]; ok {
				assert.Equal(t, want, detail["status"])
			}
			if id == "op-epic" {
				assert.Equal(t, map[string]any{"completed": float64(1), "total": float64(2)}, detail["epic_progress"])
				assert.Equal(t, map[string]any{"parent": nil, "children": []any{"op-child", "op-closed"}, "dependencies": []any{}, "dependents": []any{}}, detail["relationships"])
			}
			if id == "op-blocked" {
				assert.Equal(t, map[string]any{"parent": nil, "children": []any{}, "dependencies": []any{"op-ready"}, "dependents": []any{}}, detail["relationships"])
			}
			if id == "op-child" {
				assert.Equal(t, map[string]any{"parent": "op-epic", "children": []any{}, "dependencies": []any{}, "dependents": []any{}}, detail["relationships"])
			}
			if id == "op-ready" {
				assert.Equal(t, "Description\nwith newline", detail["description"])
				assert.Equal(t, "Design", detail["design"])
				assert.Equal(t, "Acceptance", detail["acceptance_criteria"])
				assert.Equal(t, "REF-1", detail["external_ref"])
				assert.Equal(t, []any{"one", "two"}, detail["labels"])
				assert.Equal(t, map[string]any{"parent": nil, "children": []any{}, "dependencies": []any{}, "dependents": []any{"op-blocked"}}, detail["relationships"])
			}
		})
	}
	assert.NotContains(t, reads.directories(), repos["other"])
	assert.Equal(t, 2, reads.count("list"), "show must not list the repository")
}

func TestIntegrationWorkflowTaskShowJSONReadFailuresProduceNoJSON(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		task      taskmodel.Task
		getErrors map[string]error
		corrupt   bool
		want      string
	}{
		{name: "task read", task: taskWFTask("op-1", "Task", taskmodel.StatusOpen, taskmodel.IssueTypeTask), getErrors: map[string]error{"op-1": errors.New("relationship read failed")}, want: "relationship read failed"},
		{name: "dependency read", task: taskWFTask("op-1", "Task", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFBlocks("op-dep")), getErrors: map[string]error{"op-dep": errors.New("dependency unavailable")}, want: "dependency unavailable"},
		{name: "parent read", task: taskWFTask("op-1", "Task", taskmodel.StatusOpen, taskmodel.IssueTypeTask, taskWFParent("op-parent")), getErrors: map[string]error{"op-parent": errors.New("parent unavailable")}, want: "parent unavailable"},
		{name: "child read", task: taskWFTask("op-1", "Epic", taskmodel.StatusOpen, taskmodel.IssueTypeEpic, func(item *taskmodel.Task) { item.Relations.ChildIDs = []string{"op-child"} }), getErrors: map[string]error{"op-child": errors.New("child unavailable")}, want: "child unavailable"},
		{name: "local state", task: taskWFTask("op-1", "Task", taskmodel.StatusOpen, taskmodel.IssueTypeTask), corrupt: true, want: "load local task-state"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			fixture := newCommandWorkflow(t)
			repo := registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
			withWorkflowSources(t, fixture, map[string]taskSourceResult{repo: {tasks: []taskmodel.Task{scenario.task}, getErrors: scenario.getErrors}})
			if scenario.corrupt {
				writeMismatchedTaskState(t, fixture, "alpha", "op-1")
			}

			stdout, stderr, err := fixture.execute("task", "show", "op-1", "--json")

			require.Error(t, err, "main exits nonzero for command errors")
			assert.Empty(t, stdout)
			// main writes returned errors to stderr; relationship diagnostics may already be there.
			assert.Contains(t, stderr+err.Error(), scenario.want)
		})
	}
}

func TestIntegrationWorkflowTaskShowJSONRejectsIncompleteRelationships(t *testing.T) {
	fixture := newCommandWorkflow(t)
	t.Parallel()
	registerWorkflowRepo(t, fixture, "alpha", "Alpha", "op")
	fixture.options.Dependencies.TaskBackendFactory = func(taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		return incompleteShowJSONBackend{}, nil
	}

	stdout, _, err := fixture.execute("task", "show", "op-1", "--json")

	require.ErrorContains(t, err, "incomplete relationships")
	assert.Empty(t, stdout)
}

type incompleteShowJSONBackend struct{}

func (incompleteShowJSONBackend) Get(context.Context, string) (taskmodel.Task, error) {
	return taskWFTask("op-1", "Task", taskmodel.StatusOpen, taskmodel.IssueTypeTask), nil
}

func (incompleteShowJSONBackend) List(context.Context) ([]taskmodel.Task, error) {
	return nil, errors.New("unexpected list")
}
