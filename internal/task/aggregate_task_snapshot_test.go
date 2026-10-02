package task_test

import (
	"context"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAggregatorSnapshotForTaskReusesDetailAndLoadsOnlyRequiredContext(t *testing.T) {
	repository := task.Repository{ID: "alpha", TaskIDPrefix: "a"}
	child := task.Task{ID: "a-child", IssueType: task.IssueTypeTask, Status: task.StatusClosed, Relations: task.RelationSummary{ParentID: "a-epic"}}
	candidate := task.RepoTask{Repository: repository, Task: task.Task{
		ID: "a-epic", IssueType: task.IssueTypeEpic,
		Relations:    task.RelationSummary{Complete: true, ChildIDs: []string{child.ID}, DependencyIDs: []string{"a-blocker"}, DependentIDs: []string{"a-dependent"}},
		RelatedItems: []task.Task{child},
	}}
	backend := &recordingFilteredReadBackend{tasks: []task.Task{
		{ID: "a-blocker", IssueType: task.IssueTypeTask, Status: task.StatusOpen},
		{ID: "a-unrelated", IssueType: task.IssueTypeTask},
	}}
	aggregator, err := task.NewAggregator([]task.RepositorySource{
		{Repository: repository, BackendDir: "/fixture/alpha"},
		{Repository: task.Repository{ID: "other", TaskIDPrefix: "b"}, BackendDir: "/fixture/other"},
	}, func(source task.RepositorySource) (task.ReadBackend, error) {
		assert.Equal(t, repository.ID, source.Repository.ID)
		return backend, nil
	})
	require.NoError(t, err)

	got := aggregator.SnapshotForTask(context.Background(), candidate)

	assert.Empty(t, got.Failures)
	require.Len(t, got.Repositories, 1)
	assert.Equal(t, []string{"a-blocker", "a-child", "a-epic"}, snapshotTaskIDsForRepository(t, got, "alpha"))
	assert.Equal(t, []string{"a-blocker"}, backend.gets)
	assert.Empty(t, backend.filters)
	assert.Equal(t, candidate.Task, got.Repositories[0].Tasks[0])
}
