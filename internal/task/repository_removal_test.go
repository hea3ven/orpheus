package task_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func removalSources() []task.RepositorySource {
	return []task.RepositorySource{
		{Repository: task.Repository{ID: "alpha", TaskIDPrefix: "a"}},
		{Repository: task.Repository{ID: "beta", TaskIDPrefix: "b"}},
	}
}

func TestRepositoryRemovalRefusesCrossRepositoryRelationshipsInBothDirections(t *testing.T) {
	for _, test := range []struct {
		name      string
		relations task.RelationSummary
		want      []string
	}{
		{"outgoing dependency", task.RelationSummary{DependencyIDs: []string{"b-2"}}, []string{"alpha/a-1 depends on beta/b-2"}},
		{"incoming dependency", task.RelationSummary{DependentIDs: []string{"b-2"}}, []string{"beta/b-2 depends on alpha/a-1"}},
		{"outgoing epic membership", task.RelationSummary{ParentID: "b-2"}, []string{"alpha/a-1 belongs to epic beta/b-2"}},
		{"incoming epic membership", task.RelationSummary{ChildIDs: []string{"b-2"}}, []string{"beta/b-2 belongs to epic alpha/a-1"}},
		{"all relationships", task.RelationSummary{DependencyIDs: []string{"b-2", "b-3"}, ParentID: "b-epic", ChildIDs: []string{"b-child"}}, []string{
			"alpha/a-1 depends on beta/b-2", "alpha/a-1 depends on beta/b-3",
			"alpha/a-1 belongs to epic beta/b-epic", "beta/b-child belongs to epic alpha/a-1",
		}},
	} {
		for _, owner := range []string{"alpha", "beta"} {
			t.Run(test.name+"/remove "+owner, func(t *testing.T) {
				relations := test.relations
				relations.Complete = true
				item := task.Task{ID: "a-1", Status: task.StatusClosed, Relations: relations}
				sources := removalSources()
				// Removing either end must refuse, even for closed items and
				// references to tasks not present in the target's inventory.
				err := task.CheckRepositoryRemovalRelationships(t.Context(), sources, func(source task.RepositorySource) (task.ReadBackend, error) {
					if source.Repository.ID == "alpha" {
						return fakeReadBackend{tasks: []task.Task{item}}, nil
					}
					return fakeReadBackend{}, nil
				}, owner)

				for _, relationship := range test.want {
					require.ErrorContains(t, err, relationship)
				}
				assert.ErrorContains(t, err, "remove these relationships before deleting the repository")
			})
		}
	}
}

type removalReadBackend struct {
	listed  []task.Task
	detail  task.Task
	listErr error
	getErr  error
}

func (b removalReadBackend) List(context.Context) ([]task.Task, error) { return b.listed, b.listErr }
func (b removalReadBackend) Get(context.Context, string) (task.Task, error) {
	return b.detail, b.getErr
}

func TestRepositoryRemovalRequiresCompleteRelationshipReadsAndAllowsLocalRelationships(t *testing.T) {
	failure := errors.New("relationship read unavailable")
	for _, scenario := range []string{"complete local", "partial inventory", "factory failure", "list failure", "detail failure", "incomplete detail", "unknown relationship"} {
		t.Run(scenario, func(t *testing.T) {
			item := task.Task{ID: "a-1", Relations: task.RelationSummary{Complete: true, ParentID: "a-epic", DependencyIDs: []string{"a-2"}}}
			backend := removalReadBackend{listed: []task.Task{item}, detail: item}
			switch scenario {
			case "partial inventory":
				backend.listed[0].Relations.Complete = false
			case "list failure":
				backend.listErr = failure
			case "detail failure":
				backend.listed[0].Relations.Complete = false
				backend.getErr = failure
			case "incomplete detail":
				backend.listed[0].Relations.Complete = false
				backend.detail.Relations.Complete = false
			case "unknown relationship":
				backend.listed[0].Relations.ParentID = "unknown-2"
			}
			err := task.CheckRepositoryRemovalRelationships(t.Context(), removalSources(), func(source task.RepositorySource) (task.ReadBackend, error) {
				if source.Repository.ID == "beta" {
					return fakeReadBackend{}, nil
				}
				if scenario == "factory failure" {
					return nil, failure
				}
				return backend, nil
			}, "alpha")

			switch scenario {
			case "complete local", "partial inventory":
				require.NoError(t, err)
			default:
				require.ErrorContains(t, err, "cannot complete cross-repository relationship check")
				assert.ErrorContains(t, err, "repair task-source reads before retrying")
				if scenario == "factory failure" || scenario == "list failure" || scenario == "detail failure" {
					assert.ErrorIs(t, err, failure)
				}
			}
		})
	}
}
