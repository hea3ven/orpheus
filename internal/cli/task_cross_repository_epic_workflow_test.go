//go:build integration

package cli_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowCrossRepositoryNestedEpicsPreserveOwnershipAndLifecycle(t *testing.T) {
	fixture := newCommandWorkflow(t)
	alpha := newOwnershipAuthoringBackend(ownershipAuthoringEpic("op-root", taskmodel.StatusOpen), ownershipAuthoringEpic("op-other", taskmodel.StatusOpen))
	beta := newOwnershipAuthoringBackend()
	beta.createdID = "be-nested"
	betaRepo := crossRepositoryOwner()
	betaRepo.TaskSource, betaRepo.TaskMode, betaRepo.TaskPrefix = "gig", "managed", "be"
	betaRepo.BeadsMode, betaRepo.BeadsPrefix = "", ""
	fixture.withRegisteredRepos(ownershipAuthoringRepo(), betaRepo)
	fixture.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		if source.Repository.ID == "alpha" {
			return alpha, nil
		}
		return beta, nil
	}

	fixture.mustExecute("task", "create", "--repo", "beta", "--type", "epic", "--title", "Nested", "--description", "Plan", "--acceptance", "Done", "--parent", "op-root")
	fixture.mustExecute("task", "create", "--repo", "alpha", "--title", "Child", "--description", "Plan", "--acceptance", "Done", "--parent", "be-nested")
	assert.Equal(t, "op-root", beta.task("be-nested").Relations.ParentID)
	assert.Equal(t, "be-nested", alpha.task("op-9").Relations.ParentID)
	assert.True(t, beta.creates[0].CrossRepositoryParent)
	assert.True(t, alpha.creates[0].CrossRepositoryParent)
	_, _, err := fixture.execute("task", "start", "be-nested")
	require.ErrorContains(t, err, "parent epic op-root must be in progress")
	fixture.mustExecute("task", "start", "op-root")
	fixture.mustExecute("task", "start", "be-nested")

	for _, args := range [][]string{{"status"}, {"task", "list"}} {
		text, stderr := fixture.mustExecute(append(args, "--sort", "status")...)
		assert.Empty(t, stderr)
		assertStatusGroupOrder(t, text, []string{"op-root", "└─ be-nested", "  └─ op-9", "op-other"})
		for _, id := range []string{"op-root", "be-nested", "op-9", "op-other"} {
			rowCount := 0
			for _, line := range strings.Split(text, "\n") {
				if strings.Contains(line, id) {
					rowCount++
				}
			}
			assert.Equal(t, 1, rowCount, "%v: %s must appear once", args, id)
		}
		assert.Contains(t, tableRowForTask(text, "be-nested"), betaRepo.Name)
		assert.Contains(t, tableRowForTask(text, "be-nested"), "Working")
		assert.Contains(t, tableRowForTask(text, "be-nested"), "0/1 done")
		assert.Contains(t, tableRowForTask(text, "op-9"), "Alpha Repo")
		assert.Contains(t, tableRowForTask(text, "op-9"), "Ready")

		jsonOut, stderr := fixture.mustExecute(append(args, "--sort", "status", "--json")...)
		assert.Empty(t, stderr)
		var entries []taskViewJSONTaskEntry
		require.NoError(t, json.Unmarshal([]byte(jsonOut), &entries))
		require.Len(t, entries, 4)
		assert.Equal(t, []string{"op-root", "be-nested", "op-9", "op-other"}, []string{entries[0].ID, entries[1].ID, entries[2].ID, entries[3].ID})
		assert.Equal(t, taskViewJSONRepository{ID: "beta", Name: betaRepo.Name, TaskIDPrefix: "be"}, entries[1].Repository)
		assert.Equal(t, "idle", entries[1].Status)
		assert.Equal(t, "no_run", entries[1].Detail.Kind)
		assert.Equal(t, "epic", entries[1].IssueType)
		assert.Equal(t, &taskViewJSONEpicProgress{Completed: 0, Total: 1}, entries[1].EpicProgress)
		assert.Equal(t, "alpha", entries[2].Repository.ID)
		assert.Equal(t, "ready", entries[2].Status)
		assert.Equal(t, "none", entries[2].Detail.Kind)
	}

	for _, filter := range [][]string{{"--repo", "alpha", "--query", "op-9"}, {"--type", "task"}} {
		args := append([]string{"task", "list"}, filter...)
		text, stderr := fixture.mustExecute(args...)
		assert.Empty(t, stderr)
		assert.Contains(t, text, "op-9")
		assert.NotContains(t, text, "op-root")
		assert.NotContains(t, text, "be-nested")
		assert.NotContains(t, text, "└─")
		jsonOut, _ := fixture.mustExecute(append(args, "--json")...)
		var entries []taskViewJSONTaskEntry
		require.NoError(t, json.Unmarshal([]byte(jsonOut), &entries))
		require.Len(t, entries, 1)
		assert.Equal(t, "op-9", entries[0].ID)
		assert.Equal(t, "ready", entries[0].Status)
	}

	listed, _ := fixture.mustExecute("task", "list", "--repo", "alpha", "--query", "op-root", "--json")
	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(listed), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, map[string]any{"completed": float64(0), "total": float64(1)}, rows[0]["epic_progress"])
	shown, _ := fixture.mustExecute("task", "show", "op-root", "--json")
	var detail map[string]any
	require.NoError(t, json.Unmarshal([]byte(shown), &detail))
	assert.Equal(t, []any{"be-nested"}, detail["relationships"].(map[string]any)["children"])
	text, _ := fixture.mustExecute("task", "show", "op-root")
	assert.Contains(t, text, "be-nested")
	_, _, err = fixture.execute("task", "close", "op-root")
	require.ErrorContains(t, err, "be-nested")
	_, _, err = fixture.execute("task", "close", "be-nested")
	require.ErrorContains(t, err, "op-9")

	fixture.mustExecute("task", "edit", "be-nested", "--parent", "op-other")
	assert.Equal(t, "op-other", beta.task("be-nested").Relations.ParentID)
	assert.Empty(t, alpha.task("be-nested").ID, "reparenting does not transfer ownership")
	fixture.mustExecute("task", "edit", "be-nested", "--parent", "op-root")
	fixture.mustExecute("task", "edit", "op-9", "--parent", "")
	assert.Empty(t, alpha.task("op-9").Relations.ParentID)
	fixture.mustExecute("task", "close", "be-nested")
	fixture.mustExecute("task", "close", "op-root")
	assert.Equal(t, taskmodel.StatusClosed, alpha.task("op-root").Status)
	assert.Equal(t, taskmodel.StatusClosed, beta.task("be-nested").Status)
}

func TestIntegrationWorkflowEpicReparentRejectsCircularWaitThroughExistingDescendants(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "direct child"
		if nested {
			name = "nested descendant"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newCommandWorkflow(t)
			alpha := newOwnershipAuthoringBackend(ownershipAuthoringEpic("op-root", taskmodel.StatusOpen))
			beta := newOwnershipAuthoringBackend(ownershipAuthoringEpic("be-parent", taskmodel.StatusOpen))
			alpha.createdID = "op-child"
			beta.createdID = "be-nested"
			fixture.withRegisteredRepos(ownershipAuthoringRepo(), crossRepositoryOwner())
			fixture.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
				if source.Repository.ID == "alpha" {
					return alpha, nil
				}
				return beta, nil
			}
			childParent := "op-root"
			if nested {
				fixture.mustExecute("task", "create", "--repo", "beta", "--type", "epic", "--title", "Nested", "--description", "Plan", "--acceptance", "Done", "--parent", "op-root")
				childParent = "be-nested"
			}
			fixture.mustExecute("task", "create", "--repo", "alpha", "--title", "Child", "--description", "Plan", "--acceptance", "Done", "--parent", childParent, "--blocked-by", "be-parent")
			beforeRoot := alpha.task("op-root")
			beforeChild := alpha.task("op-child")
			beforeParent := beta.task("be-parent")

			_, _, err := fixture.execute("task", "edit", "op-root", "--parent", "be-parent")

			require.ErrorContains(t, err, "circular")
			assert.Empty(t, alpha.updates)
			assert.Empty(t, beta.updates)
			assert.Equal(t, beforeRoot, alpha.task("op-root"))
			assert.Equal(t, beforeChild, alpha.task("op-child"))
			assert.Equal(t, beforeParent, beta.task("be-parent"))

			fixture.mustExecute("task", "edit", "op-child", "--remove-block", "be-parent")
			fixture.mustExecute("task", "edit", "op-root", "--parent", "be-parent")
			assert.Equal(t, "be-parent", alpha.task("op-root").Relations.ParentID)
			assert.Empty(t, alpha.task("op-child").Relations.DependencyIDs)
			assert.Equal(t, childParent, alpha.task("op-child").Relations.ParentID)
		})
	}
}

func TestIntegrationWorkflowEpicReparentRequiresCompleteInventoryButBrokenParentCanBeCleared(t *testing.T) {
	fixture := newCommandWorkflow(t)
	root := ownershipAuthoringEpic("op-root", taskmodel.StatusOpen)
	root.Relations.ParentID = "unregistered-parent"
	nested := ownershipAuthoringEpic("be-nested", taskmodel.StatusOpen)
	nested.Relations.ParentID = root.ID
	alpha := newOwnershipAuthoringBackend(root)
	beta := newOwnershipAuthoringBackend(ownershipAuthoringEpic("be-parent", taskmodel.StatusOpen), nested)
	beta.listError = errors.New("child inventory unavailable")
	fixture.withRegisteredRepos(ownershipAuthoringRepo(), crossRepositoryOwner())
	fixture.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		if source.Repository.ID == "alpha" {
			return alpha, nil
		}
		return beta, nil
	}

	_, _, err := fixture.execute("task", "edit", root.ID, "--parent", "be-parent")

	require.ErrorContains(t, err, "direct children incomplete")
	assert.Empty(t, alpha.updates)
	assert.Empty(t, beta.updates)
	assert.Equal(t, root, alpha.task(root.ID))
	inventoryReads := beta.listCalls

	fixture.mustExecute("task", "edit", root.ID, "--parent", "")

	assert.Empty(t, alpha.task(root.ID).Relations.ParentID)
	assert.Equal(t, inventoryReads, beta.listCalls, "detachment must not require child inventory")
	assert.Equal(t, nested, beta.task(nested.ID))
}

func TestIntegrationWorkflowEpicUnavailableMembershipIsIncompleteAndCannotClose(t *testing.T) {
	fixture := newCommandWorkflow(t)
	alpha := newOwnershipAuthoringBackend(ownershipAuthoringEpic("op-root", taskmodel.StatusInProgress))
	child := ownershipAuthoringTask("be-child", "Child", taskmodel.StatusClosed, taskmodel.IssueTypeTask)
	child.Relations.ParentID = "op-root"
	beta := newOwnershipAuthoringBackend(child)
	beta.listError = errors.New("child inventory unavailable")
	fixture.withRegisteredRepos(ownershipAuthoringRepo(), crossRepositoryOwner())
	fixture.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		if source.Repository.ID == "alpha" {
			return alpha, nil
		}
		return beta, nil
	}

	for _, args := range [][]string{{"status", "--full", "--json"}, {"task", "list", "--repo", "alpha", "--query", "op-root", "--json"}} {
		stdout, stderr, err := fixture.execute(args...)
		require.Error(t, err)
		assert.Contains(t, stderr, "incomplete")
		var rows []map[string]any
		require.NoError(t, json.Unmarshal([]byte(stdout), &rows))
		found := false
		for _, row := range rows {
			if row["id"] == "op-root" {
				assert.Equal(t, true, row["epic_progress"].(map[string]any)["incomplete"])
				found = true
			}
		}
		require.True(t, found)
	}
	stdout, _, err := fixture.execute("task", "show", "op-root", "--json")
	require.Error(t, err)
	assert.Empty(t, stdout)
	_, _, err = fixture.execute("task", "close", "op-root")
	require.ErrorContains(t, err, "incomplete")
	assert.Empty(t, alpha.closed)

	beta.listError = nil
	fixture.mustExecute("task", "close", "op-root")
	assert.Equal(t, taskmodel.StatusClosed, alpha.task("op-root").Status)
}

func TestIntegrationWorkflowCrossRepositoryParentReadinessAgreesWithDispatch(t *testing.T) {
	for _, scenario := range []string{"open", "in_progress", "closed", "missing", "unreadable", "unknown"} {
		t.Run(scenario, func(t *testing.T) {
			item := anOpenTask("op-child")
			item.Relations.ParentID = "be-parent"
			fixture := newTaskWorkflowFixture(t, item)
			parent := taskWFTask("be-parent", "Parent", taskmodel.Status(scenario), taskmodel.IssueTypeEpic)
			beta := newMemoryTaskBackend([]taskmodel.Task{parent})
			if scenario == "missing" {
				delete(beta.tasks, parent.ID)
			}
			if scenario != "unknown" {
				fixture.withRegisteredRepos(taskWorkflowRepository(), crossRepositoryOwner())
			}
			fixture.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
				if source.Repository.ID == "alpha" {
					return crossRepositoryReadBackend{memoryTaskBackend: fixture.backend}, nil
				}
				if scenario == "unreadable" {
					return nil, errors.New("parent unavailable")
				}
				return crossRepositoryReadBackend{memoryTaskBackend: beta}, nil
			}
			want := "needs_attention"
			if scenario == "open" {
				want = "blocked"
			}
			if scenario == "in_progress" {
				want = "ready"
			}

			stdout, _, err := fixture.execute("task", "list", "--repo", "alpha", "--query", "op-child", "--json")
			if scenario == "unreadable" {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			var rows []map[string]any
			require.NoError(t, json.Unmarshal([]byte(stdout), &rows))
			require.Len(t, rows, 1)
			assert.Equal(t, want, rows[0]["status"])
			if scenario == "in_progress" {
				fixture.withAgentExitingWithoutCompletion(1)
			}
			_, _, err = fixture.execute("task", "run", item.ID)
			state, final := fixture.loadFinalTask(item.ID)
			if scenario == "in_progress" {
				require.NoError(t, err)
				assert.Len(t, state.Runs, 1)
				assert.Equal(t, taskmodel.StatusInProgress, final.Status)
			} else {
				require.Error(t, err)
				fixture.assertDispatchDidNotStart(state)
			}
		})
	}
}

func TestIntegrationWorkflowCrossRepositoryParentFailureDoesNotPreventInProgressRetry(t *testing.T) {
	item := anOpenTask("op-retry")
	item.Relations.ParentID = "be-parent"
	fixture := newTaskWorkflowFixture(t, item)
	beta := newMemoryTaskBackend([]taskmodel.Task{taskWFTask("be-parent", "Parent", taskmodel.StatusInProgress, taskmodel.IssueTypeEpic)})
	fixture.withRegisteredRepos(taskWorkflowRepository(), crossRepositoryOwner())
	unavailable := false
	fixture.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		if source.Repository.ID == "alpha" {
			return crossRepositoryReadBackend{memoryTaskBackend: fixture.backend}, nil
		}
		if unavailable {
			return nil, errors.New("parent unavailable")
		}
		return crossRepositoryReadBackend{memoryTaskBackend: beta}, nil
	}
	failure := errors.New("implementation failed")
	fixture.withFailingAgent(failure)
	_, _, err := fixture.execute("task", "run", item.ID)
	require.ErrorIs(t, err, failure)
	before, _ := fixture.loadFinalTask(item.ID)
	unavailable = true
	fixture.withAgentExitingWithoutCompletion(1)

	_, _, err = fixture.execute("task", "run", item.ID)

	require.NoError(t, err)
	after, final := fixture.loadFinalTask(item.ID)
	require.Len(t, after.Runs, 2)
	assert.Equal(t, before.Runs[0], after.Runs[0])
	assert.Equal(t, taskmodel.StatusInProgress, final.Status)
}
