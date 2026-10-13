//go:build integration

package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type crossRepositoryReadBackend struct {
	*memoryTaskBackend
	getError error
}

func (b crossRepositoryReadBackend) Get(ctx context.Context, id string) (taskmodel.Task, error) {
	if b.getError != nil {
		return taskmodel.Task{}, b.getError
	}
	return b.memoryTaskBackend.Get(ctx, id)
}

func (b crossRepositoryReadBackend) ListFiltered(ctx context.Context, filter taskmodel.ListFilter) ([]taskmodel.Task, error) {
	items, err := b.List(ctx)
	if err != nil {
		return nil, err
	}
	var result []taskmodel.Task
	for _, item := range items {
		if filter.Matches(item) {
			result = append(result, item)
		}
	}
	return result, nil
}

func crossRepositoryOwner() registry.Repo {
	repo := aRegisteredRepo("beta")
	repo.BeadsPrefix = "be"
	return repo
}

func TestIntegrationWorkflowCrossRepositoryReadinessAgreesWithInitialDispatch(t *testing.T) {
	for _, scenario := range []string{"open", "in_progress", "closed", "missing", "unreadable", "unreadable detail", "unknown", "ambiguous"} {
		t.Run(scenario, func(t *testing.T) {
			dependencyID := "be-1"
			if scenario == "ambiguous" {
				dependencyID = "be-long-1"
			}
			item := anOpenTask("op-consumer")
			item.Relations = taskmodel.RelationSummary{Complete: true, DependencyIDs: []string{dependencyID}}
			// A consumer's embedded row is not authoritative for another repository.
			item.RelatedItems = []taskmodel.Task{aClosedTask(dependencyID)}
			fixture := newTaskWorkflowFixture(t, item)
			owner := newMemoryTaskBackend([]taskmodel.Task{taskWFTask(dependencyID, "Prerequisite", taskmodel.Status(scenario), taskmodel.IssueTypeEpic)})
			repos := []registry.Repo{taskWorkflowRepository(), crossRepositoryOwner()}
			switch scenario {
			case "unreadable detail":
				owner.tasks[dependencyID] = aClosedTask(dependencyID)
			case "missing":
				delete(owner.tasks, dependencyID)
			case "unknown":
				repos = repos[:1]
			case "ambiguous":
				other := aRegisteredRepo("gamma")
				other.BeadsPrefix = "be-long"
				repos = append(repos, other)
			}
			fixture.withRegisteredRepos(repos...)
			fixture.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
				if source.Repository.ID == "alpha" {
					return crossRepositoryReadBackend{memoryTaskBackend: fixture.backend}, nil
				}
				if scenario == "unreadable" {
					return nil, errors.New("owner unavailable")
				}
				if scenario == "unreadable detail" {
					return crossRepositoryReadBackend{memoryTaskBackend: owner, getError: errors.New("reference detail unavailable")}, nil
				}
				return crossRepositoryReadBackend{memoryTaskBackend: owner}, nil
			}
			wantStatus := "needs_attention"
			if scenario == "open" || scenario == "in_progress" {
				wantStatus = "blocked"
			}
			if scenario == "closed" {
				wantStatus = "ready"
			}
			for _, args := range [][]string{
				{"status", "--full", "--json"},
				{"task", "list", "--repo", "alpha", "--json"},
				{"task", "list", "--repo", "alpha", "--query", "consumer", "--json"},
			} {
				stdout, _, err := fixture.execute(args...)
				if scenario == "unreadable" || scenario == "unreadable detail" {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				var rows []map[string]any
				require.NoError(t, json.Unmarshal([]byte(stdout), &rows))
				found := false
				for _, row := range rows {
					if row["id"] == item.ID {
						assert.Equal(t, wantStatus, row["status"], "%v", args)
						found = true
					}
				}
				require.True(t, found, "consumer absent from %s", stdout)
				if args[0] == "task" {
					assert.Len(t, rows, 1, "remote context must not leak through output filters")
				}
			}
			stdout, _, err := fixture.execute("task", "show", item.ID, "--json")
			if scenario == "unreadable" || scenario == "unreadable detail" {
				require.Error(t, err)
				assert.Empty(t, stdout)
			} else {
				require.NoError(t, err)
				var shown map[string]any
				require.NoError(t, json.Unmarshal([]byte(stdout), &shown))
				assert.Equal(t, wantStatus, shown["status"])
				assert.Equal(t, []any{dependencyID}, shown["relationships"].(map[string]any)["dependencies"])
			}

			if scenario == "closed" {
				fixture.withAgentExitingWithoutCompletion(1)
			}
			_, _, err = fixture.execute("task", "run", item.ID)

			state, final := fixture.loadFinalTask(item.ID)
			if scenario == "closed" {
				require.NoError(t, err)
				assert.Len(t, state.Runs, 1)
				assert.Equal(t, taskmodel.StatusInProgress, final.Status)
			} else {
				require.Error(t, err)
				fixture.assertDispatchDidNotStart(state)
				assert.Equal(t, taskmodel.StatusOpen, final.Status)
			}
		})
	}
}

func TestIntegrationWorkflowCrossRepositoryAuthoringAndEpicActivation(t *testing.T) {
	fixture := newCommandWorkflow(t)
	consumer := newOwnershipAuthoringBackend()
	prerequisite := ownershipAuthoringTask("be-1", "Prerequisite", taskmodel.StatusInProgress, taskmodel.IssueTypeTask)
	owner := newOwnershipAuthoringBackend(prerequisite, ownershipAuthoringEpic("be-2", taskmodel.StatusClosed))
	fixture.withRegisteredRepos(ownershipAuthoringRepo(), crossRepositoryOwner())
	fixture.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		if source.Repository.ID == "alpha" {
			return consumer, nil
		}
		return owner, nil
	}

	fixture.mustExecute("task", "create", "--repo", "alpha", "--type", "epic", "--title", "Consumer", "--description", "Plan", "--acceptance", "Done", "--blocked-by", "be-1")
	require.Len(t, consumer.creates, 1)
	assert.Equal(t, []string{"be-1"}, consumer.creates[0].CrossRepositoryBlockingIDs)
	fixture.mustExecute("task", "edit", "op-9", "--add-block", "be-1")
	assert.Equal(t, []string{"be-1"}, consumer.task("op-9").Relations.DependencyIDs)
	fixture.mustExecute("task", "edit", "op-9", "--add-block", "be-2")
	assert.Equal(t, []string{"be-2"}, consumer.updates[len(consumer.updates)-1].CrossRepositoryBlockingIDs)
	_, _, err := fixture.execute("task", "start", "op-9")
	require.ErrorContains(t, err, "blocking dependencies are not closed")
	assert.Empty(t, consumer.started)

	prerequisite.Status = taskmodel.StatusClosed
	owner.tasks[prerequisite.ID] = prerequisite
	fixture.mustExecute("task", "start", "op-9")
	assert.Equal(t, taskmodel.StatusInProgress, consumer.task("op-9").Status)
	assert.Zero(t, owner.mutationCount(), "activation and authoring only mutate the consumer")

	fixture.withRegisteredRepos(ownershipAuthoringRepo())
	for range 2 {
		fixture.mustExecute("task", "edit", "op-9", "--remove-block", "be-1", "--remove-block", "be-2")
	}
	assert.Empty(t, consumer.task("op-9").Relations.DependencyIDs)
}

func TestIntegrationWorkflowEpicStartRejectsUnavailableCrossRepositoryDependencies(t *testing.T) {
	for _, scenario := range []string{"missing", "unreadable", "unknown", "ambiguous"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newCommandWorkflow(t)
			dependencyID := "be-1"
			if scenario == "ambiguous" {
				dependencyID = "be-long-1"
			}
			epic := ownershipAuthoringEpic("op-epic", taskmodel.StatusOpen)
			epic.Relations.DependencyIDs = []string{dependencyID}
			consumer := newOwnershipAuthoringBackend(epic)
			owner := newOwnershipAuthoringBackend()
			repos := []registry.Repo{ownershipAuthoringRepo(), crossRepositoryOwner()}
			if scenario == "unknown" {
				repos = repos[:1]
			}
			if scenario == "ambiguous" {
				other := aRegisteredRepo("gamma")
				other.BeadsPrefix = "be-long"
				repos = append(repos, other)
			}
			fixture.withRegisteredRepos(repos...)
			fixture.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
				if source.Repository.ID == "alpha" {
					return consumer, nil
				}
				if scenario == "unreadable" {
					return nil, errors.New("source unavailable")
				}
				return owner, nil
			}

			_, _, err := fixture.execute("task", "start", epic.ID)

			require.ErrorContains(t, err, "cannot inspect blocking dependency")
			assert.Zero(t, consumer.mutationCount())
			assert.Equal(t, taskmodel.StatusOpen, consumer.task(epic.ID).Status)
		})
	}
}
