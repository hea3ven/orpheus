//go:build integration

package cli_test

import (
	"errors"
	"fmt"
	"testing"

	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskCreateRendersCreatedTypeAndIDAndSendsSemanticContent(t *testing.T) {
	fixture := newCommandWorkflow(t)
	is := assert.New(t)
	backend := newOwnershipAuthoringBackend()
	setupOwnershipAuthoringWorkflow(t, fixture, ownershipAuthoringRepo(), backend)

	stdout, stderr := fixture.mustExecute(
		"task", "create", "--repo", "alpha", "--type", "epic", "--title", "Plan work",
		"--description", "# Description\n\nLong form.", "--design", "# Design", "--acceptance", "- works",
		"--external-ref", "PLAN-3",
	)

	is.Empty(stderr)
	is.Equal("Created epic op-9.\n", stdout)
	require.Len(t, backend.creates, 1)
	is.Equal(taskmodel.CreateOptions{
		Title:              "Plan work",
		Description:        "# Description\n\nLong form.",
		Design:             "# Design",
		AcceptanceCriteria: "- works",
		ExternalRef:        "PLAN-3",
		IssueType:          taskmodel.IssueTypeEpic,
	}, backend.creates[0])
}

func TestIntegrationWorkflowTaskCreateRequiresExternalReferenceForGatedRepository(t *testing.T) {
	fixture := newCommandWorkflow(t)
	is := assert.New(t)
	backend := newOwnershipAuthoringBackend()
	setupOwnershipAuthoringWorkflow(t, fixture, ownershipAuthoringGatedRepo(), backend)

	_, _, err := fixture.execute(
		"task", "create", "--repo", "alpha", "--title", "Implement work", "--description", "Description", "--acceptance", "Acceptance",
	)
	require.Error(t, err)
	is.Contains(err.Error(), "--external-ref <reference>")
	is.Empty(backend.creates)

	stdout, stderr := fixture.mustExecute(
		"task", "create", "--repo", "alpha", "--title", "Implement work", "--description", "Description", "--acceptance", "Acceptance", "--external-ref", "PLAN-7",
	)
	is.Empty(stderr)
	is.Equal("Created task op-9.\n", stdout)
	require.Len(t, backend.creates, 1)
	is.Equal("PLAN-7", backend.creates[0].ExternalRef)
	is.Equal(taskmodel.IssueTypeTask, backend.creates[0].IssueType)
}

func TestIntegrationWorkflowTaskEditReportsRecoveryGuidanceAfterPartialMutation(t *testing.T) {
	fixture := newCommandWorkflow(t)
	backend := newOwnershipAuthoringBackend(
		ownershipAuthoringTask("op-1", "Original", taskmodel.StatusOpen, taskmodel.IssueTypeTask),
		ownershipAuthoringTask("op-blocker", "Blocker", taskmodel.StatusOpen, taskmodel.IssueTypeTask),
	)
	storageErr := errors.New("internal dependency write failure")
	backend.dependencyUpdateErr = fmt.Errorf("source edit: %w", taskmodel.PartialUpdateError{Cause: storageErr})
	repo := ownershipAuthoringRepo()
	repo.TaskSource, repo.TaskMode, repo.TaskPrefix = "gig", "managed", "op"
	repo.BeadsMode, repo.BeadsPrefix = "", ""
	setupOwnershipAuthoringWorkflow(t, fixture, repo, backend)

	stdout, stderr, err := fixture.execute("task", "edit", "op-1", "--title", "Changed", "--add-block", "op-blocker")

	require.ErrorIs(t, err, storageErr)
	assert.Empty(t, stdout, "a partial edit must not print success")
	assert.Empty(t, stderr)
	// main prints the returned command error verbatim to the operator.
	assert.EqualError(t, err, `task edit op-1: cannot update task "op-1" in repository alpha; edit partially applied; inspect content and dependencies before retrying`)
	assert.Equal(t, "Changed", backend.task("op-1").Title)
	assert.Empty(t, backend.task("op-1").Relations.DependencyIDs)
}

func TestIntegrationWorkflowTaskEditRequiresExternalReferenceForGatedRepository(t *testing.T) {
	fixture := newCommandWorkflow(t)
	is := assert.New(t)
	backend := newOwnershipAuthoringBackend(taskmodel.Task{
		ID:                 "op-1",
		Title:              "Existing task",
		Description:        "Description",
		AcceptanceCriteria: "Acceptance",
		Status:             taskmodel.StatusOpen,
		IssueType:          taskmodel.IssueTypeTask,
	})
	setupOwnershipAuthoringWorkflow(t, fixture, ownershipAuthoringGatedRepo(), backend)

	stdout, stderr, err := fixture.execute("task", "edit", "op-1", "--title", "Updated task")

	require.Error(t, err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "--external-ref <reference>")
	is.Empty(backend.updates)
}
