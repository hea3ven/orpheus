package task_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateServiceWritesCrossRepositoryDependenciesOnlyToConsumer(t *testing.T) {
	alpha := createTestSource("alpha", "op", "/fixture/alpha")
	beta := createTestSource("beta", "be", "/fixture/beta")
	for _, kind := range []task.IssueType{task.IssueTypeTask, task.IssueTypeEpic} {
		t.Run(string(kind), func(t *testing.T) {
			consumer := &fakeCreateBackend{}
			owner := &fakeCreateBackend{tasks: map[string]task.Task{"be-1": {ID: "be-1", IssueType: kind, Status: task.StatusOpen}}}
			service := task.CreateService{Sources: []task.RepositorySource{alpha, beta}, BackendFactory: func(source task.RepositorySource) (task.CreateBackend, error) {
				if source.Repository.ID == "alpha" {
					return consumer, nil
				}
				return owner, nil
			}}

			_, err := service.Create(context.Background(), alpha, task.CreateRequest{Title: "Consumer", Description: "Plan", AcceptanceCriteria: "Done", BlockingIDs: []string{"be-1", "be-1"}})

			require.NoError(t, err)
			assert.Equal(t, []string{"be-1"}, consumer.created.BlockingIDs)
			assert.Equal(t, []string{"be-1"}, consumer.created.CrossRepositoryBlockingIDs)
			assert.Empty(t, owner.created.Title)
		})
	}
}

func TestUpdateServiceTraversesCrossRepositoryGraphBeforeAnyMutation(t *testing.T) {
	for _, scenario := range []string{"valid", "cycle", "missing", "unreadable", "ambiguous", "unknown", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			alpha := createTestSource("alpha", "op", "/fixture/alpha")
			beta := createTestSource("beta", "be", "/fixture/beta")
			gamma := createTestSource("gamma", "ga", "/fixture/gamma")
			sources := []task.RepositorySource{alpha, beta, gamma}
			consumer := updateBackendWithCurrent()
			owner := &fakeUpdateBackend{tasks: map[string]task.Task{"be-1": {ID: "be-1", IssueType: task.IssueTypeEpic, Status: task.StatusOpen, Relations: task.RelationSummary{DependencyIDs: []string{"ga-1"}}}}}
			transitive := &fakeUpdateBackend{tasks: map[string]task.Task{"ga-1": {ID: "ga-1", IssueType: task.IssueTypeTask, Status: task.StatusClosed}}}
			switch scenario {
			case "cycle":
				transitive.tasks["ga-1"] = task.Task{ID: "ga-1", IssueType: task.IssueTypeTask, Relations: task.RelationSummary{DependencyIDs: []string{"op-current"}}}
			case "missing":
				delete(transitive.tasks, "ga-1")
			case "ambiguous":
				duplicate := gamma
				duplicate.Repository.ID = "collision"
				sources = append(sources, duplicate)
			case "unknown":
				sources = sources[:2]
			case "unsupported":
				owner.tasks["be-1"] = task.Task{ID: "be-1", IssueType: task.IssueTypeBug}
			}
			service := task.UpdateService{Sources: sources, BackendFactory: func(source task.RepositorySource) (task.UpdateBackend, error) {
				switch source.Repository.ID {
				case "alpha":
					return consumer, nil
				case "beta":
					return owner, nil
				default:
					if scenario == "unreadable" {
						return nil, errors.New("source unavailable")
					}
					return transitive, nil
				}
			}}

			_, err := service.Update(context.Background(), alpha, task.UpdateOptions{ID: "op-current", Title: stringPtr("Changed"), AddBlockingIDs: []string{"be-1"}})

			if scenario == "valid" {
				require.NoError(t, err)
				assert.Equal(t, []string{"be-1"}, consumer.opts.CrossRepositoryBlockingIDs)
				assert.True(t, consumer.updated)
			} else {
				require.Error(t, err)
				assert.False(t, consumer.updated)
				if scenario == "cycle" {
					assert.ErrorContains(t, err, "dependency cycle")
				}
			}
			assert.False(t, owner.updated)
			assert.False(t, transitive.updated)
		})
	}
}

func TestUpdateServiceCanRepeatAndRemoveUnavailableReferencesWithoutReadingOwner(t *testing.T) {
	source := createTestSource("alpha", "op", "/fixture/alpha")
	for _, opts := range []task.UpdateOptions{
		{ID: "op-current", AddBlockingIDs: []string{"missing-1"}},
		{ID: "op-current", RemoveBlockingIDs: []string{"missing-1", "missing-absent"}},
	} {
		consumer := updateBackendWithCurrent()
		current := consumer.tasks["op-current"]
		current.Relations.DependencyIDs = []string{"missing-1"}
		consumer.tasks[current.ID] = current

		_, err := updateService(source, consumer).Update(context.Background(), source, opts)

		require.NoError(t, err)
		assert.Empty(t, consumer.opts.AddBlockingIDs)
		if len(opts.RemoveBlockingIDs) > 0 {
			assert.Equal(t, []string{"missing-1"}, consumer.opts.RemoveBlockingIDs)
		}
	}
}

func TestCrossRepositoryMetadataEditsAreIdempotentAndPreserveOtherKeys(t *testing.T) {
	metadata := task.Metadata{"other": "value"}
	for range 2 {
		var err error
		metadata, err = task.EditCrossRepositoryBlocks(metadata, []string{"be-2", " be-1 ", "be-1"}, nil)
		require.NoError(t, err)
	}
	ids, err := task.CrossRepositoryBlocks(metadata)
	require.NoError(t, err)
	assert.Equal(t, []string{"be-1", "be-2"}, ids)
	for range 2 {
		metadata, err = task.EditCrossRepositoryBlocks(metadata, nil, []string{"be-1", "absent"})
		require.NoError(t, err)
	}
	ids, err = task.CrossRepositoryBlocks(metadata)
	require.NoError(t, err)
	assert.Equal(t, []string{"be-2"}, ids)
	assert.Equal(t, "value", metadata["other"])
	_, err = task.CrossRepositoryBlocks(task.Metadata{task.MetadataCrossRepositoryBlocks: "invalid"})
	require.Error(t, err)
}
