package task_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCrossRepositoryParentProjectionRejectsConflictingParents(t *testing.T) {
	metadata := task.Metadata{task.MetadataCrossRepositoryParent: " be-parent ", "other": "kept"}
	got, err := task.IncludeCrossRepositoryParent(task.RelationSummary{}, metadata)
	require.NoError(t, err)
	assert.Equal(t, "be-parent", got.ParentID)
	assert.Equal(t, "kept", metadata["other"])
	_, err = task.IncludeCrossRepositoryParent(task.RelationSummary{ParentID: "op-parent"}, metadata)
	require.ErrorContains(t, err, "conflicting")
}

func TestRelationshipReaderIncludesAllDirectChildrenAndRejectsIncompleteInformation(t *testing.T) {
	alpha := createTestSource("alpha", "op", "/fixture/alpha")
	beta := createTestSource("beta", "be", "/fixture/beta")
	parent := task.Task{ID: "op-parent", IssueType: task.IssueTypeEpic, Relations: task.RelationSummary{ChildIDs: []string{"op-local"}, ChildCount: 1}}
	local := task.Task{ID: "op-local", IssueType: task.IssueTypeTask, Status: task.StatusClosed, Relations: task.RelationSummary{ParentID: parent.ID}}
	remote := task.Task{ID: "be-nested", IssueType: task.IssueTypeEpic, Status: task.StatusOpen, Relations: task.RelationSummary{ParentID: parent.ID}}
	for _, scenario := range []string{"complete", "inventory unavailable", "backend unavailable", "missing declared child", "wrong child parent", "declared count unavailable", "ambiguous owner"} {
		t.Run(scenario, func(t *testing.T) {
			owner := &recordingFilteredReadBackend{tasks: []task.Task{parent, local}}
			childOwner := &recordingFilteredReadBackend{tasks: []task.Task{remote}}
			sources := []task.RepositorySource{alpha, beta}
			item := parent.Clone()
			switch scenario {
			case "missing declared child":
				item.Relations.ChildIDs = append(item.Relations.ChildIDs, "op-missing")
			case "wrong child parent":
				item.Relations.ChildIDs = append(item.Relations.ChildIDs, "be-other")
				childOwner.tasks = append(childOwner.tasks, task.Task{ID: "be-other", IssueType: task.IssueTypeTask})
			case "declared count unavailable":
				item.Relations.ChildCount = 3
			case "ambiguous owner":
				other := beta
				other.Repository.ID = "collision"
				sources = append(sources, other)
			}
			reader := task.RelationshipReader{Sources: sources, Factory: func(source task.RepositorySource) (task.Getter, error) {
				if source.Repository.ID == "alpha" {
					return owner, nil
				}
				if scenario == "backend unavailable" {
					return nil, errors.New("unavailable")
				}
				if scenario == "inventory unavailable" {
					return failingReadBackend{err: errors.New("unavailable")}, nil
				}
				return childOwner, nil
			}}

			children, err := reader.Children(t.Context(), item)

			if scenario == "complete" {
				require.NoError(t, err)
				require.Len(t, children, 2)
				assert.Equal(t, "be-nested", children[0].ID)
				assert.Equal(t, "op-local", children[1].ID)
			} else {
				require.Error(t, err)
				assert.Empty(t, children)
			}
		})
	}
}

func TestAggregatorEpicMissingChildReportsIncompleteProgressContext(t *testing.T) {
	source := createTestSource("alpha", "op", "/fixture/alpha")
	epic := task.Task{ID: "op-epic", IssueType: task.IssueTypeEpic, Relations: task.RelationSummary{Complete: true, ChildIDs: []string{"op-missing"}, ChildCount: 1}}
	backend := &recordingFilteredReadBackend{tasks: []task.Task{epic}}
	aggregator, err := task.NewAggregator([]task.RepositorySource{source}, func(task.RepositorySource) (task.ReadBackend, error) { return backend, nil })
	require.NoError(t, err)

	snapshot := aggregator.SnapshotForTask(context.Background(), task.RepoTask{Repository: source.Repository, Task: epic})

	assert.Empty(t, snapshot.Failures, "confirmed missing children are policy diagnostics")
	assert.Equal(t, []string{epic.ID}, snapshot.Repositories[0].IncompleteEpicIDs)
	assert.Equal(t, []string{"op-missing"}, snapshot.Repositories[0].Tasks[0].Relations.ChildIDs)
}
