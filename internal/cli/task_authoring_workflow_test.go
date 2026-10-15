//go:build integration

package cli_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskAuthoringPrioritiesReadBackThroughExistingViews(t *testing.T) {
	fixture := newCommandWorkflow(t)
	is := assert.New(t)
	backend := newOwnershipAuthoringBackend()
	setupOwnershipAuthoringWorkflow(t, fixture, ownershipAuthoringRepo(), backend)

	stdout, stderr := fixture.mustExecute(
		"task", "create", "--repo", "alpha", "--type", "epic", "--title", "Plan work",
		"--description", "# Description\n\nLong form.", "--design", "# Design", "--acceptance", "- works",
		"--external-ref", "PLAN-3", "-p", "P1",
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
		Priority:           new(1),
	}, backend.creates[0])

	before := backend.task("op-9")
	fixture.mustExecute("task", "edit", "op-9", "--priority", "0")
	before.Priority = 0
	is.Equal(before, backend.task("op-9"), "priority-only edits preserve the rest of the plan")
	fixture.mustExecute("task", "edit", "op-9", "--design", "Updated design")
	is.Nil(backend.updates[1].Priority, "omitted edit priority must not become P2 or P0")
	is.Zero(backend.task("op-9").Priority)

	backend.createdID = "op-10"
	fixture.mustExecute("task", "create", "--repo", "alpha", "--title", "Implement work",
		"--description", "Description", "--acceptance", "Acceptance")
	require.Len(t, backend.creates, 2)
	is.Equal(new(2), backend.creates[1].Priority, "creation defaults to P2")
	is.Equal(2, backend.task("op-10").Priority)
	fixture.mustExecute("task", "edit", "op-10", "-p", "P4")

	show, _ := fixture.mustExecute("task", "show", "op-10")
	is.Contains(show, "Priority: 4")
	showJSON, _ := fixture.mustExecute("task", "show", "op-10", "--json")
	var detail struct {
		ID       string `json:"id"`
		Priority int    `json:"priority"`
	}
	require.NoError(t, json.Unmarshal([]byte(showJSON), &detail))
	is.Equal("op-10", detail.ID)
	is.Equal(4, detail.Priority)
	for _, args := range [][]string{{"task", "list", "--json"}, {"status", "--json"}} {
		output, _ := fixture.mustExecute(args...)
		var entries []taskViewJSONTaskEntry
		require.NoError(t, json.Unmarshal([]byte(output), &entries))
		priorities := make(map[string]int)
		for _, entry := range entries {
			priorities[entry.ID] = entry.Priority
		}
		is.Equal(map[string]int{"op-9": 0, "op-10": 4}, priorities)
	}
}

func TestIntegrationWorkflowTaskAuthoringRejectsInvalidPriorityAndClosedEditsWithoutMutation(t *testing.T) {
	fixture := newCommandWorkflow(t)
	backend := newOwnershipAuthoringBackend(
		ownershipAuthoringTask("op-1", "Original", taskmodel.StatusInProgress, taskmodel.IssueTypeTask),
		ownershipAuthoringEpic("op-closed", taskmodel.StatusClosed),
	)
	setupOwnershipAuthoringWorkflow(t, fixture, ownershipAuthoringRepo(), backend)
	before := backend.task("op-1")
	closed := backend.task("op-closed")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"task", "create", "--repo", "alpha", "--title", "Invalid", "--description", "Description", "--acceptance", "Acceptance", "--priority", "P5"}, "invalid --priority"},
		{[]string{"task", "edit", "op-1", "--title", "Must not change", "-p", "-1"}, "invalid --priority"},
		{[]string{"task", "edit", "op-1", "--priority="}, "invalid --priority"},
		{[]string{"task", "edit", "op-closed", "-p", "P0"}, "is closed"},
	} {
		stdout, _, err := fixture.execute(tc.args...)
		require.ErrorContains(t, err, tc.want)
		assert.Empty(t, stdout)
		assert.Zero(t, backend.mutationCount())
		assert.Equal(t, before, backend.task("op-1"))
		assert.Equal(t, closed, backend.task("op-closed"))
	}
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
		"task", "create", "--repo", "alpha", "--title", "Implement work", "--description", "Description", "--acceptance", "Acceptance", "--external-ref", "PLAN-7", "--priority", "P0",
	)
	is.Empty(stderr)
	is.Equal("Created task op-9.\n", stdout)
	require.Len(t, backend.creates, 1)
	is.Equal("PLAN-7", backend.creates[0].ExternalRef)
	is.Equal(new(0), backend.creates[0].Priority)
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
