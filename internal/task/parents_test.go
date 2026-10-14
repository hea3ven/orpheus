package task_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateServiceAllowsParentInAnotherRepositoryWithoutChangingOwnership(t *testing.T) {
	alpha := createTestSource("alpha", "op", "/fixture/alpha")
	beta := createTestSource("beta", "be", "/fixture/beta")
	consumer := &fakeCreateBackend{}
	owner := &fakeCreateBackend{tasks: map[string]task.Task{"be-epic": {ID: "be-epic", IssueType: task.IssueTypeEpic, Status: task.StatusOpen}}}
	service := task.CreateService{Sources: []task.RepositorySource{alpha, beta}, BackendFactory: func(source task.RepositorySource) (task.CreateBackend, error) {
		if source.Repository.ID == "alpha" {
			return consumer, nil
		}
		return owner, nil
	}}

	created, err := service.Create(t.Context(), alpha, task.CreateRequest{Title: "Child", Description: "Plan", AcceptanceCriteria: "Done", ParentID: "be-epic"})

	require.NoError(t, err)
	assert.Equal(t, "op-new", created.ID)
	assert.Equal(t, "be-epic", consumer.created.ParentID)
	assert.Empty(t, owner.created.Title)
}
