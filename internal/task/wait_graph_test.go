package task_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateServiceRejectsCircularParentAndDependencyWaitsBeforeMutation(t *testing.T) {
	for _, scenario := range []string{"parent cycle", "depends on parent", "parent depends on child", "epic depends on descendant", "mixed transitive wait", "combined parent and dependency"} {
		t.Run(scenario, func(t *testing.T) {
			alpha := createTestSource("alpha", "op", "/fixture/alpha")
			beta := createTestSource("beta", "be", "/fixture/beta")
			gamma := createTestSource("gamma", "ga", "/fixture/gamma")
			consumer := updateBackendWithCurrent()
			current := consumer.tasks["op-current"]
			current.IssueType = task.IssueTypeEpic
			parent := task.Task{ID: "be-parent", IssueType: task.IssueTypeEpic, Status: task.StatusOpen}
			prerequisite := task.Task{ID: "ga-prerequisite", IssueType: task.IssueTypeTask, Status: task.StatusOpen}
			opts := task.UpdateOptions{ID: current.ID}
			switch scenario {
			case "parent cycle":
				parent.Relations.ParentID = current.ID
				opts.ParentID = &parent.ID
			case "depends on parent":
				current.Relations.ParentID = parent.ID
				opts.AddBlockingIDs = []string{parent.ID}
			case "parent depends on child":
				parent.Relations.DependencyIDs = []string{current.ID}
				opts.ParentID = &parent.ID
			case "epic depends on descendant":
				parent.Relations.ParentID = current.ID
				prerequisite.Relations.ParentID = parent.ID
				opts.AddBlockingIDs = []string{prerequisite.ID}
			case "mixed transitive wait":
				current.Relations.ParentID = parent.ID
				prerequisite.IssueType = task.IssueTypeEpic
				prerequisite.Relations.DependencyIDs = []string{current.ID}
				parent.Relations.ParentID = prerequisite.ID
				opts.AddBlockingIDs = []string{prerequisite.ID}
			case "combined parent and dependency":
				opts.ParentID = &parent.ID
				opts.AddBlockingIDs = []string{parent.ID}
			}
			consumer.tasks[current.ID] = current
			owner := &fakeUpdateBackend{tasks: map[string]task.Task{parent.ID: parent}}
			transitive := &fakeUpdateBackend{tasks: map[string]task.Task{prerequisite.ID: prerequisite}}
			service := task.UpdateService{Sources: []task.RepositorySource{alpha, beta, gamma}, BackendFactory: func(source task.RepositorySource) (task.UpdateBackend, error) {
				switch source.Repository.ID {
				case "alpha":
					return consumer, nil
				case "beta":
					return owner, nil
				default:
					return transitive, nil
				}
			}}

			_, err := service.Update(t.Context(), alpha, opts)

			require.Error(t, err)
			if scenario == "parent cycle" {
				assert.ErrorContains(t, err, "parent-child cycle")
			} else {
				assert.ErrorContains(t, err, "circular")
			}
			assert.False(t, consumer.updated)
			assert.False(t, owner.updated)
		})
	}
}

func TestUpdateServiceRejectsEpicReparentWhenExistingDescendantDependsOnNewParent(t *testing.T) {
	for _, nested := range []bool{false, true} {
		name := "direct child"
		if nested {
			name = "nested descendant"
		}
		t.Run(name, func(t *testing.T) {
			alpha := createTestSource("alpha", "op", "/fixture/alpha")
			beta := createTestSource("beta", "be", "/fixture/beta")
			consumer := updateBackendWithCurrent()
			epic := consumer.tasks["op-current"]
			epic.IssueType = task.IssueTypeEpic
			consumer.tasks[epic.ID] = epic
			parent := task.Task{ID: "be-parent", IssueType: task.IssueTypeEpic, Status: task.StatusOpen}
			child := task.Task{ID: "op-child", IssueType: task.IssueTypeTask, Status: task.StatusOpen, Relations: task.RelationSummary{ParentID: epic.ID, DependencyIDs: []string{parent.ID}}}
			owner := &fakeUpdateBackend{tasks: map[string]task.Task{parent.ID: parent}}
			if nested {
				middle := task.Task{ID: "be-nested", IssueType: task.IssueTypeEpic, Status: task.StatusOpen, Relations: task.RelationSummary{ParentID: epic.ID}}
				owner.tasks[middle.ID] = middle
				child.Relations.ParentID = middle.ID
			}
			consumer.tasks[child.ID] = child
			service := task.UpdateService{Sources: []task.RepositorySource{alpha, beta}, BackendFactory: func(source task.RepositorySource) (task.UpdateBackend, error) {
				if source.Repository.ID == "alpha" {
					return consumer, nil
				}
				return owner, nil
			}}

			_, err := service.Update(t.Context(), alpha, task.UpdateOptions{ID: epic.ID, ParentID: &parent.ID})

			require.ErrorContains(t, err, "circular")
			assert.False(t, consumer.updated)
			assert.False(t, owner.updated)
			assert.Empty(t, consumer.tasks[epic.ID].Relations.ParentID)
			assert.Equal(t, []string{parent.ID}, consumer.tasks[child.ID].Relations.DependencyIDs)
		})
	}
}

func TestUpdateServiceUsesResultingGraphAndAllowsSiblingDependencies(t *testing.T) {
	alpha := createTestSource("alpha", "op", "/fixture/alpha")
	beta := createTestSource("beta", "be", "/fixture/beta")
	consumer := updateBackendWithCurrent()
	parent := task.Task{ID: "be-parent", IssueType: task.IssueTypeEpic, Status: task.StatusOpen}
	sibling := task.Task{ID: "be-sibling", IssueType: task.IssueTypeTask, Status: task.StatusOpen, Relations: task.RelationSummary{ParentID: parent.ID}}
	current := consumer.tasks["op-current"]
	current.Relations.DependencyIDs = []string{parent.ID}
	consumer.tasks[current.ID] = current
	owner := &fakeUpdateBackend{tasks: map[string]task.Task{parent.ID: parent, sibling.ID: sibling}}
	service := task.UpdateService{Sources: []task.RepositorySource{alpha, beta}, BackendFactory: func(source task.RepositorySource) (task.UpdateBackend, error) {
		if source.Repository.ID == "alpha" {
			return consumer, nil
		}
		return owner, nil
	}}

	_, err := service.Update(t.Context(), alpha, task.UpdateOptions{ID: current.ID, ParentID: &parent.ID, RemoveBlockingIDs: []string{parent.ID}, AddBlockingIDs: []string{sibling.ID}})

	require.NoError(t, err)
	assert.True(t, consumer.opts.CrossRepositoryParent)
	assert.Equal(t, parent.ID, *consumer.opts.ParentID)
	assert.Equal(t, []string{sibling.ID}, consumer.opts.CrossRepositoryBlockingIDs)
}

func TestCreateServiceRejectsChildWaitingForItsOwnParentToClose(t *testing.T) {
	source := createTestSource("alpha", "op", "/fixture/alpha")
	parent := task.Task{ID: "op-parent", IssueType: task.IssueTypeEpic, Status: task.StatusOpen}
	backend := &fakeCreateBackend{tasks: map[string]task.Task{parent.ID: parent}}
	service := task.CreateService{Sources: []task.RepositorySource{source}, BackendFactory: func(task.RepositorySource) (task.CreateBackend, error) { return backend, nil }}

	_, err := service.Create(t.Context(), source, task.CreateRequest{Title: "Child", Description: "Plan", AcceptanceCriteria: "Done", ParentID: parent.ID, BlockingIDs: []string{parent.ID}})

	require.ErrorContains(t, err, "circular")
	assert.Empty(t, backend.created.Title)
}

func TestUpdateServiceClearsUnavailableParentWithoutReadingItsOwner(t *testing.T) {
	for _, issueType := range []task.IssueType{task.IssueTypeTask, task.IssueTypeEpic} {
		t.Run(string(issueType), func(t *testing.T) {
			source := createTestSource("alpha", "op", "/fixture/alpha")
			backend := updateBackendWithCurrent()
			current := backend.tasks["op-current"]
			current.IssueType = issueType
			current.Relations.ParentID = "unregistered-parent"
			backend.tasks[current.ID] = current

			_, err := updateService(source, backend).Update(t.Context(), source, task.UpdateOptions{ID: current.ID, ParentID: stringPtr("")})

			require.NoError(t, err)
			require.NotNil(t, backend.opts.ParentID)
			assert.Empty(t, *backend.opts.ParentID)
		})
	}
}
